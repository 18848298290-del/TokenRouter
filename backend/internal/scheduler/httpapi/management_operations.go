package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"providers.advanced_scheduler_score.list": adminapi.Define(adminapi.Operation{
		ID:     "providers.advanced_scheduler_score.list",
		Module: "providers",
		Title:  "Get Advanced Scheduler Score",
		Description: "GetAdvancedSchedulerScore returns advanced-group summaries or a baseline explanation for one " +
			"positive group ID. It validates the provider first, then reports an unavailable source or " +
			"decodes the optional group query; recognized diagnostic input errors become bad requests. " +
			"This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/providers/:id/advanced-scheduler-score",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/providers",
		Input:       adminapi.Arguments("/api/v1/admin/providers/:id/advanced-scheduler-score", []string{"group_id"}, nil),
	}),
	"providers.advanced_scheduler_score.preview.create": adminapi.Define(adminapi.Operation{
		ID:     "providers.advanced_scheduler_score.preview.create",
		Module: "providers",
		Title:  "Preview Advanced Scheduler Score",
		Description: "PreviewAdvancedSchedulerScore returns an advanced-group explanation using " +
			"administrator-supplied model and provider IDs. It requires one JSON object containing known " +
			"request fields and a positive group ID, then converts input-related diagnostic errors to bad " +
			"requests. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings. This operation is available through " +
			"read_operation.",
		Method:      "POST",
		Path:        "/api/v1/admin/providers/:id/advanced-scheduler-score/preview",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/providers",
		Input:       adminapi.Arguments("/api/v1/admin/providers/:id/advanced-scheduler-score/preview", []string{}, (*policy.AdvancedSchedulerScoreDiagnosticRequest)(nil)),
	}),
}
