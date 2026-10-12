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

// CreativeRunOutbox stores creation, settlement, and release actions. Image bytes belong in temporary storage.
type CreativeRunOutbox struct {
	ent.Schema
}

func (CreativeRunOutbox) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "creative_run_outbox"}}
}

func (CreativeRunOutbox) Fields() []ent.Field {
	return []ent.Field{
		field.String("run_id").MaxLen(64),
		field.String("operation").MaxLen(32),
		field.String("status").MaxLen(16).Default("pending"),
		field.Time("available_at").Default(time.Now),
		field.String("lease_token").Optional().Nillable().MaxLen(128),
		field.Time("lease_until").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("attempt_count").Default(0),
		field.String("last_error").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CreativeRunOutbox) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "operation").Unique(),
		index.Fields("available_at", "id"),
		index.Fields("run_id", "status"),
	}
}
