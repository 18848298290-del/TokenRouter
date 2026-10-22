package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"settings.web_search_emulation.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.web_search_emulation.list",
		Module: "settings",
		Title:  "Get Web Search Emulation Config",
		Description: "GetWebSearchEmulationConfig returns a configuration copy decorated with current Manager " +
			"usage and key-presence flags. Configured API keys remain in the response, and service read " +
			"failures map through the common HTTP error responder. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/web-search-emulation",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/web-search-emulation", []string{}, nil),
	}),
	"settings.web_search_emulation.reset_usage.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.web_search_emulation.reset_usage.create",
		Module: "settings",
		Title:  "Reset Web Search Usage",
		Description: "ResetWebSearchUsage deletes the literal supplier counter selected by the JSON provider_type. " +
			"An exactly empty type is rejected; other strings reach Manager's reset operation, and a " +
			"missing Manager returns an error. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings. Browser confirmation " +
			"by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/web-search-emulation/reset-usage",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/web-search-emulation/reset-usage", []string{}, (*WebSearchUsageResetRequest)(nil)),
	}),
	"settings.web_search_emulation.test.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.web_search_emulation.test.create",
		Module: "settings",
		Title:  "Test Web Search Emulation",
		Description: "TestWebSearchEmulation binds an administrator query and runs a fifteen-second test through " +
			"the current Manager. Empty or whitespace-only queries select the built-in Chinese query; " +
			"malformed JSON is rejected before testing. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings. Browser " +
			"confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/web-search-emulation/test",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/web-search-emulation/test", []string{}, (*WebSearchEmulationTestRequest)(nil)),
	}),
	"settings.web_search_emulation.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.web_search_emulation.update",
		Module: "settings",
		Title:  "Update Web Search Emulation Config",
		Description: "UpdateWebSearchEmulationConfig validates and saves submitted search settings, then returns " +
			"the published configuration with current usage. Exactly empty keys can retain stored " +
			"credentials, and the response includes keys; later read failures are reported after " +
			"persistence. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings. Browser confirmation by the token owner is " +
			"required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings/web-search-emulation",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/web-search-emulation", []string{}, (*search.WebSearchEmulationConfig)(nil)),
	}),
}

// WebSearchEmulationTestRequest carries the administrative request decoded by its existing HTTP handler.
type WebSearchEmulationTestRequest struct {
	// Query is submitted as supplied when it contains non-whitespace text. An
	// omitted, empty, or whitespace-only value selects the handler's built-in
	// test query.
	Query string `json:"query"`
}

// WebSearchUsageResetRequest carries the administrative request decoded by its existing HTTP handler.
type WebSearchUsageResetRequest struct {
	// ProviderType selects a literal Redis counter suffix. The handler rejects
	// the empty string and passes other values through without trimming or
	// supplier validation.
	ProviderType string `json:"provider_type"`
}
