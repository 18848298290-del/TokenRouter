package app

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
	"requests.list": adminapi.Define(adminapi.Operation{
		ID:     "requests.list",
		Module: "requests",
		Title:  "Find request records",
		Description: "Find recorded request diagnostics by request_id. Results are restricted to authenticated " +
			"administrators.",
		Method:   "GET",
		Path:     "/api/v1/admin/requests",
		ReadOnly: true,
		Input:    adminapi.Arguments("/api/v1/admin/requests", []string{"request_id"}, nil),
	}),
	"requests.get": adminapi.Define(adminapi.Operation{
		ID:          "requests.get",
		Module:      "requests",
		Title:       "Get request records",
		Description: "Find recorded request diagnostics using the path request_id.",
		Method:      "GET",
		Path:        "/api/v1/admin/requests/:request_id",
		ReadOnly:    true,
		Input:       adminapi.Arguments("/api/v1/admin/requests/:request_id", []string{"request_id"}, nil),
	}),
	"requests.health": adminapi.Define(adminapi.Operation{
		ID:          "requests.health",
		Module:      "requests",
		Title:       "Read request recorder health",
		Description: "Read recorder queue and persistence health without changing collection state.",
		Method:      "GET",
		Path:        "/api/v1/admin/requests/health",
		ReadOnly:    true,
		Input:       adminapi.Arguments("/api/v1/admin/requests/health", nil, nil),
	}),
	"protocol_capabilities.list": adminapi.Define(adminapi.Operation{
		ID:     "protocol_capabilities.list",
		Module: "protocol-capabilities",
		Title:  "Protocol Capabilities",
		Description: "Lists supported protocols and provider profiles. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/protocol-capabilities",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/protocol-capabilities",
		Input:       adminapi.Arguments("/api/v1/admin/protocol-capabilities", []string{}, nil),
	}),
}
