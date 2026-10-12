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

// SubscriptionPlan holds the schema definition for the SubscriptionPlan entity.
//
// Plans are hard-deleted.
// Plans are administrator-managed product configurations.
// for_sale controls availability for purchase; deletion removes the plan.
// Purchased subscriptions and their per-subscription limits are stored in UserSubscription.
type SubscriptionPlan struct {
	ent.Schema
}

func (SubscriptionPlan) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "subscription_plans"},
	}
}

func (SubscriptionPlan) Fields() []ent.Field {
	return []ent.Field{
		field.JSON("localization", billing.PlanLocalization{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("description").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.Float("price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("original_price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Optional().
			Nillable(),
		field.String("currency").
			MaxLen(3).
			Default(""),
		field.Int("validity_days").
			Default(30),
		field.Float("daily_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("weekly_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("monthly_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.String("validity_unit").
			MaxLen(10).
			Default("day"),
		field.JSON("group_ids", []int64{}).
			Default([]int64{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Group IDs covered by the plan; an empty list covers all groups"),
		field.JSON("group_rate_multipliers", map[int64]float64{}).
			Default(func() map[int64]float64 { return map[int64]float64{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Subscription billing multipliers by group_id for this plan"),
		field.String("features").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.String("product_name").
			MaxLen(100).
			Default(""),
		field.Bool("for_sale").
			Default(true),
		field.Int("sort_order").
			Default(0),
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

func (SubscriptionPlan) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("subscriptions", UserSubscription.Type),
		edge.To("redeem_codes", RedeemCode.Type),
	}
}

func (SubscriptionPlan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("for_sale"),
	}
}
