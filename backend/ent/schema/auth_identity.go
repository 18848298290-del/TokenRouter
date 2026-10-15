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

// auth_identity.go defines canonical login identities keyed by provider type,
// issuer key, and stable subject. That triplet identifies the same account
// across callbacks, while a required user relationship records its local owner
// and separate channels retain identifiers limited to one provider
// application.

// authProviderTypes restricts identity schemas to the supported email and third-party
// authentication providers accepted by validateAuthProviderType during Ent field validation.
var authProviderTypes = map[string]struct{}{
	"email":    {},
	"github":   {},
	"google":   {},
	"linuxdo":  {},
	"oidc":     {},
	"wechat":   {},
	"dingtalk": {},
}

// AuthIdentity stores the canonical login identity for an account.
type AuthIdentity struct {
	ent.Schema // generated entity definition
}

// validateAuthProviderType accepts a provider type present in authProviderTypes.
// Matching uses the supplied string exactly; unsupported or differently cased
// values return an error during generated Ent field validation.
func validateAuthProviderType(value string) error {
	if _, ok := authProviderTypes[value]; ok {
		return nil
	}
	return fmt.Errorf("invalid auth provider type %q", value)
}

// Annotations maps canonical identity queries and mutations to the auth_identities table.
func (AuthIdentity) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "auth_identities"},
	}
}

// Mixin supplies creation and update times for canonical authentication identities.
func (AuthIdentity) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields declares identity ownership, the external identity triplet, and verification
// metadata. Provider type is validated against the supported set, verification
// time and issuer are nullable, and omitted metadata becomes an empty JSON object.
func (AuthIdentity) Fields() []ent.Field {
	return []ent.Field{
		// Local owner identity.
		field.Int64("user_id"),
		// provider_type, provider_key, and provider_subject form the globally
		// unique external identity. The key distinguishes issuers or provider
		// configurations, so equal subjects under different keys identify separate
		// external accounts.
		// Authentication provider type.
		field.String("provider_type").
			MaxLen(20).
			NotEmpty().
			Validate(validateAuthProviderType),
		// Issuer configuration key.
		field.String("provider_key").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Stable external subject.
		field.String("provider_subject").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Observed verification timestamp.
		field.Time("verified_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Optional issuer identity.
		field.String("issuer").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Provider identity metadata.
		field.JSON("metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

// Edges requires each canonical identity to belong to one user.
// Channel records are deleted with their identity, while adoption decisions
// expose the identity's recorded profile choices.
func (AuthIdentity) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("auth_identities").
			Field("user_id").
			Required().
			Unique(),
		edge.To("channels", AuthIdentityChannel.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("adoption_decisions", IdentityAdoptionDecision.Type),
	}
}

// Indexes enforces the provider type, key, and subject identity triplet.
// The additional user indexes support listing an account's identities and
// finding identities for a selected provider type.
func (AuthIdentity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider_type", "provider_key", "provider_subject").Unique(),
		index.Fields("user_id"),
		index.Fields("user_id", "provider_type"),
	}
}
