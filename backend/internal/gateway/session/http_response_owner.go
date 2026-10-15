package session

import (
	"context"
	"strings"
)

// http_response_owner.go checks stored HTTP response ownership against the
// requesting user and API Key. A recorded user permits that user's other API
// Keys to continue the response; legacy records with no positive user ID
// instead require the original key. Requester user and key IDs are positive
// before storage is consulted, so an unavailable identity cannot obtain
// permission from an absent or legacy record.
//
// The stored user takes precedence over stored key equality. For example, a
// response owned by user 1 and key 4 can continue through user 1's key 5, while
// user 2's matching key number does not satisfy that user-owned record. A
// legacy record with no positive user owner instead uses key equality. Missing
// records return false, and storage errors remain visible to the caller.

// HTTPResponseOwnerReader supplies user/key ownership for a group's response ID.
type HTTPResponseOwnerReader interface {
	// GetHTTPResponseOwner reads the stored user ID, API Key ID, and presence
	// for the supplied group and response ID. Storage failures return an error;
	// a missing record returns found=false for the caller's authorization decision.
	GetHTTPResponseOwner(context.Context, int64, string) (int64, int64, bool, error)
}

// ValidateHTTPResponseOwner checks continuation against stored user or legacy key
// ownership. Blank response IDs, nonpositive requester IDs, a nil read callback,
// or a nil returned reader interface produce false with nil error. Other inputs
// perform one lookup using the response ID as supplied.
//
// A positive stored user ID authorizes the same requester user across API Keys.
// A nonpositive stored user ID uses equality with the requester key instead.
// Missing records reject continuation, and storage errors are returned so callers
// can distinguish unavailable ownership from a recorded mismatch.
func ValidateHTTPResponseOwner(ctx context.Context, read func() HTTPResponseOwnerReader, groupID int64, responseID string, userID, keyID int64) (bool, error) {
	if read == nil || strings.TrimSpace(responseID) == "" || userID <= 0 || keyID <= 0 {
		return false, nil
	}
	store := read()
	if store == nil {
		return false, nil
	}
	ownerUserID, ownerKeyID, found, err := store.GetHTTPResponseOwner(ctx, groupID, responseID)
	if err != nil || !found {
		return false, err
	}
	return ownerUserID == userID || (ownerUserID <= 0 && ownerKeyID == keyID), nil
}
