package httpapi

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// admin_handler.go handles administrator team queries, updates, ownership
// transfers, and dissolution.
// Administrator team requests use a route team identifier rather than the
// current user's membership. Handlers bind optional update pointers so
// omission remains distinguishable from zero, then let the service validate
// all selected fields before its repository write and readback.

// AdminHandler translates platform-authorized requests into administrator
// team service operations and response envelopes.
type AdminHandler struct {
	// service is retained for administrator team requests. Route assembly
	// authenticates administrators before invocation, and the supplied service
	// must remain usable for the handler lifetime.
	service *team.TeamService
}

// NewAdminHandler retains the supplied team service for platform
// administration. Route assembly installs administrator authentication before
// these handlers; the constructor creates no storage/client resources and
// expects a usable service.
func NewAdminHandler(teamService *team.TeamService) *AdminHandler {
	return &AdminHandler{service: teamService}
}

// List returns the administrator team list in the shared response envelope.
// It uses request context and maps service failure before publishing a
// result; administrator authentication belongs to route assembly.
func (h *AdminHandler) List(c *gin.Context) {
	items, err := h.service.AdminList(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// Get parses the route team ID and returns its administrator context. Parse
// failure writes bad-request, while missing or unreadable service results use
// shared application-error mapping before any success envelope.
func (h *AdminHandler) Get(c *gin.Context) {
	teamID, ok := adminTeamID(c)
	if !ok {
		return
	}
	teamCtx, err := h.service.AdminGet(c.Request.Context(), teamID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// ListMembers parses the team ID and requests its administrator membership
// listing. The service checks team availability first; parameter or service
// failure sends an error envelope instead of the list.
func (h *AdminHandler) ListMembers(c *gin.Context) {
	teamID, ok := adminTeamID(c)
	if !ok {
		return
	}
	items, err := h.service.AdminListMembers(c.Request.Context(), teamID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// GetUsage returns an administrator team summary with an optional positive
// member filter. It parses no time range or Key filter here, so service
// normalization supplies its default range; invalid supplied member IDs are
// rejected.
func (h *AdminHandler) GetUsage(c *gin.Context) {
	teamID, ok := adminTeamID(c)
	if !ok {
		return
	}
	query := team.TeamUsageQuery{}
	// A supplied positive member_id narrows the administrator report; absence
	// keeps team aggregation, while unparsed dates remain zero for the service's
	// default reporting range.
	if rawMemberID := strings.TrimSpace(c.Query("member_id")); rawMemberID != "" {
		memberID, err := strconv.ParseInt(rawMemberID, 10, 64)
		if err != nil || memberID <= 0 {
			response.BadRequest(c, "Invalid member_id")
			return
		}
		query.ActorUserID = &memberID
	}
	item, err := h.service.AdminGetUsageSummary(c.Request.Context(), teamID, query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Create binds required owner/name and an optional member ceiling before
// administrator creation. A nil ceiling selects service defaults, zero
// remains supplied, and binding/service errors precede the created response;
// routes authorize the administrator.
func (h *AdminHandler) Create(c *gin.Context) {
	var req AdminTeamCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.AdminCreate(c.Request.Context(), req.OwnerUserID, req.Name, req.MemberLimit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, teamCtx)
}

// Update binds optional name/status/member-ceiling pointers and delegates
// their joint validation/update. Omission or JSON null leaves a pointer nil;
// service success returns readback, while a readback failure can follow a
// completed write.
func (h *AdminHandler) Update(c *gin.Context) {
	teamID, ok := adminTeamID(c)
	if !ok {
		return
	}
	var req AdminTeamUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.AdminUpdate(c.Request.Context(), teamID, team.TeamAdminUpdate{
		Name: req.Name, Status: req.Status, MemberLimit: req.MemberLimit,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// ForceTransfer binds a required target identity and delegates administrator
// ownership transfer for the route team. The service/repository enforce
// participant validity; parameter, binding or operation failure maps to an
// error response.
func (h *AdminHandler) ForceTransfer(c *gin.Context) {
	teamID, ok := adminTeamID(c)
	if !ok {
		return
	}
	var req AdminTeamTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.AdminForceTransfer(c.Request.Context(), teamID, req.TargetUserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// Dissolve parses the team ID and delegates administrator teardown. Success
// returns a dissolved acknowledgement after the service completes; shared
// application-error mapping handles failures and route assembly owns
// administrator authorization.
func (h *AdminHandler) Dissolve(c *gin.Context) {
	teamID, ok := adminTeamID(c)
	if !ok {
		return
	}
	if err := h.service.AdminDissolve(c.Request.Context(), teamID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"dissolved": true})
}

// adminTeamID parses signed decimal route text as int64 and writes
// bad-request on parse failure. Parsed zero and negative values are returned
// successfully; downstream lookup determines whether that team identity
// exists.
func adminTeamID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid team ID")
		return 0, false
	}
	return id, true
}
