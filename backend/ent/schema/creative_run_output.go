package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// CreativeRunOutput stores metadata for one Creative Studio output.
//
// Output image bytes live in temporary Redis storage until their TTL expires.
// This table records status, MIME type, byte size, and expiration time.
type CreativeRunOutput struct {
	ent.Schema
}

func (CreativeRunOutput) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "creative_run_outputs"},
	}
}

func (CreativeRunOutput) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (CreativeRunOutput) Fields() []ent.Field {
	return []ent.Field{
		field.String("run_id").MaxLen(64),
		field.Int("output_index"),
		field.String("status").MaxLen(20).Default("pending"),
		field.String("mime_type").Optional().Nillable().MaxLen(128),
		field.Int64("byte_size").Optional().Nillable(),
		// transient_expires_at is the deadline after which the output must not be served to clients.
		field.Time("transient_expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("acked_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("error_code").Optional().Nillable().MaxLen(128),
		field.String("error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (CreativeRunOutput) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "output_index").Unique(),
		index.Fields("run_id", "status"),
		index.Fields("transient_expires_at"),
	}
}
