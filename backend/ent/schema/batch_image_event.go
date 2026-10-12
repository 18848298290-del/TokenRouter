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

// Batch job processing appends milestone events with optional JSON payloads.
// Events without a supplied hash can recur; the schema provides a partial
// unique index for nonempty job-scoped hashes when a writer supplies one,
// alongside indexes for chronological history and event-type lookup.

// BatchImageEvent stores recorded milestones and payload metadata for batch jobs.
type BatchImageEvent struct {
	ent.Schema // generated entity definition
}

// Annotations maps generated batch-event operations to the batch_image_events table.
func (BatchImageEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "batch_image_events"},
	}
}

// Fields declares the job, event kind, optional payload, and event hash.
// Generated Ent inserts default the immutable creation time to application time.
// The repository append path supplies no hash, so those events use unkeyed insertion.
func (BatchImageEvent) Fields() []ent.Field {
	return []ent.Field{
		// Public batch identity.
		field.String("job_id").MaxLen(64),
		// Processing milestone name.
		field.String("event_type").MaxLen(64),
		// Event-specific JSON metadata.
		field.JSON("payload", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// event_hash participates in job-scoped uniqueness when nonempty. NULL
		// and empty hashes are excluded from the partial index, allowing repeat
		// events emitted by writers that omit a deduplication hash.
		field.String("event_hash").Optional().Nillable().MaxLen(128),
		// Immutable event timestamp.
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes supports job chronology and event-type filtering, with optional
// job-scoped hash uniqueness. The hash constraint applies to nonempty values;
// unkeyed events can have multiple rows for the same job and kind.
func (BatchImageEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_id", "created_at"),
		index.Fields("event_type"),
		index.Fields("job_id", "event_hash").Unique().Annotations(entsql.IndexWhere("event_hash IS NOT NULL AND event_hash <> ''")),
	}
}
