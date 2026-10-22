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

// team_invitation.go defines invitations that bind token digests to recipient
// emails and expiration times. Accepting the invitation records the joining
// user and acceptance time, while persisted status distinguishes pending
// invitations from responses, revocations, and expiry.

// TeamInvitation stores a short-lived invitation bound to the recipient's email.
type TeamInvitation struct {
	ent.Schema // generated entity definition
}

// Annotations binds invitation persistence to the team's dedicated team_invitations table.
func (TeamInvitation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "team_invitations"}}
}

// Mixin supplies creation and update times for stored team invitations.
func (TeamInvitation) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

// Fields declares the invitation's team, recipient, token digest, and outcome.
// Status starts pending and accepts the invitation lifecycle values; acceptance
// fields are nullable until the repository records a successful join.
func (TeamInvitation) Fields() []ent.Field {
	return []ent.Field{
		// Inviting team identity.
		field.Int64("team_id"),
		// Inviting user identity.
		field.Int64("inviter_user_id"),
		// Target recipient email.
		field.String("email").MaxLen(255).NotEmpty(),
		// Invitation token digest.
		field.String("token_hash").MaxLen(64).NotEmpty(),
		// status is pending while awaiting the recipient, accepted after joining,
		// declined when refused, revoked when withdrawn, or expired after the
		// invitation's validity window closes.
		field.String("status").MaxLen(20).Default("pending").Validate(func(value string) error {
			switch value {
			case "pending", "accepted", "declined", "revoked", "expired":
				return nil
			default:
				return fmt.Errorf("invalid team invitation status")
			}
		}),
		// Invitation validity endpoint.
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// accepted_by_user_id and accepted_at describe the successful acceptance.
		// They remain nullable for pending or unresolved invitations and are
		// written together by the team repository when membership is established.
		// Accepting user identity.
		field.Int64("accepted_by_user_id").Optional().Nillable(),
		// Successful acceptance timestamp.
		field.Time("accepted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires an invitation to belong to an existing team record.
// The team_id column backs the unique team edge used by generated invitation
// creation and team invitation traversal.
func (TeamInvitation) Edges() []ent.Edge {
	return []ent.Edge{edge.From("team", Team.Type).Ref("invitations").Field("team_id").Unique().Required()}
}

// Indexes makes token digests unique and supports team and recipient-status lookup.
func (TeamInvitation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("token_hash").Unique(), index.Fields("team_id"), index.Fields("email", "status", "expires_at")}
}
