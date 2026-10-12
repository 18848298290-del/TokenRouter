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

// PromoCode holds the schema definition for the PromoCode entity.
//
// Signup promotion codes credit a user's balance during registration.
// A code can be used repeatedly up to its configured limit.
//
// Codes are hard-deleted.
type PromoCode struct {
	ent.Schema
}

func (PromoCode) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "promo_codes"},
	}
}

func (PromoCode) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").
			MaxLen(32).
			NotEmpty().
			Unique().
			Comment("Promotion code"),
		field.Float("bonus_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Balance credit amount"),
		field.Int("max_uses").
			Default(0).
			Comment("Maximum uses; 0 means unlimited"),
		field.Int("used_count").
			Default(0).
			Comment("Number of uses"),
		field.String("status").
			MaxLen(20).
			Default(promotion.PromoCodeStatusActive).
			Comment("Status: active, disabled"),
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Expiration time; null means no expiration"),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("Notes"),
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

func (PromoCode) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("usage_records", PromoCodeUsage.Type),
	}
}

func (PromoCode) Indexes() []ent.Index {
	return []ent.Index{
		// Fields declares the unique code constraint.
		index.Fields("status"),
		index.Fields("expires_at"),
	}
}
