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

// proxy.go defines reusable outbound proxy endpoints referenced by providers.
// Each record combines endpoint credentials with availability and expiration
// settings, allowing egress maintenance to leave a provider bound, move it to
// a configured backup, or select direct connections when the proxy expires.

// Proxy stores an outbound proxy endpoint and its expiration policy.
type Proxy struct {
	ent.Schema // generated entity definition
}

// Annotations maps proxy queries and mutations to the shared proxies table.
func (Proxy) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "proxies"},
	}
}

// Mixin supplies timestamp maintenance and default soft-deletion handling for proxies.
// Ordinary Ent reads exclude deleted rows, and deletes retain a timestamped
// record; SkipSoftDelete lets maintenance query or physically remove those rows.
func (Proxy) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields describes proxy connection details, availability, and expiry handling.
// New proxies start active with no expiry fallback; nullable credentials and
// expiration time let records describe unauthenticated or open-ended endpoints.
func (Proxy) Fields() []ent.Field {
	return []ent.Field{
		// Administrator display name.
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		// Proxy connection protocol.
		field.String("protocol").
			MaxLen(20).
			NotEmpty(),
		// Proxy endpoint host.
		field.String("host").
			MaxLen(255).
			NotEmpty(),
		// Proxy endpoint port.
		field.Int("port"),
		// Optional authentication username.
		field.String("username").
			MaxLen(100).
			Optional().
			Nillable(),
		// Optional authentication password.
		field.String("password").
			MaxLen(100).
			Optional().
			Nillable(),
		// Stored availability status.
		field.String("status").
			MaxLen(20).
			Default("active"),
		// expires_at is optional. Egress treats a missing timestamp as having no
		// scheduled expiration, while status still determines the proxy's availability.
		field.Time("expires_at").
			Optional().Nillable().
			Comment("Proxy expiration time (NULL means never expires)."),
		// fallback_mode and backup_proxy_id guide expired-provider reassignment.
		// "none" keeps bindings, "direct" removes them, and "proxy" follows the
		// configured backup chain until it finds an unexpired target or stops.
		// Expired endpoint action.
		field.String("fallback_mode").
			MaxLen(20).Default("none").
			Comment("Fallback target on expiry: none | proxy | direct."),
		// Backup endpoint identity.
		field.Int64("backup_proxy_id").
			Optional().Nillable().
			Comment("Backup proxy id when fallback_mode=proxy (self-reference)."),
		// Days before expiration.
		field.Int("expiry_warn_days").
			Default(7).
			Comment("Days before expiry to flag as expiring-soon (per proxy)."),
	}
}

// Edges connects proxy records with the providers and backup proxies using them.
func (Proxy) Edges() []ent.Edge {
	return []ent.Edge{
		// Providers that use this proxy.
		edge.From("providers", Provider.Type).
			Ref("proxy"),
		edge.To("backup_proxy", Proxy.Type).
			Field("backup_proxy_id").
			Unique(),
	}
}

// Indexes supports status filtering, soft-deleted lookup, expiration scans, and backup references.
func (Proxy) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("deleted_at"),
		index.Fields("expires_at"),
		index.Fields("backup_proxy_id"),
	}
}
