package httpapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// Team actions take the acting account from the authenticated request context
// and let the service establish membership or ownership. Body omission
// matters for full numeric/boolean triples, while usage queries retain absent
// dates for service defaults and convert date-only ends with the injected
// server calendar.

// UserHandler translates authenticated team requests and date filters into
// team service operations.
type UserHandler struct {
	// service is retained for team authorization and lifecycle operations.
	// Callers supply a usable instance before route handling and keep its
	// dependencies alive.
	service *team.TeamService
	// calendar supplies the server date location for usage-query parsing. Its
	// value is copied at construction, retaining the location/clock references
	// owned by the supplied calendar.
	calendar timezone.Calendar
}

// NewUserHandler retains a usable team service and copies the supplied
// calendar value. The calendar's location/clock references remain shared, and
// route assembly installs authentication before the handlers are invoked.
func NewUserHandler(teamService *team.TeamService, calendar timezone.Calendar) *UserHandler {
	return &UserHandler{service: teamService, calendar: calendar}
}

// teamSubject reads the authenticated subject placed in the Gin context.
// Missing subjects produce an unauthorized response and false; successful
// handlers pass its account ID to team authorization in the service.
func teamSubject(c *gin.Context) (authctx.AuthSubject, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
	}
	return subject, ok
}

// GetCurrent returns the signed-in account's current team context. Missing
// authentication is rejected before the service call; service errors use
// application-error mapping and successful results use the shared response
// envelope.
func (h *UserHandler) GetCurrent(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	teamCtx, err := h.service.GetCurrent(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// Create binds a required team name for the authenticated account and
// delegates self-service creation. The service applies feature/self-service
// gates and name rules; success uses a created envelope after its repository
// result.
func (h *UserHandler) Create(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"` // requested team display name
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.Create(c.Request.Context(), subject.UserID, req.Name)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, teamCtx)
}

// Update binds a required team name and delegates the current owner's rename.
// The service establishes active ownership and validates trimmed name text;
// authentication, binding or operation failures precede response success.
func (h *UserHandler) Update(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"` // requested team display name
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.UpdateName(c.Request.Context(), subject.UserID, req.Name)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// UpdateDefaultMemberLimits binds all three future-member USD defaults and
// delegates owner validation. Omitted numeric fields become zero, so requests
// replace the entire triple; existing member limits remain governed by their
// separate operation.
func (h *UserHandler) UpdateDefaultMemberLimits(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		// Daily, Weekly and Monthly replace the complete future-member USD default
		// triple. Omitted numeric fields decode zero, and the service rejects
		// negatives before saving defaults for later joins.
		Daily   float64 `json:"default_daily_limit_usd"`   // daily member allowance in USD
		Weekly  float64 `json:"default_weekly_limit_usd"`  // weekly member allowance in USD
		Monthly float64 `json:"default_monthly_limit_usd"` // monthly member allowance in USD
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.UpdateDefaultMemberLimits(c.Request.Context(), subject.UserID, req.Daily, req.Weekly, req.Monthly)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// SetStatus binds active or suspended status for the authenticated owner. The
// service permits an owner of a suspended team to resume it; route
// registration also installs step-up for this endpoint.
func (h *UserHandler) SetStatus(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		// Status: requested active or suspended status
		Status string `json:"status" binding:"required,oneof=active suspended"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.SetStatus(c.Request.Context(), subject.UserID, req.Status)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// ListMembers returns the service-authorized membership view for the
// signed-in account. Owners receive the full team list, while ordinary
// members receive owner/self records; missing authentication or service
// failure writes an error.
func (h *UserHandler) ListMembers(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	members, err := h.service.ListMembers(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, members)
}

// parseTeamUsageQuery parses the date range, optional positive member/Key
// filters and pagination inputs from a request. Unzoned timestamps use the
// injected calendar; timezone query text is ignored, while offset-bearing
// timestamps retain their supplied offset.
//
// A date-only end advances one calendar day to form the exclusive upper
// bound, preserving daylight-saving day length. Missing dates stay zero for
// later service defaults. Invalid dates/IDs return the partially filled query
// with an error. Pagination parse errors are ignored: syntax failure yields
// zero, while out-of-range text can yield a saturated integer. Service
// normalization selects a usable limit and nonnegative offset; a positive
// saturated offset remains. The service separately constrains an ordinary
// member's actor filter.
func parseTeamUsageQuery(c *gin.Context, calendar timezone.Calendar) (team.TeamUsageQuery, error) {
	query := team.TeamUsageQuery{}
	parseTime := func(value string, end bool) (time.Time, error) {
		if strings.TrimSpace(value) == "" {
			return time.Time{}, nil
		}
		parsed, dateOnly, err := calendar.ParseDateTimeInUserLocation(value, "")
		if err != nil {
			return time.Time{}, err
		}
		// Date-only upper bounds include the selected day by advancing its local
		// calendar date; the resulting exclusive endpoint follows DST rather than a
		// fixed 24-hour duration.
		if end && dateOnly {
			parsed = parsed.AddDate(0, 0, 1)
		}
		return parsed, nil
	}
	var err error
	if query.From, err = parseTime(c.Query("from"), false); err != nil {
		return query, err
	}
	if query.To, err = parseTime(c.Query("to"), true); err != nil {
		return query, err
	}
	parseID := func(name string) (*int64, error) {
		value := strings.TrimSpace(c.Query(name))
		if value == "" {
			return nil, nil
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, strconv.ErrSyntax
		}
		return &id, nil
	}
	if query.ActorUserID, err = parseID("member_id"); err != nil {
		return query, err
	}
	if query.APIKeyID, err = parseID("api_key_id"); err != nil {
		return query, err
	}
	// Ignore pagination parse errors and retain each conversion result: syntax
	// failures yield zero and range failures may saturate, while service
	// normalization bounds the resulting limit/offset.
	query.Limit, _ = strconv.Atoi(c.DefaultQuery("limit", "20"))
	query.Offset, _ = strconv.Atoi(c.DefaultQuery("offset", "0"))
	return query, nil
}

// GetUsageSummary parses usage filters and returns the signed-in account's
// authorized team totals. Malformed query input is bad-request, and the
// service narrows ordinary members to themselves before repository reporting.
func (h *UserHandler) GetUsageSummary(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	query, err := parseTeamUsageQuery(c, h.calendar)
	if err != nil {
		response.BadRequest(c, "Invalid team usage query")
		return
	}
	result, err := h.service.GetUsageSummary(c.Request.Context(), subject.UserID, query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListMemberUsageSeries parses usage filters and returns authorized member
// trends. The service selects owner/team or ordinary-member/self scope, while
// report readers include represented current and departed members within that
// scope.
func (h *UserHandler) ListMemberUsageSeries(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	query, err := parseTeamUsageQuery(c, h.calendar)
	if err != nil {
		response.BadRequest(c, "Invalid team usage query")
		return
	}
	result, err := h.service.ListMemberUsageSeries(c.Request.Context(), subject.UserID, query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListUsageLogs parses dates, optional filters and pagination for authorized
// team records. Query failure is bad-request; the service normalizes
// pagination and narrows ordinary-member scope before returning its shared
// envelope.
func (h *UserHandler) ListUsageLogs(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	query, err := parseTeamUsageQuery(c, h.calendar)
	if err != nil {
		response.BadRequest(c, "Invalid team usage query")
		return
	}
	result, err := h.service.ListUsageLogs(c.Request.Context(), subject.UserID, query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListTeamKeys returns metadata visible to the authenticated team membership,
// including suspended teams. The service applies role-based creator filtering
// and clears plaintext credentials after masking before this handler
// serializes success.
func (h *UserHandler) ListTeamKeys(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	items, err := h.service.ListTeamKeys(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// DisableTeamKey parses a positive Key ID and delegates the team owner's
// disable operation. Missing authentication, malformed ID or service failure
// is rejected; success returns the disabled acknowledgement after mutation.
func (h *UserHandler) DisableTeamKey(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	if err := h.service.DisableTeamKey(c.Request.Context(), subject.UserID, keyID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"disabled": true})
}

// EnableTeamKey parses a positive Key ID and delegates the team owner's
// reactivation operation. The service clears owner disablement and permits
// suspended-team management; parameter/operation failures precede its enabled
// acknowledgement.
func (h *UserHandler) EnableTeamKey(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	if err := h.service.EnableTeamKey(c.Request.Context(), subject.UserID, keyID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": true})
}

// DeleteTeamKey parses a positive Key ID and delegates the authenticated
// owner's deletion. Service success precedes the deleted acknowledgement; its
// repository owns credential deletion and the service requests cache
// invalidation afterward.
func (h *UserHandler) DeleteTeamKey(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	if err := h.service.DeleteTeamKey(c.Request.Context(), subject.UserID, keyID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// Invite binds a required mailbox for an authenticated owner and requests an
// invitation. The service normalizes mailbox text, records rate admission and
// handles persistence/delivery ordering; success returns the created
// invitation view.
func (h *UserHandler) Invite(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email" binding:"required,email"` // requested invitation recipient mailbox
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	invitation, err := h.service.Invite(c.Request.Context(), subject.UserID, req.Email)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, invitation)
}

// ListInvitations returns invitations for the authenticated active owner's
// team. Authentication and service authorization failures produce error
// envelopes before the selected invitation statuses/timestamps are exposed to
// that caller.
func (h *UserHandler) ListInvitations(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	items, err := h.service.ListInvitations(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// ReissueInvitation parses a signed invitation ID and delegates owner
// reissue. Zero/negative parsed IDs reach repository matching; the service
// records rate admission before replacing token state and delivering its
// message.
func (h *UserHandler) ReissueInvitation(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation ID")
		return
	}
	item, err := h.service.ReissueInvitation(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// RevokeInvitation parses a signed invitation ID and delegates active-owner
// revocation. Parsed zero/negative IDs reach repository matching; success
// returns a revoked acknowledgement after the pending invitation operation
// completes.
func (h *UserHandler) RevokeInvitation(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid invitation ID")
		return
	}
	if err := h.service.RevokeInvitation(c.Request.Context(), subject.UserID, id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"revoked": true})
}

// PreviewInvitation binds required token text and trims it before service
// preview. The service hashes it and supplies the current account mailbox for
// repository confirmation; errors precede the invitation summary envelope.
func (h *UserHandler) PreviewInvitation(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token" binding:"required"` // submitted plaintext review token
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	preview, err := h.service.PreviewInvitation(c.Request.Context(), subject.UserID, strings.TrimSpace(req.Token))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

// ResolveInvitation binds required token/resolution text for the
// authenticated account. The service accepts exact accepted or declined
// resolution and supplies the current mailbox; decline can return nil context
// in a successful envelope.
func (h *UserHandler) ResolveInvitation(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		Token      string `json:"token" binding:"required"`      // submitted plaintext review token
		Resolution string `json:"resolution" binding:"required"` // requested accepted or declined decision
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.ResolveInvitation(c.Request.Context(), subject.UserID, req.Token, req.Resolution)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// RemoveMember parses a signed member ID and delegates active-owner removal.
// The service rejects owner self-removal and storage enforces current
// ordinary membership; success returns removed after its lifecycle write
// completes.
func (h *UserHandler) RemoveMember(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	memberID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid member ID")
		return
	}
	if err := h.service.RemoveMember(c.Request.Context(), subject.UserID, memberID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"removed": true})
}

// Leave delegates the authenticated membership's departure, including
// suspended teams. Owners receive the leave restriction, while successful
// ordinary departure precedes a left acknowledgement and related service
// invalidation attempts.
func (h *UserHandler) Leave(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	if err := h.service.Leave(c.Request.Context(), subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"left": true})
}

// UpdateMemberLimits parses signed member ID and binds a complete
// daily/weekly/monthly USD triple. Omitted amounts become zero; the service
// validates nonnegative values and active ownership before replacing the
// member limits.
func (h *UserHandler) UpdateMemberLimits(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	memberID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid member ID")
		return
	}
	var req struct {
		// Daily, Weekly and Monthly replace the complete selected member USD limit
		// triple. Omitted numeric fields decode zero, and the service rejects
		// negatives before the repository update.
		Daily   float64 `json:"daily_limit_usd"`   // daily member allowance in USD
		Weekly  float64 `json:"weekly_limit_usd"`  // weekly member allowance in USD
		Monthly float64 `json:"monthly_limit_usd"` // monthly member allowance in USD
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.UpdateMemberLimits(c.Request.Context(), subject.UserID, memberID, req.Daily, req.Weekly, req.Monthly); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"updated": true})
}

