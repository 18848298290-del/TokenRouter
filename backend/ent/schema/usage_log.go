package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// UsageLog stores API usage and billing facts.
//
// Records include token counts and calculated costs for API calls.
// Callers append usage records to this table.
type UsageLog struct {
	ent.Schema
}

// Annotations sets the database table name.
func (UsageLog) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_logs"},
	}
}

// Fields defines request attribution, usage, and billing fields.
func (UsageLog) Fields() []ent.Field {
	return []ent.Field{
		// Request attribution.
		field.Int64("user_id"),
		// Repositories write the payer snapshot; a database trigger supports legacy raw SQL inserts.
		field.Int64("billing_user_id").Optional(),
		field.Int64("team_id").Optional().Nillable(),
		field.Int64("api_key_id"),
		field.Int64("provider_id"),
		// Snapshot the platform so later provider or group edits cannot change historical statistics.
		field.String("platform").MaxLen(64).Default("unknown"),
		// billing_key deduplicates fund operations; legacy rows with no value use request_id.
		field.String("billing_key").MaxLen(255).Optional().Nillable(),
		field.String("request_id").
			MaxLen(64).
			NotEmpty(),
		field.String("model").
			MaxLen(100).
			NotEmpty(),
		// RequestedModel stores the client-requested model name for stable display and analytics.
		// NULL means historical rows written before requested_model dual-write was introduced.
		field.String("requested_model").
			MaxLen(100).
			Optional().
			Nillable(),
		// UpstreamModel stores the actual upstream model name when model mapping
		// is applied. NULL means no upstream model was recorded.
		field.String("upstream_model").
			MaxLen(100).
			Optional().
			Nillable(),
		// The upstream response model is for administrator audits; missing observations and legacy rows remain NULL.
		field.String("upstream_response_model").MaxLen(200).Optional().Nillable(),
		field.Bool("upstream_model_mismatch").Optional().Nillable(),
		field.Int64("pricing_config_id").Optional().Nillable().Comment("Shared pricing configuration ID"),
		field.String("model_mapping_chain").MaxLen(500).Optional().Nillable().Comment("Model mapping chain"),
		field.String("billing_tier").MaxLen(50).Optional().Nillable().Comment("Billing tier label"),
		field.String("billing_mode").MaxLen(20).Optional().Nillable().Comment("Billing mode: token/per_request/image"),
		field.Int64("group_id").
			Optional().
			Nillable(),
		field.Int64("subscription_id").
			Optional().
			Nillable(),

		// Token counts.
		field.Int("input_tokens").
			Default(0),
		field.Int("output_tokens").
			Default(0),
		field.Int("cache_creation_tokens").
			Default(0),
		field.Int("cache_read_tokens").
			Default(0),
		field.Int("cache_creation_5m_tokens").
			Default(0),
		field.Int("cache_creation_1h_tokens").
			Default(0),

		// Costs and funding allocations.
		field.Float("input_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("output_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("cache_creation_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("cache_read_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("total_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("actual_cost").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("subscription_amount_usd").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("balance_amount_usd").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.JSON("billing_allocations", []billing.BillingAllocation{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Float("rate_multiplier").
			Default(1).
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		field.Bool("long_context_billing_applied").
			Default(false).
			Comment("Records whether long-context rules increased this request's cost"),

		// Provider cost multiplier snapshot; NULL is treated as 1.0.
		field.Float("provider_rate_multiplier").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),

		// Request timing and client metadata.
		field.Int8("billing_type").
			Default(0),
		field.Bool("stream").
			Default(false),
		field.Int("duration_ms").
			Optional().
			Nillable(),
		field.Int("first_token_ms").
			Optional().
			Nillable(),
		field.String("user_agent").
			MaxLen(512).
			Optional().
			Nillable(),
		field.String("ip_address").
			MaxLen(45). // Fits IPv6 addresses.
			Optional().
			Nillable(),

		// Image generation usage, including image models such as gemini-3-pro-image.
		field.Int("image_count").
			Default(0),
		field.String("image_size").
			MaxLen(10).
			Optional().
			Nillable(),
		field.String("image_input_size").
			MaxLen(32).
			Optional().
			Nillable(),
		field.String("image_output_size").
			MaxLen(32).
			Optional().
			Nillable(),
		field.String("image_size_source").
			MaxLen(16).
			Optional().
			Nillable(),
		field.JSON("image_size_breakdown", map[string]int{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Video usage remains identifiable under any billing_mode; Grok video charges use seconds.
		field.Int("video_count").
			Default(0).
			Comment("Generated video count; a positive value identifies video usage"),
		field.String("video_resolution").
			MaxLen(10).
			Optional().
			Nillable().
			Comment("Video resolution used for billing: 480p/720p/1080p"),
		field.Int("video_duration_seconds").
			Optional().
			Nillable().
			Comment("Requested video duration at submission, in seconds, used for per-second billing"),
		// Records an administrator override of cache TTL billing.
		field.Bool("cache_ttl_overridden").
			Default(false),

		// Creation time is immutable; usage records have no updated_at field.
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges defines request attribution and subscription relationships.
func (UsageLog) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("usage_logs").
			Field("user_id").
			Required().
			Unique(),
		edge.From("api_key", APIKey.Type).
			Ref("usage_logs").
			Field("api_key_id").
			Required().
			Unique(),
		edge.From("provider", Provider.Type).
			Ref("usage_logs").
			Field("provider_id").
			Required().
			Unique(),
		edge.From("group", Group.Type).
			Ref("usage_logs").
			Field("group_id").
			Unique(),
		edge.From("subscription", UserSubscription.Type).
			Ref("usage_logs").
			Field("subscription_id").
			Unique(),
		edge.From("team", Team.Type).
			Ref("usage_logs").
			Field("team_id").
			Unique(),
	}
}

// Indexes supports attribution, model, request, and time-range lookups.
func (UsageLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("billing_user_id"),
		index.Fields("team_id"),
		index.Fields("api_key_id"),
		index.Fields("provider_id"),
		index.Fields("group_id"),
		index.Fields("subscription_id"),
		index.Fields("created_at"),
		index.Fields("model"),
		index.Fields("requested_model"),
		index.Fields("request_id"),
		// Composite indexes support time-range queries for users and keys.
		index.Fields("user_id", "created_at"),
		index.Fields("api_key_id", "created_at"),
		// Production uses a SQL partial index with group_id IS NOT NULL for group time-range queries.
		index.Fields("group_id", "created_at"),
	}
}
