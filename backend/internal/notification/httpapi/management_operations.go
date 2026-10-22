package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/notification/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"settings.email_template_preview.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.email_template_preview.create",
		Module: "settings",
		Title:  "Preview Email Template",
		Description: "PreviewEmailTemplate renders unsaved subject/HTML content with request-supplied sample " +
			"replacements. Missing service and malformed JSON are rejected before rendering. The service " +
			"fills blank companion fields from the selected template and escapes body substitutions while " +
			"retaining literal template markup. Validation failures become bad requests. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/email-template-preview",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/email-template-preview", []string{}, (*dto.PreviewEmailTemplateRequest)(nil)),
	}),
	"settings.email_templates.get": adminapi.Define(adminapi.Operation{
		ID:     "settings.email_templates.get",
		Module: "settings",
		Title:  "Get Email Template",
		Description: "GetEmailTemplate returns selected template details for the path event and language. A " +
			"missing service returns an internal error, and rejected event input becomes a bad request. " +
			"The service's built-in-on-override-failure behavior determines the selected content. This " +
			"operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/email-templates/:event/:locale",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/email-templates/:event/:locale", []string{}, nil),
	}),
	"settings.email_templates.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.email_templates.list",
		Module: "settings",
		Title:  "List Email Templates",
		Description: "ListEmailTemplates returns editable events, manifest locales, and event-major template " +
			"summaries. A missing notification service returns an internal error. Service failures use " +
			"the shared error mapper, and placeholder union preserves first encounter order across the " +
			"event definitions used for this listing. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/email-templates",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/email-templates", []string{}, nil),
	}),
	"settings.email_templates.restore_official.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.email_templates.restore_official.create",
		Module: "settings",
		Title:  "Restore Official Email Template",
		Description: "RestoreOfficialEmailTemplate removes a stored override and returns the service's selected " +
			"built-in details. A missing service returns an internal error. Invalid event and deletion " +
			"failures become bad requests; the service treats a missing override as already restored. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/email-templates/:event/:locale/restore-official",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/email-templates/:event/:locale/restore-official", []string{}, nil),
	}),
	"settings.email_templates.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.email_templates.update",
		Module: "settings",
		Title:  "Update Email Template",
		Description: "UpdateEmailTemplate binds and saves a subject/HTML override for the path event and language. " +
			"A missing service or invalid JSON is rejected before the update. Validation and persistence " +
			"failures become bad requests, while success maps the service's selected template into the " +
			"administrator detail response. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings. Browser confirmation " +
			"by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings/email-templates/:event/:locale",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/email-templates/:event/:locale", []string{}, (*dto.UpdateEmailTemplateRequest)(nil)),
	}),
	"settings.send_test_email.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.send_test_email.create",
		Module: "settings",
		Title:  "Send Test Email",
		Description: "SendTestEmail sends an administrator test body using supplied and saved SMTP values. The " +
			"recipient must pass request binding, and sender/connection strings are trimmed. Blank values " +
			"use saved configuration, a nonpositive port selects a saved positive port or 587, and the " +
			"TLS pointer retains an explicit choice.  Saved configuration failures do not prevent " +
			"complete request values from reaching the transport. The handler reads site branding for its " +
			"fixed test message and passes the request context to a direct configured send. Successful " +
			"sending returns an acknowledgement; this endpoint performs no template selection or settings " +
			"persistence. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings. Browser confirmation by the token owner is " +
			"required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/send-test-email",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/send-test-email", []string{}, (*SendTestEmailRequest)(nil)),
	}),
	"settings.test_smtp.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.test_smtp.create",
		Module: "settings",
		Title:  "Test SMTP Connection",
		Description: "TestSMTPConnection verifies a requested connection after filling missing values from current " +
			"settings. Binding failures are rejected before transport work. Host, account, and password " +
			"are trimmed; blank fields use saved values when available, and a nonpositive port selects a " +
			"saved positive port or 587.  Saved-read failures leave no saved configuration for these " +
			"choices. The TLS pointer retains explicit false versus omission through ResolveSMTPUseTLS, " +
			"and a still-empty host is rejected. The request context then reaches the shared mailer test; " +
			"transport failures become a bad-request response rather than a saved settings change. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/test-smtp",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/test-smtp", []string{}, (*TestSMTPRequest)(nil)),
	}),
}
