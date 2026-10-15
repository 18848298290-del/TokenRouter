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

// api_key_composite_group.go defines prefix-to-group bindings for composite
// API keys. Its mappings retain the displayed prefix and a normalized lookup
// value, with key-scoped uniqueness preventing duplicate group bindings and
// duplicate normalized prefixes in the stored mapping set.

// APIKeyCompositeGroup stores one composite key's group and prefix mapping.
type APIKeyCompositeGroup struct {
	ent.Schema // generated entity definition
}

// Annotations maps composite prefix bindings to the api_key_composite_groups table.
func (APIKeyCompositeGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "api_key_composite_groups"}}
}

// Mixin supplies creation and update timestamps for composite prefix mappings.
func (APIKeyCompositeGroup) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

// Fields declares the owning key, group, prefix forms, and mapping order.
// The key service supplies a trimmed display prefix and its lowercase
// normalized value; Ent requires both strings and defaults order to zero.
func (APIKeyCompositeGroup) Fields() []ent.Field {
	return []ent.Field{
		// Owning key identity.
		field.Int64("api_key_id"),
		// Selected group identity.
		field.Int64("group_id"),
		// prefix and normalized_prefix retain presentation and lookup separately.
		// The key service preserves display casing in prefix, stores the
		// lowercase form in normalized_prefix, and uses that form for duplicate
		// detection and request prefix matching.
		// Displayed model prefix.
		field.String("prefix").MaxLen(32).NotEmpty(),
		// Lowercase lookup prefix.
		field.String("normalized_prefix").MaxLen(32).NotEmpty(),
		// Stored mapping order.
		field.Int("sort_order").Default(0),
	}
}

// Edges requires each mapping to reference an existing key and group.
// Physical deletion of either parent cascades to the relationship row;
// scalar identity fields back the generated unique entity edges.
func (APIKeyCompositeGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("api_key", APIKey.Type).
			Ref("composite_groups").
			Field("api_key_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("group", Group.Type).
			Ref("api_key_composite_groups").
			Field("group_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

// Indexes enforces unique key-group and key-normalized-prefix pairs. Group
// lookup and per-key ordering indexes support enumerating bindings and
// returning the stored mapping sequence to key consumers.
func (APIKeyCompositeGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("api_key_id", "group_id").Unique(),
		index.Fields("api_key_id", "normalized_prefix").Unique(),
		index.Fields("group_id"),
		index.Fields("api_key_id", "sort_order"),
	}
}
