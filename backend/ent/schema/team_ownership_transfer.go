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

// team_ownership_transfer.go defines pending ownership-transfer requests
// addressed to target members. Its token digest identifies the request, and
// expiry and resolution timestamps let the team repository distinguish open
// requests from accepted, declined, cancelled, or expired transfers.

// TeamOwnershipTransfer stores a transfer that the target member must accept.
type TeamOwnershipTransfer struct {
	ent.Schema // generated entity definition
}

// Annotations maps transfer persistence to the dedicated team_ownership_transfers database table.
func (TeamOwnershipTransfer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "team_ownership_transfers"}}
}

// Mixin supplies creation and update timestamps for stored ownership transfers.
func (TeamOwnershipTransfer) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

// Fields declares transfer participants, token digest, lifecycle state, and times.
// New transfers start pending. A required expiration limits acceptance time,
// while resolved_at can remain absent until the repository handles an outcome.
func (TeamOwnershipTransfer) Fields() []ent.Field {
	return []ent.Field{
		// Affected team identity.
		field.Int64("team_id"),
		// Transferring owner identity.
		field.Int64("from_user_id"),
		// Receiving member identity.
		field.Int64("to_user_id"),
		// Transfer token digest.
		field.String("token_hash").MaxLen(64).NotEmpty(),
		// status is pending while awaiting the target, accepted after transfer,
		// declined when refused, cancelled when withdrawn, or expired when the
		// transfer request exceeds its validity window.
		field.String("status").MaxLen(20).Default("pending").Validate(func(value string) error {
			switch value {
			case "pending", "accepted", "declined", "cancelled", "expired":
				return nil
			default:
				return fmt.Errorf("invalid ownership transfer status")
			}
		}),
		// Transfer validity endpoint.
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// resolved_at records when the team repository handles an accepted,
		// declined, cancelled, or expired transfer. Pending requests can leave
		// the timestamp unset until one of those outcomes occurs.
		field.Time("resolved_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges requires every ownership transfer to reference an existing team.
// Generated mutations use team_id for the unique team edge, and team queries
// can traverse the team's transfer requests.
func (TeamOwnershipTransfer) Edges() []ent.Edge {
	return []ent.Edge{edge.From("team", Team.Type).Ref("ownership_transfers").Field("team_id").Unique().Required()}
}

// Indexes supports unique token lookup, team history, and target-member status filtering.
func (TeamOwnershipTransfer) Indexes() []ent.Index {
	return []ent.Index{index.Fields("token_hash").Unique(), index.Fields("team_id"), index.Fields("to_user_id", "status")}
}
