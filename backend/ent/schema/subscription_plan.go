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

// subscription_plan.go defines editable subscription products available for
// purchase or entitlement issuance. Grant services copy duration and quota
// limits into UserSubscription records, while the plan retains product
// presentation, price, and group coverage used by its current consumers.

// SubscriptionPlan stores administrator-managed product and entitlement template settings.
// Deleting a plan removes its stored product row.
type SubscriptionPlan struct {
	ent.Schema // generated entity definition
}

// Annotations maps subscription product settings to the subscription_plans database table.
func (SubscriptionPlan) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "subscription_plans"},
	}
}

// Fields describes product text, sale price, duration, quota, and group settings.
// Generated Ent inserts use thirty day-units, empty currency, unrestricted
// group IDs, and for_sale true. Billing's plan service validates and normalizes
// administrative values before repository writes.
func (SubscriptionPlan) Fields() []ent.Field {
	return []ent.Field{
		// Translated product content.
		field.JSON("localization", billing.PlanLocalization{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// Default product name.
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		// Default product description.
		field.String("description").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		// price and original_price use the plan's currency for displayed product
		// amounts. original_price is optional comparison pricing; currency can
		// remain empty after NormalizePlanCurrency, while supplied codes are
		// trimmed and uppercased by that service helper.
		// Listed product amount.
		field.Float("price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		// Optional comparison amount.
		field.Float("original_price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Optional().
			Nillable(),
		// Product currency code.
		field.String("currency").
			MaxLen(3).
			Default(""),
		// validity_days is the quantity interpreted through validity_unit by
		// ComputeValidityDays. Week units multiply by seven and month units by
		// thirty; other unit values leave the quantity expressed in days.
		// Entitlement duration quantity.
		field.Int("validity_days").
			Default(30),
		// daily_limit_usd, weekly_limit_usd, and monthly_limit_usd supply the
		// quota template copied by subscription assignment. Positive values
		// configure finite windows; absent or nonpositive values impose no
		// limit for that period in the subscription model.
		// USD daily quota.
		field.Float("daily_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		// USD weekly quota.
		field.Float("weekly_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		// USD monthly quota.
		field.Float("monthly_limit_usd").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		// Duration quantity unit.
		field.String("validity_unit").
			MaxLen(10).
			Default("day"),
		// group_ids lists eligible groups. subscriptionPlanIncludesGroup treats
		// an empty list as unrestricted for a positive group ID and rejects
		// nonpositive group IDs before examining the list.
		field.JSON("group_ids", []int64{}).
			Default([]int64{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Group IDs covered by the plan; an empty list covers all groups"),
		// group_rate_multipliers configures subscription pricing per selected
		// group. NormalizePlanGroupRateMultipliers filters unselected identities
		// and rejects nonpositive rates for retained entries before plan writes.
		field.JSON("group_rate_multipliers", map[int64]float64{}).
			Default(func() map[int64]float64 { return map[int64]float64{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Subscription billing multipliers by group_id for this plan"),
		// Product feature text.
		field.String("features").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		// Checkout product label.
		field.String("product_name").
			MaxLen(100).
			Default(""),
		// for_sale controls availability in purchase flows. The
		// subscriptionPlanIncludesGroup helper reads group_ids for issued
		// entitlement group eligibility, keeping this selling flag separate
		// from that particular check.
		field.Bool("for_sale").
			Default(true),
		// Configured product order.
		field.Int("sort_order").
			Default(0),
		// Immutable creation timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Latest mutation timestamp.
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges relates product templates to issued subscriptions and subscription redeem codes.
func (SubscriptionPlan) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("subscriptions", UserSubscription.Type),
		edge.To("redeem_codes", RedeemCode.Type),
	}
}

// Indexes supports selecting plans by their current purchase-availability flag.
func (SubscriptionPlan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("for_sale"),
	}
}
