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

// RedeemCode holds the schema definition for the RedeemCode entity.
//
// Redemption codes are hard-deleted.
// max_uses controls how many times a code can be redeemed.
// status and used_at track redemption state.
//
// Archive any required audit information before deleting a code.
type RedeemCode struct {
	ent.Schema
}

func (RedeemCode) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "redeem_codes"},
	}
}

func (RedeemCode) Fields() []ent.Field {
	return []ent.Field{
		// Codes contain at most 32 Unicode code points, matching API validation.
		field.String("code").
			MaxRuneLen(32).
			NotEmpty().
			Unique(),
		field.String("type").
			MaxLen(20).
			Default(billing.RedeemTypeBalance),
		field.Float("value").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.String("status").
			MaxLen(20).
			Default(billing.StatusUnused),
		// Require a successful payment before redemption when enabled.
		field.Bool("requires_payment").
			Default(false),
		field.Int("max_uses").
			Default(1),
		field.Int("used_count").
			Default(0),
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int64("used_by").
			Optional().
			Nillable(),
		field.Time("used_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int64("plan_id").
			Optional().
			Nillable(),
	}
}

func (RedeemCode) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("usage_records", RedeemCodeUsage.Type),
		edge.From("user", User.Type).
			Ref("redeem_codes").
			Field("used_by").
			Unique(),
		edge.From("plan", SubscriptionPlan.Type).
			Ref("redeem_codes").
			Field("plan_id").
			Unique(),
	}
}

func (RedeemCode) Indexes() []ent.Index {
	return []ent.Index{
		// Fields declares the unique code constraint.
		index.Fields("status"),
		index.Fields("expires_at"),
		index.Fields("used_by"),
		index.Fields("plan_id"),
	}
}
