package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Upstream credentials belong to a provider that can participate in several
// client-facing groups. Scheduling also reads persistent activation,
// expiry, and cooldown state, while Spark shadows reference a parent for
// credentials and retain a separate quota dimension and scheduling switch.

// Provider declares upstream credentials, egress settings, and persistent scheduling state.
type Provider struct {
	ent.Schema // generated entity definition
}

// Annotations maps upstream provider records to the providers database table.
func (Provider) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "providers"},
	}
}

// Mixin supplies provider timestamps and default soft-deletion filtering.
// Ordinary Ent queries exclude deleted providers; SkipSoftDelete contexts
// allow maintenance to inspect retained rows or remove them physically.
func (Provider) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields declares credentials, configuration, scheduling state, and shadow
// identity. Generated inserts use empty credential and metadata maps,
// active and schedulable state, three concurrent slots, and global quota;
// provider services validate platform-specific configuration before persistence.
func (Provider) Fields() []ent.Field {
	return []ent.Field{
		// Administrator provider display name.
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		// Optional administrator provider notes.
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// platform and type select the upstream implementation and credential
		// interpretation. A group can contain providers with different platforms,
		// so each candidate retains these facts for capability checks and execution.
		// Upstream platform identifier.
		field.String("platform").
			MaxLen(50).
			NotEmpty(),

		// Upstream authentication type.
		field.String("type").
			MaxLen(20).
			NotEmpty(),

		// credentials holds authentication and execution configuration as JSON.
		// The platform and type determine the supported keys, including API tokens,
		// OAuth data, service-account material, and provider-specific model rules.
		field.JSON("credentials", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// extra stores provider metadata and runtime observations. Record helpers
		// read platform-specific keys for capabilities, privacy, quotas, and
		// monitoring; the schema supplies a fresh empty map for generated inserts.
		field.JSON("extra", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// proxy_id selects the optional egress proxy. Proxy fallback can replace
		// this field and retain proxy_fallback_origin_id so administrators can
		// restore the original selection after an expiry fallback.
		field.Int64("proxy_id").
			Optional().
			Nillable(),
		// Recorded proxy-fallback origin.
		field.Int64("proxy_fallback_origin_id").
			Optional().Nillable().
			Comment("Original proxy id replaced by expiry-fallback; for manual revert. NULL = not in fallback."),

		// concurrency sets the provider physical-slot ceiling. Zero is unlimited
		// to the concurrency limiter; load_factor changes scoring load but does
		// not replace the slot limit used for admission.
		field.Int("concurrency").
			Default(3),

		// load_factor is the optional load-scoring denominator. EffectiveLoadFactor
		// uses a positive value first, then positive concurrency, then one;
		// physical slot acquisition continues to use concurrency.
		field.Int("load_factor").Optional().Nillable(),

		// priority orders providers across their group memberships, with lower
		// values preferred. ProviderGroup stores membership only, so the provider
		// has one priority value shared by every group that contains it.
		field.Int("priority").
			Default(50),

		// rate_multiplier scales provider costs and quota accounting. Zero is
		// a valid free-cost multiplier; Record.BillingRateMultiplier treats absent
		// or negative values as one. User charges use their own funding multipliers.
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0),

		// Stored provider activation state.
		field.String("status").
			MaxLen(20).
			Default(provider.StatusActive),

		// Recorded provider error details.
		field.String("error_message").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Latest recorded provider usage.
		field.Time("last_used_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// expires_at and auto_pause_on_expired control lifetime-based scheduling
		// exclusion. Record.IsSchedulable rejects a timestamp at or before now
		// when auto-pausing is enabled; a missing expiry leaves the other
		// provider qualification checks applicable.
		// Optional provider expiry timestamp.
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("Provider expiration time (NULL means no expiration).").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Expiry-based scheduling exclusion flag.
		field.Bool("auto_pause_on_expired").
			Default(true).
			Comment("Auto pause scheduling when provider expires."),

		// schedulable is the stored scheduling switch consulted with active status
		// and cooldowns. Setting it false excludes the provider even when its
		// credentials and other qualification checks remain usable.
		field.Bool("schedulable").
			Default(true),

		// rate_limited_at and rate_limit_reset_at retain the observed limit event
		// and its deadline. Record.IsRateLimited and IsSchedulable use a future
		// reset timestamp to keep the provider excluded until that cooldown ends.
		// Observed rate-limit time.
		field.Time("rate_limited_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// Rate-limit cooldown deadline.
		field.Time("rate_limit_reset_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// overload_until records the overload cooldown deadline. Record.IsSchedulable
		// rejects the provider while that deadline is in the future and lets
		// other qualification checks decide once it has elapsed.
		field.Time("overload_until").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// temp_unschedulable_until and temp_unschedulable_reason retain temporary
		// exclusion state. Record.IsSchedulable rejects a future deadline, and
		// credential shadows also consult the parent deadline before sharing
		// its credentials and transport.
		// Recorded temporary-exclusion deadline.
		field.Time("temp_unschedulable_until").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// Recorded temporary-exclusion reason.
		field.String("temp_unschedulable_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// session_window_start, session_window_end, and session_window_status
		// retain the sampled upstream session window. EstimateSetupTokenUsage
		// uses the end and status when reporting five-hour usage for setup-token
		// providers, with separately stored utilization taking precedence.
		// Sampled session-window start.
		field.Time("session_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Sampled session-window end.
		field.Time("session_window_end").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Sampled session-window status.
		field.String("session_window_status").
			Optional().
			Nillable().
			MaxLen(20),

		// parent_provider_id and quota_dimension identify a linked Spark shadow.
		// The shadow resolves credentials through its parent while maintaining
		// its own Spark quota and scheduling state; migration 176 enforces
		// one undeleted shadow per parent.
		// Optional credential-parent identifier.
		field.Int64("parent_provider_id").Optional().Nillable().
			Comment("Parent provider id for a linked spark shadow (NULL = normal)."),
		// Global-or-Spark quota selection.
		field.Enum("quota_dimension").Values("global", "spark").Default("global").
			Comment("'global' (default) or 'spark' (shadow reads codex_bengalfox)."),
	}
}

// Edges declares group membership, optional egress, shadow ancestry, and
// usage relationships. Membership uses a join entity, and the parent edge
// restricts hard deletion while child-provider references remain.
func (Provider) Edges() []ent.Edge {
	return []ent.Edge{
		// provider_groups stores many-to-many membership, allowing one provider
		// to serve several groups and each group to contain several providers.
		edge.To("groups", Group.Type).
			Through("provider_groups", ProviderGroup.Type),
		edge.To("proxy", Proxy.Type).
			Field("proxy_id").
			Unique(),
		// The nullable parent_provider_id backs the parent/children relationship.
		// Restrict prevents a hard parent deletion while referencing children
		// remain; migration 176 supplies active shadow uniqueness separately.
		edge.To("children", Provider.Type).
			Annotations(entsql.OnDelete(entsql.Restrict)).
			From("parent").
			Field("parent_provider_id").
			Unique(),
		edge.To("usage_logs", UsageLog.Type),
	}
}

// Indexes declares provider filtering and scheduling lookup indexes for
// Ent. Production SQL migrations supply partial scheduling indexes and
// the one-shadow-per-parent constraint, so these declarations do not
// describe every deployed index.
func (Provider) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("platform"),
		index.Fields("type"),
		index.Fields("status"),
		index.Fields("proxy_id"),
		index.Fields("priority"),
		index.Fields("last_used_at"),
		index.Fields("schedulable"),
		index.Fields("rate_limited_at"),
		index.Fields("rate_limit_reset_at"),
		index.Fields("overload_until"),
		index.Fields("platform", "priority"),
		index.Fields("priority", "status"),
		index.Fields("deleted_at"),
		index.Fields("parent_provider_id"),
	}
}
