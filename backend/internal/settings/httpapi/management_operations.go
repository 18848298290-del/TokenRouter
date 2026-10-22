package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"settings.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.list",
		Module: "settings",
		Title:  "Get Settings",
		Description: "GetSettings returns parsed administrator settings with grants, effective monitoring, payment " +
			"data, and optional tier policy. System and grant read errors stop the response; payment read " +
			"errors are discarded while a returned config can still be used, or nil becomes an empty " +
			"config.  Monitoring is intersected with the installed service's effective availability, and " +
			"this GET fills provider thresholds from the Snapshot. A fast-policy read error is logged and " +
			"its optional field remains absent. The final flattener appends authentication-source fields " +
			"and follows its JSON-conversion error rule before the success response is emitted. This " +
			"operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings", []string{}, nil),
	}),
	"settings.pre_aggregation.backfill.create": adminapi.Define(adminapi.Operation{
		ID:     "settings.pre_aggregation.backfill.create",
		Module: "settings",
		Title:  "Backfill Pre Aggregation",
		Description: "BackfillPreAggregation admits recent usage backfill after checking deployment permission, " +
			"day limits, and current usage enablement. It computes the requested UTC calendar-day range " +
			"and delegates persistence plus task wakeup before returning 202 acceptance with the " +
			"submitted days.  The usage service owns hour alignment and later execution. Its disabled and " +
			"range-limit errors become forbidden or bad-request responses, while other trigger failures " +
			"keep their error classification. Acceptance reports admitted work; clients use later task " +
			"observations to determine completion and coverage. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/settings/pre-aggregation/backfill",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/pre-aggregation/backfill", []string{}, (*preAggregationBackfillRequest)(nil)),
	}),
	"settings.pre_aggregation.list": adminapi.Define(adminapi.Operation{
		ID:     "settings.pre_aggregation.list",
		Module: "settings",
		Title:  "Get Pre Aggregation Settings",
		Description: "GetPreAggregationSettings returns resolved controls, deployment permissions, and observed " +
			"task status through the success response. An absent controller fails; settings read failures " +
			"follow Resolve's cached or default display selection. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/settings/pre-aggregation",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/pre-aggregation", []string{}, nil),
	}),
	"settings.pre_aggregation.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.pre_aggregation.update",
		Module: "settings",
		Title:  "Update Pre Aggregation Settings",
		Description: "UpdatePreAggregationSettings validates and persists a complete runtime replacement. The " +
			"usage interval must be 30 through 3,600 seconds, and submitted enabled switches require " +
			"deployment availability. Omitted object fields keep the bound struct's zero values; this " +
			"endpoint supplies no partial merge. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings. Browser confirmation " +
			"by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings/pre-aggregation",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings/pre-aggregation", []string{}, (*preaggregation.PreAggregationSettings)(nil)),
	}),
	"settings.update": adminapi.Define(adminapi.Operation{
		ID:     "settings.update",
		Module: "settings",
		Title:  "Update Settings",
		Description: "UpdateSettings validates a partial administrator request, commits one combined batch, and " +
			"returns its saved readback. It decodes raw presence before typed values, rejects retired " +
			"fields before service access, and holds an update session from current-value reads through " +
			"response construction.  Pointer fields use specific prior-value/default selection, while " +
			"omitted non-pointer keys are removed after normalization. The handler prepares every " +
			"registered owner before Commit and replaces broad-map keys with those results. Changed " +
			"credential probes and step-up transitions occur before persistence.  Commit success precedes " +
			"audit and final reads. Runtime application errors carry persisted=true from the session; " +
			"response read failures can also follow saved settings and completed audit logging. Optional " +
			"fast-policy read failures are logged, while payment readback failure stops PUT response " +
			"construction. The caller supplies complete module dependencies before reaching their " +
			"branches. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings. Browser confirmation by the token owner is " +
			"required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/settings",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/settings",
		Input:       adminapi.Arguments("/api/v1/admin/settings", []string{}, (*UpdateSettingsRequest)(nil)),
	}),
}
