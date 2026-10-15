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
)

// promo_code_usage.go defines per-user promotion receipts with the credited
// amount at use time. The promotion and user pair is unique, so concurrent
// grant attempts cannot persist two receipts for the same user even when the
// code allows unlimited total usage.

// PromoCodeUsage records a user's signup promotion grant and credited amount.
type PromoCodeUsage struct {
	ent.Schema // generated entity definition
}

// Annotations maps recorded signup promotion grants to the promo_code_usages table.
func (PromoCodeUsage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "promo_code_usages"},
	}
}

// Fields declares the promotion, recipient, credited amount, and use timestamp.
// Grant amounts use USD decimal columns. used_at receives application time
// when the granting repository omits an observed timestamp.
func (PromoCodeUsage) Fields() []ent.Field {
	return []ent.Field{
		// Applied promotion identity.
		field.Int64("promo_code_id").
			Comment("Promotion code ID"),
		// Credited user identity.
		field.Int64("user_id").
			Comment("User ID that used the code"),
		// USD granted credit.
		field.Float("bonus_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("Actual balance credit amount"),
		// Successful grant timestamp.
		field.Time("used_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Time the code was used"),
	}
}

// Edges requires each grant receipt to reference its promotion and user.
// Scalar identity fields back the unique entity edges, so receipts can be
// traversed from either the granting code or recipient account.
func (PromoCodeUsage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("promo_code", PromoCode.Type).
			Ref("usage_records").
			Field("promo_code_id").
			Required().
			Unique(),
		edge.From("user", User.Type).
			Ref("promo_code_usages").
			Field("user_id").
			Required().
			Unique(),
	}
}

// Indexes enforces one use of a promotion code per user.
// Separate promotion and user indexes support listing grants for either
// entity while the pair protects repeated application.
func (PromoCodeUsage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("promo_code_id"),
		index.Fields("user_id"),
		// Each user can use a given promotion code once.
		index.Fields("promo_code_id", "user_id").Unique(),
	}
}
