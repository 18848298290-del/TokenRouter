package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// identity_adoption_decision.go defines persisted choices to adopt an upstream
// display name or avatar during authentication. The decision belongs to one
// pending session and can reference the resulting canonical identity, keeping
// the selected profile choices associated with that authentication outcome.

// IdentityAdoptionDecision stores profile choices associated with one pending
// authentication session.
type IdentityAdoptionDecision struct {
	ent.Schema // generated entity definition
}

// Annotations maps recorded profile choices to the identity_adoption_decisions database table.
func (IdentityAdoptionDecision) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "identity_adoption_decisions"},
	}
}

// Mixin supplies creation and update times for persisted profile decisions.
func (IdentityAdoptionDecision) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields declares the pending session, optional identity, and selected profile
// choices. Both adoption flags default to false. decided_at uses application
// time on insertion and remains immutable through generated Ent updates.
func (IdentityAdoptionDecision) Fields() []ent.Field {
	return []ent.Field{
		// Originating pending session.
		field.Int64("pending_auth_session_id"),
		// Resulting canonical identity.
		field.Int64("identity_id").
			Optional().
			Nillable(),
		// adopt_display_name and adopt_avatar are independent profile choices.
		// Either can remain false while the other is accepted, and both are
		// stored on the same pending-session decision.
		// Display-name adoption choice.
		field.Bool("adopt_display_name").
			Default(false),
		// Avatar adoption choice.
		field.Bool("adopt_avatar").
			Default(false),
		// Immutable decision timestamp.
		field.Time("decided_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires one pending session and optionally associates a canonical identity.
// The scalar identity fields back unique entity edges, and the session's
// unique index restricts each flow to one recorded decision.
func (IdentityAdoptionDecision) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("pending_auth_session", PendingAuthSession.Type).
			Ref("adoption_decision").
			Field("pending_auth_session_id").
			Required().
			Unique(),
		edge.From("identity", AuthIdentity.Type).
			Ref("adoption_decisions").
			Field("identity_id").
			Unique(),
	}
}

// Indexes enforces one profile decision per pending session and supports identity
// lookup. identity_id is separately indexed so decisions associated with one
// canonical identity can be retrieved together.
func (IdentityAdoptionDecision) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("pending_auth_session_id").Unique(),
		index.Fields("identity_id"),
	}
}
