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
)

// Access to an exclusive group is represented by an individual user-group
// grant. The pair forms the row identity, so a user can have multiple distinct
// grants while each grant records its own creation time and required entities.

// UserAllowedGroup represents a stored exclusive-group access grant for one user.
type UserAllowedGroup struct {
	ent.Schema // generated entity definition
}

// Annotations selects user_allowed_groups and the user-group composite primary key.
// The paired identity makes repeated grants for the same user and group
// address one relationship row.
func (UserAllowedGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_allowed_groups"},
		// Composite primary key: (user_id, group_id).
		field.ID("user_id", "group_id"),
	}
}

// Fields defines the granted user and group identities with an insertion time.
// The creation timestamp uses application time when omitted and remains
// immutable through generated Ent updates.
func (UserAllowedGroup) Fields() []ent.Field {
	return []ent.Field{
		// Granted user identity.
		field.Int64("user_id"),
		// Granted group identity.
		field.Int64("group_id"),
		// Immutable grant timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires each grant to connect an existing user and group.
// The row's scalar identity columns back the unique entity edges used to
// traverse the granted relationship.
func (UserAllowedGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Unique().
			Required().
			Field("user_id"),
		edge.To("group", Group.Type).
			Unique().
			Required().
			Field("group_id"),
	}
}

// Indexes supports finding access grants by their target group identity.
func (UserAllowedGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
	}
}
