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

	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// promo_code.go defines signup promotion credits, availability settings, and
// global use counters. The promotion service checks activation, expiry, and
// the configured total limit when applying a code, while per-user usage
// records enforce one grant per user for that code.

// PromoCode stores signup balance grants and their availability controls.
//
// Codes are hard-deleted.
type PromoCode struct {
	ent.Schema // generated entity definition
}

// Annotations maps signup promotion persistence to the promo_codes database table.
func (PromoCode) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "promo_codes"},
	}
}

// Fields describes the unique code, grant amount, availability, and counters.
// New codes start active with zero bonus and unlimited total uses. Expiration
// is optional, and timestamps default to application time.
func (PromoCode) Fields() []ent.Field {
	return []ent.Field{
		// Unique promotion text.
		field.String("code").
			MaxLen(32).
			NotEmpty().
			Unique().
			Comment("Promotion code"),
		// USD registration credit.
		field.Float("bonus_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Balance credit amount"),
		// max_uses and used_count govern the global grant count. A positive
		// maximum caps total usage, while zero permits unlimited uses; each
		// successful grant increments the stored counter in its transaction.
		// Total grant limit.
		field.Int("max_uses").
			Default(0).
			Comment("Maximum uses; 0 means unlimited"),
		// Completed grant count.
		field.Int("used_count").
			Default(0).
			Comment("Number of uses"),
		// Active or disabled.
		field.String("status").
			MaxLen(20).
			Default(promotion.PromoCodeStatusActive).
			Comment("Status: active, disabled"),
		// expires_at limits when the promotion can be granted. A NULL value
		// leaves the time window open while status and use counts still govern
		// whether the code is eligible.
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Expiration time; null means no expiration"),
		// Administrator promotion notes.
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("Notes"),
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

// Edges exposes the user grant records belonging to each signup promotion.
func (PromoCode) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("usage_records", PromoCodeUsage.Type),
	}
}

// Indexes supports active-code filtering and expiration lookup in addition to
// the unique code field. Per-user grant uniqueness is declared separately by
// the PromoCodeUsage schema.
func (PromoCode) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("expires_at"),
	}
}
