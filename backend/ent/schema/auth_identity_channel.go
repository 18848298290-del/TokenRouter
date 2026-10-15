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

// auth_identity_channel.go defines application-scoped identifiers attached to
// canonical login identities. Channel rows retain the provider, application,
// and subject together, so an identifier from one application can resolve to
// its owning identity without colliding with another channel.

// AuthIdentityChannel stores channel-scoped identifiers for a canonical identity.
type AuthIdentityChannel struct {
	ent.Schema // generated entity definition
}

// Annotations maps channel identity persistence to the auth_identity_channels table.
func (AuthIdentityChannel) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "auth_identity_channels"},
	}
}

// Mixin supplies creation and update timestamps for stored identity channels.
func (AuthIdentityChannel) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields describes the owning identity and the provider's channel-scoped identifier.
// Provider type uses the shared supported-type validator. Omitted metadata is
// stored as an empty JSON object alongside the required channel identifiers.
func (AuthIdentityChannel) Fields() []ent.Field {
	return []ent.Field{
		// Canonical identity reference.
		field.Int64("identity_id"),
		// provider_type, provider_key, channel, channel_app_id, and channel_subject
		// identify one channel account together. The composite index separates
		// subjects that share a value but belong to different applications or
		// configured providers.
		// Authentication provider type.
		field.String("provider_type").
			MaxLen(20).
			NotEmpty().
			Validate(validateAuthProviderType),
		// Provider configuration key.
		field.String("provider_key").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Provider channel name.
		field.String("channel").
			MaxLen(20).
			NotEmpty(),
		// Channel application identity.
		field.String("channel_app_id").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Channel-scoped account subject.
		field.String("channel_subject").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Channel identity metadata.
		field.JSON("metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

// Edges binds each channel account to its required canonical identity.
// identity_id backs the unique identity edge, allowing multiple channel rows
// to refer to the same canonical login identity.
func (AuthIdentityChannel) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("identity", AuthIdentity.Type).
			Ref("channels").
			Field("identity_id").
			Required().
			Unique(),
	}
}

// Indexes enforces uniqueness within each configured provider and channel application.
// The identity index supports enumerating channel identifiers attached to one
// canonical identity for lookup and administration.
func (AuthIdentityChannel) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider_type", "provider_key", "channel", "channel_app_id", "channel_subject").Unique(),
		index.Fields("identity_id"),
	}
}
