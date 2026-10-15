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

// pending_auth_session.go defines authentication sessions awaiting account
// creation, binding, or adoption decisions. Pending sessions retain the
// verified upstream identity, browser binding, completion credential, and
// expiration until the identity workflow records the final decision and
// consumes the session.

// pendingAuthIntents names the supported post-authentication decisions checked by
// validatePendingAuthIntent when generated Ent builders validate a pending session.
var pendingAuthIntents = map[string]struct{}{
	// Login resolves or creates a local account.
	"login": {},
	// Bind attaches the identity to the current account.
	"bind_current_user": {},
	// Adopt confirms ownership of an existing email-matched account.
	"adopt_existing_user_by_email": {},
}

// PendingAuthSession stores a short-lived decision session after upstream authentication.
type PendingAuthSession struct {
	ent.Schema // generated entity definition
}

// validatePendingAuthIntent accepts a post-authentication intent from pendingAuthIntents.
// The supplied value is matched exactly, so unknown or differently cased
// intents return an error to the generated Ent builder.
func validatePendingAuthIntent(value string) error {
	if _, ok := pendingAuthIntents[value]; ok {
		return nil
	}
	return fmt.Errorf("invalid pending auth intent %q", value)
}

// Annotations maps pending authentication persistence to the pending_auth_sessions table.
func (PendingAuthSession) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "pending_auth_sessions"},
	}
}

// Mixin supplies creation and update timestamps for pending authentication sessions.
func (PendingAuthSession) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields declares the external identity, decision intent, and pending-flow security
// state. Provider and intent values use shared validators. Optional verification
// timestamps and empty-string defaults let the workflow record completed steps
// incrementally before consuming the session.
func (PendingAuthSession) Fields() []ent.Field {
	return []ent.Field{
		// Pending session credential.
		field.String("session_token").
			MaxLen(255).
			NotEmpty(),
		// Post-authentication decision intent.
		field.String("intent").
			MaxLen(40).
			NotEmpty().
			Validate(validatePendingAuthIntent),
		// provider_type, provider_key, and provider_subject retain the external
		// identity established by the callback. Completion uses this same triplet
		// when it checks canonical identity ownership in the database.
		// Authentication provider type.
		field.String("provider_type").
			MaxLen(20).
			NotEmpty().
			Validate(validateAuthProviderType),
		// Provider configuration key.
		field.String("provider_key").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Stable external subject.
		field.String("provider_subject").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Candidate account identity.
		field.Int64("target_user_id").
			Optional().
			Nillable(),
		// Post-completion redirect destination.
		field.String("redirect_to").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Resolved account email.
		field.String("resolved_email").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Registration password digest.
		field.String("registration_password_hash").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Upstream identity claims.
		field.JSON("upstream_identity_claims", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// Local decision state.
		field.JSON("local_flow_state", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// browser_session_key, completion_code_hash, and completion_code_expires_at
		// bind the exchange to its browser and short-lived completion credential.
		// The identity workflow checks them before permitting session completion.
		// Browser binding key.
		field.String("browser_session_key").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Completion credential digest.
		field.String("completion_code_hash").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Completion credential expiry.
		field.Time("completion_code_expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Email verification timestamp.
		field.Time("email_verified_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Password verification timestamp.
		field.Time("password_verified_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// TOTP verification timestamp.
		field.Time("totp_verified_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Pending session expiry.
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// consumed_at records successful consumption of this pending decision.
		// Identity completion rejects sessions with a recorded consumption time
		// when a later request tries to reuse them.
		field.Time("consumed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges optionally identifies a target user and the session's adoption decision.
// Each session has at most one decision, which is removed when the pending
// session is physically deleted.
func (PendingAuthSession) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("target_user", User.Type).
			Ref("pending_auth_sessions").
			Field("target_user_id").
			Unique(),
		edge.To("adoption_decision", IdentityAdoptionDecision.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)).
			Unique(),
	}
}

// Indexes enforces unique session credentials and supports identity, target, expiry,
// and completion-code lookup. The external-identity index is nonunique because
// multiple pending decisions can refer to the same provider subject.
func (PendingAuthSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_token").Unique(),
		index.Fields("target_user_id"),
		index.Fields("expires_at"),
		index.Fields("provider_type", "provider_key", "provider_subject"),
		index.Fields("completion_code_hash"),
	}
}
