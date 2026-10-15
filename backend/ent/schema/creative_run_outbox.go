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

// creative_run_outbox.go defines durable action records for retrying creative
// provisioning, settlement, and release. Each run-operation pair has one row,
// with availability and lease metadata used by the outbox repository to claim
// work and reject updates from a superseded claim.

// CreativeRunOutbox stores recoverable actions and their current processing lease.
type CreativeRunOutbox struct {
	ent.Schema // generated entity definition
}

// Annotations maps recoverable creative actions to the creative_run_outbox table.
func (CreativeRunOutbox) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "creative_run_outbox"}}
}

// Fields declares the run-operation identity, availability, claim, and retry
// metadata. Generated Ent inserts start pending and available at application time.
// Lease fields remain nullable until Claim records its token and expiration.
func (CreativeRunOutbox) Fields() []ent.Field {
	return []ent.Field{
		// Associated run identity.
		field.String("run_id").MaxLen(64),
		// Recoverable action kind.
		field.String("operation").MaxLen(32),
		// Pending, leased, done, or cancelled describes the outbox repository's
		// processing state. Claim selects pending rows or expired leases;
		// completion and cancellation retain their terminal state during Ensure.
		field.String("status").MaxLen(16).Default("pending"),
		// available_at delays claim eligibility until PostgreSQL current time
		// reaches it. Retry writes the next availability time while returning
		// a leased row to pending for another claim.
		field.Time("available_at").Default(time.Now),
		// lease_token and lease_until identify and time-limit the current claim.
		// Claim can replace an expired lease; Complete, Retry, and Cancel compare
		// the token and leased status before updating that row.
		// Claim ownership token.
		field.String("lease_token").Optional().Nillable().MaxLen(128),
		// Claim expiration timestamp.
		field.Time("lease_until").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Claim acquisition count.
		field.Int("attempt_count").Default(0),
		// Latest retry failure.
		field.String("last_error").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Immutable creation timestamp.
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Latest mutation timestamp.
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes enforces one action for each run and operation pair.
// Availability and run-status indexes support claim scans and locating
// recoverable actions associated with a particular creative run.
func (CreativeRunOutbox) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "operation").Unique(),
		index.Fields("available_at", "id"),
		index.Fields("run_id", "status"),
	}
}
