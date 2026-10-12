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

// UsageCleanupTask defines usage-record cleanup jobs and their progress.
type UsageCleanupTask struct {
	ent.Schema
}

func (UsageCleanupTask) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_cleanup_tasks"},
	}
}

func (UsageCleanupTask) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (UsageCleanupTask) Fields() []ent.Field {
	return []ent.Field{
		field.String("status").
			MaxLen(20).
			Validate(validateUsageCleanupStatus),
		field.JSON("filters", json.RawMessage{}),
		field.Int64("created_by"),
		field.Int64("deleted_rows").
			Default(0),
		field.String("error_message").
			Optional().
			Nillable(),
		field.Int64("canceled_by").
			Optional().
			Nillable(),
		field.Time("canceled_at").
			Optional().
			Nillable(),
		field.Time("started_at").
			Optional().
			Nillable(),
		field.Time("finished_at").
			Optional().
			Nillable(),
	}
}

func (UsageCleanupTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
		index.Fields("created_at"),
		index.Fields("canceled_at"),
	}
}

func validateUsageCleanupStatus(status string) error {
	switch status {
	case "pending", "running", "succeeded", "failed", "canceled":
		return nil
	default:
		return fmt.Errorf("invalid usage cleanup status: %s", status)
	}
}
