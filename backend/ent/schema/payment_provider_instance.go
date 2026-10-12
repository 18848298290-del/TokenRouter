package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PaymentProviderInstance holds the schema definition for the PaymentProviderInstance entity.
//
// Provider instances are hard-deleted.
// Each instance is an administrator-configured payment channel.
// Use enabled to suspend a channel; deletion removes its configuration.
// config contains encrypted credentials and is removed with the row.
type PaymentProviderInstance struct {
	ent.Schema
}

func (PaymentProviderInstance) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "payment_provider_instances"},
	}
}

func (PaymentProviderInstance) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider_key").
			MaxLen(30).
			NotEmpty(),
		field.String("name").
			MaxLen(100).
			Default(""),
		field.String("config").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("supported_types").
			MaxLen(200).
			Default(""),
		field.Bool("enabled").
			Default(true),
		field.String("payment_mode").
			MaxLen(20).
			Default(""),
		field.Int("sort_order").
			Default(0),
		field.String("limits").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.Bool("refund_enabled").
			Default(false),
		field.Bool("allow_user_refund").
			Default(false),
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

func (PaymentProviderInstance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider_key"),
		index.Fields("enabled"),
	}
}
