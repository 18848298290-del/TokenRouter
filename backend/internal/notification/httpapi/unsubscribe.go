package httpapi

import (
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// RegisterUnsubscribeRoute registers email unsubscribe handling on the public settings group.
func RegisterUnsubscribeRoute(group *gin.RouterGroup, endpoint *Handler) {
	group.GET("/email-unsubscribe", endpoint.UnsubscribeNotificationEmail)
}

// UnsubscribeNotificationEmail handles unsubscribe requests for optional notification events.
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
