package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// user_attribute_value.go defines text storage for custom user attributes,
// including numbers, dates, and selections. The identity application validates
// and interprets that text using the definition; each user has at most one
// value for each attribute.

// UserAttributeValue stores one user's text value for a custom attribute.
type UserAttributeValue struct {
	ent.Schema // generated entity definition
}

// Annotations maps persisted attribute values to the user_attribute_values database table.
func (UserAttributeValue) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_attribute_values"},
	}
}

// Mixin supplies creation and update times for custom user values.
func (UserAttributeValue) Mixin() []ent.Mixin {
	return []ent.Mixin{
		// Attribute values are hard-deleted.
		mixins.TimeMixin{},
	}
}

// Fields declares the owning user, attribute definition, and stored text value.
// An omitted value becomes an empty string. The application interprets
// multi-select values as JSON array strings when applying definition rules.
func (UserAttributeValue) Fields() []ent.Field {
	return []ent.Field{
		// user_id: References the user this value belongs to
		field.Int64("user_id"),

		// attribute_id: References the attribute definition
		field.Int64("attribute_id"),

		// value: The actual value stored as a string
		// For multi_select, this is a JSON array string
		field.Text("value").
			Default(""),
	}
}

// Edges requires each custom value to belong to an existing user and definition.
// The scalar user_id and attribute_id columns back unique entity edges used
// by generated mutations and attribute traversal.
func (UserAttributeValue) Edges() []ent.Edge {
	return []ent.Edge{
		// user: The user who owns this attribute value
		edge.From("user", User.Type).
			Ref("attribute_values").
			Field("user_id").
			Required().
			Unique(),

		// definition: The attribute definition this value is for
		edge.From("definition", UserAttributeDefinition.Type).
			Ref("values").
			Field("attribute_id").
			Required().
			Unique(),
	}
}

// Indexes limits each user to one value for an attribute definition.
// The separate attribute index supports finding all user values for a
// definition when it is queried or removed.
func (UserAttributeValue) Indexes() []ent.Index {
	return []ent.Index{
		// Unique index on (user_id, attribute_id)
		index.Fields("user_id", "attribute_id").Unique(),
		index.Fields("attribute_id"),
	}
}
