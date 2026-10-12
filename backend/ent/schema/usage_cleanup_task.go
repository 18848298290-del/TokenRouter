package schema

import (
	"encoding/json"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// Administrators request usage deletion through persistent cleanup tasks with
// serialized filters. Task records retain who requested deletion, how many
// rows were deleted, and the cancellation or failure outcome, so partially
// completed work remains visible after the worker stops.

// UsageCleanupTask stores usage-record cleanup requests, progress, and execution outcomes.
type UsageCleanupTask struct {
	ent.Schema // generated entity definition
}

// Annotations maps cleanup request persistence to the usage_cleanup_tasks database table.
func (UsageCleanupTask) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_cleanup_tasks"},
	}
}

// Mixin supplies insertion and progress-update timestamps for usage cleanup tasks.
func (UsageCleanupTask) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields describes the cleanup request, deleted-row count, and lifecycle metadata.
// Callers supply filters, creator, and supported status. Progress starts at
// zero; cancellation, execution, and failure details remain nullable until recorded.
func (UsageCleanupTask) Fields() []ent.Field {
	return []ent.Field{
		// status is pending before claim, running during deletion, succeeded
		// after completion, failed after an execution error, or canceled after
		// an administrator requests that the worker stop.
		field.String("status").
			MaxLen(20).
			Validate(validateUsageCleanupStatus),
		// Serialized deletion filters.
		field.JSON("filters", json.RawMessage{}),
		// Requesting administrator identity.
		field.Int64("created_by"),
		// deleted_rows retains completed deletion progress even when the task
		// later fails or is canceled. Usage cleanup uses positive progress to
		// schedule aggregate recomputation during its final handling.
		field.Int64("deleted_rows").
			Default(0),
		// Recorded execution failure.
		field.String("error_message").
			Optional().
			Nillable(),
		// canceled_by and canceled_at identify the cancellation request together.
		// They can remain unset before cancellation, and the worker checks
		// cancellation state while it advances deletion batches.
		// Canceling administrator identity.
		field.Int64("canceled_by").
			Optional().
			Nillable(),
		// Cancellation request timestamp.
		field.Time("canceled_at").
			Optional().
			Nillable(),
		// First execution timestamp.
		field.Time("started_at").
			Optional().
			Nillable(),
		// Final outcome timestamp.
		field.Time("finished_at").
			Optional().
			Nillable(),
	}
}

// Indexes supports chronological task listing, pending-task scans, and cancellation lookup.
func (UsageCleanupTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
		index.Fields("created_at"),
		index.Fields("canceled_at"),
	}
}

// validateUsageCleanupStatus accepts the five persisted cleanup lifecycle states.
// Values are matched exactly; unsupported status strings return an error
// during generated Ent field validation before the mutation is saved.
func validateUsageCleanupStatus(status string) error {
	switch status {
	case "pending", "running", "succeeded", "failed", "canceled":
		return nil
	default:
		return fmt.Errorf("invalid usage cleanup status: %s", status)
	}
}
