package httpapi

import (
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// management_operations.go describes registered administrative HTTP operations for
// MCP discovery and permission selection. Request schemas reference the same input
// types decoded by handlers; metadata records query arguments, side effects, and
// browser confirmation requirements before the shared executor admits a call.

// ManagementOperations supplies stable permission identities and parameter descriptions
// for this module. Registrars mount these descriptions together with existing HTTP handlers.
var ManagementOperations = map[string]*adminapi.Operation{
	"payment.plans.create": adminapi.Define(adminapi.Operation{
		ID:     "payment.plans.create",
		Module: "payment",
		Title:  "Create Plan",
		Description: "CreatePlan binds a plan creation request and delegates its validation/persistence to the " +
			"service. Invalid JSON yields bad request, service failures use the shared error response, " +
			"and a successful plan is returned in the administrator record shape with created status. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/payment/plans",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/payment",
		Input:       adminapi.Arguments("/api/v1/admin/payment/plans", []string{}, (*billing.CreatePlanRequest)(nil)),
	}),
	"payment.plans.delete": adminapi.Define(adminapi.Operation{
		ID:     "payment.plans.delete",
		Module: "payment",
		Title:  "Delete Plan",
		Description: "DeletePlan parses the path ID and delegates deletion to the plan service. Malformed numeric " +
			"text yields bad request, service errors use shared mapping, and success returns the existing " +
			"deleted message object. Browser confirmation by the token owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/payment/plans/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/payment",
		Input:       adminapi.Arguments("/api/v1/admin/payment/plans/:id", []string{}, nil),
	}),
	"payment.plans.list": adminapi.Define(adminapi.Operation{
		ID:     "payment.plans.list",
		Module: "payment",
		Title:  "List Plans",
		Description: "ListPlans returns all service plans mapped to administrator record responses. Service errors " +
			"use the shared response; localization source data, optional amounts and group collection " +
			"ownership follow planRecord's read-only mapping rules. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/payment/plans",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/payment",
		Input:       adminapi.Arguments("/api/v1/admin/payment/plans", []string{}, nil),
	}),
	"payment.plans.update": adminapi.Define(adminapi.Operation{
		ID:     "payment.plans.update",
		Module: "payment",
		Title:  "Update Plan",
		Description: "UpdatePlan parses the path ID and binds the replacement request before delegating the plan " +
			"update. Parsing/JSON errors yield bad request; service failures use shared mapping, and " +
			"success returns the administrator record view. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings. Browser " +
			"confirmation by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/payment/plans/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/payment",
		Input:       adminapi.Arguments("/api/v1/admin/payment/plans/:id", []string{}, (*billing.UpdatePlanRequest)(nil)),
	}),
	"plans.subscriptions.list": adminapi.Define(adminapi.Operation{
		ID:     "plans.subscriptions.list",
		Module: "plans",
		Title:  "List By Plan",
		Description: "ListByPlan parses a signed plan path ID and returns its administrator grant page. Invalid " +
			"numeric text yields bad request; service failures use shared error mapping, and page " +
			"metadata is converted to the HTTP response structure. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/plans/:id/subscriptions",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/plans",
		Input:       adminapi.Arguments("/api/v1/admin/plans/:id/subscriptions", []string{"page", "page_size"}, nil),
	}),
	"redeem_codes.batch_delete.create": adminapi.Define(adminapi.Operation{
		ID:     "redeem_codes.batch_delete.create",
		Module: "redeem-codes",
		Title:  "Batch Delete",
		Description: "BatchDelete requires a nonempty record ID sequence and returns the administrator's deleted " +
			"count. Invalid JSON returns bad request; the concrete administrator permits partial " +
			"independent deletion, so a successful reply can report fewer deletes than supplied IDs. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/redeem-codes/batch-delete",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/batch-delete", []string{}, (*RedeemCodeBatchDeleteRequest)(nil)),
	}),
	"redeem_codes.batch_update.create": adminapi.Define(adminapi.Operation{
		ID:     "redeem_codes.batch_update.create",
		Module: "redeem-codes",
		Title:  "Batch Update",
		Description: "BatchUpdate requires its redemption service, binds IDs and optional fields, and delegates " +
			"validated changes through that service. Missing configuration or invalid JSON stops first; " +
			"service errors use shared mapping, and success reports the updated count. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/redeem-codes/batch-update",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/batch-update", []string{}, (*BatchUpdateRedeemCodesRequest)(nil)),
	}),
	"redeem_codes.create_and_redeem.create": adminapi.Define(adminapi.Operation{
		ID:     "redeem_codes.create_and_redeem.create",
		Module: "redeem-codes",
		Title:  "Create And Redeem",
		Description: "CreateAndRedeem creates a fixed single-use credential when missing and redeems it for the " +
			"requested recipient. Missing redemption service or invalid request/expiry stops first; " +
			"omitted type becomes balance, and subscription input requires a positive plan ID before " +
			"command replay or code lookup.  Existing credentials use their stored terms and the latest " +
			"UsedBy summary for recovery. Creation failure triggers a reread that can resolve a " +
			"concurrent creator's record; if that reread fails, the original create error returns. " +
			"Credential creation and redemption are separate service operations, so failed redemption can " +
			"leave a newly created code for a later request. Bound command replay determines whether the " +
			"callback runs again. Omitted body fields follow the HTTP handler defaults; explicit null and " +
			"empty collections retain their documented meanings. Browser confirmation by the token owner " +
			"is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/redeem-codes/create-and-redeem",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Idempotent:  true,
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/create-and-redeem", []string{}, (*CreateAndRedeemCodeRequest)(nil)),
	}),
	"redeem_codes.delete": adminapi.Define(adminapi.Operation{
		ID:      "redeem_codes.delete",
		Module:  "redeem-codes",
		Title:   "Delete redeem code",
		TitleZH: "删除兑换码",
		Description: "Delete parses a signed record ID and delegates eligible credential deletion to the " +
			"administrator. Malformed text returns bad request; service failures use shared error " +
			"mapping, and completion returns the existing success message. Browser confirmation by the " +
			"token owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/redeem-codes/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/:id", []string{}, nil),
	}),
	"redeem_codes.expire.create": adminapi.Define(adminapi.Operation{
		ID:      "redeem_codes.expire.create",
		Module:  "redeem-codes",
		Title:   "Expire redeem code",
		TitleZH: "使其失效：兑换码",
		Description: "Expire parses a signed record ID and asks the administrator to mark its stored status " +
			"expired. Invalid numeric text returns bad request; service failures use shared mapping, and " +
			"success maps the resulting credential view. Browser confirmation by the token owner is " +
			"required.",
		Method:      "POST",
		Path:        "/api/v1/admin/redeem-codes/:id/expire",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/:id/expire", []string{}, nil),
	}),
	"redeem_codes.export.list": adminapi.Define(adminapi.Operation{
		ID:     "redeem_codes.export.list",
		Module: "redeem-codes",
		Title:  "Export",
		Description: "Export returns the first ten thousand matching credentials as CSV using the same search and " +
			"sort inputs as List. Sorting defaults to ID descending; finite values use two decimal places " +
			"and timestamps omit offsets. Service or CSV write/flush failures return before attachment " +
			"output, and optional recipient/time fields use empty cells. This operation is available " +
			"through read_operation. Browser confirmation by the token owner is required.",
		Method:      "GET",
		Path:        "/api/v1/admin/redeem-codes/export",
		ReadOnly:    true,
		Sensitive:   true,
		Response:    "file",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/export", []string{"search", "sort_by", "sort_order", "status", "type"}, nil),
	}),
	"redeem_codes.generate.create": adminapi.Define(adminapi.Operation{
		ID:      "redeem_codes.generate.create",
		Module:  "redeem-codes",
		Title:   "Generate redeem code",
		TitleZH: "生成兑换码",
		Description: "Generate binds bounded count and category, trims custom code text and resolves optional " +
			"expiry before command execution. The executor hashes the normalized request with its " +
			"supplied expiry inputs; the resolved time is retained for this callback, and administrator " +
			"validation/creation errors use the command response helper. Omitted body fields follow the " +
			"HTTP handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/redeem-codes/generate",
		Sensitive:   true,
		Response:    "file",
		BrowserPath: "/admin/redeem-codes",
		Idempotent:  true,
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/generate", []string{}, (*GenerateRedeemCodesRequest)(nil)),
	}),
	"redeem_codes.get": adminapi.Define(adminapi.Operation{
		ID:      "redeem_codes.get",
		Module:  "redeem-codes",
		Title:   "Get redeem code details",
		TitleZH: "查看兑换码详情",
		Description: "GetByID parses a signed decimal record ID and maps the administrator's credential response. " +
			"Malformed text returns bad request; zero and negative values reach the service, and read " +
			"failures use shared error mapping. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/redeem-codes/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/:id", []string{}, nil),
	}),
	"redeem_codes.list": adminapi.Define(adminapi.Operation{
		ID:      "redeem_codes.list",
		Module:  "redeem-codes",
		Title:   "List redeem code records",
		TitleZH: "查询兑换码",
		Description: "List returns a filtered administrator credential page with normalized Unicode search and " +
			"service mapping. Sorting defaults to ID descending; pagination comes from shared parsing, " +
			"and service failures use the shared error response. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/redeem-codes",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes", []string{"page", "page_size", "search", "sort_by", "sort_order", "status", "type"}, nil),
	}),
	"redeem_codes.update": adminapi.Define(adminapi.Operation{
		ID:      "redeem_codes.update",
		Module:  "redeem-codes",
		Title:   "Update redeem code",
		TitleZH: "修改兑换码",
		Description: "Update applies optional single-code terms while retaining the difference between omitted " +
			"expiry and an explicit clear. Invalid signed ID text or JSON stops before the administrator " +
			"call; other validation and mutation errors use shared HTTP mapping. A missing expiry pointer " +
			"leaves ExpiresAtSet false, while zero sets the flag with a nil time and positive seconds " +
			"supply a time value.  The administrator owns used-value, plan, capacity and future-expiry " +
			"checks plus transaction handling. Request pointers pass through for synchronous consumption, " +
			"and success maps the administrator's refreshed credential rather than constructing a " +
			"response from requested fields. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings. Browser confirmation " +
			"by the token owner is required.",
		Method:      "PUT",
		Path:        "/api/v1/admin/redeem-codes/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/redeem-codes",
		Input:       adminapi.Arguments("/api/v1/admin/redeem-codes/:id", []string{}, (*UpdateRedeemCodeRequest)(nil)),
	}),
	"subscriptions.assign.create": adminapi.Define(adminapi.Operation{
		ID:     "subscriptions.assign.create",
		Module: "subscriptions",
		Title:  "Assign",
		Description: "Assign binds one recipient/plan grant and records the authentication subject ID as assigner " +
			"metadata. Invalid JSON yields bad request; the route owner supplies authorization, while " +
			"service errors use shared mapping and success returns the administrator DTO. Omitted body " +
			"fields follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/subscriptions/assign",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/assign", []string{}, (*AssignSubscriptionRequest)(nil)),
	}),
	"subscriptions.bulk_assign.create": adminapi.Define(adminapi.Operation{
		ID:     "subscriptions.bulk_assign.create",
		Module: "subscriptions",
		Title:  "Bulk Assign",
		Description: "BulkAssign binds the recipient sequence and common grant terms, then returns the service's " +
			"per-user results. Invalid JSON yields bad request; individual service failures can remain in " +
			"a successful batch response, and assigner metadata comes from the authentication subject. " +
			"Omitted body fields follow the HTTP handler defaults; explicit null and empty collections " +
			"retain their documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/subscriptions/bulk-assign",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/bulk-assign", []string{}, (*BulkAssignSubscriptionRequest)(nil)),
	}),
	"subscriptions.delete": adminapi.Define(adminapi.Operation{
		ID:      "subscriptions.delete",
		Module:  "subscriptions",
		Title:   "Revoke subscription",
		TitleZH: "撤销订阅",
		Description: "Revoke parses a signed target ID and delegates administrator revocation, returning the " +
			"existing success message after service completion. The route supports both POST and DELETE " +
			"spellings; malformed IDs and service failures use their existing bad-request/shared-error " +
			"paths. Browser confirmation by the token owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/subscriptions/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id", []string{}, nil),
	}),
	"subscriptions.extend.create": adminapi.Define(adminapi.Operation{
		ID:     "subscriptions.extend.create",
		Module: "subscriptions",
		Title:  "Extend",
		Description: "Extend binds a positive target-day count and invokes SetSubscriptionValidityDays through the " +
			"administrator command executor. It sets duration from the service's current/pending anchor " +
			"rather than adding that number of days; payload identity includes the path ID and bound body " +
			"for replay handling. Omitted body fields follow the HTTP handler defaults; explicit null and " +
			"empty collections retain their documented meanings. Browser confirmation by the token owner " +
			"is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/subscriptions/:id/extend",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Idempotent:  true,
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id/extend", []string{}, (*AdjustSubscriptionRequest)(nil)),
	}),
	"subscriptions.get": adminapi.Define(adminapi.Operation{
		ID:      "subscriptions.get",
		Module:  "subscriptions",
		Title:   "Get subscription details",
		TitleZH: "查看订阅详情",
		Description: "GetByID parses a signed decimal subscription path ID and returns the administrator grant " +
			"mapping. Malformed text yields bad request, while zero/negative values reach service " +
			"interpretation and service failures use shared error mapping. This operation is available " +
			"through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/subscriptions/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id", []string{}, nil),
	}),
	"subscriptions.list": adminapi.Define(adminapi.Operation{
		ID:      "subscriptions.list",
		Module:  "subscriptions",
		Title:   "List subscription records",
		TitleZH: "查询订阅",
		Description: "List returns administrator grant views with pagination, optional IDs, status and sorting " +
			"inputs. Malformed optional ID text is ignored; sorting defaults to created_at descending, " +
			"platform reaches a concrete service that ignores it, and service failures use shared error " +
			"mapping. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/subscriptions",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions", []string{"page", "page_size", "plan_id", "platform", "sort_by", "sort_order", "status", "user_id"}, nil),
	}),
	"subscriptions.progress.list": adminapi.Define(adminapi.Operation{
		ID:     "subscriptions.progress.list",
		Module: "subscriptions",
		Title:  "Get Progress",
		Description: "GetProgress parses the signed target ID and returns service quota progress. Malformed text " +
			"yields bad request; every progress-service error is converted to the existing not-found " +
			"response instead of shared error classification. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/subscriptions/:id/progress",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id/progress", []string{}, nil),
	}),
	"subscriptions.reset_quota.create": adminapi.Define(adminapi.Operation{
		ID:     "subscriptions.reset_quota.create",
		Module: "subscriptions",
		Title:  "Reset Quota",
		Description: "ResetQuota binds window selection and requires at least one selected flag before service " +
			"mutation. Malformed ID/JSON yields bad request, service failures use shared mapping and the " +
			"updated grant uses the administrator DTO. Omitted body fields follow the HTTP handler " +
			"defaults; explicit null and empty collections retain their documented meanings. Browser " +
			"confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/subscriptions/:id/reset-quota",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id/reset-quota", []string{}, (*ResetSubscriptionQuotaRequest)(nil)),
	}),
	"subscriptions.restore.create": adminapi.Define(adminapi.Operation{
		ID:      "subscriptions.restore.create",
		Module:  "subscriptions",
		Title:   "Restore subscription",
		TitleZH: "恢复订阅",
		Description: "Restore parses a signed target ID and delegates revoked-grant restoration before mapping the " +
			"administrator result. Malformed text yields bad request, while eligibility/chain/persistence " +
			"errors retain the service's shared HTTP error classification. Browser confirmation by the " +
			"token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/subscriptions/:id/restore",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id/restore", []string{}, nil),
	}),
	"subscriptions.revoke.create": adminapi.Define(adminapi.Operation{
		ID:      "subscriptions.revoke.create",
		Module:  "subscriptions",
		Title:   "Revoke subscription",
		TitleZH: "撤销订阅",
		Description: "Revoke parses a signed target ID and delegates administrator revocation, returning the " +
			"existing success message after service completion. The route supports both POST and DELETE " +
			"spellings; malformed IDs and service failures use their existing bad-request/shared-error " +
			"paths. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/subscriptions/:id/revoke",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/subscriptions",
		Input:       adminapi.Arguments("/api/v1/admin/subscriptions/:id/revoke", []string{}, nil),
	}),
	"users.subscriptions.list": adminapi.Define(adminapi.Operation{
		ID:     "users.subscriptions.list",
		Module: "users",
		Title:  "List By User",
		Description: "ListByUser parses a signed user path ID and returns that user's administrator grant views. " +
			"Invalid text yields bad request; service failures use shared error mapping, and the service " +
			"supplies status/window normalization before DTO conversion. This operation is available " +
			"through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/users/:id/subscriptions",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/users",
		Input:       adminapi.Arguments("/api/v1/admin/users/:id/subscriptions", []string{}, nil),
	}),
}

// RedeemCodeBatchDeleteRequest carries the administrative request decoded by its existing HTTP handler.
type RedeemCodeBatchDeleteRequest struct {
	// IDs retain requested order and duplicates for the administrator's
	// independent delete attempts. The nonempty binding requirement does not
	// validate individual IDs or promise an aggregate transaction.
	IDs []int64 `json:"ids" binding:"required,min=1"` // requested credential deletion sequence
}
