package admission

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// GroupAllowed reports whether the user may bind the group. Missing key, user, or group data returns true.
func GroupAllowed(key *apikey.APIKey) bool {
	if key == nil || key.GroupID == nil || key.User == nil || key.Group == nil {
		return true
	}
	return key.User.CanBindGroup(key.Group.ID, key.Group.IsExclusive)
}

// GroupAvailable returns a reason, message, and availability flag, distinguishing deleted and disabled groups.
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
