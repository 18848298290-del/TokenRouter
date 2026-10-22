package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"groups.all.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.all.list",
		Module:  "groups",
		Title:   "List all group records",
		TitleZH: "查询全部分组",
		Description: "GetAll returns administrator group choices through the active-group reader by default. " +
			"include_inactive=true selects the broader administration reader, and any supplied platform " +
			"parameter is rejected before reading groups. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/all",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/all", []string{"include_inactive", "platform"}, nil),
	}),
	"groups.api_keys.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.api_keys.list",
		Module:  "groups",
		Title:   "Get Group API Keys",
		TitleZH: "查询分组 API 密钥",
		Description: "GetGroupAPIKeys returns a paginated list of keys linked to the route's group ID. It parses " +
			"the numeric ID and standard page parameters, then returns the reader's total or its " +
			"structured error response. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/:id/api-keys",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/api-keys", []string{"page", "page_size"}, nil),
	}),
	"groups.capacity_summary.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.capacity_summary.list",
		Module:  "groups",
		Title:   "Get Capacity Summary",
		TitleZH: "查询分组容量汇总",
		Description: "GetCapacitySummary returns aggregate concurrency, session, and RPM observations for active " +
			"groups. It delegates the request context to the capacity reader and returns the generic HTTP " +
			"500 summary response when that query fails. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/capacity-summary",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/capacity-summary", []string{}, nil),
	}),
	"groups.create": adminapi.Define(adminapi.Operation{
		Examples: []map[string]any{{"body": map[string]any{"name": "Example group", "rate_multiplier": 1}}},
		ID:       "groups.create",
		Module:   "groups",
		Title:    "Create group",
		TitleZH:  "创建分组",
		Description: "Create strictly decodes a group creation request and returns its administrator DTO after " +
			"administration succeeds. Product defaults, validation, provider associations, and " +
			"persistence belong to GroupAdmin before the normalized settings are mapped for the response. " +
			"If AllowedProtocols is absent and a legacy text selection is present, the handler passes " +
			"that selection with LegacyProtocolInput so GroupAdmin can apply compatibility rules. A " +
			"supplied complete selection, including an empty array, takes precedence. Omitted body fields " +
			"follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/groups",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Output:      adminapi.JSONResponse((*dto.AdminGroup[struct{}])(nil)),
		Input:       adminapi.Arguments("/api/v1/admin/groups", []string{}, (*CreateGroupRequest)(nil)),
	}),
	"groups.delete": adminapi.Define(adminapi.Operation{
		ID:      "groups.delete",
		Module:  "groups",
		Title:   "Delete group",
		TitleZH: "删除分组",
		Description: "Delete parses the route's numeric group ID and asks administration to remove it. Parse " +
			"failures become bad requests, administration failures use the standard error response, and " +
			"successful removal returns the deletion message.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/groups/:id",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id", []string{}, nil),
	}),
	"groups.duplicate.create": adminapi.Define(adminapi.Operation{
		ID:      "groups.duplicate.create",
		Module:  "groups",
		Title:   "Copy group",
		TitleZH: "复制分组",
		Description: "Duplicate returns a disabled group copy identified by a positive source ID and the " +
			"administrator's request scope. The idempotency executor can return a saved response; the " +
			"service also stores duplication identity for the same source, actor, and key.  When " +
			"execution reports an in-progress operation or unavailable idempotency storage, the handler " +
			"attempts to load the committed copy before returning that original error. A found copy " +
			"produces X-Idempotency-Recovered, while ordinary response replay produces " +
			"X-Idempotency-Replayed; recovery read failures are logged and the original execution error " +
			"remains the response.",
		Method:      "POST",
		Path:        "/api/v1/admin/groups/:id/duplicate",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Idempotent:  true,
		Output:      adminapi.JSONResponse((*dto.AdminGroup[struct{}])(nil)),
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/duplicate", []string{}, nil),
	}),
	"groups.get": adminapi.Define(adminapi.Operation{
		ID:      "groups.get",
		Module:  "groups",
		Title:   "Get group details",
		TitleZH: "查看分组详情",
		Description: "GetByID parses the route's numeric group ID and returns its administrator DTO. Parse " +
			"failures become bad requests; administration determines whether the parsed ID identifies an " +
			"available stored group. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Output:      adminapi.JSONResponse((*dto.AdminGroup[struct{}])(nil)),
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id", []string{}, nil),
	}),
	"groups.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.list",
		Module:  "groups",
		Title:   "List group records",
		TitleZH: "查询分组",
		Description: "List returns administrator group rows with standard pagination and requested filters. It " +
			"rejects a supplied platform parameter, caps trimmed search by bytes, defaults sorting to " +
			"ascending sort_order, and interprets nonempty is_exclusive as true only when spelled true. " +
			"This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups", []string{"is_exclusive", "page", "page_size", "platform", "search", "sort_by", "sort_order", "status"}, nil),
	}),
	"groups.live_capability.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.live_capability.list",
		Module:  "groups",
		Title:   "Get Live Capability",
		TitleZH: "查询分组实时能力",
		Description: "GetLiveCapability returns the server's current Live attestation readiness as capability " +
			"data. A checker error still produces HTTP success with supported=false and its error text as " +
			"reason; the successful check produces supported=true. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/live-capability",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/live-capability", []string{}, nil),
	}),
	"groups.models_list_candidates.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.models_list_candidates.list",
		Module:  "groups",
		Title:   "Get Models List Candidates",
		TitleZH: "查询分组候选模型",
		Description: "GetModelsListCandidates returns model choices for the route's group. Zero selects new-group " +
			"suggestions, negative or malformed IDs return bad requests, and a supplied platform " +
			"parameter is rejected because group model choices are platform-independent. This operation " +
			"is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/:id/models-list-candidates",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/models-list-candidates", []string{"platform"}, nil),
	}),
	"groups.rate_multipliers.delete": adminapi.Define(adminapi.Operation{
		ID:      "groups.rate_multipliers.delete",
		Module:  "groups",
		Title:   "Clear Group Rate Multipliers",
		TitleZH: "清除分组倍率覆盖",
		Description: "ClearGroupRateMultipliers removes every stored user-specific billing factor for the route's " +
			"group ID. It delegates the request context after numeric ID parsing and returns success when " +
			"administration completes.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/groups/:id/rate-multipliers",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/rate-multipliers", []string{}, nil),
	}),
	"groups.rate_multipliers.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.rate_multipliers.list",
		Module:  "groups",
		Title:   "Get Group Rate Multipliers",
		TitleZH: "查询分组倍率覆盖",
		Description: "GetGroupRateMultipliers returns user-specific billing factors for the route's group ID. A " +
			"malformed ID returns a bad request, reader errors use the standard error response, and a nil " +
			"entry list is encoded as an empty array. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/:id/rate-multipliers",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/rate-multipliers", []string{}, nil),
	}),
	"groups.rate_multipliers.update": adminapi.Define(adminapi.Operation{
		ID:      "groups.rate_multipliers.update",
		Module:  "groups",
		Title:   "Batch Set Group Rate Multipliers",
		TitleZH: "批量设置分组倍率覆盖",
		Description: "BatchSetGroupRateMultipliers decodes and saves user-specific billing factors for the route's " +
			"group ID. Numeric ID and JSON binding failures return bad requests; administration validates " +
			"and applies the supplied entries before success is reported. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/groups/:id/rate-multipliers",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/rate-multipliers", []string{}, (*BatchSetGroupRateMultipliersRequest)(nil)),
	}),
	"groups.rpm_overrides.delete": adminapi.Define(adminapi.Operation{
		ID:      "groups.rpm_overrides.delete",
		Module:  "groups",
		Title:   "Clear Group RPM Overrides",
		TitleZH: "清除分组 RPM 覆盖",
		Description: "ClearGroupRPMOverrides removes every stored user-specific RPM override for the route's group " +
			"ID. It delegates the request context after numeric ID parsing and returns success when " +
			"administration completes.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/groups/:id/rpm-overrides",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/rpm-overrides", []string{}, nil),
	}),
	"groups.rpm_overrides.update": adminapi.Define(adminapi.Operation{
		ID:      "groups.rpm_overrides.update",
		Module:  "groups",
		Title:   "Batch Set Group RPM Overrides",
		TitleZH: "批量设置分组 RPM 覆盖",
		Description: "BatchSetGroupRPMOverrides decodes and saves user-specific RPM overrides for the route's " +
			"group ID. Numeric ID and JSON binding failures return bad requests; administration validates " +
			"and applies the supplied entries before success is reported. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/groups/:id/rpm-overrides",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id/rpm-overrides", []string{}, (*BatchSetGroupRPMOverridesRequest)(nil)),
	}),
	"groups.sort_order.update": adminapi.Define(adminapi.Operation{
		ID:      "groups.sort_order.update",
		Module:  "groups",
		Title:   "Update Sort Order",
		TitleZH: "调整显示顺序",
		Description: "UpdateSortOrder strictly decodes a nonempty list of group display-order changes. It forwards " +
			"each ID and order in request order to administration and reports success after the batch " +
			"update completes. Omitted body fields follow the HTTP handler defaults; explicit null and " +
			"empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/groups/sort-order",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/sort-order", []string{}, (*UpdateSortOrderRequest)(nil)),
	}),
	"groups.update": adminapi.Define(adminapi.Operation{
		Examples: []map[string]any{{"path": map[string]string{"id": "1"}, "body": map[string]any{"description": "Updated group description"}}},
		ID:       "groups.update",
		Module:   "groups",
		Title:    "Update group",
		TitleZH:  "修改分组",
		Description: "Update strictly decodes supplied changes for the route's numeric group ID and returns its " +
			"administrator DTO. Optional request values preserve unchanged settings, false values, and " +
			"empty collections for GroupAdmin to interpret.  A legacy text protocol selection is used " +
			"when the complete protocol field is absent, and the compatibility marker lets GroupAdmin " +
			"retain non-text entry permissions. Supplied complete protocol input takes precedence over " +
			"the legacy selection. Omitted body fields follow the HTTP handler defaults; explicit null " +
			"and empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/groups/:id",
		Response:    "json",
		BrowserPath: "/admin/groups",
		Output:      adminapi.JSONResponse((*dto.AdminGroup[struct{}])(nil)),
		Input:       adminapi.Arguments("/api/v1/admin/groups/:id", []string{}, (*UpdateGroupRequest)(nil)),
	}),
	"groups.usage_summary.list": adminapi.Define(adminapi.Operation{
		ID:      "groups.usage_summary.list",
		Module:  "groups",
		Title:   "Get Usage Summary",
		TitleZH: "查询分组用量汇总",
		Description: "GetUsageSummary returns today, yesterday, and retained-total group charges using the " +
			"injected calendar. It passes Today's start instant to the usage reader and turns a read " +
			"failure into the generic HTTP 500 summary response. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/groups/usage-summary",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/groups",
		Input:       adminapi.Arguments("/api/v1/admin/groups/usage-summary", []string{}, nil),
	}),
	"model_attributes.configs.create": adminapi.Define(adminapi.Operation{
		ID:      "model_attributes.configs.create",
		Module:  "model-attributes",
		Title:   "Save model attribute profile",
		TitleZH: "保存模型属性配置",
		Description: "Save strictly decodes an attribute profile and persists it through the service. Creation " +
			"resets the submitted ID to zero; update replaces that value with the route's validated " +
			"positive ID, making the route determine which stored record is changed. The service " +
			"validates rules, associates groups, and prepares the saved value before the handler returns " +
			"it. A binding, route-ID, or service failure returns its corresponding HTTP error. Omitted " +
			"body fields follow the HTTP handler defaults; explicit null and empty collections retain " +
			"their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/model-attributes/configs",
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/configs", []string{}, (*routing.ModelAttributeConfig)(nil)),
	}),
	"model_attributes.configs.delete": adminapi.Define(adminapi.Operation{
		ID:      "model_attributes.configs.delete",
		Module:  "model-attributes",
		Title:   "Delete model attribute profile",
		TitleZH: "删除模型属性配置",
		Description: "Delete removes the profile selected by a positive route ID through the attribute service. It " +
			"stops after route validation or service errors, and reports deleted=true after successful " +
			"removal and service-owned cache invalidation.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/model-attributes/configs/:id",
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/configs/:id", []string{}, nil),
	}),
	"model_attributes.configs.get": adminapi.Define(adminapi.Operation{
		ID:      "model_attributes.configs.get",
		Module:  "model-attributes",
		Title:   "Get model attribute profile details",
		TitleZH: "查看模型属性配置详情",
		Description: "Get returns the stored profile named by the route's positive configuration ID. It stops " +
			"after attributeID writes a validation error and otherwise forwards repository errors through " +
			"the standard HTTP response. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/model-attributes/configs/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/configs/:id", []string{}, nil),
	}),
	"model_attributes.configs.list": adminapi.Define(adminapi.Operation{
		ID:      "model_attributes.configs.list",
		Module:  "model-attributes",
		Title:   "List model attribute profile records",
		TitleZH: "查询模型属性配置",
		Description: "List returns stored attribute profiles filtered by exact status and case-insensitive " +
			"name/description search. It applies standard pagination after filtering and retains the " +
			"repository's result order; the returned total counts all matching profiles before " +
			"pagination. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/model-attributes/configs",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/configs", []string{"page", "page_size", "search", "status"}, nil),
	}),
	"model_attributes.configs.update": adminapi.Define(adminapi.Operation{
		ID:      "model_attributes.configs.update",
		Module:  "model-attributes",
		Title:   "Save model attribute profile",
		TitleZH: "保存模型属性配置",
		Description: "Save strictly decodes an attribute profile and persists it through the service. Creation " +
			"resets the submitted ID to zero; update replaces that value with the route's validated " +
			"positive ID, making the route determine which stored record is changed. The service " +
			"validates rules, associates groups, and prepares the saved value before the handler returns " +
			"it. A binding, route-ID, or service failure returns its corresponding HTTP error. Omitted " +
			"body fields follow the HTTP handler defaults; explicit null and empty collections retain " +
			"their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/model-attributes/configs/:id",
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/configs/:id", []string{}, (*routing.ModelAttributeConfig)(nil)),
	}),
	"model_attributes.defaults.list": adminapi.Define(adminapi.Operation{
		ID:     "model_attributes.defaults.list",
		Module: "model-attributes",
		Title:  "Defaults",
		Description: "Defaults returns published catalog attributes filtered by search, provider, and capability. " +
			"It uses the prepared index when available and otherwise filters one snapshot, accepting " +
			"capability fields declared true. The response includes the matching total, the complete " +
			"provider choices, and status from that publication. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/model-attributes/defaults",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/defaults", []string{"capability", "page", "page_size", "provider", "search"}, nil),
	}),
	"model_attributes.defaults.model.list": adminapi.Define(adminapi.Operation{
		ID:     "model_attributes.defaults.model.list",
		Module: "model-attributes",
		Title:  "Model",
		Description: "Model returns default attributes for a trimmed model query through the catalog lookup " +
			"callback. Empty model text returns MODEL_REQUIRED; unknown attributes keep the lookup " +
			"callback's result and are returned through the success envelope. This operation is available " +
			"through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/model-attributes/defaults/model",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/defaults/model", []string{"model"}, nil),
	}),
	"model_attributes.defaults.models.list": adminapi.Define(adminapi.Operation{
		ID:     "model_attributes.defaults.models.list",
		Module: "model-attributes",
		Title:  "Models",
		Description: "Models returns catalog model identities using the prepared index when available. The " +
			"snapshot path collects IDs in snapshot order; both paths read already published data and " +
			"leave catalog refresh to the Update endpoint. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/model-attributes/defaults/models",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/defaults/models", []string{}, nil),
	}),
	"model_attributes.defaults.update.create": adminapi.Define(adminapi.Operation{
		ID:      "model_attributes.defaults.update.create",
		Module:  "model-attributes",
		Title:   "Update model attribute profile",
		TitleZH: "修改模型属性配置",
		Description: "Update invokes the configured catalog refresh callback for an administrator request. It " +
			"returns updated=true on success and a generic HTTP 502 catalog-update error when the " +
			"callback reports failure.",
		Method:      "POST",
		Path:        "/api/v1/admin/model-attributes/defaults/update",
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/defaults/update", []string{}, nil),
	}),
	"model_attributes.preview.create": adminapi.Define(adminapi.Operation{
		ID:     "model_attributes.preview.create",
		Module: "model-attributes",
		Title:  "Preview",
		Description: "Preview returns effective attributes for the requested group's matching model routes. It " +
			"requires strict JSON, a positive group ID, and nonblank model text, and validates supplied " +
			"draft attributes and route conditions before delegating. Omitted drafts use the saved group " +
			"profile; an empty client protocol includes matching routes across entries. A successful " +
			"preview with no matching route returns an empty routes list. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/model-attributes/preview",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/model-attributes",
		Input:       adminapi.Arguments("/api/v1/admin/model-attributes/preview", []string{}, (*ModelAttributePreviewRequest)(nil)),
	}),
	"pricing.configs.create": adminapi.Define(adminapi.Operation{
		ID:      "pricing.configs.create",
		Module:  "pricing",
		Title:   "Create pricing configuration",
		TitleZH: "创建价格配置",
		Description: "Create strictly decodes shared price configuration input and forwards converted cards and " +
			"settings to the service. Statistics rules require a group/provider selector and pricing " +
			"entries; their request order becomes SortOrder before the service validates and persists the " +
			"configuration. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/pricing/configs",
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/configs", []string{}, (*createPricingConfigRequest)(nil)),
	}),
	"pricing.configs.delete": adminapi.Define(adminapi.Operation{
		ID:      "pricing.configs.delete",
		Module:  "pricing",
		Title:   "Delete pricing configuration",
		TitleZH: "删除价格配置",
		Description: "Delete parses the route's numeric price-configuration ID and delegates removal to the " +
			"service. Parsing and service errors use standard HTTP responses; success returns the " +
			"configuration deletion message after removal completes.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/pricing/configs/:id",
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/configs/:id", []string{}, nil),
	}),
	"pricing.configs.get": adminapi.Define(adminapi.Operation{
		ID:      "pricing.configs.get",
		Module:  "pricing",
		Title:   "Get pricing configuration details",
		TitleZH: "查看价格配置详情",
		Description: "GetByID parses the route's numeric price-configuration ID and returns its administrator DTO. " +
			"Parse failures return INVALID_PRICING_CONFIG_ID, while the service validates whether the " +
			"parsed ID identifies a stored configuration. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/pricing/configs/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/configs/:id", []string{}, nil),
	}),
	"pricing.configs.list": adminapi.Define(adminapi.Operation{
		ID:      "pricing.configs.list",
		Module:  "pricing",
		Title:   "List pricing configuration records",
		TitleZH: "查询价格配置",
		Description: "List returns shared configurations with requested status, search, sorting, and pagination. " +
			"Trimmed search is capped by bytes, and default ordering is descending created_at; service " +
			"results are mapped into administrator DTOs with the returned matching total. This operation " +
			"is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/pricing/configs",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/configs", []string{"page", "page_size", "search", "sort_by", "sort_order", "status"}, nil),
	}),
	"pricing.configs.update": adminapi.Define(adminapi.Operation{
		ID:      "pricing.configs.update",
		Module:  "pricing",
		Title:   "Update pricing configuration",
		TitleZH: "修改价格配置",
		Description: "Update strictly decodes changes for the route's numeric price-configuration ID. Omitted " +
			"collection pointers leave stored cards and statistics rules unchanged; supplied collections " +
			"replace them, with statistics rule request order assigned to SortOrder. Service validation " +
			"and persistence complete before the administrator DTO is returned. Omitted body fields " +
			"follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/pricing/configs/:id",
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/configs/:id", []string{}, (*updatePricingConfigRequest)(nil)),
	}),
	"pricing.defaults.list": adminapi.Define(adminapi.Operation{
		ID:     "pricing.defaults.list",
		Module: "pricing",
		Title:  "List Default Pricing",
		Description: "ListDefaultPricing returns default catalog prices filtered by model search, platform, and " +
			"billing mode. It uses the optional paged query callback or reads one snapshot, then returns " +
			"the match count, catalog status, and platform choices with the selected page. Snapshot " +
			"results sort by model and then platform. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/pricing/defaults",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/defaults", []string{"billing_mode", "page", "page_size", "platform", "search"}, nil),
	}),
	"pricing.defaults.model.list": adminapi.Define(adminapi.Operation{
		ID:     "pricing.defaults.model.list",
		Module: "pricing",
		Title:  "Get Model Default Pricing",
		Description: "GetModelDefaultPricing returns catalog token amounts for model-form autofill. A nonblank " +
			"trimmed model is required; any lookup error produces successful found=false. Cache-write " +
			"autofill uses a positive 5m rate when breakdown is supported, otherwise retains the " +
			"aggregate rate, and exposes the 1h rate whenever breakdown is supported. This operation is " +
			"available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/pricing/defaults/model",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/pricing",
		Input:       adminapi.Arguments("/api/v1/admin/pricing/defaults/model", []string{"model"}, nil),
	}),
	"pricing.defaults.update.create": adminapi.Define(adminapi.Operation{
		ID: "pricing.defaults.update.create", Module: "pricing",
		Title: "Refresh default pricing catalogue", TitleZH: "刷新默认价格目录",
		Description: "Download the configured default pricing catalogue and replace the local catalogue snapshot. This action accepts no body and returns updated=true after refresh; download or refresh failures return the existing HTTP error.",
		Method:      "POST", Path: "/api/v1/admin/pricing/defaults/update", Response: "json", BrowserPath: "/admin/pricing",
		Input: adminapi.Arguments("/api/v1/admin/pricing/defaults/update", nil, nil),
	}),
}

// ModelAttributePreviewRequest carries the administrative request decoded by its existing HTTP handler.
type ModelAttributePreviewRequest struct {
	GroupID int64  `json:"group_id"` // positive preview group identifier
	Model   string `json:"model"`    // requested preview model ID
	// ClientProtocol restricts preview results to one entry when nonempty; an
	// empty value keeps every matching route returned by the service.
	ClientProtocol string `json:"client_protocol"` // optional preview client entry
	// Config supplies a draft profile for this preview; nil makes the service
	// read the stored profile associated with GroupID.
	Config *routing.ModelAttributeConfig `json:"config,omitempty"` // optional draft attribute profile
}
