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

// Payment orders retain buyer, entitlement, fee, and provider facts from
// checkout so confirmation and fulfillment use the purchase that was made.
// Stored payment and refund state survives callbacks and recovery attempts,
// while PaymentAuditLog records the corresponding operations.

// PaymentOrder declares stored purchases, payment progress, and refund metadata.
type PaymentOrder struct {
	ent.Schema // generated entity definition
}

// Annotations maps payment coordination records to the payment_orders table.
func (PaymentOrder) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "payment_orders"},
	}
}

// Fields declares checkout snapshots, channel results, refunds, and lifecycle
// timestamps. Generated inserts begin PENDING with zero fees and refunds;
// payment services write subsequent transitions and preserve order-specific
// product and provider data.
func (PaymentOrder) Fields() []ent.Field {
	return []ent.Field{
		// user_id, user_email, user_name, and user_notes retain the buyer details
		// written by BuildCheckoutDraft. The order stores the display snapshot while
		// user_id remains the required user relationship used for ownership checks.
		// Linked buyer identity.
		field.Int64("user_id"),
		// Checkout buyer email.
		field.String("user_email").
			MaxLen(255),
		// Checkout buyer name.
		field.String("user_name").
			MaxLen(100),
		// Optional checkout buyer notes.
		field.String("user_notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// amount is the balance credit after the configured recharge multiplier,
		// or the subscription plan price. pay_amount stores the channel charge after
		// fees and any subscription currency conversion, so the amounts can differ.
		// Entitlement amount snapshot.
		field.Float("amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		// Channel payment amount.
		field.Float("pay_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		// fee_rate, fee_fixed, fee_rate_amount, and fee_amount preserve the
		// checkout fee breakdown. Percentage and fixed fees contribute to fee_amount,
		// which is included in pay_amount at the channel currency precision.
		// Percentage fee rate.
		field.Float("fee_rate").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(0),
		// Fixed channel fee.
		field.Float("fee_fixed").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		// Calculated percentage fee.
		field.Float("fee_rate_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		// Combined checkout fees.
		field.Float("fee_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		// recharge_code identifies the order-specific balance grant. Checkout
		// writes it after obtaining the numeric order ID, and fulfillment reuses
		// the code and its redemption state when recovering partial work.
		field.String("recharge_code").
			MaxLen(64),

		// out_trade_no is the merchant number sent to the channel. The partial
		// unique index covers nonempty values, leaving historical empty numbers
		// outside that uniqueness rule while new checkout writes a generated number.
		field.String("out_trade_no").
			MaxLen(64).
			Default(""),
		// Selected payment method.
		field.String("payment_type").
			MaxLen(30),
		// Channel transaction identifier.
		field.String("payment_trade_no").
			MaxLen(128),
		// Optional hosted checkout URL.
		field.String("pay_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Optional payment QR payload.
		field.String("qr_code").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Optional payment QR image.
		field.String("qr_code_img").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// payment_customer_id, payment_invoice_id, payment_invoice_url,
		// payment_invoice_pdf_url, and payment_invoice_status retain Stripe channel
		// results. Legacy PaymentIntent orders can omit these fields; the document
		// query then asks the historical provider for its receipt.
		// Stripe customer identifier.
		field.String("payment_customer_id").
			Optional().
			Nillable().
			MaxLen(128),
		// Stripe invoice identifier.
		field.String("payment_invoice_id").
			Optional().
			Nillable().
			MaxLen(128),
		// Hosted invoice URL.
		field.String("payment_invoice_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Invoice PDF URL.
		field.String("payment_invoice_pdf_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Recorded invoice state.
		field.String("payment_invoice_status").
			Optional().
			Nillable().
			MaxLen(30),
		// Checkout billing-information snapshot.
		field.JSON("billing_snapshot", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// order_type and plan_id distinguish balance grants from subscription
		// purchases. BuildCheckoutDraft copies plan_snapshot when a plan is present,
		// and subscription fulfillment uses that snapshot to reconstruct the issued
		// duration and quota values.
		// Purchased entitlement type.
		field.String("order_type").
			MaxLen(20).
			Default("balance"),
		// Optional purchased plan identifier.
		field.Int64("plan_id").
			Optional().
			Nillable(),
		// Checkout subscription-plan snapshot.
		field.JSON("plan_snapshot", billing.SubscriptionPlanSnapshot{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// provider_instance_id, provider_key, and provider_snapshot retain the
		// selected channel and merchant facts. ProviderBindings resolves historical
		// orders from this selection, while notification checks compare reported
		// merchant metadata with the stored snapshot.
		// Selected channel instance.
		field.String("provider_instance_id").
			Optional().
			Nillable().
			MaxLen(64),
		// Selected provider implementation.
		field.String("provider_key").
			Optional().
			Nillable().
			MaxLen(30),
		// Checkout provider facts.
		field.JSON("provider_snapshot", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// status records payment confirmation, fulfillment, and refund progress.
		// Generated inserts begin PENDING; payment services use conditional updates
		// so callbacks and recovery attempts inspect the state that actually won.
		field.String("status").
			MaxLen(30).
			Default("PENDING"),

		// refund_amount records the requested or prepared refund in the units of
		// amount. Refund planning separately calculates the proportional channel
		// refund from pay_amount, so this field is not the channel refund amount.
		field.Float("refund_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0),
		// Recorded refund reason.
		field.String("refund_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// refund_at is written when a local refund-success transaction completes.
		// The pending path clears it because channel acceptance alone does not
		// establish the final refund result.
		field.Time("refund_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Administrator force-refund choice.
		field.Bool("force_refund").
			Default(false),
		// Recorded refund-request time.
		field.Time("refund_requested_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Submitted refund-request reason.
		field.String("refund_request_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Refund requester category.
		field.String("refund_requested_by").
			Optional().
			Nillable().
			MaxLen(20),

		// expires_at is the local checkout deadline and can be replaced by a
		// nonzero channel deadline. Cancellation and expiry services query the
		// channel before closing linked payments, and later confirmed payments
		// can still recover an expired order.
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Recorded payment-confirmation time.
		field.Time("paid_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Recorded entitlement-completion time.
		field.Time("completed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Recorded local failure time.
		field.Time("failed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Recorded local failure reason.
		field.String("failed_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Checkout client address.
		field.String("client_ip").
			MaxLen(50),
		// Checkout source host.
		field.String("src_host").
			MaxLen(255),
		// Optional checkout source URL.
		field.String("src_url").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// created_at and updated_at use time.Now defaults for generated Ent
		// inserts. created_at is immutable; generated updates refresh updated_at,
		// and payment recovery also uses that timestamp for fulfillment leases
		// and prepared-refund ownership checks.
		// Order creation time.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Latest persisted order update.
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges links each stored order to its required buyer identity.
func (PaymentOrder) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("payment_orders").
			Field("user_id").
			Unique().
			Required(),
	}
}

// Indexes declares merchant-number uniqueness and order lookup indexes for
// Ent. Empty historical merchant numbers remain outside the partial unique
// index; user, status, payment type, and timestamps support order queries.
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
