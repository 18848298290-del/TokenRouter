package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// CreativeRun defines asynchronous Creative Studio jobs.
//
// This table stores task metadata and billing snapshots.
// Image bytes, masks, plaintext prompts, and provider responses belong in temporary Redis storage.
// Persist prompts as SHA-256 hashes in prompt_hash.
type CreativeRun struct {
	ent.Schema
}

func (CreativeRun) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "creative_runs"},
	}
}

func (CreativeRun) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (CreativeRun) Fields() []ent.Field {
	return []ent.Field{
		field.String("run_id").MaxLen(64).Immutable(),
		field.Int64("user_id"),
		// A missing workspace_id identifies a legacy job hidden from user workspace queries.
		field.String("workspace_id").Optional().Nillable().MaxLen(64),
		field.Int64("group_id"),
		// api_key_id references the hidden execution key with managed_by = 'creative_studio'.
		field.Int64("api_key_id"),
		// The worker fills provider_id during execution.
		field.Int64("provider_id").Optional().Nillable(),
		// platform snapshots the execution provider's platform so later group or provider edits cannot change it.
		field.String("platform").MaxLen(32).Default(""),
		field.String("model").MaxLen(128),
		// requested_model stores the client value; model stores the billing and routing model.
		field.String("requested_model").MaxLen(128).Default(""),
		field.String("operation").MaxLen(16),
		field.Int("requested_output_count").Default(1),
		field.String("image_size").MaxLen(16).Default("1K"),
		field.String("aspect_ratio").MaxLen(16).Default(""),
		field.String("response_mime_type").MaxLen(64).Default("image/png"),
		// Store the prompt's SHA-256 hash here; plaintext belongs in temporary storage.
		field.String("prompt_hash").MaxLen(64),
		// request_fingerprint hashes canonical JSON with SHA-256 for idempotency.
		field.String("request_fingerprint").MaxLen(64),
		field.String("idempotency_key").Optional().Nillable().MaxLen(255),
		field.String("status").MaxLen(20).Default("queued"),
		field.Float("estimated_cost").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		field.Float("hold_amount").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("actual_cost").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		// As with batch_image_jobs, reserve subscription funds before balance funds.
		field.Float("balance_hold_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		field.JSON("subscription_hold_allocations", []billing.BillingAllocation{}).
			Default(func() []billing.BillingAllocation { return []billing.BillingAllocation{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// Pricing snapshot: base unit price and multipliers for subscription and balance funding.
		field.Float("base_unit_price").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		field.Float("subscription_rate_multiplier").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(1),
		field.Float("balance_rate_multiplier").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(1),
		field.Bool("plan_group_rate_multiplier_enabled").Default(true),
		field.String("error_code").Optional().Nillable().MaxLen(128),
		field.String("error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		// release_target_status is the terminal status after release_pending completes.
		field.String("release_target_status").MaxLen(20).Default("failed"),
		field.Int("attempt_count").Default(0),
		// Like batch_image_jobs.allowance_reserved, set this on reservation and reset it on capture or release.
		field.Bool("allowance_reserved").Default(false),
		// provisioning_phase records the last completed creation step so crash recovery can resume.
		field.String("provisioning_phase").MaxLen(32).Default("created"),
		field.Time("provider_result_recorded_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("settlement_attempt_count").Default(0),
		field.Int("release_attempt_count").Default(0),
		field.Time("next_reconcile_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_reconcile_error").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Increment version on each state transition for optimistic locking.
		field.Int64("version").Default(1),
		field.Time("started_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("cancelled_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CreativeRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id").Unique(),
		index.Fields("user_id", "created_at"),
		index.Fields("user_id", "workspace_id", "created_at"),
		index.Fields("status").Annotations(entsql.IndexWhere("status IN ('queued', 'running')")),
		index.Fields("user_id", "workspace_id", "idempotency_key").
			Unique().
			Annotations(entsql.IndexWhere("workspace_id IS NOT NULL AND idempotency_key IS NOT NULL AND idempotency_key <> ''")),
	}
}
