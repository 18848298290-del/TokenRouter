package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"proxies.all.list": adminapi.Define(adminapi.Operation{
		ID:      "proxies.all.list",
		Module:  "proxies",
		Title:   "List all proxy records",
		TitleZH: "查询全部代理",
		Description: "GetAll returns active-status administrator proxy responses without pagination. The exact " +
			"with_count=true value chooses provider-count and probe augmentation; other values use " +
			"ordinary proxy responses, and time-based expiration is evaluated separately by maintenance. " +
			"This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/proxies/all",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/all", []string{"with_count"}, nil),
	}),
	"proxies.batch.create": adminapi.Define(adminapi.Operation{
		ID:     "proxies.batch.create",
		Module: "proxies",
		Title:  "Batch Create",
		Description: "BatchCreate validates an ordered connection list and creates each nonduplicate record with " +
			"the fixed default name. Duplicate matching uses trimmed host, port, username, and password " +
			"without protocol; a query error aborts remaining work.  Each persistence failure increments " +
			"skipped and allows later items to proceed, while earlier successful creations remain stored. " +
			"The returned created/skipped counts therefore describe partial batch work, with duplicate " +
			"and create failures combined in skipped. The administration service handles default " +
			"expiration policy and optional new-record probes separately from this handler's iteration. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/proxies/batch",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/batch", []string{}, (*BatchCreateRequest)(nil)),
	}),
	"proxies.batch_delete.create": adminapi.Define(adminapi.Operation{
		ID:     "proxies.batch_delete.create",
		Module: "proxies",
		Title:  "Batch Delete",
		Description: "BatchDelete validates a nonempty ID list and returns administration's independent per-item " +
			"deletion results. It does not deduplicate IDs or validate each ID's positivity here; skipped " +
			"errors remain part of the returned batch outcome. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/proxies/batch-delete",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/batch-delete", []string{}, (*BatchDeleteRequest)(nil)),
	}),
	"proxies.create": adminapi.Define(adminapi.Operation{
		ID:      "proxies.create",
		Module:  "proxies",
		Title:   "Create proxy",
		TitleZH: "创建代理",
		Description: "Create validates JSON and runs proxy creation through the bound administrator idempotency " +
			"executor. The raw bound request is the command payload; the callback trims connection " +
			"strings and converts positive Unix expiration seconds before service validation.  The " +
			"service returns a privileged proxy DTO containing its password, so routing supplies " +
			"administrator access. Nil or nonpositive expiration becomes absent time and empty fallback " +
			"mode is normalized by administration. A zero executor supports direct fixture callbacks, " +
			"while production binding supplies persistence-backed replay decisions and their write " +
			"retention period. Omitted body fields follow the HTTP handler defaults; explicit null and " +
			"empty collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/proxies",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Idempotent:  true,
		Input:       adminapi.Arguments("/api/v1/admin/proxies", []string{}, (*CreateProxyRequest)(nil)),
	}),
	"proxies.data.create": adminapi.Define(adminapi.Operation{
		ID:     "proxies.data.create",
		Module: "proxies",
		Title:  "Import Data",
		Description: "ImportData binds and validates a proxy transfer envelope before processing its proxy " +
			"collection. Invalid JSON or envelope headers return bad request; successful batch processing " +
			"returns partial creation/reuse/failure counts and diagnostics while repository-load failure " +
			"uses the shared error response. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings. Browser confirmation " +
			"by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/proxies/data",
		Sensitive:   true,
		Response:    "file",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/data", []string{}, (*ProxyDataImportRequest)(nil)),
	}),
	"proxies.data.list": adminapi.Define(adminapi.Operation{
		ID:     "proxies.data.list",
		Module: "proxies",
		Title:  "Export Data",
		Description: "ExportData returns an administrator proxy backup envelope containing credentials and an " +
			"export timestamp. Selected positive IDs bypass filters; otherwise search is trimmed and " +
			"capped at one hundred bytes before protocol/status/order values reach transfer export. The " +
			"route owns step-up authentication. This operation is available through read_operation. " +
			"Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/proxies/data",
		ReadOnly:    true,
		Sensitive:   true,
		StepUp:      true,
		Response:    "file",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/data", []string{"ids", "protocol", "search", "sort_by", "sort_order", "status"}, nil),
	}),
	"proxies.delete": adminapi.Define(adminapi.Operation{
		ID:      "proxies.delete",
		Module:  "proxies",
		Title:   "Delete proxy",
		TitleZH: "删除代理",
		Description: "Delete parses a signed decimal ID and delegates provider-count rejection and repository " +
			"deletion. Parse failure returns bad request, service errors use common response handling, " +
			"and success returns the fixed administrator confirmation payload.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/proxies/:id",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id", []string{}, nil),
	}),
	"proxies.get": adminapi.Define(adminapi.Operation{
		ID:      "proxies.get",
		Module:  "proxies",
		Title:   "Get proxy details",
		TitleZH: "查看代理详情",
		Description: "GetByID parses a signed decimal path ID and returns credential-bearing administrator proxy " +
			"data. Parse failure is bad request; repository errors use shared response handling, with " +
			"positivity and missing-record behavior determined by the delegated lookup. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/proxies/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id", []string{}, nil),
	}),
	"proxies.list": adminapi.Define(adminapi.Operation{
		ID:      "proxies.list",
		Module:  "proxies",
		Title:   "List proxy records",
		TitleZH: "查询代理",
		Description: "List returns a paginated administrator proxy DTO collection with provider counts and cached " +
			"probe details. It forwards protocol/status/order filters, trims search to a one-hundred-byte " +
			"prefix, and uses common pagination parsing and error response helpers. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/proxies",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies", []string{"page", "page_size", "protocol", "search", "sort_by", "sort_order", "status"}, nil),
	}),
	"proxies.providers.list": adminapi.Define(adminapi.Operation{
		ID:     "proxies.providers.list",
		Module: "proxies",
		Title:  "Get Proxy Providers",
		Description: "GetProxyProviders parses a signed decimal proxy ID and maps its bound-provider summaries for " +
			"administrator display. Nullable Notes remains shared through mapping; parsing and service " +
			"errors use the endpoint's common response helpers. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/proxies/:id/providers",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id/providers", []string{}, nil),
	}),
	"proxies.quality_check.create": adminapi.Define(adminapi.Operation{
		ID:     "proxies.quality_check.create",
		Module: "proxies",
		Title:  "Check Quality",
		Description: "CheckQuality parses a signed decimal ID and returns a weighted proxy target assessment. " +
			"Probe failures are represented by assessment items and counters; parsing or delegated " +
			"service errors use the common administrator error response.",
		Method:      "POST",
		Path:        "/api/v1/admin/proxies/:id/quality-check",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id/quality-check", []string{}, nil),
	}),
	"proxies.stats.list": adminapi.Define(adminapi.Operation{
		ID:     "proxies.stats.list",
		Module: "proxies",
		Title:  "Get Stats",
		Description: "GetStats validates decimal path-ID syntax and returns the fixed placeholder statistics " +
			"payload. It performs no repository lookup or measurements, so successful parsing yields the " +
			"same values even for an absent proxy ID. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/proxies/:id/stats",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id/stats", []string{}, nil),
	}),
	"proxies.test.create": adminapi.Define(adminapi.Operation{
		ID:      "proxies.test.create",
		Module:  "proxies",
		Title:   "Test proxy",
		TitleZH: "测试代理",
		Description: "Test parses a signed decimal ID and returns the service's connectivity result for its stored " +
			"proxy. A failed probe can be a successful HTTP response carrying Success=false; " +
			"lookup/service errors use shared response handling.",
		Method:      "POST",
		Path:        "/api/v1/admin/proxies/:id/test",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id/test", []string{}, nil),
	}),
	"proxies.update": adminapi.Define(adminapi.Operation{
		ID:      "proxies.update",
		Module:  "proxies",
		Title:   "Update proxy",
		TitleZH: "修改代理",
		Description: "Update parses a decimal ID, validates JSON, and forwards connection patches plus complete " +
			"expiration replacement to administration. Strings are trimmed, and only positive expiration " +
			"seconds become time values; nil or nonpositive expiration clears the service's optional " +
			"timestamp. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/proxies/:id",
		Response:    "json",
		BrowserPath: "/admin/proxies",
		Input:       adminapi.Arguments("/api/v1/admin/proxies/:id", []string{}, (*UpdateProxyRequest)(nil)),
	}),
	"tls_fingerprint_profiles.collector.sessions.captures.list": adminapi.Define(adminapi.Operation{
		ID:     "tls_fingerprint_profiles.collector.sessions.captures.list",
		Module: "tls-fingerprint-profiles",
		Title:  "List Collector Captures",
		Description: "ListCollectorCaptures returns the session's retained capture records for the token path " +
			"parameter. Missing adapter returns service unavailable; missing/expired session error " +
			"becomes not found, and success serializes shared immutable records. This operation is " +
			"available through read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/collector/sessions/:token/captures",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "file",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/collector/sessions/:token/captures", []string{}, nil),
	}),
	"tls_fingerprint_profiles.collector.sessions.create": adminapi.Define(adminapi.Operation{
		ID:     "tls_fingerprint_profiles.collector.sessions.create",
		Module: "tls-fingerprint-profiles",
		Title:  "Create Collector Session",
		Description: "CreateCollectorSession requests a temporary token, URL, expiry, and available trust value " +
			"from the collector. Missing adapter returns service unavailable, a session-creation failure " +
			"becomes bad request, and success returns that session value. Browser confirmation by the " +
			"token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/collector/sessions",
		Sensitive:   true,
		Response:    "file",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/collector/sessions", []string{}, nil),
	}),
	"tls_fingerprint_profiles.collector.sessions.delete": adminapi.Define(adminapi.Operation{
		ID:     "tls_fingerprint_profiles.collector.sessions.delete",
		Module: "tls-fingerprint-profiles",
		Title:  "Delete Collector Session",
		Description: "DeleteCollectorSession asks the collector to remove the supplied token and returns a fixed " +
			"success value. Missing adapter returns service unavailable; an unknown token is handled by " +
			"the adapter's no-op deletion rule. Browser confirmation by the token owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/collector/sessions/:token",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/collector/sessions/:token", []string{}, nil),
	}),
	"tls_fingerprint_profiles.collector.start.create": adminapi.Define(adminapi.Operation{
		ID:     "tls_fingerprint_profiles.collector.start.create",
		Module: "tls-fingerprint-profiles",
		Title:  "Start Collector",
		Description: "StartCollector delegates listener startup using the administrator request context when an " +
			"adapter is present. Missing adapter returns service unavailable, adapter startup failure " +
			"becomes bad request, and success returns its effective status.",
		Method:      "POST",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/collector/start",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/collector/start", []string{}, nil),
	}),
	"tls_fingerprint_profiles.collector.status.list": adminapi.Define(adminapi.Operation{
		ID:     "tls_fingerprint_profiles.collector.status.list",
		Module: "tls-fingerprint-profiles",
		Title:  "Collector Status",
		Description: "CollectorStatus returns current collector status or a zero status value when no adapter is " +
			"installed. It is a display query, while control/session operations use their separate " +
			"unavailable-adapter error responses. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/collector/status",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/collector/status", []string{}, nil),
	}),
	"tls_fingerprint_profiles.collector.stop.create": adminapi.Define(adminapi.Operation{
		ID:     "tls_fingerprint_profiles.collector.stop.create",
		Module: "tls-fingerprint-profiles",
		Title:  "Stop Collector",
		Description: "StopCollector delegates restartable listener shutdown with the administrator request " +
			"context. Missing adapter returns service unavailable; shutdown failure returns an internal " +
			"error, and successful stop reads the resulting collector status for its response.",
		Method:      "POST",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/collector/stop",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/collector/stop", []string{}, nil),
	}),
	"tls_fingerprint_profiles.create": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_profiles.create",
		Module:  "tls-fingerprint-profiles",
		Title:   "Create TLS fingerprint profile",
		TitleZH: "创建TLS 指纹配置",
		Description: "Create binds profile JSON, builds its editable values, and delegates validation and " +
			"persistence. Omitted GREASE input leaves false, and service ValidationError becomes bad " +
			"request; other write failures use the standard error response. Omitted body fields follow " +
			"the HTTP handler defaults; explicit null and empty collections retain their documented " +
			"meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/tls-fingerprint-profiles",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles", []string{}, (*CreateTLSFingerprintProfileRequest)(nil)),
	}),
	"tls_fingerprint_profiles.delete": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_profiles.delete",
		Module:  "tls-fingerprint-profiles",
		Title:   "Delete TLS fingerprint profile",
		TitleZH: "删除TLS 指纹配置",
		Description: "Delete parses a signed decimal ID and delegates profile removal plus service refresh. Parse " +
			"failure is bad request, write failure uses common response handling, and success returns the " +
			"fixed administrator confirmation payload.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/:id",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/:id", []string{}, nil),
	}),
	"tls_fingerprint_profiles.get": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_profiles.get",
		Module:  "tls-fingerprint-profiles",
		Title:   "Get TLS fingerprint profile details",
		TitleZH: "查看TLS 指纹配置详情",
		Description: "GetByID parses a signed decimal path ID and returns a persisted profile. Parse failure is " +
			"bad request, a nil repository result is not found, and other repository errors use shared " +
			"response handling. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/:id", []string{}, nil),
	}),
	"tls_fingerprint_profiles.list": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_profiles.list",
		Module:  "tls-fingerprint-profiles",
		Title:   "List TLS fingerprint profile records",
		TitleZH: "查询TLS 指纹配置",
		Description: "List returns persisted TLS profile values through the standard administrator success " +
			"envelope. Repository failure uses shared error handling; stored missing arrays are " +
			"normalized by the repository mapping before JSON serialization. This operation is available " +
			"through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/tls-fingerprint-profiles",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles", []string{}, nil),
	}),
	"tls_fingerprint_profiles.update": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_profiles.update",
		Module:  "tls-fingerprint-profiles",
		Title:   "Update TLS fingerprint profile",
		TitleZH: "修改TLS 指纹配置",
		Description: "Update reads the existing profile and applies decoded patch fields before submitting a " +
			"complete value to persistence. Invalid decimal ID or JSON returns bad request; a missing " +
			"profile returns not found.  Nil scalar pointers and list slices retain current values, while " +
			"present empty lists clear stored parameters and a supplied false GREASE flag remains an " +
			"update. Description JSON null is indistinguishable from omission in this request type. Read " +
			"and replacement are separate repository operations, so the assembled complete value can " +
			"overwrite intervening changes by another administrative write. Omitted body fields follow " +
			"the HTTP handler defaults; explicit null and empty collections retain their documented " +
			"meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/tls-fingerprint-profiles/:id",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-profiles",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-profiles/:id", []string{}, (*UpdateTLSFingerprintProfileRequest)(nil)),
	}),
	"tls_fingerprint_routers.create": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_routers.create",
		Module:  "tls-fingerprint-routers",
		Title:   "Create TLS fingerprint route",
		TitleZH: "创建TLS 指纹路由",
		Description: "Create binds router input, defaults Enabled to true, and supplies a present empty Rules " +
			"slice when omitted. The service normalizes and validates configuration before persistence; " +
			"ValidationError is a bad request and other failures use common responses. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/tls-fingerprint-routers",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-routers",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-routers", []string{}, (*CreateTLSFingerprintRouterRequest)(nil)),
	}),
	"tls_fingerprint_routers.delete": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_routers.delete",
		Module:  "tls-fingerprint-routers",
		Title:   "Delete TLS fingerprint route",
		TitleZH: "删除TLS 指纹路由",
		Description: "Delete parses a signed decimal router ID and delegates persistence removal plus service " +
			"refresh. Parse failure returns bad request, delegated errors use common response handling, " +
			"and success returns the fixed administrator confirmation payload.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/tls-fingerprint-routers/:id",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-routers",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-routers/:id", []string{}, nil),
	}),
	"tls_fingerprint_routers.get": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_routers.get",
		Module:  "tls-fingerprint-routers",
		Title:   "Get TLS fingerprint route details",
		TitleZH: "查看TLS 指纹路由详情",
		Description: "GetByID parses a signed decimal path ID and returns the selected persisted router. Parse " +
			"failure is bad request, an absent nil result is not found, and other repository errors use " +
			"standard response handling. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/tls-fingerprint-routers/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-routers",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-routers/:id", []string{}, nil),
	}),
	"tls_fingerprint_routers.list": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_routers.list",
		Module:  "tls-fingerprint-routers",
		Title:   "List TLS fingerprint route records",
		TitleZH: "查询TLS 指纹路由",
		Description: "List returns persisted router configuration through the common administrator success " +
			"envelope. The service forwards repository errors to shared response handling, and repository " +
			"mapping supplies a present Rules array when stored rules are absent. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/tls-fingerprint-routers",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-routers",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-routers", []string{}, nil),
	}),
	"tls_fingerprint_routers.update": adminapi.Define(adminapi.Operation{
		ID:      "tls_fingerprint_routers.update",
		Module:  "tls-fingerprint-routers",
		Title:   "Update TLS fingerprint route",
		TitleZH: "修改TLS 指纹路由",
		Description: "Update reads current router values and applies field-presence decisions before " +
			"complete-value replacement. Invalid ID or JSON is bad request, and an absent router is not " +
			"found. Dedicated profile patches use Set to distinguish omission from explicit null.  Other " +
			"nullable pointers and nil rule slices retain stored values, while present false, empty " +
			"strings, and empty rule arrays remain updates. The service then normalizes and validates the " +
			"merged value. Read and write are separate repository operations, so a patch can replace " +
			"intervening edits that occurred after the initial read. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/tls-fingerprint-routers/:id",
		Response:    "json",
		BrowserPath: "/admin/tls-fingerprint-routers",
		Input:       adminapi.Arguments("/api/v1/admin/tls-fingerprint-routers/:id", []string{}, (*UpdateTLSFingerprintRouterRequest)(nil)),
	}),
}

// ProxyDataImportRequest carries the administrative request decoded by its existing HTTP handler.
type ProxyDataImportRequest struct {
	Data transfer.DataPayload `json:"data"` // submitted shared backup envelope
}

// BatchDeleteRequest supplies the ordered IDs for independent administration deletions.
type BatchDeleteRequest struct {
	IDs []int64 `json:"ids" binding:"required,min=1"` // ordered proxy deletion identities
}
