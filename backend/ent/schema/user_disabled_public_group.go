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

// A user can exclude a public group from their otherwise available groups.
// Exclusions use a user-group pair as the row identity and record when the
// choice was persisted; exclusive-group grants are represented separately by
// UserAllowedGroup.

// UserDisabledPublicGroup records one user's exclusion of a non-exclusive group.
type UserDisabledPublicGroup struct {
	ent.Schema // generated entity definition
}

// Annotations selects user_disabled_public_groups and its paired user-group primary key.
// Each excluded pair identifies one row, allowing a user to disable multiple
// public groups independently of other users.
func (UserDisabledPublicGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_disabled_public_groups"},
		field.ID("user_id", "group_id"),
	}
}

// Fields defines the user and excluded group identities with creation time.
// The timestamp defaults to application time on insertion and stays immutable
// when generated Ent updates change the record.
func (UserDisabledPublicGroup) Fields() []ent.Field {
	return []ent.Field{
		// Excluding user identity.
		field.Int64("user_id"),
		// Excluded group identity.
		field.Int64("group_id"),
		// Immutable exclusion timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges binds each exclusion to the required user and group records.
// Scalar identities back unique entity edges, so a stored exclusion resolves
// to one user and one public group.
func (UserDisabledPublicGroup) Edges() []ent.Edge {
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

// Indexes supports looking up user exclusions for a target public group.
func (UserDisabledPublicGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
	}
}
