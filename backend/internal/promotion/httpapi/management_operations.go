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
	"affiliates.invites.list": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.invites.list",
		Module: "affiliates",
		Title:  "List Invite Records",
		Description: "ListInviteRecords returns paged inviter relationships using search, sorting, and inclusive " +
			"time filters.  It uses the calendar parsers and reports the normalized filter page and size " +
			"in the response. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/affiliates/invites",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/invites", []string{"end_at", "page", "page_size", "search", "sort_by", "sort_order", "start_at", "timezone"}, nil),
	}),
	"affiliates.rebates.list": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.rebates.list",
		Module: "affiliates",
		Title:  "List Rebate Records",
		Description: "ListRebateRecords returns paged source-order accruals using search, sorting, and inclusive " +
			"time filters.  It uses the calendar parsers and keeps price, charged money, and granted " +
			"quota in their service fields. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/affiliates/rebates",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/rebates", []string{"end_at", "page", "page_size", "search", "sort_by", "sort_order", "start_at", "timezone"}, nil),
	}),
	"affiliates.transfers.list": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.transfers.list",
		Module: "affiliates",
		Title:  "List Transfer Records",
		Description: "ListTransferRecords returns paged quota-transfer receipts using search, sorting, and " +
			"inclusive time filters.  It uses the calendar parsers; old ledger receipts can expose " +
			"missing post-transfer snapshots. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/affiliates/transfers",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/transfers", []string{"end_at", "page", "page_size", "search", "sort_by", "sort_order", "start_at", "timezone"}, nil),
	}),
	"affiliates.users.batch_rate.create": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.users.batch_rate.create",
		Module: "affiliates",
		Title:  "Batch Set Rate",
		Description: "BatchSetRate replaces or removes one inviter percentage for the submitted account list. " +
			"Clear takes precedence; otherwise a rate is required. The affected response is the submitted " +
			"list length, including entries the service discards. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/affiliates/users/batch-rate",
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/users/batch-rate", []string{}, (*BatchSetRateRequest)(nil)),
	}),
	"affiliates.users.delete": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.users.delete",
		Module: "affiliates",
		Title:  "Clear User Settings",
		Description: "ClearUserSettings removes the user's rebate override and requests a generated invitation " +
			"code.  It rejects nonpositive user_id before writing, then clears the rate before resetting " +
			"the code. These calls are separate persistent operations, so code-reset failure can return " +
			"an error after the override has already been removed. Successful completion reports the " +
			"requested user ID; existing referral relationships continue to reference that user's " +
			"identity.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/affiliates/users/:user_id",
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/users/:user_id", []string{}, nil),
	}),
	"affiliates.users.list": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.users.list",
		Module: "affiliates",
		Title:  "List Users",
		Description: "ListUsers returns the paged accounts with custom invitation-code or percentage settings.  It " +
			"passes search and parsed pagination to the service and uses the shared error mapper for " +
			"failed queries. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/affiliates/users",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/users", []string{"page", "page_size", "search"}, nil),
	}),
	"affiliates.users.lookup.list": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.users.lookup.list",
		Module: "affiliates",
		Title:  "Lookup Users",
		Description: "LookupUsers returns recipient selector results for a nonempty q query.  An empty query " +
			"returns a nonnil empty list without invoking lookup. Search errors use the shared HTTP error " +
			"mapper. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/affiliates/users/lookup",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/users/lookup", []string{"q"}, nil),
	}),
	"affiliates.users.overview.list": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.users.overview.list",
		Module: "affiliates",
		Title:  "Get User Overview",
		Description: "GetUserOverview returns the affiliate overview for a positive parsed user_id.  It reports " +
			"invalid IDs as bad requests and delegates effective-rate selection and available-quota " +
			"observation to the service. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/affiliates/users/:user_id/overview",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/users/:user_id/overview", []string{}, nil),
	}),
	"affiliates.users.update": adminapi.Define(adminapi.Operation{
		ID:     "affiliates.users.update",
		Module: "affiliates",
		Title:  "Update User Settings",
		Description: "UpdateUserSettings applies optional code and rate edits for a positive user_id.  JSON " +
			"omission retains each setting; ClearRebateRate takes precedence over a supplied rate. The " +
			"code update runs first, then a rate removal or replacement is attempted. These calls have " +
			"separate persistence ownership, so a rate failure can return an error after the code is " +
			"already saved. Success reports the requested user ID. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/affiliates/users/:user_id",
		Response:    "json",
		BrowserPath: "/admin/affiliates",
		Input:       adminapi.Arguments("/api/v1/admin/affiliates/users/:user_id", []string{}, (*UpdateAffiliateUserRequest)(nil)),
	}),
	"promo_codes.create": adminapi.Define(adminapi.Operation{
		ID:      "promo_codes.create",
		Module:  "promo-codes",
		Title:   "Create promo code",
		TitleZH: "创建优惠码",
		Description: "Create validates JSON fields and requests a new active registration Promo.  An empty code " +
			"requests generation; an optional deadline is converted from Unix seconds. Success returns " +
			"the stored promotion response. Omitted body fields follow the HTTP handler defaults; " +
			"explicit null and empty collections retain their documented meanings.",
		Method:      "POST",
		Path:        "/api/v1/admin/promo-codes",
		Response:    adminapi.ResponseFile,
		BrowserPath: "/admin/promo-codes",
		Input:       adminapi.Arguments("/api/v1/admin/promo-codes", []string{}, (*CreatePromoCodeRequest)(nil)),
	}),
	"promo_codes.delete": adminapi.Define(adminapi.Operation{
		ID:      "promo_codes.delete",
		Module:  "promo-codes",
		Title:   "Delete promo code",
		TitleZH: "删除优惠码",
		Description: "Delete requests hard deletion of the Promo identified by the parsed path integer.  Malformed " +
			"text is a bad request. Storage errors use the shared mapper, and success returns the " +
			"deletion message.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/promo-codes/:id",
		Response:    "json",
		BrowserPath: "/admin/promo-codes",
		Input:       adminapi.Arguments("/api/v1/admin/promo-codes/:id", []string{}, nil),
	}),
	"promo_codes.get": adminapi.Define(adminapi.Operation{
		ID:      "promo_codes.get",
		Module:  "promo-codes",
		Title:   "Get promo code details",
		TitleZH: "查看优惠码详情",
		Description: "GetByID returns the administrator Promo response for the parsed path identifier.  Malformed " +
			"integer text is a bad request; numeric values are passed to storage, which reports an absent " +
			"code. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/promo-codes/:id",
		ReadOnly:    true,
		Response:    adminapi.ResponseFile,
		BrowserPath: "/admin/promo-codes",
		Input:       adminapi.Arguments("/api/v1/admin/promo-codes/:id", []string{}, nil),
	}),
	"promo_codes.list": adminapi.Define(adminapi.Operation{
		ID:      "promo_codes.list",
		Module:  "promo-codes",
		Title:   "List promo code records",
		TitleZH: "查询优惠码",
		Description: "List returns a page of Promo controls using status, search, and sort queries.  Search is " +
			"trimmed and limited to one hundred bytes; the repository maps supported sort keys and " +
			"directions. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/promo-codes",
		ReadOnly:    true,
		Response:    adminapi.ResponseFile,
		BrowserPath: "/admin/promo-codes",
		Input:       adminapi.Arguments("/api/v1/admin/promo-codes", []string{"page", "page_size", "search", "sort_by", "sort_order", "status"}, nil),
	}),
	"promo_codes.update": adminapi.Define(adminapi.Operation{
		ID:      "promo_codes.update",
		Module:  "promo-codes",
		Title:   "Update promo code",
		TitleZH: "修改优惠码",
		Description: "Update applies supplied administrator Promo edits for the parsed path identifier.  Omitted " +
			"pointer fields retain their stored values. A supplied expiration of zero becomes a Go " +
			"zero-time pointer, which PromoService interprets as a request to remove the deadline. Other " +
			"supplied Unix seconds become a concrete time. This conversion keeps omission separate from " +
			"clearing while amount, count, and status binding validates the submitted replacements before " +
			"persistence. Omitted body fields follow the HTTP handler defaults; explicit null and empty " +
			"collections retain their documented meanings.",
		Method:      "PUT",
		Path:        "/api/v1/admin/promo-codes/:id",
		Response:    adminapi.ResponseFile,
		BrowserPath: "/admin/promo-codes",
		Input:       adminapi.Arguments("/api/v1/admin/promo-codes/:id", []string{}, (*UpdatePromoCodeRequest)(nil)),
	}),
	"promo_codes.usages.list": adminapi.Define(adminapi.Operation{
		ID:     "promo_codes.usages.list",
		Module: "promo-codes",
		Title:  "Get Usages",
		Description: "GetUsages returns a page of grant receipts for the parsed Promo identifier.  It converts " +
			"recorded usage amounts and optional loaded users to responses; a missing code can produce an " +
			"empty usage page. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/promo-codes/:id/usages",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/promo-codes",
		Input:       adminapi.Arguments("/api/v1/admin/promo-codes/:id/usages", []string{"page", "page_size"}, nil),
	}),
}
