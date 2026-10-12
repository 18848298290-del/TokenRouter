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

// A batch result is indexed by its submitted custom ID so users can list
// outcomes separately from the provider's output file. Stored metadata identifies
// successful images or item errors and retains optional source locations for
// the result readers.

// BatchImageItem stores searchable result metadata for one submitted batch item.
type BatchImageItem struct {
	ent.Schema // generated entity definition
}

// Annotations maps indexed batch result records to the batch_image_items table.
func (BatchImageItem) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "batch_image_items"},
	}
}

// Fields describes item identity, result status, source metadata, and billing
// information. Image count defaults to zero. Optional result and error values
// let the indexer record both successful and failed provider rows.
func (BatchImageItem) Fields() []ent.Field {
	return []ent.Field{
		// Public batch identity.
		field.String("job_id").MaxLen(64),
		// Submitted item identity.
		field.String("custom_id").MaxLen(255),
		// Indexed result state.
		field.String("status").MaxLen(32),
		// Submitted request digest.
		field.String("request_hash").Optional().Nillable().MaxLen(128),
		// Optional prompt preview.
		field.String("prompt_preview").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		// provider_source_object and source_line_number locate the provider output
		// row recorded by the indexer. source_byte_offset and source_byte_length
		// retain optional byte-location metadata; the current indexer leaves
		// those byte fields unset.
		// Provider output object.
		field.String("provider_source_object").Optional().Nillable().MaxLen(1024),
		// Output row number.
		field.Int("source_line_number").Optional().Nillable(),
		// Optional byte offset.
		field.Int64("source_byte_offset").Optional().Nillable(),
		// Optional byte length.
		field.Int64("source_byte_length").Optional().Nillable(),
		// Returned image MIME.
		field.String("mime_type").Optional().Nillable().MaxLen(128),
		// Download filename extension.
		field.String("file_extension").Optional().Nillable().MaxLen(32),
		// Returned image count.
		field.Int("image_count").Default(0),
		// Provider failure code.
		field.String("error_code").Optional().Nillable().MaxLen(128),
		// Provider failure description.
		field.String("error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Recorded item charge.
		field.Float("billed_amount").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		// Immutable creation timestamp.
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Result indexing timestamp.
		field.Time("indexed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes enforces one result row per job and submitted custom ID.
// Additional indexes support status-filtered item lists and finding rows
// associated with a provider output object.
func (BatchImageItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_id", "custom_id").Unique(),
		index.Fields("job_id", "status"),
		index.Fields("provider_source_object"),
	}
}
