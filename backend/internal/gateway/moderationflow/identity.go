package moderationflow

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// identity.go resolves moderation actor identity separately from billing
// ownership using authenticated Key evidence.
// A moderation risk actor can differ from the user that owns billing for an
// API key. Subject identity initializes both roles, then loaded key and
// user evidence can replace them independently. Actor or matching loaded
// user/membership evidence supplies the risk actor's email.
//
// A positive APIKey.UserID replaces the risk actor and fills missing
// billing identity, while a positive loaded User.ID takes billing
// precedence. A positive ActorUser overrides risk identity and email;
// otherwise a membership email applies only when its user matches the
// selected risk actor. Team identity is copied into a separate pointer,
// keeping caller mutations of the key's pointer value from rewriting the
// resolved result.

// Identity separates moderation risk actor from the API key's billing
// owner.
type Identity struct {
	// UserID and UserEmail identify the risk actor used for moderation
	// evidence. ResolveIdentity selects an actor separately from
	// BillingUserID, and email follows positive actor or matching
	// user/membership evidence.
	UserID    int64  // resolved moderation risk actor ID
	UserEmail string // trimmed risk actor email
	// BillingUserID starts from the subject and prefers a positive loaded
	// User.ID. Positive ActorUser changes risk identity and email while
	// leaving this resolved billing owner unchanged.
	BillingUserID int64 // resolved billing owner user ID
	// TeamID is copied from the key through cloneID, including a nonpositive
	// pointed value. The returned pointer is independent of the API key's
	// original pointer storage.
	TeamID *int64 // optional copied team identifier
}

// ResolveIdentity resolves risk and billing identities from subject and
// optional API key evidence. Nil key retains subject IDs and no email/team;
// positive APIKey.UserID changes the risk actor and fills billing only when
// the subject billing ID is nonpositive.
//
// A positive loaded User.ID overrides billing and supplies email only when
// matching the current risk actor. Positive ActorUser then overrides risk
// ID/email; otherwise matching TeamMembership can replace email. Billing
// remains independently selected. TeamID is copied, while nonpositive IDs
// not replaced by these rules remain as supplied rather than being globally
// normalized.
func ResolveIdentity(apiKey *apikey.APIKey, subjectUserID int64) Identity {
	identity := Identity{
		UserID:        subjectUserID,
		BillingUserID: subjectUserID,
	}
	if apiKey == nil {
		return identity
	}

	if apiKey.UserID > 0 {
		identity.UserID = apiKey.UserID
		if identity.BillingUserID <= 0 {
			identity.BillingUserID = apiKey.UserID
		}
	}
	// Loaded billing ownership can differ from risk identity, so replace
	// BillingUserID while accepting this user's email only when it belongs to
	// the current risk actor.
	if apiKey.User != nil && apiKey.User.ID > 0 {
		identity.BillingUserID = apiKey.User.ID
		if apiKey.User.ID == identity.UserID {
			identity.UserEmail = strings.TrimSpace(apiKey.User.Email)
		}
	}
	// Prefer explicit actor evidence for risk ID and email after billing is
	// selected, allowing member activity to retain the separately resolved
	// billing owner.
	if apiKey.ActorUser != nil && apiKey.ActorUser.ID > 0 {
		identity.UserID = apiKey.ActorUser.ID
		identity.UserEmail = strings.TrimSpace(apiKey.ActorUser.Email)
	} else if apiKey.TeamMembership != nil && apiKey.TeamMembership.UserID == identity.UserID {
		identity.UserEmail = strings.TrimSpace(apiKey.TeamMembership.Email)
	}
	identity.TeamID = cloneID(apiKey.TeamID)
	return identity
}
