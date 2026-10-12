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

// A reusable redeem code records each user who has received its entitlement.
// The unique code-user pair limits that user to one receipt for the code,
// while a use timestamp supports the redemption history retained by billing.

// RedeemCodeUsage stores one user's recorded use of a redeem code.
type RedeemCodeUsage struct {
	ent.Schema // generated entity definition
}

// Annotations maps redemption receipt persistence to the redeem_code_usages database table.
func (RedeemCodeUsage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "redeem_code_usages"},
	}
}

// Fields declares the redeemed code, receiving user, and redemption timestamp.
// used_at defaults to application time when omitted, and both identity fields
// are required by the generated receipt builder.
func (RedeemCodeUsage) Fields() []ent.Field {
	return []ent.Field{
		// Redeemed code identity.
		field.Int64("redeem_code_id"),
		// Receiving user identity.
		field.Int64("user_id"),
		// Recorded redemption timestamp.
		field.Time("used_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires the redemption receipt to reference its code and user.
// Scalar identities back unique entity edges, enabling code history and
// account redemption queries through the generated Ent client.
func (RedeemCodeUsage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("redeem_code", RedeemCode.Type).
			Ref("usage_records").
			Field("redeem_code_id").
			Required().
			Unique(),
		edge.From("user", User.Type).
			Ref("redeem_code_usages").
			Field("user_id").
			Required().
			Unique(),
	}
}

// Indexes enforces one redemption receipt for each code and user pair.
// Separate code and user indexes support retrieving the redemption history
// from either side of the relationship.
func (RedeemCodeUsage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("redeem_code_id"),
		index.Fields("user_id"),
		index.Fields("redeem_code_id", "user_id").Unique(),
	}
}
