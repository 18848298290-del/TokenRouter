package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// Runtime settings store one text value for each configured key. Removing a
// row lets that setting's reader apply its own default, and the table retains
// the latest value and update time rather than a history of earlier values.

// Setting represents the current persisted value of one runtime setting.
//
// Callers that need a change history store those records separately from this table.
type Setting struct {
	ent.Schema // generated entity definition
}

// Annotations maps generated runtime-setting operations to the shared settings table.
func (Setting) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "settings"},
	}
}

// Fields declares the unique setting key, text value, and update timestamp.
// Timestamp defaults use application time on insertion and Ent updates; each
// reader interprets the stored text for its setting.
func (Setting) Fields() []ent.Field {
	return []ent.Field{
		// Runtime setting name.
		field.String("key").
			MaxLen(100).
			NotEmpty().
			Unique(),
		// Serialized setting value.
		field.String("value").
			SchemaType(map[string]string{
				dialect.Postgres: "text",
			}),
		// Latest value timestamp.
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{
				dialect.Postgres: "timestamptz",
			}),
	}
}

// Indexes returns no additional indexes beyond the unique key declared by Fields.
func (Setting) Indexes() []ent.Index {
	// Fields declares the unique key constraint.
	return nil
}
