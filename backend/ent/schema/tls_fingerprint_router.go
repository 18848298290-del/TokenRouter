package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// TLSFingerprintRouter selects TLS profiles using inbound User-Agent rules.
//
// Providers can bind a router alongside a fixed profile
// to use different ClientHello settings for different client runtimes.
type TLSFingerprintRouter struct {
	ent.Schema
}

// Annotations sets the database table name.
func (TLSFingerprintRouter) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "tls_fingerprint_routers"},
	}
}

// Mixin supplies creation and update timestamps.
func (TLSFingerprintRouter) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields defines User-Agent routing and request-specific TLS settings.
func (TLSFingerprintRouter) Fields() []ent.Field {
	return []ent.Field{
		// Unique router name.
		field.String("name").
			MaxLen(100).
			NotEmpty().
			Unique(),

		// Router description.
		field.Text("description").
			Optional().
			Nillable(),

		// Whether the router is enabled.
		field.Bool("enabled").
			Default(true),

		// User-Agent for ChatGPT OAuth token requests; empty uses the system default.
		field.String("chatgpt_oauth_token_user_agent").
			MaxLen(512).
			Default(""),

		// TLS profile for ChatGPT OAuth token requests.
		// nil disables the token-specific profile; 0 selects the built-in default, -1 a random profile, and positive values a stored profile.
		field.Int64("chatgpt_oauth_token_tls_fingerprint_profile_id").
			Optional().
			Nillable(),

		// User-Agent for Codex invitation resets; empty uses the Desktop default.
		field.String("codex_invite_reset_user_agent").
			MaxLen(512).
			Default(""),

		// TLS profile for Codex invitation reset requests.
		// nil inherits the provider profile; 0 selects the built-in default, -1 a random profile, and positive values a stored profile.
		field.Int64("codex_invite_reset_tls_fingerprint_profile_id").
			Optional().
			Nillable(),

		// User-Agent rules are checked in order; the first match selects the TLS profile.
		field.JSON("rules", []egress.TLSFingerprintRouterRule{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

// Indexes supports enabled-router lookup.
func (TLSFingerprintRouter) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
	}
}
