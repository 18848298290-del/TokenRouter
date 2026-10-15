package session

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// isolation.go records first-group session ownership and enforces isolation on
// later group changes. Later use in a different group is rejected only when the
// target group requires isolation, so the first owner remains available if a
// later target activates that rule. Identity combines the user, source
// category, and session hash to separate otherwise equal client session text.
//
// EnsureIsolation first attempts atomic insertion, then reads the existing
// owner. A read failure triggers one insertion retry because the record can
// expire between those operations. Matching the target renews the owner record;
// an allowed different target retains the recorded first owner and its
// deadline.

// These source labels separate ownership identities; the conflict message identifies
// rejection when a session-owned group differs from an isolation-enabled target.
const (
	SessionIsolationSourceOpenAI                 = "openai"                   // OpenAI session ownership source
	SessionIsolationSourceOpenAIPreviousResponse = "openai_previous_response" // previous-response ownership source
	SessionIsolationSourceGateway                = "gateway"                  // generic gateway ownership source
	SessionIsolationSourceGemini                 = "gemini"                   // Gemini session ownership source

	SessionIsolationConflictMessage = "This session already belongs to another group and cannot switch to the current session-isolated group"
)

// ErrSessionIsolationConflict reports the forbidden cross-group use detected by
// EnsureIsolation when the target group enables isolation for a previously owned session.
var ErrSessionIsolationConflict = infraerrors.Forbidden("SESSION_ISOLATION_CONFLICT", SessionIsolationConflictMessage)

// OwnerStore supplies first-group insertion, lookup, and renewal for session isolation.
type OwnerStore interface {
	// SetSessionOwnerGroupID inserts the group atomically for a user/source/hash
	// identity when absent. True means this call became first owner; false retains
	// the existing group so EnsureIsolation can read it.
	SetSessionOwnerGroupID(context.Context, int64, string, string, int64, time.Duration) (bool, error)
	// GetSessionOwnerGroupID returns the recorded first owner for a user/source/hash
	// identity. A missing or unavailable record returns an error; EnsureIsolation
	// retries insertion once before deciding whether ownership can be read.
	GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error)
	// RefreshSessionOwnerTTL renews an existing owner identity using the supplied ttl.
	// It leaves the owner group unchanged; EnsureIsolation invokes it after comparing
	// the recorded group with the requested target.
	RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error
}

// IsolationInput supplies the requester identity and target group's isolation policy.
type IsolationInput struct {
	// UserID, Source and Hash select the first-owner record together. EnsureIsolation
	// trims Source and Hash before lookup; a nonpositive UserID or either trimmed
	// blank value skips storage because the session identity is incomplete.
	// GroupID names the requested target and is passed to storage as supplied,
	// including zero when the WebSocket caller has no selected group.
	UserID, GroupID int64  // requester user identifier and target group identifier
	Source, Hash    string // session ownership source and captured session hash
	// Enabled applies to the target group; a false value permits another group's existing session owner.
	Enabled bool
	// TTL renews matching ownership; nonpositive values select one hour before
	// calling the supplied store.
	TTL time.Duration
}

// EnsureIsolation records or checks the first group using a user/source/hash
// session identity. Missing storage, nonpositive users, and blank trimmed source
// or hash skip checking; nonpositive TTL selects one hour. GroupID is passed to
// storage as supplied, and Enabled applies to the target group.
//
// Insertion records ownership even for a target with isolation disabled. If the
// initial insertion loses, the existing owner is read; a read error triggers one
// insertion retry and, if needed, one further read. A matching owner renews TTL,
// a different owner passes when Enabled is false, and an isolated different
// target returns ErrSessionIsolationConflict. Storage errors propagate.
func EnsureIsolation(ctx context.Context, cache OwnerStore, input IsolationInput) error {
	userID, source, sessionHash, ttl := input.UserID, input.Source, input.Hash, input.TTL
	source = strings.TrimSpace(source)
	sessionHash = strings.TrimSpace(sessionHash)
	if cache == nil || userID <= 0 || source == "" || sessionHash == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = time.Hour
	}

	targetGroupID := input.GroupID
	targetIsolationEnabled := input.Enabled

	// Record ownership before applying the target policy so a session first
	// used by a permissive group still has an owner for later isolated targets.
	written, err := cache.SetSessionOwnerGroupID(ctx, userID, source, sessionHash, targetGroupID, ttl)
	if err != nil {
		return err
	}
	if written {
		return nil
	}

	ownerGroupID, err := cache.GetSessionOwnerGroupID(ctx, userID, source, sessionHash)
	if err != nil {
		// A failed owner read may follow expiration after the first insertion
		// lost. Retry insertion once before asking for the surviving owner again.
		written, setErr := cache.SetSessionOwnerGroupID(ctx, userID, source, sessionHash, targetGroupID, ttl)
		if setErr != nil {
			return setErr
		}
		if written {
			return nil
		}
		ownerGroupID, err = cache.GetSessionOwnerGroupID(ctx, userID, source, sessionHash)
		if err != nil {
			return err
		}
	}

	// Renew the matching owner before returning so its continued activity
	// retains the same first-group decision for later isolation checks.
	if ownerGroupID == targetGroupID {
		return cache.RefreshSessionOwnerTTL(ctx, userID, source, sessionHash, ttl)
	}
	if !targetIsolationEnabled {
		return nil
	}
	return ErrSessionIsolationConflict
}
