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

// A group defines the product permissions and pricing multiplier chosen by
// a client key, while its members can use different upstream platforms.
// Stored protocol selections determine new entry permissions, and legacy
// booleans remain compatibility values for task and pricing consumers.

// Group declares product settings, provider membership, and user access relationships.
type Group struct {
	ent.Schema // generated entity definition
}

// Annotations maps persisted product groups to the groups database table.
func (Group) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "groups"},
	}
}

// Mixin supplies timestamps and default soft-deletion filtering for groups.
// Ordinary Ent queries exclude deleted groups; SkipSoftDelete contexts
// allow maintenance to inspect retained rows or delete them physically.
func (Group) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields declares product configuration, client permissions, fallback
// targets, and scheduling choices. Generated inserts select basic scheduling,
// active status, inherited Responses image policy, and empty protocol
// selection; group management normalizes runtime configuration before saving.
func (Group) Fields() []ent.Field {
	return []ent.Field{
		// Localized group display copy.
		field.JSON("localization", routing.GroupLocalization{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// name identifies the business group. Migration 016 creates a unique
		// index for undeleted names, allowing a later group to reuse a name
		// after the earlier group has been soft-deleted.
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		// Optional administrator group description.
		field.String("description").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Base user pricing multiplier.
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0),
		// is_exclusive distinguishes explicitly granted groups from public groups.
		// Access resolution combines this value with user grants and public-group
		// exclusions before admitting a key to the selected group.
		field.Bool("is_exclusive").
			Default(false),
		// Stored group activation state.
		field.String("status").
			MaxLen(20).
			Default(routing.StatusActive),
		// duplicate_operation_id identifies a recoverable group-copy operation.
		// It is immutable once written, and its partial unique index covers
		// non-null values on undeleted groups to resolve repeated operation requests.
		field.String("duplicate_operation_id").
			MaxLen(64).
			Optional().
			Nillable().
			Immutable().
			Comment("Internal idempotent recovery identifier, excluded from API responses"),

		// scheduler_type chooses basic or advanced selection for this group.
		// The generated default is basic; advanced_scheduler_overrides applies
		// only when the selected mode uses the advanced scheduler.
		field.String("scheduler_type").
			MaxLen(16).
			Default("basic").
			Comment("Group scheduler type: basic or advanced"),
		// advanced_scheduler_overrides stores sparse advanced parameters. Missing
		// fields inherit gateway settings, while present false or zero values
		// remain configured overrides; basic selection ignores this object.
		field.JSON("advanced_scheduler_overrides", policy.GroupAdvancedSchedulerOverrides{}).
			Default(policy.GroupAdvancedSchedulerOverrides{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Advanced scheduler overrides; unset fields inherit gateway settings"),
		// Marketplace display brand.
		field.String("display_brand").
			MaxLen(50).
			Default("").
			Comment("Brand displayed in the model marketplace"),

		// allow_image_generation and allow_batch_image_generation are persisted
		// compatibility mirrors. NormalizeGroupProtocolPolicy derives them from
		// allowed_protocols so existing task and pricing consumers observe the
		// configured image and batch entry points.
		// Derived image-entry permission.
		field.Bool("allow_image_generation").
			Default(false).
			Comment("Allows image generation in this group"),
		// Derived batch-entry permission.
		field.Bool("allow_batch_image_generation").
			Default(false).
			Comment("Allows batch image generation in this group"),

		// routing_policy stores group model mappings, allowlists, and feature
		// settings as JSON. Runtime interpretation uses the group policy rules;
		// shared pricing configuration supplies prices separately from these
		// request-permission and mapping choices.
		field.JSON("routing_policy", json.RawMessage{}).Optional().Comment("Model and feature policy for this group"),

		// claude_code_only and fallback_group_id govern restricted-client access.
		// ResolveClientGroup follows the configured fallback for a rejected client,
		// and the entry point rechecks the target group permissions and funding
		// before executing the request.
		// Claude Code client restriction.
		field.Bool("claude_code_only").
			Default(false).
			Comment("Restricts access to Claude Code clients"),
		// fallback_group_id stores the restricted-client target as a scalar ID.
		// This schema defines no Ent fallback edge, and several source groups
		// can reference the same target.
		field.Int64("fallback_group_id").
			Optional().
			Nillable().
			Comment("Fallback group ID for non-Claude Code requests"),
		// fallback_group_id_on_invalid_request names a target for qualified
		// upstream request failures. Text attempt handlers reauthorize the target
		// and resolve its funding before switching groups, subject to their
		// failure and response-state eligibility checks.
		field.Int64("fallback_group_id_on_invalid_request").
			Optional().
			Nillable().
			Comment("Fallback group ID for invalid requests"),
		// unavailable_fallback_group_id names the administrator-selected target
		// for a key that enables unavailable-group fallback. Key resolution
		// revalidates the target group and stops when no usable target is configured.
		field.Int64("unavailable_fallback_group_id").
			Optional().
			Nillable().
			Comment("Preferred fallback group ID when this group is unavailable"),

		// model_routing and model_routing_enabled select preferred providers for
		// model patterns. The enabled flag activates the stored ordered provider
		// lists; these preferences remain subject to provider qualification and
		// actual scheduling availability.
		// Pattern-to-provider preference lists.
		field.JSON("model_routing", map[string][]int64{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Model patterns mapped to preferred provider ID lists"),

		// Provider-preference activation flag.
		field.Bool("model_routing_enabled").
			Default(false).
			Comment("Enables model routing configuration"),

		// mcp_xml_inject retains the legacy Antigravity setting in stored groups
		// and API snapshots. Current Antigravity conversion applies its MCP tool
		// handling independently of this persisted compatibility switch.
		field.Bool("mcp_xml_inject").
			Default(true).
			Comment("Injects MCP XML tool-call instructions for Antigravity"),

		// supported_model_scopes retains the legacy Antigravity family selection.
		// Current routing carries these values through storage and snapshots but
		// does not use them to filter candidate providers.
		field.JSON("supported_model_scopes", []string{}).
			Default([]string{"claude", "gemini_text", "gemini_image"}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Supported model families: claude, gemini_text, gemini_image"),

		// Administrative group display order.
		field.Int("sort_order").
			Default(0).
			Comment("Group display order; lower values appear first"),

		// allow_messages_dispatch is a persisted Messages compatibility mirror.
		// NormalizeGroupProtocolPolicy derives it from allowed_protocols before
		// repository writes; client admission reads the complete protocol selection.
		field.Bool("allow_messages_dispatch").
			Default(false).
			Comment("Allows /v1/messages dispatch to this OpenAI group"),
		// allowed_protocols contains the complete set of permitted client entry
		// points. AllowsClientProtocol checks membership directly, and an empty
		// selection closes every new entry while existing resource operations
		// retain their own ownership checks.
		field.JSON("allowed_protocols", []protocol.ProtocolID{}).
			Default([]protocol.ProtocolID{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Complete set of client protocols and feature entry points allowed for this group"),
		// protocol_fallbacks selects ordered conversion targets per entry point.
		// Missing entries use automatic route selection, empty arrays require
		// native execution, and configured arrays limit the eligible conversion
		// targets and their order.
		field.JSON("protocol_fallbacks", map[protocol.ProtocolID][]protocol.ProtocolID{}).
			Default(map[protocol.ProtocolID][]protocol.ProtocolID{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// responses_image_policy controls Responses image-tool handling separately
		// from Images entry permissions. The inherit default defers to provider
		// and global policy; enabled, disabled, and block select the other
		// supported handling modes.
		field.String("responses_image_policy").Default("inherit"),
		// allow_live mirrors the Live entry in allowed_protocols.
		// NormalizeGroupProtocolPolicy writes this compatibility value alongside
		// the other derived protocol permissions before repository persistence.
		field.Bool("allow_live").
			Default(false).
			Comment("Allows Live access through this OpenAI group"),
		// openai_fast_policy selects follow_request, force_priority, force_ultrafast,
		// or force_off. EffectiveOpenAIFastPolicy prefers a nonempty named choice
		// and consults force_openai_fast only when that choice is missing.
		field.String("openai_fast_policy").
			Default("follow_request").
			Comment("Group acceleration policy: follow_request/force_priority/force_ultrafast/force_off"),
		// Legacy Fast forcing flag.
		field.Bool("force_openai_fast").
			Default(false).
			Comment("Forces service_tier=priority on requests through this OpenAI group"),
		// require_oauth_only excludes API-key providers during membership copying.
		// Candidate selection separately requires Record.IsOAuth, which accepts
		// OAuth and setup-token types, so the copy filter alone does not establish
		// runtime scheduling qualification.
		field.Bool("require_oauth_only").
			Default(false).
			Comment("Restricts group membership to providers whose type is not apikey"),
		// require_privacy_set applies the provider privacy check when enabled.
		// Candidate selection reads the platform-specific confirmed privacy state
		// alongside the provider's other scheduling qualification checks.
		field.Bool("require_privacy_set").
			Default(false).
			Comment("Restricts scheduling to providers with privacy successfully configured"),
		// default_mapped_model retains the legacy default-model configuration.
		// Group storage, management responses, duplication, and authentication
		// snapshots carry this value; current model execution does not read it.
		field.String("default_mapped_model").
			MaxLen(100).
			Default("").
			Comment("Default model ID when provider-level mapping has no match"),
		// models_list_config customizes the models-list response when enabled.
		// The display selection does not establish provider execution capability;
		// request scheduling still applies group and provider qualification rules.
		field.JSON("models_list_config", accessview.GroupModelsListConfig{}).
			Default(accessview.GroupModelsListConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Custom /v1/models display configuration; affects list responses, not scheduling"),
		// Configured active availability probes.
		field.JSON("availability_probe_config", accessview.GroupAvailabilityProbeConfig{}).
			Default(accessview.GroupAvailabilityProbeConfig{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Active availability probe configuration for this group"),

		// rpm_limit caps each user within this group when positive. RPMAdmission
		// uses a supplied user-group override in preference to this value, then
		// checks the independent positive user-wide limit after group admission.
		field.Int("rpm_limit").
			Default(0).
			Comment("Per-user RPM limit in this group; zero disables the group limit. The user-wide limit still applies"),

		// max_reasoning_effort and max_reasoning_effort_over_limit govern effort
		// capping after reasoning_effort_mappings. An empty maximum imposes no cap;
		// downgrade rewrites an excessive client choice and deny rejects it.
		// Configured maximum reasoning effort.
		field.String("max_reasoning_effort").
			MaxLen(20).
			Default("").
			Comment("Maximum OpenAI reasoning effort: minimal/low/medium/high/xhigh/max"),
		// Excess-effort handling mode.
		field.String("max_reasoning_effort_over_limit").
			MaxLen(20).
			Default("downgrade").
			Comment("Over-limit action: downgrade lowers reasoning effort; deny rejects the request"),
		// reasoning_effort_mappings rewrites client-supplied effort for matching
		// model rules before the maximum is applied. Exact, prefix, and suffix
		// model conditions scope each stored mapping.
		field.JSON("reasoning_effort_mappings", []routing.ReasoningEffortMapping{}).
			Default([]routing.ReasoningEffortMapping{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("OpenAI reasoning effort mappings by exact model, prefix, or suffix; applied before the maximum"),

		// session_isolation_enabled rejects designated sessions already attributed
		// to another group. The default leaves that group-isolation check disabled
		// while the request still undergoes its other admission rules.
		field.Bool("session_isolation_enabled").
			Default(false).
			Comment("Enables session isolation"),
	}
}

// Edges declares key, usage, provider, and user access relationships.
// Provider membership uses a join entity, and hard group deletion cascades
// the composite-key mappings that reference that group.
func (Group) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("api_keys", APIKey.Type),
		edge.To("api_key_composite_groups", APIKeyCompositeGroup.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("usage_logs", UsageLog.Type),
		// Provider membership uses provider_groups, allowing providers from
		// different platforms to belong to the same client-facing group.
		edge.From("providers", Provider.Type).
			Ref("groups").
			Through("provider_groups", ProviderGroup.Type),
		edge.From("allowed_users", User.Type).
			Ref("allowed_groups").
			Through("user_allowed_groups", UserAllowedGroup.Type),
		edge.From("disabled_public_users", User.Type).
			Ref("disabled_public_groups").
			Through("user_disabled_public_groups", UserDisabledPublicGroup.Type),
	}
}

// Indexes declares group filtering and recoverable-copy indexes for Ent.
// Name uniqueness belongs to the SQL migration partial index, while the
// declared duplicate-operation index excludes deleted groups and missing
// operation identifiers.
func (Group) Indexes() []ent.Index {
	return []ent.Index{
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
