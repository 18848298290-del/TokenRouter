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

// Inbound User-Agent rules can select a provider's outbound TLS profile and
// headers. A stored router retains the ordered rules alongside separate token
// and invitation-reset settings, so those special requests can choose their
// transport identity independently of ordinary model requests.

// TLSFingerprintRouter stores User-Agent rules and special-request TLS overrides.
type TLSFingerprintRouter struct {
	ent.Schema // generated entity definition
}

// Annotations maps router queries and mutations to the tls_fingerprint_routers table.
func (TLSFingerprintRouter) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "tls_fingerprint_routers"},
	}
}

// Mixin adds creation and update timestamps to stored TLS routers.
func (TLSFingerprintRouter) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields describes ordered routing rules and special-request TLS settings.
// New routers start enabled with empty User-Agent overrides. Nullable profile
// selections distinguish inherited behavior from built-in, random, or stored
// profile choices.
func (TLSFingerprintRouter) Fields() []ent.Field {
	return []ent.Field{
		// Unique router name.
		field.String("name").
			MaxLen(100).
			NotEmpty().
			Unique(),

		// Administrator router description.
		field.Text("description").
			Optional().
			Nillable(),

		// Router activation flag.
		field.Bool("enabled").
			Default(true),

		// chatgpt_oauth_token_user_agent overrides the token request identity.
		// The authorization dependency uses its configured Codex User-Agent when
		// this value is empty and a token-specific TLS profile is selected.
		field.String("chatgpt_oauth_token_user_agent").
			MaxLen(512).
			Default(""),

		// TLS profile for ChatGPT OAuth token requests.
		// nil leaves token-specific TLS unset. Zero selects the built-in default,
		// -1 selects a random profile, and positive values select stored profiles.
		field.Int64("chatgpt_oauth_token_tls_fingerprint_profile_id").
			Optional().
			Nillable(),

		// codex_invite_reset_user_agent overrides invitation/reset request identity.
		// The Codex invitation client uses its Desktop default when this value is
		// empty after trimming whitespace.
		field.String("codex_invite_reset_user_agent").
			MaxLen(512).
			Default(""),

		// TLS profile for Codex invitation reset requests.
		// nil inherits the provider selection. Zero selects the built-in default,
		// -1 selects a random profile, and positive values select stored profiles.
		field.Int64("codex_invite_reset_tls_fingerprint_profile_id").
			Optional().
			Nillable(),

		// rules supplies ordered User-Agent matches to the egress router service.
		// The first matching enabled rule determines the request's selected TLS
		// profile and any configured upstream header overrides.
		field.JSON("rules", []egress.TLSFingerprintRouterRule{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

// Indexes supports finding routers with the requested enabled-state filter.
func (TLSFingerprintRouter) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
	}
}