// ResetMemberUsage parses signed member ID and binds calendar-period reset
// flags. Omitted flags remain false, and the service requires at least one
// selected period before repository reset and invalidation attempts.
func (h *UserHandler) ResetMemberUsage(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	memberID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid member ID")
		return
	}
	var req struct {
		// Daily, Weekly and Monthly select independent calendar-usage resets.
		// Missing flags remain false, and service validation rejects a request with
		// no selected period before delegation.
		Daily   bool `json:"daily"`   // daily calendar reset switch
		Weekly  bool `json:"weekly"`  // weekly calendar reset switch
		Monthly bool `json:"monthly"` // monthly calendar reset switch
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.ResetMemberUsage(c.Request.Context(), subject.UserID, memberID, req.Daily, req.Weekly, req.Monthly); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"reset": true})
}

// StartOwnershipTransfer binds a required target identity and delegates
// active-owner transfer preparation. Route registration adds step-up; the
// service generates review state and can return mail failure after its
// transfer is saved.
func (h *UserHandler) StartOwnershipTransfer(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		TargetUserID int64 `json:"target_user_id" binding:"required"` // requested successor account identity
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	transfer, err := h.service.StartOwnershipTransfer(c.Request.Context(), subject.UserID, req.TargetUserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, transfer)
}

