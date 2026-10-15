package admission

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// group.go checks whether an API key can use its loaded group and whether that
// group remains available. An authenticated request can have permission to use
// a group that was disabled or deleted, so handlers apply both checks and
// translate availability reasons into their protocol's error response.

// GroupAllowed reports whether the key's user may bind its loaded group.
// Missing key, group ID, user, or group data returns true; authentication callers
// establish those records separately before relying on this authorization check.
func GroupAllowed(key *apikey.APIKey) bool {
	if key == nil || key.GroupID == nil || key.User == nil || key.Group == nil {
		return true
	}
	return key.User.CanBindGroup(key.Group.ID, key.Group.IsExclusive)
}

// GroupAvailable returns a rejection reason, message, and availability flag for
// the key's group. A missing key or group ID passes; a configured ID with missing
// or deleted group data yields GROUP_DELETED. An inactive group yields
// GROUP_DISABLED; handlers receive empty reason and message strings on success.
func GroupAvailable(key *apikey.APIKey) (string, string, bool) {
	if key == nil || key.GroupID == nil {
		return "", "", true
	}
	group := key.Group
	if group == nil || strings.EqualFold(group.Status, "deleted") {
		return "GROUP_DELETED", "The API key group has been deleted.", false
	}
	if !group.IsActive() {
		return "GROUP_DISABLED", "The API key group is disabled.", false
	}
	return "", "", true
}
