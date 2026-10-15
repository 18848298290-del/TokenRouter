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
)

// user_attribute_definition.go defines administrator-configured user
// attributes separately from their stored values. A definition supplies form
// presentation, value type, and validation rules; soft deletion lets a later
// definition reuse the key while retained definitions remain distinguishable
// by their numeric identities.

// UserAttributeDefinition stores presentation and validation rules for user attributes.
type UserAttributeDefinition struct {
	ent.Schema // generated entity definition
}

// Annotations maps custom-attribute definitions to the user_attribute_definitions database table.
func (UserAttributeDefinition) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_attribute_definitions"},
	}
}

// Mixin adds timestamps and default soft-deletion handling to attribute definitions.
// Ordinary queries hide deleted definitions, while SkipSoftDelete contexts let
// maintenance inspect or physically remove those retained rows.
func (UserAttributeDefinition) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields describes an attribute's key, form presentation, type, and validation
// configuration. Definitions start enabled and optional. Options and validation
// use JSONB columns, while omitted help and placeholder text default to empty.
func (UserAttributeDefinition) Fields() []ent.Field {
	return []ent.Field{
		// Attribute lookup key.
		field.String("key").
			MaxLen(100).
			NotEmpty(),

		// Form display name.
		field.String("name").
			MaxLen(255).
			NotEmpty(),

		// Optional attribute help.
		field.String("description").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),

		// type: Attribute type - text, textarea, number, email, url, date, select, multi_select
		field.String("type").
			MaxLen(20).
			NotEmpty(),

		// options supplies value-label choices for select and multi_select
		// attributes. The user editing form presents these entries as the
		// available selections for that definition.
		// Format: [{"value": "xxx", "label": "XXX"}, ...]
		field.JSON("options", []map[string]any{}).
			Default([]map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// required controls user editing and collection validation. Existing users
		// can lack a value, so the flag does not establish a database-level
		// completeness guarantee for historical user records.
		field.Bool("required").
			Default(false),

		// validation: Validation rules for the attribute value (stored as JSONB)
		// Format: {"min_length": 1, "max_length": 100, "min": 0, "max": 100, "pattern": "^[a-z]+$", "message": "..."}
		field.JSON("validation", map[string]any{}).
			Default(map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Form input placeholder.
		field.String("placeholder").
			MaxLen(255).
			Default(""),

		// display_order controls the attribute's position in user forms. Lower
		// values place the definition ahead of higher-valued definitions.
		field.Int("display_order").
			Default(0),

		// enabled controls whether user forms show this definition. Disabling it
		// retains previously saved values, which continue to belong to the
		// existing user and attribute records.
		field.Bool("enabled").
			Default(true),
	}
}

// Edges exposes users' stored values for each custom attribute definition.
func (UserAttributeDefinition) Edges() []ent.Edge {
	return []ent.Edge{
		// values: All user values for this attribute definition
		edge.To("values", UserAttributeValue.Type),
	}
}

// Indexes supports definition lookup, form filtering, ordering, and retained-row
// queries. Migration 018 enforces key uniqueness for rows with deleted_at NULL;
// the Ent key index itself is nonunique.
func (UserAttributeDefinition) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key"),
		index.Fields("enabled"),
		index.Fields("display_order"),
		index.Fields("deleted_at"),
	}
}
