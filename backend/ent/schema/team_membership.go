package schema

import (
	"fmt"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// team_membership.go defines each user's team role and spending limits. The
// record retains calendar-period usage and an optional departure time, so
// leaving a team can end admission while historical usage remains associated
// with the team's acting user.

// TeamMembership stores a member's role, limits, and calendar-period usage.
type TeamMembership struct {
	ent.Schema // generated entity definition
}

// Annotations maps member queries and mutations to the team_memberships table.
func (TeamMembership) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "team_memberships"}}
}

// Mixin supplies insertion and update timestamps for team membership records.
func (TeamMembership) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

// Fields declares member identity, role, limits, usage, and membership times.
// New records use the member role, nonnegative zero-valued monetary fields,
// and an application-time join timestamp; window starts and departure are optional.
func (TeamMembership) Fields() []ent.Field {
	validateNonNegative := func(value float64) error {
		if value < 0 {
			return fmt.Errorf("team member quotas and usage must not be negative")
		}
		return nil
	}
	decimalLimit := func(name string) ent.Field {
		return field.Float(name).SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0).Validate(validateNonNegative)
	}
	decimalUsage := func(name string) ent.Field {
		return field.Float(name).SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0).Validate(validateNonNegative)
	}
	return []ent.Field{
		// Membership team identity.
		field.Int64("team_id"),
		// Membership user identity.
		field.Int64("user_id"),
		// role identifies the funding owner or an ordinary member of this team.
		// It is distinct from the user's global administrator or user role.
		field.String("role").MaxLen(20).Default("member").Validate(func(value string) error {
			if value != "owner" && value != "member" {
				return fmt.Errorf("team role must be owner or member")
			}
			return nil
		}),
		// For ordinary members, positive daily_limit_usd, weekly_limit_usd, and
		// monthly_limit_usd constrain daily_usage_usd, weekly_usage_usd, and
		// monthly_usage_usd respectively. Zero disables that limit; owner admission
		// bypasses member quota checks.
		// USD daily limit.
		decimalLimit("daily_limit_usd"),
		// USD weekly limit.
		decimalLimit("weekly_limit_usd"),
		// USD monthly limit.
		decimalLimit("monthly_limit_usd"),
		// USD daily usage.
		decimalUsage("daily_usage_usd"),
		// USD weekly usage.
		decimalUsage("weekly_usage_usd"),
		// USD monthly usage.
		decimalUsage("monthly_usage_usd"),
		// daily_window_start, weekly_window_start, and monthly_window_start identify
		// the calendar windows for the matching usage totals. Billing resets a
		// total when its start is missing or precedes the current period.
		// Daily usage origin.
		field.Time("daily_window_start").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Weekly usage origin.
		field.Time("weekly_window_start").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Monthly usage origin.
		field.Time("monthly_window_start").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Membership admission timestamp.
		field.Time("joined_at").Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// left_at ends current membership in team-repository admission queries.
		// A NULL departure time identifies a membership still participating in
		// the team, while departed records remain available for historical reporting.
		field.Time("left_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires each membership to identify an existing team and user.
// The team_id and user_id columns back these unique entity edges, while a
// team can expose multiple membership records.
func (TeamMembership) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("team", Team.Type).Ref("memberships").Field("team_id").Unique().Required(),
		edge.From("user", User.Type).Ref("team_memberships").Field("user_id").Unique().Required(),
	}
}

// Indexes supports team member lists, user membership lookup, and role filtering.
func (TeamMembership) Indexes() []ent.Index {
	return []ent.Index{index.Fields("team_id"), index.Fields("user_id"), index.Fields("team_id", "role")}
}
