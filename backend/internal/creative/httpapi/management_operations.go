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
	"settings.creative_model_candidates.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.creative_model_candidates.list",
		Module: "settings",
		Title:  "List Creative Model Candidates",
		Description: "ListCreativeModelCandidates returns the model candidate list in a success envelope. A nil " +
			"handler or reader yields a server error, and lookup failures use the shared HTTP error " +
			"mapping. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/creative-model-candidates",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/creative-model-candidates", []string{}, nil),
	}),
	"settings.creative_worker_status.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.creative_worker_status.list",
		Module: "settings",
		Title:  "Get Creative Worker Status",
		Description: "GetCreativeWorkerStatus returns the current runtime snapshot from the configured callback. A " +
			"nil handler or callback yields a server error before any worker status can be read. This " +
			"operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/creative-worker-status",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/creative-worker-status", []string{}, nil),
	}),
}
