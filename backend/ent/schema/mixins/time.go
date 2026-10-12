package mixins

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
)

// Entity timestamps are assigned by the application process when Ent creates or
// updates a record. The creation time is immutable after insertion, while the
// update time receives a fresh default for mutations using the generated Ent
// update builders.

// TimeMixin supplies automatic creation and update times for persisted entities.
type TimeMixin struct {
	mixin.Schema // reusable schema definition
}

// Fields declares immutable created_at and automatically refreshed updated_at timestamps.
// Both use PostgreSQL time-zone-aware columns and application time defaults when
// a caller omits a value on creation.
func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		// Immutable creation timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{
				dialect.Postgres: "timestamptz",
			}),
		// Latest mutation timestamp.
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{
				dialect.Postgres: "timestamptz",
			}),
	}
}
