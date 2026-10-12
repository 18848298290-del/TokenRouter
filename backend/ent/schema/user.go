package schema

import (
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "users"},
	}
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		// A partial unique index on active rows allows email addresses to be reused after soft deletion.
		// See migration 016_soft_delete_partial_unique_indexes.sql.
		field.String("email").
			MaxLen(255).
			NotEmpty(),
		field.String("password_hash").
			MaxLen(255).
			NotEmpty(),
		field.String("role").
			MaxLen(20).
			Default(identity.RoleUser),
		field.Float("balance").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Float("frozen_balance").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Int("concurrency").
			Default(5),
		field.String("status").
			MaxLen(20).
			Default(identity.StatusActive),

		// Optional profile fields.
		// preferred_locale stores the user's chosen interface and notification language.
		field.String("preferred_locale").MaxLen(35).Optional().Nillable(),
		field.String("username").
			MaxLen(100).
			Default(""),
		// The wechat profile field lives in user_attribute_values; see migration 019.
		field.String("notes").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),

		// TOTP two-factor authentication.
		field.String("totp_secret_encrypted").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Optional().
			Nillable(),
		field.Bool("totp_enabled").
			Default(false),
		field.Time("totp_enabled_at").
			Optional().
			Nillable(),
		field.String("signup_source").
			Validate(func(value string) error {
				switch value {
				case "email", "linuxdo", "wechat", "oidc", "github", "google", "dingtalk":
					return nil
				default:
					return fmt.Errorf("must be one of email, linuxdo, wechat, oidc, github, google, dingtalk")
				}
			}).
			Default("email"),
		field.Time("last_login_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_active_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		// Low-balance notifications.
		field.Bool("balance_notify_enabled").
			Default(true),
		field.String("balance_notify_threshold_type").
			Default("fixed"), // "fixed" | "percentage"
		field.Float("balance_notify_threshold").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Optional().
			Nillable(),
		field.String("balance_notify_extra_emails").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default("[]"),
		field.Float("total_recharged").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),

		// User-wide RPM limit, checked after group limits; zero means unlimited.
		field.Int("rpm_limit").
			Default(0),
		// Maximum API keys the user can create; zero means unlimited.
		field.Int("api_key_limit").
			Default(identity.DefaultUserAPIKeyLimit).
			Range(0, identity.MaxUserAPIKeyLimit),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("api_keys", APIKey.Type),
		edge.To("redeem_codes", RedeemCode.Type),
		edge.To("redeem_code_usages", RedeemCodeUsage.Type),
		edge.To("subscriptions", UserSubscription.Type),
		edge.To("assigned_subscriptions", UserSubscription.Type),
		edge.To("announcement_reads", AnnouncementRead.Type),
		edge.To("allowed_groups", Group.Type).
			Through("user_allowed_groups", UserAllowedGroup.Type),
		edge.To("disabled_public_groups", Group.Type).
			Through("user_disabled_public_groups", UserDisabledPublicGroup.Type),
		edge.To("usage_logs", UsageLog.Type),
		edge.To("attribute_values", UserAttributeValue.Type),
		edge.To("promo_code_usages", PromoCodeUsage.Type),
		edge.To("payment_orders", PaymentOrder.Type),
		edge.To("auth_identities", AuthIdentity.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("pending_auth_sessions", PendingAuthSession.Type),
		edge.To("team_memberships", TeamMembership.Type),
	}
}

func (User) Indexes() []ent.Index {
	return []ent.Index{
		// Active-email uniqueness is enforced by the SQL migration's partial index.
		index.Fields("status"),
		index.Fields("deleted_at"),
	}
}
