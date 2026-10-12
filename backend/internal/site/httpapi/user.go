package httpapi

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Announcement lists use the authenticated account's balance, plans, language,
// and first-read markers to select and display records. Read acknowledgements
// use that same account identity, so route parameters select the announcement
// while middleware determines whose read state changes.

// AnnouncementHandler serves account-specific announcement lists and read acknowledgements through the service.
type AnnouncementHandler struct {
	announcementService *site.AnnouncementService // eligibility and read operations
}

// NewAnnouncementHandler binds announcement eligibility and read operations for user HTTP endpoints.
func NewAnnouncementHandler(announcementService *site.AnnouncementService) *AnnouncementHandler {
	return &AnnouncementHandler{
		announcementService: announcementService,
	}
}

// List returns announcements visible to the authenticated user.
// Missing account context returns an unauthorized response. unread_only accepts
// the positive values recognized by parseBoolQuery; domain filtering, language
// resolution, and unread-first ordering come from the announcement service.
// GET /api/v1/announcements
func (h *AnnouncementHandler) List(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	unreadOnly := parseBoolQuery(c.Query("unread_only"))

	items, err := h.announcementService.ListForUser(c.Request.Context(), subject.UserID, unreadOnly)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]UserAnnouncement, 0, len(items))
	for i := range items {
		out = append(out, *UserAnnouncementFromService(&items[i]))
	}
	response.Success(c, out)
}

// MarkRead records that the authenticated user has read a visible announcement.
// It requires account context and a positive decimal announcement ID. The
// service checks schedule and targeting before persisting the marker, with
// failures mapped through the shared HTTP error response.
// POST /api/v1/announcements/:id/read
func (h *AnnouncementHandler) MarkRead(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	announcementID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || announcementID <= 0 {
		response.BadRequest(c, "Invalid announcement ID")
		return
	}

	if err := h.announcementService.MarkRead(c.Request.Context(), subject.UserID, announcementID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "ok"})
}

// parseBoolQuery reports whether v names a supported affirmative query value.
// Input is trimmed and lowercased; 1, true, yes, y, and on are affirmative,
// while an absent or unrecognized value leaves the filter disabled.
func parseBoolQuery(v string) bool {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
