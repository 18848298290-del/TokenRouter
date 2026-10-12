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
	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// APIKey holds the schema definition for the APIKey entity.
type APIKey struct {
	ent.Schema
}

func (APIKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "api_keys"},
	}
}

func (APIKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (APIKey) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("team_id").Optional().Nillable(),
		// Owner-imposed locks are separate from member deactivation; ordinary key updates must retain this flag.
		field.Bool("team_owner_disabled").Default(false),
		field.String("key").
			MaxLen(128).
			NotEmpty().
			Unique(),
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.Int64("group_id").
			Optional().
			Nillable(),
		// Composite keys select groups through prefix mappings rather than a single group_id.
		field.Bool("is_composite").
			Default(false).
			Comment("Routes across groups using model prefixes"),
		field.String("status").
			MaxLen(20).
			Default(apikey.StatusActive),
		// The default Fast mode policy follows the client's request.
		field.String("fast_mode_policy").
			MaxLen(32).
			Default("follow_request").
			Comment("API key Fast mode policy: follow_request, force_on, or force_off"),
		// Keys can select subscription or balance funding; auto spends subscription funds first.
		field.String("billing_mode").
			MaxLen(32).
			Default("auto").
			Comment("API key billing mode: auto, subscription, or balance"),
		field.Int64("preferred_subscription_id").
			Optional().
			Nillable().
			Comment("User subscription ID selected when billing_mode is subscription"),
		// Key model mappings apply before group and provider mappings.
		field.JSON("model_mapping", map[string]string{}).
			Default(func() map[string]string { return map[string]string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Custom model redirect rules for this API key"),
		field.Time("last_used_at").
			Optional().
			Nillable().
			Comment("Last usage time of this API key"),
		field.JSON("ip_whitelist", []string{}).
			Optional().
			Comment("Allowed IPs/CIDRs, e.g. [\"192.168.1.100\", \"10.0.0.0/8\"]"),
		field.JSON("ip_blacklist", []string{}).
			Optional().
			Comment("Blocked IPs/CIDRs"),

		// Quota fields
		// Quota limit in USD (0 = unlimited)
		field.Float("quota").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Quota limit in USD for this API key (0 = unlimited)"),
		// Used quota amount
		field.Float("quota_used").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Used quota amount in USD"),
		// Expiration time (nil = never expires)
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("Expiration time for this API key (null = never expires)"),

		// The gateway checks these request limits at admission; zero disables each limit.
		field.Int("concurrency_limit").NonNegative().Default(0),
		field.Int("rpm_limit").NonNegative().Default(0),

		// Rate limit fields
		// Rate limit configuration (0 = unlimited)
		field.Float("rate_limit_5h").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Rate limit in USD per 5 hours (0 = unlimited)"),
		field.Float("rate_limit_1d").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Rate limit in USD per day (0 = unlimited)"),
		field.Float("rate_limit_7d").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Rate limit in USD per 7 days (0 = unlimited)"),
		// Rate limit usage tracking
		field.Float("usage_5h").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Used amount in USD for the current 5h window"),
		field.Float("usage_1d").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Used amount in USD for the current 1d window"),
		field.Float("usage_7d").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("Used amount in USD for the current 7d window"),
		// Window start times
		field.Time("window_5h_start").
			Optional().
			Nillable().
			Comment("Start time of the current 5h rate limit window"),
		field.Time("window_1d_start").
			Optional().
			Nillable().
			Comment("Start time of the current 1d rate limit window"),
		field.Time("window_7d_start").
			Optional().
			Nillable().
			Comment("Start time of the current 7d rate limit window"),

		// Allows request-level fallback to the same platform's default group when the bound group is unavailable.
		field.Bool("fallback_when_group_unavailable").
			Default(true).
			Comment("Falls back to the same platform's default group when the bound group is unavailable"),
		// managed_by identifies hidden server-managed keys; ordinary user APIs must not expose or modify them.
		field.String("managed_by").
			MaxLen(32).
			Optional().
			Nillable().
			Comment("Managing source: creative_studio for hidden execution keys; NULL for ordinary user keys"),
	}
}

func (APIKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("api_keys").
			Field("user_id").
			Unique().
			Required(),
		edge.From("group", Group.Type).
			Ref("api_keys").
			Field("group_id").
			Unique(),
		edge.To("usage_logs", UsageLog.Type),
		edge.To("composite_groups", APIKeyCompositeGroup.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("team", Team.Type).
			Ref("api_keys").
			Field("team_id").
			Unique(),
	}
}

func (APIKey) Indexes() []ent.Index {
	return []ent.Index{
		// Fields declares the unique key constraint.
		index.Fields("user_id"),
		index.Fields("team_id"),
		index.Fields("group_id"),
		index.Fields("status"),
		index.Fields("deleted_at"),
		index.Fields("last_used_at"),
		// Index for quota queries
		index.Fields("quota", "quota_used"),
		index.Fields("expires_at"),
	}
}
