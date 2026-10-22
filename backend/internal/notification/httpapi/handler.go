package httpapi

import (
	"context"
	"html"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// handler.go handles administrator SMTP tests and email template management
// requests.
// Administrator SMTP tests combine submitted connection values with a currently
// stored configuration when fields are omitted or blank. Explicit TLS false
// remains distinguishable from omission, while invalid nonpositive port input
// can select a saved positive port or the submission default.
//
// Saved configuration read failures leave the request values available for the
// test. Template management uses event/language selection from the notification
// service and maps unsupported input or save failures to administrator errors.
// Response mappers retain ordering and selected placeholder metadata so previews
// and editable listings use the same event definitions as actual delivery.

// TestSMTPRequest supplies connection-test inputs with an omission-aware TLS preference.
type TestSMTPRequest struct {
	// SMTPHost, SMTPUsername, and SMTPPassword use trimmed request text when
	// nonblank. TestSMTPConnection fills blanks from saved configuration when
	// that read succeeds; a failed read leaves submitted values authoritative.
	SMTPHost string `json:"smtp_host"` // requested server host
	// SMTPPort supplies the requested port. A nonpositive value selects a
	// saved positive port when available, then the submission default of 587.
	SMTPPort     int    `json:"smtp_port"`     // requested TCP port
	SMTPUsername string `json:"smtp_username"` // requested authentication account
	SMTPPassword string `json:"smtp_password"` // requested authentication secret
	// SMTPUseTLS distinguishes an explicit choice from omission; nil inherits
	// the stored transport setting when configuration is available, and otherwise selects false.
	SMTPUseTLS *bool `json:"smtp_use_tls"`
}

// SendTestEmailRequest contains a recipient and SMTP parameters for a test email.
type SendTestEmailRequest struct {
	// Email is required and checked by Gin's email binding rule before
	// SendTestEmail attempts configuration lookup or invokes the SMTP transport.
	Email string `json:"email" binding:"required,email"` // test recipient address
	// SMTPHost, SMTPUsername, SMTPPassword, SMTPFrom, and SMTPFromName use
	// trimmed request text when nonblank. SendTestEmail fills blanks from saved
	// configuration when that read succeeds, while failed reads leave request values.
	SMTPHost string `json:"smtp_host"` // requested server host
	// SMTPPort supplies the requested port. A nonpositive value selects a
	// saved positive port when available, then the submission default of 587.
	SMTPPort     int    `json:"smtp_port"`       // requested TCP port
	SMTPUsername string `json:"smtp_username"`   // requested authentication account
	SMTPPassword string `json:"smtp_password"`   // requested authentication secret
	SMTPFrom     string `json:"smtp_from_email"` // requested sender address
	SMTPFromName string `json:"smtp_from_name"`  // requested sender name
	// SMTPUseTLS distinguishes an explicit choice from omission; nil inherits
	// the stored transport setting when configuration is available, and otherwise selects false.
	SMTPUseTLS *bool `json:"smtp_use_tls"`
}

type (
	// SiteNameReader supplies language-selected branding for administrator test messages.
	SiteNameReader interface {
		// GetSiteName returns branding for the language carried by the request context.
		// The handler supplies that context for its test message, then escapes
		// the returned branding when embedding it in HTML content.
		GetSiteName(context.Context) string
	}
	// Handler maps SMTP test and template-management requests to the assembled notification services.
	Handler struct {
		emailService             *notification.Mailer                   // configured SMTP operations
		notificationEmailService *notification.NotificationEmailService // template and preference operations
		settingService           SiteNameReader                         // localized branding reader
	}
)

// ResolveSMTPUseTLS returns an explicit TLS preference or the saved configuration's value.
// A nil requested pointer represents omission; it uses savedConfig when available
// and false otherwise. An explicit false remains authoritative even over stored true.
func ResolveSMTPUseTLS(requested *bool, savedConfig *notification.SMTPConfig) bool {
	if requested != nil {
		return *requested
	}
	return savedConfig != nil && savedConfig.UseTLS
}

// TestSMTPConnection verifies a requested connection after filling missing values from current settings.
// Binding failures are rejected before transport work. Host, account, and password
// are trimmed; blank fields use saved values when available, and a nonpositive
// port selects a saved positive port or 587.
//
// Saved-read failures leave no saved configuration for these choices. The TLS
// pointer retains explicit false versus omission through ResolveSMTPUseTLS, and
// a still-empty host is rejected. The request context then reaches the shared
// mailer test; transport failures become a bad-request response rather than a saved settings change.
// POST /api/v1/admin/settings/test-smtp
func (h *Handler) TestSMTPConnection(c *gin.Context) {
	var req TestSMTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)

	// Saved configuration is an optional input to this administrator test.
	// A failed read leaves submitted values available and lets the transport report the resulting configuration outcome.
	var savedConfig *notification.SMTPConfig
	if cfg, err := h.emailService.GetSMTPConfig(c.Request.Context()); err == nil && cfg != nil {
		savedConfig = cfg
	}

	if req.SMTPHost == "" && savedConfig != nil {
		req.SMTPHost = savedConfig.Host
	}
	// Prefer a usable saved port when the request has no positive number;
	// the submission default remains available when both request and saved values are unusable.
	if req.SMTPPort <= 0 {
		if savedConfig != nil && savedConfig.Port > 0 {
			req.SMTPPort = savedConfig.Port
		} else {
			req.SMTPPort = 587
		}
	}
	if req.SMTPUsername == "" && savedConfig != nil {
		req.SMTPUsername = savedConfig.Username
	}
	password := strings.TrimSpace(req.SMTPPassword)
	if password == "" && savedConfig != nil {
		password = savedConfig.Password
	}
	if req.SMTPHost == "" {
		response.BadRequest(c, "SMTP host is required")
		return
	}

	config := &notification.SMTPConfig{
		Host:     req.SMTPHost,
		Port:     req.SMTPPort,
		Username: req.SMTPUsername,
		Password: password,
		UseTLS:   ResolveSMTPUseTLS(req.SMTPUseTLS, savedConfig),
	}

	err := h.emailService.TestSMTPConnectionContext(c.Request.Context(), config)
	if err != nil {
		response.BadRequest(c, "SMTP connection test failed: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "SMTP connection successful"})
}

// SendTestEmail sends an administrator test body using supplied and saved SMTP values.
// The recipient must pass request binding, and sender/connection strings are trimmed.
// Blank values use saved configuration, a nonpositive port selects a saved positive
// port or 587, and the TLS pointer retains an explicit choice.
//
// Saved configuration failures do not prevent complete request values from reaching
// the transport. The handler reads site branding for its fixed test message and
// passes the request context to a direct configured send. Successful sending
// returns an acknowledgement; this endpoint performs no template selection or settings persistence.
// POST /api/v1/admin/settings/send-test-email
func (h *Handler) SendTestEmail(c *gin.Context) {
	var req SendTestEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)
	req.SMTPFrom = strings.TrimSpace(req.SMTPFrom)
	req.SMTPFromName = strings.TrimSpace(req.SMTPFromName)

	// Saved configuration is an optional input to this administrator test.
	// A failed read leaves submitted values available and lets the transport report the resulting configuration outcome.
	var savedConfig *notification.SMTPConfig
	if cfg, err := h.emailService.GetSMTPConfig(c.Request.Context()); err == nil && cfg != nil {
		savedConfig = cfg
	}

	if req.SMTPHost == "" && savedConfig != nil {
		req.SMTPHost = savedConfig.Host
	}
	// Prefer a usable saved port when the request has no positive number;
	// the submission default remains available when both request and saved values are unusable.
	if req.SMTPPort <= 0 {
		if savedConfig != nil && savedConfig.Port > 0 {
			req.SMTPPort = savedConfig.Port
		} else {
			req.SMTPPort = 587
		}
	}
	if req.SMTPUsername == "" && savedConfig != nil {
		req.SMTPUsername = savedConfig.Username
	}
	password := strings.TrimSpace(req.SMTPPassword)
	if password == "" && savedConfig != nil {
		password = savedConfig.Password
	}
	if req.SMTPFrom == "" && savedConfig != nil {
		req.SMTPFrom = savedConfig.From
	}
	if req.SMTPFromName == "" && savedConfig != nil {
		req.SMTPFromName = savedConfig.FromName
	}
	if req.SMTPHost == "" {
		response.BadRequest(c, "SMTP host is required")
		return
	}

	config := &notification.SMTPConfig{
		Host:     req.SMTPHost,
		Port:     req.SMTPPort,
		Username: req.SMTPUsername,
		Password: password,
		From:     req.SMTPFrom,
		FromName: req.SMTPFromName,
		UseTLS:   ResolveSMTPUseTLS(req.SMTPUseTLS, savedConfig),
	}

	siteName := h.settingService.GetSiteName(c.Request.Context())
	subject := "[" + siteName + "] Test Email"
	body := `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #f5f5f5; margin: 0; padding: 20px; }
        .container { max-width: 600px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        .header { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white; padding: 30px; text-align: center; }
        .content { padding: 40px 30px; text-align: center; }
        .success { color: #10b981; font-size: 48px; margin-bottom: 20px; }
        .footer { background-color: #f8f9fa; padding: 20px; text-align: center; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>` + html.EscapeString(siteName) + `</h1>
        </div>
        <div class="content">
            <div class="success">✓</div>
            <h2>Email Configuration Successful!</h2>
            <p>This is a test email to verify your SMTP settings are working correctly.</p>
        </div>
        <div class="footer">
            <p>This is an automated test message.</p>
        </div>
    </div>
</body>
</html>
`

	if err := h.emailService.SendEmailWithConfigContext(c.Request.Context(), config, req.Email, subject, body); err != nil {
		response.BadRequest(c, "Failed to send test email: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "Test email sent successfully"})
}

// ListEmailTemplates returns editable events, manifest locales, and event-major template summaries.
// A missing notification service returns an internal error. Service failures use
// the shared error mapper, and placeholder union preserves first encounter order
// across the event definitions used for this listing.
// GET /api/v1/admin/settings/email-templates
func (h *Handler) ListEmailTemplates(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	events := h.notificationEmailService.ListEventInfos()
	templates, err := h.notificationEmailService.ListTemplates(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.EmailTemplateListResponse{
		Events:       emailTemplateEventOptionsToDTO(events),
		Locales:      h.notificationEmailService.SupportedLocales(),
		Templates:    emailTemplateSummariesToDTO(templates),
		Placeholders: emailTemplatePlaceholderUnion(events),
	})
}

// GetEmailTemplate returns selected template details for the path event and language.
// A missing service returns an internal error, and rejected event input becomes
// a bad request. The service's built-in-on-override-failure behavior determines the selected content.
// GET /api/v1/admin/settings/email-templates/:event/:locale
func (h *Handler) GetEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	tmpl, err := h.notificationEmailService.GetTemplate(c.Request.Context(), c.Param("event"), c.Param("locale"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, emailTemplateDetailToDTO(tmpl))
}

// UpdateEmailTemplate binds and saves a subject/HTML override for the path event and language.
// A missing service or invalid JSON is rejected before the update. Validation
// and persistence failures become bad requests, while success maps the service's
// selected template into the administrator detail response.
// PUT /api/v1/admin/settings/email-templates/:event/:locale
func (h *Handler) UpdateEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.UpdateEmailTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tmpl, err := h.notificationEmailService.UpdateTemplate(c.Request.Context(), c.Param("event"), c.Param("locale"), req.Subject, req.HTML)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, emailTemplateDetailToDTO(tmpl))
}

// RestoreOfficialEmailTemplate removes a stored override and returns the service's selected built-in details.
// A missing service returns an internal error. Invalid event and deletion failures
// become bad requests; the service treats a missing override as already restored.
// POST /api/v1/admin/settings/email-templates/:event/:locale/restore-official
func (h *Handler) RestoreOfficialEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	tmpl, err := h.notificationEmailService.RestoreOfficialTemplate(c.Request.Context(), c.Param("event"), c.Param("locale"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, emailTemplateDetailToDTO(tmpl))
}

// PreviewEmailTemplate renders unsaved subject/HTML content with request-supplied sample replacements.
// Missing service and malformed JSON are rejected before rendering. The service
// fills blank companion fields from the selected template and escapes body
// substitutions while retaining literal template markup. Validation failures
// become bad requests.
// POST /api/v1/admin/settings/email-template-preview
func (h *Handler) PreviewEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.PreviewEmailTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	preview, err := h.notificationEmailService.PreviewTemplate(c.Request.Context(), notification.NotificationEmailPreviewInput{
		Event:     req.Event,
		Locale:    req.Locale,
		Subject:   req.Subject,
		HTML:      req.HTML,
		Variables: req.Variables,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailTemplatePreviewResponse{Subject: preview.Subject, HTML: preview.HTML})
}

// emailTemplateEventOptionsToDTO maps event metadata into new ordered administrator option values.
func emailTemplateEventOptionsToDTO(events []notification.NotificationEmailEventInfo) []dto.EmailTemplateEventOption {
	items := make([]dto.EmailTemplateEventOption, 0, len(events))
	for _, event := range events {
		items = append(items, dto.EmailTemplateEventOption{
			Value:       event.Event,
			Label:       event.Label,
			Description: event.Description,
			Category:    event.Category,
			Optional:    event.Optional,
		})
	}
	return items
}

// emailTemplateSummariesToDTO maps ordered templates to subjects, override flags, and formatted save-time summaries.
func emailTemplateSummariesToDTO(templates []notification.NotificationEmailTemplate) []dto.EmailTemplateSummary {
	items := make([]dto.EmailTemplateSummary, 0, len(templates))
	for _, tmpl := range templates {
		items = append(items, dto.EmailTemplateSummary{
			Event:     tmpl.Event,
			Locale:    tmpl.Locale,
			Subject:   tmpl.Subject,
			IsCustom:  tmpl.IsCustom,
			UpdatedAt: emailTemplateUpdatedAt(tmpl),
		})
	}
	return items
}

// emailTemplateDetailToDTO maps selected content and metadata into one administrator detail value.
// It retains the input template's placeholder slice. Callers copy that slice
// before independently editing response metadata that must not alter the supplied template value.
func emailTemplateDetailToDTO(tmpl notification.NotificationEmailTemplate) dto.EmailTemplateDetail {
	return dto.EmailTemplateDetail{
		Event:        tmpl.Event,
		Locale:       tmpl.Locale,
		Subject:      tmpl.Subject,
		HTML:         tmpl.HTML,
		IsCustom:     tmpl.IsCustom,
		UpdatedAt:    emailTemplateUpdatedAt(tmpl),
		Placeholders: tmpl.Placeholders,
	}
}

// emailTemplateUpdatedAt returns an empty string for built-in content without a save time.
// A present timestamp is formatted at whole-second precision with its own timezone
// offset, retaining that offset for the administrator response rather than converting timezones.
func emailTemplateUpdatedAt(tmpl notification.NotificationEmailTemplate) string {
	if tmpl.UpdatedAt == nil {
		return ""
	}
	// Administrator metadata carries whole-second offset text; subsecond save
	// precision remains in the service timestamp and is omitted from this display representation.
	return tmpl.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
}

// emailTemplatePlaceholderUnion returns unique variable names in first-event and first-placeholder encounter order.
// The result is newly allocated, so listing consumers can change its slice without
// altering the event definitions or the placeholder slices supplied by the service.
func emailTemplatePlaceholderUnion(events []notification.NotificationEmailEventInfo) []string {
	seen := make(map[string]struct{})
	placeholders := make([]string, 0)
	for _, event := range events {
		for _, placeholder := range event.Placeholders {
			if _, ok := seen[placeholder]; ok {
				continue
			}
			seen[placeholder] = struct{}{}
			placeholders = append(placeholders, placeholder)
		}
	}
	return placeholders
}

// New binds the mailer, optional template service, and branding reader for administrator HTTP endpoints.
// Construction performs no network or settings work. SMTP tests use the mailer,
// while test-message branding and template endpoints require their corresponding assembled dependencies.
func New(email *notification.Mailer, n *notification.NotificationEmailService, site SiteNameReader) *Handler {
	return &Handler{emailService: email, notificationEmailService: n, settingService: site}
}

// RegisterSettingsRoutes installs SMTP testing and notification-template administration beneath adminSettings.
// The caller supplies the administrator group's authentication and other middleware
// before registration, so all bound handler routes inherit that configured request policy.
func RegisterSettingsRoutes(adminSettings *gin.RouterGroup, endpoint *Handler) {
	adminapi.Register(adminSettings, ManagementOperations["settings.test_smtp.create"], endpoint.TestSMTPConnection)
	adminapi.Register(adminSettings, ManagementOperations["settings.send_test_email.create"], endpoint.SendTestEmail)
	adminapi.Register(adminSettings, ManagementOperations["settings.email_templates.list"], endpoint.ListEmailTemplates)
	adminapi.Register(adminSettings, ManagementOperations["settings.email_template_preview.create"], endpoint.PreviewEmailTemplate)
	adminapi.Register(adminSettings, ManagementOperations["settings.email_templates.get"], endpoint.GetEmailTemplate)
	adminapi.Register(adminSettings, ManagementOperations["settings.email_templates.update"], endpoint.UpdateEmailTemplate)
	adminapi.Register(adminSettings, ManagementOperations["settings.email_templates.restore_official.create"], endpoint.RestoreOfficialEmailTemplate)
}