// ResolveOwnershipTransfer binds required token/resolution text for the
// authenticated account. Route registration adds step-up; the repository
// confirms the intended target and service success returns its resulting team
// context.
func (h *UserHandler) ResolveOwnershipTransfer(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	var req struct {
		Token      string `json:"token" binding:"required"`      // submitted plaintext review token
		Resolution string `json:"resolution" binding:"required"` // requested accepted or declined decision
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	teamCtx, err := h.service.ResolveOwnershipTransfer(c.Request.Context(), subject.UserID, req.Token, req.Resolution)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, teamCtx)
}

// CancelOwnershipTransfer delegates cancellation by the authenticated active
// owner. Route registration adds step-up; operation failure uses
// application-error mapping and success returns a cancelled acknowledgement
// after the repository result.
func (h *UserHandler) CancelOwnershipTransfer(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	if err := h.service.CancelOwnershipTransfer(c.Request.Context(), subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}

// Dissolve delegates teardown by the authenticated team owner, including
// suspended teams. Route registration adds step-up; successful storage
// teardown precedes service cache-invalidation attempts and the dissolved
// response acknowledgement.
func (h *UserHandler) Dissolve(c *gin.Context) {
	subject, ok := teamSubject(c)
	if !ok {
		return
	}
	if err := h.service.Dissolve(c.Request.Context(), subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"dissolved": true})
}
