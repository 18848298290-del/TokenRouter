package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Group holds the schema definition for the Group entity.
type Group struct {
	ent.Schema
}

func (Group) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "groups"},
	}
}

func (Group) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (Group) Fields() []ent.Field {
	return []ent.Field{
		field.JSON("localization", routing.GroupLocalization{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// A partial unique index on active rows allows names to be reused after soft deletion.
		// See migration 016_soft_delete_partial_unique_indexes.sql.
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("description").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0),
		field.Bool("is_exclusive").
			Default(false),
		field.String("status").
			MaxLen(20).
			Default(routing.StatusActive),
		field.String("duplicate_operation_id").
			MaxLen(64).
			Optional().
			Nillable().
			Immutable().
			Comment("Internal idempotent recovery identifier, excluded from API responses"),

		// Groups select the basic or advanced scheduler; basic is the default.
		field.String("scheduler_type").
			MaxLen(16).
			Default("basic").
			Comment("Group scheduler type: basic or advanced"),
		field.JSON("advanced_scheduler_overrides", policy.GroupAdvancedSchedulerOverrides{}).
			Default(policy.GroupAdvancedSchedulerOverrides{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Advanced scheduler overrides; unset fields inherit gateway settings"),
		field.String("display_brand").
			MaxLen(50).
			Default("").
			Comment("Brand displayed in the model marketplace"),

		// Image generation permissions for Antigravity and Gemini.
		field.Bool("allow_image_generation").
			Default(false).
			Comment("Allows image generation in this group"),
		field.Bool("allow_batch_image_generation").
			Default(false).
			Comment("Allows batch image generation in this group"),

		field.JSON("routing_policy", json.RawMessage{}).Optional().Comment("Model and feature policy for this group"),

		// Claude Code client restriction (migration 029).
		field.Bool("claude_code_only").
			Default(false).
			Comment("Restricts access to Claude Code clients"),
		field.Int64("fallback_group_id").
			Optional().
			Nillable().
			Comment("Fallback group ID for non-Claude Code requests"),
		field.Int64("fallback_group_id_on_invalid_request").
			Optional().
			Nillable().
			Comment("Fallback group ID for invalid requests"),
		field.Int64("unavailable_fallback_group_id").
			Optional().
			Nillable().
			Comment("Preferred fallback group ID when this group is unavailable"),

		// Model routing configuration (migration 040).
		field.JSON("model_routing", map[string][]int64{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Model patterns mapped to preferred provider ID lists"),

		// Model routing switch (migration 041).
		field.Bool("model_routing_enabled").
			Default(false).
			Comment("Enables model routing configuration"),

		// MCP XML protocol injection (migration 042).
		field.Bool("mcp_xml_inject").
			Default(true).
			Comment("Injects MCP XML tool-call instructions for Antigravity"),

		// Supported model families (migration 046).
		field.JSON("supported_model_scopes", []string{}).
			Default([]string{"claude", "gemini_text", "gemini_image"}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Supported model families: claude, gemini_text, gemini_image"),

		// Group display order (migration 052).
		field.Int("sort_order").
			Default(0).
			Comment("Group display order; lower values appear first"),

		// OpenAI Messages dispatch (migration 069).
		field.Bool("allow_messages_dispatch").
			Default(false).
			Comment("Allows /v1/messages dispatch to this OpenAI group"),
		field.JSON("allowed_protocols", []protocol.ProtocolID{}).
			Default([]protocol.ProtocolID{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Complete set of client protocols and feature entry points allowed for this group"),
		// Protocol conversion and Responses image policy are independent group settings.
		field.JSON("protocol_fallbacks", map[protocol.ProtocolID][]protocol.ProtocolID{}).
			Default(map[protocol.ProtocolID][]protocol.ProtocolID{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("responses_image_policy").Default("inherit"),
		field.Bool("allow_live").
			Default(false).
			Comment("Allows Live access through this OpenAI group"),
		field.String("openai_fast_policy").
			Default("follow_request").
			Comment("Group acceleration policy: follow_request/force_priority/force_ultrafast/force_off"),
		field.Bool("force_openai_fast").
			Default(false).
			Comment("Forces service_tier=priority on requests through this OpenAI group"),
		field.Bool("require_oauth_only").
			Default(false).
			Comment("Restricts group membership to providers whose type is not apikey"),
		field.Bool("require_privacy_set").
			Default(false).
			Comment("Restricts scheduling to providers with privacy successfully configured"),
		field.String("default_mapped_model").
			MaxLen(100).
			Default("").
			Comment("Default model ID when provider-level mapping has no match"),
		field.JSON("models_list_config", accessview.GroupModelsListConfig{}).
			Default(accessview.GroupModelsListConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Custom /v1/models display configuration; affects list responses, not scheduling"),
		field.JSON("availability_probe_config", accessview.GroupAvailabilityProbeConfig{}).
			Default(accessview.GroupAvailabilityProbeConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Active availability probe configuration for this group"),

		// A positive group RPM limit caps each user in this group; the user-wide limit is checked afterward.
		field.Int("rpm_limit").
			Default(0).
			Comment("Per-user RPM limit in this group; zero disables the group limit. The user-wide limit still applies"),

		// Maximum reasoning effort for OpenAI/Codex requests; empty means unlimited.
		field.String("max_reasoning_effort").
			MaxLen(20).
			Default("").
			Comment("Maximum OpenAI reasoning effort: minimal/low/medium/high/xhigh/max"),
		field.String("max_reasoning_effort_over_limit").
			MaxLen(20).
			Default("downgrade").
			Comment("Over-limit action: downgrade lowers reasoning effort; deny rejects the request"),
		field.JSON("reasoning_effort_mappings", []routing.ReasoningEffortMapping{}).
			Default([]routing.ReasoningEffortMapping{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("OpenAI reasoning effort mappings by exact model, prefix, or suffix; applied before the maximum"),

		// Reject explicit sessions already owned by another group when isolation is enabled.
		field.Bool("session_isolation_enabled").
			Default(false).
			Comment("Enables session isolation"),
	}
}

func (Group) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("api_keys", APIKey.Type),
		edge.To("api_key_composite_groups", APIKeyCompositeGroup.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("usage_logs", UsageLog.Type),
		edge.From("providers", Provider.Type).
			Ref("groups").
			Through("provider_groups", ProviderGroup.Type),
		edge.From("allowed_users", User.Type).
			Ref("allowed_groups").
			Through("user_allowed_groups", UserAllowedGroup.Type),
		edge.From("disabled_public_users", User.Type).
			Ref("disabled_public_groups").
			Through("user_disabled_public_groups", UserDisabledPublicGroup.Type),
		// fallback_group_id is stored as a field without an Ent edge.
		// Multiple groups can reference the same fallback group.
	}
}

func (Group) Indexes() []ent.Index {
	return []ent.Index{
		// Active-name uniqueness is enforced by the SQL migration's partial index.
		index.Fields("status"),
		index.Fields("is_exclusive"),
		index.Fields("deleted_at"),
		index.Fields("sort_order"),
		index.Fields("session_isolation_enabled"),
		index.Fields("duplicate_operation_id").
			Unique().
			StorageKey("idx_groups_duplicate_operation_id_active").
			Annotations(entsql.IndexWhere("duplicate_operation_id IS NOT NULL AND deleted_at IS NULL")),
	}
}
