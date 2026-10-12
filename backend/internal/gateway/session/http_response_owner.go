package session

import (
	"context"
	"strings"
)

// HTTPResponseOwnerReader reads the user and API key that own an HTTP response.
type HTTPResponseOwnerReader interface {
	GetHTTPResponseOwner(context.Context, int64, string) (int64, int64, bool, error)
}

// ValidateHTTPResponseOwner permits continuation across keys owned by the same user.
// Legacy records without a user owner must match the API key.
// Invalid inputs or an unavailable reader return false; valid inputs trigger one ownership lookup.
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
