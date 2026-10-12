package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// PaymentOrder holds the schema definition for the PaymentOrder entity.
//
// Orders are hard-deleted.
// status tracks the order lifecycle.
// PaymentAuditLog stores order audit events; archive required history before deletion.
type PaymentOrder struct {
	ent.Schema
}

func (PaymentOrder) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "payment_orders"},
	}
}

func (PaymentOrder) Fields() []ent.Field {
	return []ent.Field{
		// Snapshot user details to avoid joins when displaying orders.
		field.Int64("user_id"),
		field.String("user_email").
			MaxLen(255),
		field.String("user_name").
			MaxLen(100),
		field.String("user_notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Amounts and fees.
		field.Float("amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("pay_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("fee_rate").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0),
		field.Float("fee_fixed").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		field.Float("fee_rate_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		field.Float("fee_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		field.String("recharge_code").
			MaxLen(64),

		// Payment transaction details.
		field.String("out_trade_no").
			MaxLen(64).
			Default(""),
		field.String("payment_type").
			MaxLen(30),
		field.String("payment_trade_no").
			MaxLen(128),
		field.String("pay_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("qr_code").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("qr_code_img").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Stripe invoice fields are empty on legacy PaymentIntent orders; the document API returns the receipt for those orders.
		field.String("payment_customer_id").
			Optional().
			Nillable().
			MaxLen(128),
		field.String("payment_invoice_id").
			Optional().
			Nillable().
			MaxLen(128),
		field.String("payment_invoice_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("payment_invoice_pdf_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("payment_invoice_status").
			Optional().
			Nillable().
			MaxLen(30),
		field.JSON("billing_snapshot", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Order type, subscription plan, and payment provider snapshots.
		field.String("order_type").
			MaxLen(20).
			Default("balance"),
		field.Int64("plan_id").
			Optional().
			Nillable(),
		field.JSON("plan_snapshot", billing.SubscriptionPlanSnapshot{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("provider_instance_id").
			Optional().
			Nillable().
			MaxLen(64),
		field.String("provider_key").
			Optional().
			Nillable().
			MaxLen(30),
		field.JSON("provider_snapshot", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		field.String("status").
			MaxLen(30).
			Default("PENDING"),

		// Refund details.
		field.Float("refund_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		field.String("refund_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("refund_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Bool("force_refund").
			Default(false),
		field.Time("refund_requested_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("refund_request_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("refund_requested_by").
			Optional().
			Nillable().
			MaxLen(20),

		// Order lifecycle timestamps.
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("paid_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("failed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("failed_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Request origin.
		field.String("client_ip").
			MaxLen(50),
		field.String("src_host").
			MaxLen(255),
		field.String("src_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Creation and update timestamps.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PaymentOrder) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("payment_orders").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (PaymentOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("out_trade_no").
			Unique().
			Annotations(entsql.IndexWhere("out_trade_no <> ''")),
		index.Fields("user_id"),
		index.Fields("status"),
		index.Fields("expires_at"),
		index.Fields("created_at"),
		index.Fields("paid_at"),
		index.Fields("payment_type", "paid_at"),
		index.Fields("order_type"),
	}
}
