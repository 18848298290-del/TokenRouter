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
	"teams.create": adminapi.Define(adminapi.Operation{
		ID:      "teams.create",
		Module:  "teams",
		Title:   "Create team",
		TitleZH: "创建团队",
		Description: "Create binds required owner/name and an optional member ceiling before administrator " +
			"creation. A nil ceiling selects service defaults, zero remains supplied, and binding/service " +
			"errors precede the created response; routes authorize the administrator. Omitted body fields " +
			"follow the HTTP handler defaults; explicit null and empty collections retain their " +
			"documented meanings. Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/teams",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams", []string{}, (*AdminTeamCreateRequest)(nil)),
	}),
	"teams.delete": adminapi.Define(adminapi.Operation{
		ID:     "teams.delete",
		Module: "teams",
		Title:  "Dissolve",
		Description: "Dissolve parses the team ID and delegates administrator teardown. Success returns a " +
			"dissolved acknowledgement after the service completes; shared application-error mapping " +
			"handles failures and route assembly owns administrator authorization. Browser confirmation " +
			"by the token owner is required.",
		Method:      "DELETE",
		Path:        "/api/v1/admin/teams/:id",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams/:id", []string{}, nil),
	}),
	"teams.force_transfer.create": adminapi.Define(adminapi.Operation{
		ID:     "teams.force_transfer.create",
		Module: "teams",
		Title:  "Force Transfer",
		Description: "ForceTransfer binds a required target identity and delegates administrator ownership " +
			"transfer for the route team. The service/repository enforce participant validity; parameter, " +
			"binding or operation failure maps to an error response. Omitted body fields follow the HTTP " +
			"handler defaults; explicit null and empty collections retain their documented meanings. " +
			"Browser confirmation by the token owner is required.",
		Method:      "POST",
		Path:        "/api/v1/admin/teams/:id/force-transfer",
		Sensitive:   true,
		StepUp:      true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams/:id/force-transfer", []string{}, (*AdminTeamTransferRequest)(nil)),
	}),
	"teams.get": adminapi.Define(adminapi.Operation{
		ID:      "teams.get",
		Module:  "teams",
		Title:   "Get team details",
		TitleZH: "查看团队详情",
		Description: "Get parses the route team ID and returns its administrator context. Parse failure writes " +
			"bad-request, while missing or unreadable service results use shared application-error " +
			"mapping before any success envelope. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/teams/:id",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams/:id", []string{}, nil),
	}),
	"teams.list": adminapi.Define(adminapi.Operation{
		ID:      "teams.list",
		Module:  "teams",
		Title:   "List team records",
		TitleZH: "查询团队",
		Description: "List returns the administrator team list in the shared response envelope. It uses request " +
			"context and maps service failure before publishing a result; administrator authentication " +
			"belongs to route assembly. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/teams",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams", []string{}, nil),
	}),
	"teams.members.list": adminapi.Define(adminapi.Operation{
		ID:     "teams.members.list",
		Module: "teams",
		Title:  "List Members",
		Description: "ListMembers parses the team ID and requests its administrator membership listing. The " +
			"service checks team availability first; parameter or service failure sends an error envelope " +
			"instead of the list. This operation is available through read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/teams/:id/members",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams/:id/members", []string{}, nil),
	}),
	"teams.update": adminapi.Define(adminapi.Operation{
		ID:      "teams.update",
		Module:  "teams",
		Title:   "Update team",
		TitleZH: "修改团队",
		Description: "Update binds optional name/status/member-ceiling pointers and delegates their joint " +
			"validation/update. Omission or JSON null leaves a pointer nil; service success returns " +
			"readback, while a readback failure can follow a completed write. Omitted body fields follow " +
			"the HTTP handler defaults; explicit null and empty collections retain their documented " +
			"meanings. Browser confirmation by the token owner is required.",
		Method:      "PATCH",
		Path:        "/api/v1/admin/teams/:id",
		Sensitive:   true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams/:id", []string{}, (*AdminTeamUpdateRequest)(nil)),
	}),
	"teams.usage.list": adminapi.Define(adminapi.Operation{
		ID:     "teams.usage.list",
		Module: "teams",
		Title:  "Get Usage",
		Description: "GetUsage returns an administrator team summary with an optional positive member filter. It " +
			"parses no time range or Key filter here, so service normalization supplies its default " +
			"range; invalid supplied member IDs are rejected. This operation is available through " +
			"read_operation.",
		Method:      "GET",
		Path:        "/api/v1/admin/teams/:id/usage",
		ReadOnly:    true,
		Response:    "json",
		BrowserPath: "/admin/teams",
		Input:       adminapi.Arguments("/api/v1/admin/teams/:id/usage", []string{"member_id"}, nil),
	}),
}

// AdminTeamCreateRequest carries the administrative request decoded by its existing HTTP handler.
type AdminTeamCreateRequest struct {
	OwnerUserID int64 `json:"owner_user_id" binding:"required"` // requested team owner identity
	// Name supplies required creation text. The service trims and validates the
	// team name before creating the requested owner's team.
	Name string `json:"name" binding:"required"`
	// MemberLimit supplies the requested creation ceiling when present. Nil
	// selects the service's configured or built-in default; zero admits no
	// ordinary members and negatives fail service validation.
	MemberLimit *int `json:"member_limit"`
}

// AdminTeamTransferRequest carries the administrative request decoded by its existing HTTP handler.
type AdminTeamTransferRequest struct {
	TargetUserID int64 `json:"target_user_id" binding:"required"` // requested successor account identity
}

// AdminTeamUpdateRequest carries the administrative request decoded by its existing HTTP handler.
type AdminTeamUpdateRequest struct {
	// Name supplies replacement text when present. Nil leaves the team name
	// unchanged, while the service validates and trims a selected value before
	// writing.
	Name *string `json:"name"`
	// Status selects a replacement team status when nonnil. Omission or JSON
	// null leaves it nil, and service validation admits active or suspended
	// text.
	Status *string `json:"status"`
	// MemberLimit replaces the stored ceiling when present. Nil leaves the
	// existing ceiling unchanged; zero admits no ordinary members and negatives
	// fail service validation.
	MemberLimit *int `json:"member_limit"`
}
