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
)

// team.go defines tenant availability, member capacity, and default member
// spending limits. A team's owner funds its API usage. New memberships copy
// the team's default spending limits so later edits can leave existing member
// limits intact.

// Team stores tenant availability, member capacity, and default member limits.
type Team struct {
	ent.Schema // generated entity definition
}

// Annotations binds generated tenant queries and mutations to the teams table.
func (Team) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "teams"}}
}

// Mixin adds timestamps and default soft-deletion behavior to team records.
// Deleted teams remain stored and are hidden from ordinary Ent queries; a
// SkipSoftDelete context permits maintenance access or physical removal.
func (Team) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}, mixins.SoftDeleteMixin{}}
}

// Fields declares the team name, lifecycle state, capacity, and default limits.
// Teams start active with capacity for ten ordinary members. Default limits accept
// nonnegative values and use USD decimal columns.
func (Team) Fields() []ent.Field {
	validateDefaultLimit := func(value float64) error {
		if value < 0 {
			return fmt.Errorf("default team member quota must not be negative")
		}
		return nil
	}
	defaultLimit := func(name string) ent.Field {
		return field.Float(name).SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0).Validate(validateDefaultLimit)
	}
	return []ent.Field{
		// Team display name.
		field.String("name").MaxLen(100).NotEmpty(),
		// status is active for participating teams or suspended to block team
		// admission. The team lifecycle checks this value when authenticating keys.
		field.String("status").MaxLen(20).Default("active").Validate(func(value string) error {
			if value != "active" && value != "suspended" {
				return fmt.Errorf("team status must be active or suspended")
			}
			return nil
		}),
		// Regular member capacity.
		field.Int("member_limit").Default(10).NonNegative(),
		// default_daily_limit_usd, default_weekly_limit_usd, and
		// default_monthly_limit_usd are copied when the team repository accepts a
		// new member. Existing memberships keep their independently stored limits.
		// USD daily default.
		defaultLimit("default_daily_limit_usd"),
		// USD weekly default.
		defaultLimit("default_weekly_limit_usd"),
		// USD monthly default.
		defaultLimit("default_monthly_limit_usd"),
	}
}

// Edges connects the tenant to memberships, invitations, transfers, keys, and usage.
func (Team) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("memberships", TeamMembership.Type),
		edge.To("invitations", TeamInvitation.Type),
		edge.To("ownership_transfers", TeamOwnershipTransfer.Type),
		edge.To("api_keys", APIKey.Type),
		edge.To("usage_logs", UsageLog.Type),
	}
}

// Indexes supports lifecycle filtering and maintenance queries for retained deleted teams.
func (Team) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status"), index.Fields("deleted_at")}
}
