package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Setting holds the schema definition for the Setting entity.
//
// Settings are hard-deleted.
// Removing a stored value leaves default handling to the setting's reader.
// This table stores current values rather than a change history.
//
// Callers that require an audit trail must record changes before updates or deletion.
type Setting struct {
	ent.Schema
}

func (Setting) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "settings"},
	}
}

func (Setting) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").
			MaxLen(100).
			NotEmpty().
			Unique(),
		field.String("value").
			SchemaType(map[string]string{
				dialect.Postgres: "text",
			}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{
				dialect.Postgres: "timestamptz",
			}),
	}
}

func (Setting) Indexes() []ent.Index {
	// Fields declares the unique key constraint.
	return nil
}
