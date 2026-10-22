package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"risk_control.api_keys.test.create": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.api_keys.test.create",
		Module: "risk-control",
		Title:  "Test API Keys",
		Description: "TestAPIKeys decodes administrator audit candidates and temporary connection overrides. It " +
			"passes the request context through key testing, returns malformed input as a bad request, " +
			"and exposes service results or validation errors through standard responses. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/risk-control/api-keys/test",
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/api-keys/test", []string{}, (*contentModerationAPIKeyTestRequest)(nil)),
	}),
	"risk_control.config.list": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.config.list",
		Module: "risk-control",
		Title:  "Get Config",
		Description: "GetConfig returns the moderation configuration with masked credential observations. It " +
			"passes the HTTP request context to the service and converts a settings read error through " +
			"the common response helper. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/config",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/config", []string{}, nil),
	}),
	"risk_control.config.update": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.config.update",
		Module: "risk-control",
		Title:  "Update Config",
		Description: "UpdateConfig decodes partial administrator configuration and passes its presence-aware " +
			"fields to the service. Invalid JSON returns a bad request; persistence or validation errors " +
			"use the common error response, and success returns the resulting masked view. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/risk-control/config",
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/config", []string{}, (*contentModerationConfigRequest)(nil)),
	}),
	"risk_control.cyber_summary.list": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.cyber_summary.list",
		Module: "risk-control",
		Title:  "Get Cyber Summary",
		Description: "GetCyberSummary parses administrator warning filters with pagination omitted and returns " +
			"grouped observations. Identifier and date errors stop the request before service access; the " +
			"repository applies its own fixed summary group limits. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/cyber-summary",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/cyber-summary", []string{"from", "page", "page_size", "provider_id", "request_id", "search", "to", "user_id"}, nil),
	}),
	"risk_control.cyber_warnings.get": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.cyber_warnings.get",
		Module: "risk-control",
		Title:  "Get Cyber Warning",
		Description: "GetCyberWarning returns full Cyber review material for a positive decimal path ID. Invalid " +
			"IDs fail before service access; the service classifies unavailable review storage and " +
			"missing rows for the common error response. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/cyber-warnings/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/cyber-warnings/:id", []string{}, nil),
	}),
	"risk_control.cyber_warnings.list": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.cyber_warnings.list",
		Module: "risk-control",
		Title:  "List Cyber Warnings",
		Description: "ListCyberWarnings parses administrator warning filters with pagination enabled and returns " +
			"their result page. Invalid identifiers or dates stop before repository access, while service " +
			"read failures use the shared error response. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/cyber-warnings",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/cyber-warnings", []string{"from", "page", "page_size", "provider_id", "request_id", "search", "to", "user_id"}, nil),
	}),
	"risk_control.hashes.all.delete": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.hashes.all.delete",
		Module: "risk-control",
		Title:  "Clear Flagged Hashes",
		Description: "ClearFlaggedHashes requests removal of the shared flagged-input set through moderation. " +
			"Success returns the adapter count sampled before deletion; service or Redis errors receive " +
			"the common response instead of a successful removal result.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/risk-control/hashes/all",
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/hashes/all", []string{}, nil),
	}),
	"risk_control.hashes.delete": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.hashes.delete",
		Module: "risk-control",
		Title:  "Delete Flagged Hash",
		Description: "DeleteFlaggedHash decodes a requested input hash and delegates normalization and deletion to " +
			"moderation. Invalid JSON fails before cache access; service validation and Redis failures " +
			"return through standard error responses. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/risk-control/hashes",
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/hashes", []string{}, (*contentModerationHashRequest)(nil)),
	}),
	"risk_control.logs.get": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.logs.get",
		Module: "risk-control",
		Title:  "Get Log",
		Description: "GetLog returns full administrator review material for a positive decimal path ID. Invalid " +
			"IDs return a bad request before storage access; missing review data and service errors use " +
			"the common error response. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/logs/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/logs/:id", []string{}, nil),
	}),
	"risk_control.logs.list": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.logs.list",
		Module: "risk-control",
		Title:  "List Logs",
		Description: "ListLogs builds administrator audit filters from query values and parsed pagination. Group " +
			"IDs must be positive; from and to accept RFC3339 or UTC dates. A date-only to value becomes " +
			"the inclusive final nanosecond of its fixed twenty-four-hour day. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/logs",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/logs", []string{"endpoint", "from", "group_id", "page", "page_size", "request_id", "result", "search", "to"}, nil),
	}),
	"risk_control.media.content.list": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.media.content.list",
		Module: "risk-control",
		Title:  "Get Media Content",
		Description: "GetMediaContent serves a ready administrator image snapshot for a positive decimal path ID. " +
			"It returns the stored MIME type and bytes with private/no-store and nosniff headers; " +
			"unavailable or failed snapshots receive a service error response. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/media/:id/content",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/media/:id/content", []string{}, nil),
	}),
	"risk_control.status.list": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.status.list",
		Module: "risk-control",
		Title:  "Get Status",
		Description: "GetStatus returns sampled moderation queue, synchronous audit, key-health, and hash " +
			"observations. Service reads use the HTTP request context; the result reflects separate " +
			"samples taken by GetStatus rather than one locked runtime snapshot. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/risk-control/status",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/status", []string{}, nil),
	}),
	"risk_control.users.unban.create": adminapi.Define(adminapi.Operation{
		ID:     "risk_control.users.unban.create",
		Module: "risk-control",
		Title:  "Unban User",
		Description: "UnbanUser requests active status for a positive decimal user path ID. Invalid IDs fail " +
			"before service access; successful responses follow any needed status write and " +
			"authentication invalidation performed by the service.",
		Method:      "POST",
		Path:        "/api/v1/admin/risk-control/users/:user_id/unban",
		Response:    "json",
		BrowserPath: "/admin/risk-control",
		Input:       adminapi.Arguments("/api/v1/admin/risk-control/users/:user_id/unban", []string{}, nil),
	}),
}
