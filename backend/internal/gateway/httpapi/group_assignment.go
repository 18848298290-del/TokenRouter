package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GroupAssignmentAccess contains read-only group eligibility for HTTP admission.
type (
	GroupAssignmentAccess  struct{ Loaded, Assigned, CompositeNoGroup bool }
	GroupAssignmentOptions struct {
		Access     func(*gin.Context) GroupAssignmentAccess
		WriteError func(*gin.Context, int, string)
		Rejected   func(*gin.Context)
	}
)

// RequireGroupAssignment rejects loaded keys without a group unless the composite endpoint permits none; it does not
// read the body.
func RequireGroupAssignment(options GroupAssignmentOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		access := options.Access(c)
		if !access.Loaded || access.Assigned || access.CompositeNoGroup {
			c.Next()
			return
		}
		if options.Rejected != nil {
			options.Rejected(c)
		}
		options.WriteError(c, http.StatusForbidden, "API Key is not assigned to any group and cannot be used. Please contact the administrator to assign it to a group.")
		c.Abort()
	}
}

// EffectiveGroupAssignment reads group state after key authentication and composite group selection.
func EffectiveGroupAssignment(c *gin.Context) GroupAssignmentAccess {
	key, ok := EffectiveAPIKey(c)
	if !ok || key == nil {
		return GroupAssignmentAccess{}
	}
	_, noGroup := c.Get(CompositeKeyNoGroupContextKey)
	return GroupAssignmentAccess{Loaded: true, Assigned: key.GroupID != nil, CompositeNoGroup: key.IsComposite && noGroup}
}
