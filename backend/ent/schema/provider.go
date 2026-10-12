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

// Provider defines upstream credentials and scheduling state for AI API access.
//
// A provider represents credentials for an upstream API.
// Examples include Claude API keys and Gemini OAuth accounts.
//
// The schema stores:
//   - Credentials for platforms such as Claude, Gemini, and OpenAI.
//   - The authentication type that determines the credential format.
//   - Availability, rate-limit, and overload state used by scheduling.
//   - Group membership used to select providers for requests.
type Provider struct {
	ent.Schema
}

// Annotations sets the database table name.
func (Provider) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "providers"},
	}
}

// Mixin supplies timestamps and soft deletion.
func (Provider) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields defines provider credentials, configuration, and scheduling state.
func (Provider) Fields() []ent.Field {
	return []ent.Field{
		// Provider display name.
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		// Optional administrator notes.
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Upstream platform, such as claude, gemini, or openai.
		field.String("platform").
			MaxLen(50).
			NotEmpty(),

		// Authentication type.
		// Determines the data stored in credentials.
		field.String("type").
			MaxLen(20).
			NotEmpty(),

		// Authentication credentials stored as JSONB.
		// The format depends on type:
		// - apikey: {"api_key": "sk-xxx"}
		// - oauth: {"access_token": "...", "refresh_token": "...", "expires_at": "..."}
		// - cookie: {"session_key": "..."}
		field.JSON("credentials", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Platform-specific metadata.
		// For example, a CRS account ID or organization details.
		field.JSON("extra", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Optional proxy configuration ID.
		// Requests use this proxy to reach the upstream API.
		field.Int64("proxy_id").
			Optional().
			Nillable(),
		field.Int64("proxy_fallback_origin_id").
			Optional().Nillable().
			Comment("Original proxy id replaced by expiry-fallback; for manual revert. NULL = not in fallback."),

		// Maximum concurrent requests to this provider.
		field.Int("concurrency").
			Default(3),

		field.Int("load_factor").Optional().Nillable(),

		// Lower priority values are preferred by scheduling.
		field.Int("priority").
			Default(50),

		// Provider cost multiplier; zero makes provider cost zero.
		// User and API key charges use group pricing multipliers.
		field.Float("rate_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1.0),

		// Provider status, such as active, error, or disabled.
		field.String("status").
			MaxLen(20).
			Default(provider.StatusActive),

		// Details of the provider's error state.
		field.String("error_message").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Last usage time for statistics and scheduling.
		field.Time("last_used_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("Provider expiration time (NULL means no expiration).").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Bool("auto_pause_on_expired").
			Default(true).
			Comment("Auto pause scheduling when provider expires."),

		// Scheduling and rate-limit state.
		// See migrations/005_schema_parity.sql.

		// Whether the scheduler may select this provider.
		// false temporarily excludes it, for example during token refresh.
		field.Bool("schedulable").
			Default(true),

		// Time a rate limit was observed.
		// Recorded when a 429 response triggers rate limiting.
		field.Time("rate_limited_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// Expected rate-limit reset time.
		// Scheduling skips the provider until this time.
		field.Time("rate_limit_reset_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// End of the overload cooldown.
		// Set when a 529 response triggers overload handling.
		field.Time("overload_until").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// Deadline for temporary scheduling exclusion.
		// A matching temporary exclusion rule prevents selection until this time.
		field.Time("temp_unschedulable_until").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// Reason for temporary exclusion, retained for diagnosis.
		field.String("temp_unschedulable_reason").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),

		// Session window state.
		// Used by APIs with session windows, such as Claude Pro.
		field.Time("session_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("session_window_end").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("session_window_status").
			Optional().
			Nillable().
			MaxLen(20),

		field.Int64("parent_provider_id").Optional().Nillable().
			Comment("Parent provider id for a linked spark shadow (NULL = normal)."),
		field.Enum("quota_dimension").Values("global", "spark").Default("global").
			Comment("'global' (default) or 'spark' (shadow reads codex_bengalfox)."),
	}
}

// Edges defines provider group, proxy, shadow, and usage relationships.
func (Provider) Edges() []ent.Edge {
	return []ent.Edge{
		// Group membership is many-to-many.
		// provider_groups stores each membership.
		// A provider can belong to multiple groups, and each group can contain multiple providers.
		edge.To("groups", Group.Type).
			Through("provider_groups", ProviderGroup.Type),
		// Each provider can reference one proxy; a proxy can serve multiple providers.
		// The relation uses the proxy_id field.
		edge.To("proxy", Proxy.Type).
			Field("proxy_id").
			Unique(),
		// children/parent: linked spark shadow relationship.
		// parent_provider_id is nullable, and the active one-shadow-per-parent rule
		// is enforced by the partial unique index in migration 154a.
		edge.To("children", Provider.Type).
			Annotations(entsql.OnDelete(entsql.Restrict)).
			From("parent").
			Field("parent_provider_id").
			Unique(),
		// Usage records attributed to this provider.
		edge.To("usage_logs", UsageLog.Type),
	}
}

// Indexes supports provider filtering and scheduling queries.
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
		// SQL migrations create partial versions of these scheduling indexes in production.
		index.Fields("platform", "priority"),
		index.Fields("priority", "status"),
		index.Fields("deleted_at"),
		index.Fields("parent_provider_id"),
	}
}
