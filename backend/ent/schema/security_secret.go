package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// security_secret.go defines database records that retain signing secrets
// under stable keys. A dedicated secret record keeps that signing material
// available independently of ordinary runtime settings, with one nonempty
// stored value per key.

// SecuritySecret stores bootstrap-managed secrets identified by a unique key.
type SecuritySecret struct {
	ent.Schema // generated entity definition
}

// Annotations maps generated secret operations to the dedicated security_secrets table.
func (SecuritySecret) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "security_secrets"},
	}
}

// Mixin supplies creation and update timestamps for persisted application secrets.
func (SecuritySecret) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields requires a unique secret key and nonempty stored secret material.
// Values occupy PostgreSQL text columns; validation of the secret's format and
// length belongs to its bootstrap consumer.
func (SecuritySecret) Fields() []ent.Field {
	return []ent.Field{
		// Bootstrap secret name.
		field.String("key").
			MaxLen(100).
			NotEmpty().
			Unique(),
		// Persisted secret material.
		field.String("value").
			NotEmpty().
			SchemaType(map[string]string{
				dialect.Postgres: "text",
			}),
	}
}
