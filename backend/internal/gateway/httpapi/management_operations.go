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
	"error_passthrough_rules.create": adminapi.Define(adminapi.Operation{
		ID:      "error_passthrough_rules.create",
		Module:  "error-passthrough-rules",
		Title:   "Create error rule",
		TitleZH: "创建错误规则",
		Description: "Create binds JSON and constructs an error rule with enabled and passthrough switches " +
			"defaulting true. Empty match mode selects any; optional response fields retain their " +
			"pointers. Nil condition lists become arrays before validation and service persistence, and " +
			"validation failures receive 400. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/error-passthrough-rules",
		Response:    "json",
		BrowserPath: "/admin/error-passthrough-rules",
		Input:       adminapi.Arguments("/api/v1/admin/error-passthrough-rules", []string{}, (*CreateErrorPassthroughRuleRequest)(nil)),
	}),
	"error_passthrough_rules.delete": adminapi.Define(adminapi.Operation{
		ID:      "error_passthrough_rules.delete",
		Module:  "error-passthrough-rules",
		Title:   "Delete error rule",
		TitleZH: "删除错误规则",
		Description: "Delete removes the rule identified by the decimal route ID. Invalid syntax returns 400, " +
			"service failures use shared presentation and successful removal returns the standard admin " +
			"success envelope with its completion message.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/error-passthrough-rules/:id",
		Response:    "json",
		BrowserPath: "/admin/error-passthrough-rules",
		Input:       adminapi.Arguments("/api/v1/admin/error-passthrough-rules/:id", []string{}, nil),
	}),
	"error_passthrough_rules.get": adminapi.Define(adminapi.Operation{
		ID:      "error_passthrough_rules.get",
		Module:  "error-passthrough-rules",
		Title:   "Get error rule details",
		TitleZH: "查看错误规则详情",
		Description: "GetByID reads one rule using the decimal route ID. Invalid ID syntax receives 400, missing " +
			"rules receive 404 and service errors use shared presentation; this handler does not apply " +
			"rule matching to the lookup. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/error-passthrough-rules/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/error-passthrough-rules",
		Input:       adminapi.Arguments("/api/v1/admin/error-passthrough-rules/:id", []string{}, nil),
	}),
	"error_passthrough_rules.list": adminapi.Define(adminapi.Operation{
		ID:      "error_passthrough_rules.list",
		Module:  "error-passthrough-rules",
		Title:   "List error rule records",
		TitleZH: "查询错误规则",
		Description: "List returns the shared service's error rules in the admin success envelope. It uses the " +
			"request context; service failures pass through the common error response helper for this " +
			"authenticated route. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/error-passthrough-rules",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/error-passthrough-rules",
		Input:       adminapi.Arguments("/api/v1/admin/error-passthrough-rules", []string{}, nil),
	}),
	"error_passthrough_rules.update": adminapi.Define(adminapi.Operation{
		ID:      "error_passthrough_rules.update",
		Module:  "error-passthrough-rules",
		Title:   "Update error rule",
		TitleZH: "修改错误规则",
		Description: "Update applies supplied JSON fields to the rule loaded by decimal ID and persists the " +
			"resulting complete value. Missing IDs return 404 and invalid input returns 400 before " +
			"saving.  Scalar pointers distinguish supplied values from omitted or null fields, which " +
			"retain existing values. Slice fields follow the same nil check, but an empty array replaces " +
			"the old list. Optional response-code, custom-message and description pointers cannot be " +
			"cleared by JSON null through this request representation. Starting from existing values " +
			"keeps omitted settings and rule identity together before service validation; nil lists are " +
			"normalized for response shape. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/error-passthrough-rules/:id",
		Response:    "json",
		BrowserPath: "/admin/error-passthrough-rules",
		Input:       adminapi.Arguments("/api/v1/admin/error-passthrough-rules/:id", []string{}, (*UpdateErrorPassthroughRuleRequest)(nil)),
	}),
	"settings.beta_policy.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.beta_policy.list",
		Module: "settings",
		Title:  "Get Beta Policy Settings",
		Description: "GetBetaPolicySettings returns the shared service's ordered beta rules as HTTP DTO values. A " +
			"zero-length list is encoded as an array; service errors are presented using the request " +
			"context and shared response helper. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/beta-policy",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/beta-policy", []string{}, nil),
	}),
	"settings.beta_policy.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.beta_policy.update",
		Module: "settings",
		Title:  "Update Beta Policy Settings",
		Description: "UpdateBetaPolicySettings replaces the supplied ordered beta rules after service validation. " +
			"The service can normalize whitelist strings in those rules, and the response reads settings " +
			"again after saving; readback failure does not roll back persisted configuration. Omitted " +
			"body fields follow the HTTP handler defaults; explicit null and empty collections retain " +
			"their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings/beta-policy",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/beta-policy", []string{}, (*UpdateBetaPolicySettingsRequest)(nil)),
	}),
	"settings.rectifier.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.rectifier.list",
		Module: "settings",
		Title:  "Get Rectifier Settings",
		Description: "GetRectifierSettings returns stored or service-default request-correction settings in the " +
			"HTTP success envelope. Missing pattern slices become JSON arrays, and service failures use " +
			"shared error presentation with the request context. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/rectifier",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/rectifier", []string{}, nil),
	}),
	"settings.rectifier.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.rectifier.update",
		Module: "settings",
		Title:  "Update Rectifier Settings",
		Description: "UpdateRectifierSettings replaces the correction switches and cleaned signature patterns. It " +
			"bounds supplied entry count before trimming, drops blank patterns and limits each retained " +
			"string by bytes. Saving precedes a readback response; a readback failure can follow " +
			"successful persistence. Omitted body fields follow the HTTP handler defaults; explicit null " +
			"and empty collections retain their documented meanings. Browser confirmation by the token " +
			"owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings/rectifier",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/rectifier", []string{}, (*UpdateRectifierSettingsRequest)(nil)),
	}),
}
