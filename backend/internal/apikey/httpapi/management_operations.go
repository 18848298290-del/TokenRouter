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
	"api_keys.update": adminapi.Define(adminapi.Operation{
		ID:     "api_keys.update",
		Module: "api-keys",
		Title:  "Update Group",
		Description: "UpdateGroup decodes the administrator's optional binding and reset choices and calls one Key " +
			"administration operation. Parseable signed IDs reach the service; malformed IDs or JSON " +
			"return bad requests. Service failures use the shared application error response, while " +
			"success returns the mapped Key and any reported exclusive-group grant details. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/api-keys/:id",
		Response:    "json",
		BrowserPath: "/admin/api-keys",
		Input:       adminapi.Arguments("/api/v1/admin/api-keys/:id", []string{}, (*AdminUpdateAPIKeyGroupRequest)(nil)),
	}),
}
