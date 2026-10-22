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
	"settings.panel_rate_limit.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.panel_rate_limit.list",
		Module: "settings",
		Title:  "Get Panel Rate Limit Settings",
		Description: "GetPanelRateLimitSettings writes the current limits in the panel success envelope. The " +
			"administrator route supplies authorization, and this handler reads storage with the request " +
			"context. Storage failures are mapped through ErrorFrom. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/panel-rate-limit",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/panel-rate-limit", []string{}, nil),
	}),
	"settings.panel_rate_limit.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.panel_rate_limit.update",
		Module: "settings",
		Title:  "Update Panel Rate Limit Settings",
		Description: "UpdatePanelRateLimitSettings saves a full replacement and returns readback values. Binding " +
			"and save failures produce bad-request responses; a readback failure produces an error after " +
			"the successful save. Omitted input fields take their bool or integer zero values during Gin " +
			"binding. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings. Browser confirmation by the token owner is " +
			"required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings/panel-rate-limit",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/panel-rate-limit", []string{}, (*UpdatePanelRateLimitSettingsRequest)(nil)),
	}),
}
