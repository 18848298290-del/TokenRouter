package httpapi

import (
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// unsubscribe.go handles signed email unsubscribe requests and renders their
// confirmation page.
// Unsubscribe links expose a signed recipient/event decision through a public
// GET endpoint. The service verifies the token and optional-event eligibility
// before saving a preference, and the handler returns escaped recipient and
// event text in an HTML confirmation page.

// RegisterUnsubscribeRoute binds signed email-preference handling under the supplied public settings group.
// The caller installs the group's request policy before registration. The
// route accepts a token query parameter and delegates validation to the service.
func RegisterUnsubscribeRoute(group *gin.RouterGroup, endpoint *Handler) {
	group.GET("/email-unsubscribe", endpoint.UnsubscribeNotificationEmail)
}

// UnsubscribeNotificationEmail handles unsubscribe requests for optional notification events.
// A missing service returns an internal error, and blank or rejected tokens
// become bad requests. Success returns an HTML page containing escaped event
// and recipient values from the persisted preference result.
// GET /api/v1/settings/email-unsubscribe?token=...
func (h *Handler) UnsubscribeNotificationEmail(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		response.BadRequest(c, "token is required")
		return
	}
	result, err := h.notificationEmailService.Unsubscribe(c.Request.Context(), token)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><title>Unsubscribed</title></head><body style=\"font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;padding:32px;\"><h1>Unsubscribed</h1><p>You have unsubscribed <strong>" + html.EscapeString(result.Email) + "</strong> from <strong>" + html.EscapeString(result.Event) + "</strong> emails.</p></body></html>"
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}
