package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Group-consuming routes need a selected group after authentication and
// composite selection. Some composite endpoints intentionally work with no
// selected group, so admission combines loaded-key state with that exception
// before invoking the protocol error writer.

type (
	// GroupAssignmentAccess represents the selected-group eligibility checked during HTTP request admission.
	GroupAssignmentAccess struct {
		// Loaded, Assigned and CompositeNoGroup are read together by the gate:
		// a missing key, selected group or allowed composite exception continues.
		// Loaded indicates an effective key; Assigned indicates a selected group;
		// CompositeNoGroup indicates a composite endpoint allowing no selected group.
		Loaded, Assigned, CompositeNoGroup bool
	}
	// GroupAssignmentOptions supplies state reading and protocol-specific rejection reporting.
	GroupAssignmentOptions struct {
		// Access and WriteError are required by the returned middleware. Access
		// reads effective eligibility, and WriteError presents denial before Abort.
		Access     func(*gin.Context) GroupAssignmentAccess // effective group-state reader
		WriteError func(*gin.Context, int, string)          // required protocol error writer
		// Rejected marks a denied request before output; nil omits this optional
		// observation while the mandatory error writer still reports the denial.
		Rejected func(*gin.Context)
	}
)

// RequireGroupAssignment rejects loaded keys lacking an assigned group unless
// the composite endpoint permits that state. Access and WriteError callbacks
// are required. Rejected runs when supplied before the 403 is written and the
// Gin chain is aborted; admitted requests continue without body inspection.
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

// EffectiveGroupAssignment reads the effective key after authentication and
// composite selection. Missing keys return empty eligibility; a GroupID pointer
// establishes assignment, and the no-group marker applies only to composite
// keys.
func EffectiveGroupAssignment(c *gin.Context) GroupAssignmentAccess {
	key, ok := EffectiveAPIKey(c)
	if !ok || key == nil {
		return GroupAssignmentAccess{}
	}
	_, noGroup := c.Get(CompositeKeyNoGroupContextKey)
	return GroupAssignmentAccess{Loaded: true, Assigned: key.GroupID != nil, CompositeNoGroup: key.IsComposite && noGroup}
}
