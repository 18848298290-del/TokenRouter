package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// HTTP deduplication and maintenance leases use scoped keys to identify a
// shared coordination record. Request fingerprints distinguish incompatible
// HTTP payloads, while maintenance claims store an operation identity and
// ownership token so lease renewal can distinguish the current acquisition
// from an earlier worker.

// IdempotencyRecord stores request deduplication outcomes and maintenance lease ownership.
type IdempotencyRecord struct {
	ent.Schema // generated entity definition
}

// Annotations maps HTTP deduplication and maintenance leases to the idempotency_records table.
func (IdempotencyRecord) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "idempotency_records"},
	}
}

// Mixin supplies creation and update timestamps for stored deduplication records.
func (IdempotencyRecord) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields declares deduplication identity, processing state, lease, and cached outcome.
// The caller supplies status and expiration. Response fields, failure reason,
// and lock expiration remain nullable until the coordinator records those phases.
func (IdempotencyRecord) Fields() []ent.Field {
	return []ent.Field{
		// scope and idempotency_key_hash identify the stored operation together.
		// For HTTP requests, request_fingerprint describes method, route, actor,
		// and payload. Maintenance leases use it as their operation identifier
		// when checking ownership during renewal and completion.
		// Operation deduplication scope.
		field.String("scope").MaxLen(128),
		// Client request-key digest.
		field.String("idempotency_key_hash").MaxLen(64),
		// Request or operation identity.
		field.String("request_fingerprint").MaxLen(64),
		// status identifies processing, succeeded, or failed_retryable records in
		// the coordinator. Success permits response replay, while retryable
		// failures can be reclaimed after their lock expires.
		field.String("status").MaxLen(32),
		// response_status and response_body retain completed HTTP results. The
		// HTTP coordinator writes redacted JSON and may truncate it to its
		// storage limit; replay reports an error when decoding fails. Maintenance
		// processing leases use response_body for the acquisition ownership token.
		// Cached HTTP status.
		field.Int("response_status").Optional().Nillable(),
		// Cached result or owner token.
		field.String("response_body").Optional().Nillable(),
		// Recorded failure reason.
		field.String("error_reason").MaxLen(128).Optional().Nillable(),
		// locked_until limits processing ownership. Reclaim operations require an
		// expired or absent lock before replacing it with the next lease.
		field.Time("locked_until").Optional().Nillable(),
		// Record retention endpoint.
		field.Time("expires_at"),
	}
}

// Indexes enforces scoped idempotency-key uniqueness and supports expiry cleanup and
// processing-lock scans. The fingerprint remains separate so the coordinator can
// compare a repeated request with the operation already stored under that key.
func (IdempotencyRecord) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scope", "idempotency_key_hash").Unique(),
		index.Fields("expires_at"),
		index.Fields("status", "locked_until"),
	}
}
