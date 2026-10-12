package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// SecuritySecret stores system secrets such as JWT signing and TOTP encryption keys.
type SecuritySecret struct {
	ent.Schema
}

func (SecuritySecret) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "security_secrets"},
	}
}

func (SecuritySecret) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (SecuritySecret) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").
			MaxLen(100).
			NotEmpty().
			Unique(),
		field.String("value").
			NotEmpty().
			SchemaType(map[string]string{
				dialect.Postgres: "text",
			}),
	}
}
