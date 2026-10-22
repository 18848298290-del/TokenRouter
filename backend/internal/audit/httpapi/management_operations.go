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
	"audit_logs.clear.create": adminapi.Define(adminapi.Operation{
		ID:     "audit_logs.clear.create",
		Module: "audit-logs",
		Title:  "Clear",
		Description: "Clear deletes all audit records and persists a trace of the operation.  Authorization and " +
			"trace requirements:  1. Verify a fresh TOTP code for each clear, regardless of the step-up " +
			"window.  2. Reject administrators who have not enabled TOTP.  3. Reject admin API keys.  4. " +
			"Persist the actor, IP, user agent, and deleted count in the clearing transaction. Omitted " +
			"body fields follow the HTTP handler defaults; explicit null and empty collections retain " +
			"their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/audit-logs/clear",
		Sensitive:   true,
		Response:    adminapi.ResponseBrowser,
		BrowserPath: "/admin/audit-logs",
		Input:       adminapi.Arguments("/api/v1/admin/audit-logs/clear", []string{}, (*auditLogClearRequest)(nil)),
	}),
	"audit_logs.get": adminapi.Define(adminapi.Operation{
		ID:      "audit_logs.get",
		Module:  "audit-logs",
		Title:   "Get audit log details",
		TitleZH: "查看操作日志详情",
		Description: "Get returns an audit detail record including its stored redacted request body. The route " +
			"identifier must be a positive decimal integer; repository absence and read errors are " +
			"translated through the shared HTTP response helpers. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/audit-logs/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/audit-logs",
		Input:       adminapi.Arguments("/api/v1/admin/audit-logs/:id", []string{}, nil),
	}),
	"audit_logs.list": adminapi.Define(adminapi.Operation{
		ID:      "audit_logs.list",
		Module:  "audit-logs",
		Title:   "List audit log records",
		TitleZH: "查询操作日志",
		Description: "List parses administrator audit filters and returns a paginated summary response. Actor " +
			"identifiers must be positive and timestamps use RFC3339. A supplied success value selects " +
			"successful statuses only when it equals \"true\"; other nonempty values select failure " +
			"statuses, and page size is capped at two hundred. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/audit-logs",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/audit-logs",
		Input:       adminapi.Arguments("/api/v1/admin/audit-logs", []string{"action", "actor_email", "actor_user_id", "auth_method", "client_ip", "end_time", "method", "page", "page_size", "q", "request_id", "start_time", "success"}, nil),
	}),
}
