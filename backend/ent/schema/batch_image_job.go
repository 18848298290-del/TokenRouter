package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// batch_image_job.go defines durable metadata for batch submission, provider
// polling, result indexing, and settlement. Persisted funding choices,
// provider references, result counts, and cleanup timestamps let those phases
// resume using the recorded job and distinguish hidden jobs from deleted
// output.

// BatchImageJob stores asynchronous batch execution metadata and funding snapshots.
type BatchImageJob struct {
	ent.Schema // generated entity definition
}

// Annotations maps generated batch-job operations to the batch_image_jobs table.
func (BatchImageJob) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "batch_image_jobs"},
	}
}

// Fields describes job ownership, execution references, funding, progress, and
// retention metadata. Generated Ent creation starts in created state with
// auto funding and USD currency; optional amounts and timestamps describe
// phases that have not yet recorded those values.
func (BatchImageJob) Fields() []ent.Field {
	return []ent.Field{
		// Immutable public identity.
		field.String("batch_id").MaxLen(64).Immutable(),
		// Resource owner identity.
		field.Int64("user_id"),
		// billing_user_id records the payer supplied by the SQL job repository.
		// The Ent declaration permits omission. Funding's BillingUserID uses
		// user_id when the recorded payer is nonpositive.
		field.Int64("billing_user_id").Optional(),
		// Optional tenant identity.
		field.Int64("team_id").Optional().Nillable(),
		// Submitting key identity.
		field.Int64("api_key_id").Optional().Nillable(),
		// Bound provider identity.
		field.Int64("provider_id").Optional().Nillable(),
		// billing_mode and preferred_subscription_id capture the submitting key's
		// funding choice. Batch funding commands use these job values for later
		// reservation, capture, and release after the key's settings may change.
		// Submitted funding mode.
		field.String("billing_mode").MaxLen(32).Default("auto"),
		// Selected subscription identity.
		field.Int64("preferred_subscription_id").Optional().Nillable(),
		// Batch backend kind.
		field.String("platform").MaxLen(32),
		// Submitted upstream model.
		field.String("model").MaxLen(128),
		// Stored job label.
		field.String("task_name").MaxLen(255).Default(""),
		// Persisted processing phase.
		field.String("status").MaxLen(32).Default("created"),
		// Provider task reference.
		field.String("provider_job_name").Optional().Nillable().MaxLen(512),
		// Provider input reference.
		field.String("provider_input_ref").Optional().Nillable().MaxLen(1024),
		// Provider output reference.
		field.String("provider_output_ref").Optional().Nillable().MaxLen(1024),
		// Managed input URI.
		field.String("gcs_input_uri").Optional().Nillable().MaxLen(1024),
		// Managed output URI.
		field.String("gcs_output_uri").Optional().Nillable().MaxLen(1024),
		// item_count is the submitted request count. success_count, fail_count,
		// and cancelled_count record result categories used by processing and
		// settlement to reconcile the submitted batch with its observed outcomes.
		// Submitted item count.
		field.Int("item_count"),
		// Successful item count.
		field.Int("success_count").Default(0),
		// Failed item count.
		field.Int("fail_count").Default(0),
		// Cancelled item count.
		field.Int("cancelled_count").Default(0),
		// estimated_cost, hold_amount, and actual_cost distinguish the expected
		// charge, reservation amount, and calculated settlement amount. The hold
		// and actual amounts can remain absent until their processing phase
		// supplies them; settled_at separately records settlement completion.
		// USD expected charge.
		field.Float("estimated_cost").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		// BuildHoldCommand prefers a non-nil hold_amount and otherwise uses
		// estimated_cost when building the job's reservation amount.
		// USD funding reservation.
		field.Float("hold_amount").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		// USD settlement charge.
		field.Float("actual_cost").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		// balance_hold_amount and subscription_hold_allocations record the
		// reserved funding sources together. Auto funding can use both sources;
		// later capture and release commands refer to this stored allocation
		// instead of selecting funding again from current key settings.
		// USD wallet reservation.
		field.Float("balance_hold_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		// Reserved subscription allocations.
		field.JSON("subscription_hold_allocations", []billing.BillingAllocation{}).
			Default(func() []billing.BillingAllocation { return []billing.BillingAllocation{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// subscription_rate_multiplier and balance_rate_multiplier retain
		// separate pricing factors for subscription and wallet funding. The
		// settlement adapter passes these recorded factors to billing for
		// version-2-or-later pricing snapshots, with the job's hold scaling.
		// Subscription pricing factor.
		field.Float("subscription_rate_multiplier").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(1),
		// Wallet pricing factor.
		field.Float("balance_rate_multiplier").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(1),
		// plan_group_rate_multiplier_enabled supplies the settlement adapter's
		// plan-rate choice. For version-2-or-later pricing snapshots, false sets
		// DisablePlanGroupRateMultiplier on the billing command built from this job.
		field.Bool("plan_group_rate_multiplier_enabled").Default(true),
		// allowance_reserved records whether the job estimate has entered
		// allowance accounting. SettlementStore writes true when reserving key
		// and eligible member allowances, then false during capture or release
		// through the batch funding participant in the settlement transaction.
		field.Bool("allowance_reserved").Default(false),
		// Stored charge currency.
		field.String("currency").MaxLen(16).Default("USD"),
		// Funding hold reference.
		field.String("hold_id").Optional().Nillable().MaxLen(128),
		// Client retry key.
		field.String("idempotency_key").Optional().Nillable().MaxLen(255),
		// Submitted request digest.
		field.String("request_hash").Optional().Nillable().MaxLen(128),
		// Settlement manifest digest.
		field.String("manifest_hash").Optional().Nillable().MaxLen(128),
		// Recorded retry count.
		field.Int("retry_count").Default(0),
		// version increases during status transitions and input or output cleanup.
		// The batch repository uses row locks for transitions; its funding
		// participant updates reservation metadata and allowance_reserved independently.
		field.Int("version").Default(0),
		// Output retention deadline.
		field.Time("output_expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Input cleanup timestamp.
		field.Time("input_deleted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Output cleanup timestamp.
		field.Time("output_deleted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Observed download timestamp.
		field.Time("downloaded_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// user_deleted_at hides the retained job from owner-facing lists when
		// populated. Output deletion is tracked separately by output_deleted_at
		// and output_deleted status, so visibility and storage cleanup remain distinct.
		field.Time("user_deleted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Latest failure code.
		field.String("last_error_code").Optional().Nillable().MaxLen(128),
		// Latest failure description.
		field.String("last_error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Immutable creation timestamp.
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Latest mutation timestamp.
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Provider submission timestamp.
		field.Time("submitted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Observed execution start.
		field.Time("started_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Recorded execution finish.
		field.Time("finished_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Recorded settlement completion.
		field.Time("settled_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes supports public job identity, owner/payer/team history, processing,
// and retention scans. Nonempty manifest hashes are unique; idempotency keys
// have a partial lookup index but are not declared unique here.
func (BatchImageJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id").Unique(),
		index.Fields("user_id", "created_at"),
		index.Fields("billing_user_id", "created_at"),
		index.Fields("team_id", "created_at"),
		index.Fields("status"),
		index.Fields("platform", "status"),
		index.Fields("idempotency_key").Annotations(entsql.IndexWhere("idempotency_key IS NOT NULL AND idempotency_key <> ''")),
		index.Fields("manifest_hash").Unique().Annotations(entsql.IndexWhere("manifest_hash IS NOT NULL AND manifest_hash <> ''")),
		index.Fields("output_expires_at"),
		index.Fields("downloaded_at"),
		index.Fields("user_deleted_at"),
	}
}
