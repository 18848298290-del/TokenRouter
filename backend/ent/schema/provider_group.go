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

// provider_group.go defines each provider-to-routing-group membership as a
// distinct row. Provider and group identities form its primary key, while a
// creation timestamp records when that association was persisted and a group
// index supports enumerating its providers.

// ProviderGroup represents one provider's membership in a configured routing group.
type ProviderGroup struct {
	ent.Schema // generated entity definition
}

// Annotations binds provider memberships to provider_groups with a paired primary key.
// The provider-group identity allows many memberships for either entity while
// each particular pair addresses one row.
func (ProviderGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "provider_groups"},
		// Composite primary key: (provider_id, group_id).
		field.ID("provider_id", "group_id"),
	}
}

// Fields defines the provider and group identities with membership creation time.
// The timestamp defaults to application time and remains immutable when the
// generated Ent client updates a membership.
func (ProviderGroup) Fields() []ent.Field {
	return []ent.Field{
		// Member provider identity.
		field.Int64("provider_id"),
		// Routing group identity.
		field.Int64("group_id"),
		// Immutable membership timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires the membership to reference an existing provider and group.
// The paired scalar fields back unique entity edges used by generated
// relationship mutations and provider-group traversal.
func (ProviderGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("provider", Provider.Type).
			Unique().
			Required().
			Field("provider_id"),
		edge.To("group", Group.Type).
			Unique().
			Required().
			Field("group_id"),
	}
}

// Indexes supports enumerating provider memberships for a selected routing group.
func (ProviderGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
	}
}
