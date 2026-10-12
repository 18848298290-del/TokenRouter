package httpapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	billingdto "github.com/TokenFlux/TokenRouter/internal/billing/httpapi/dto"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// UserAdministration provides user operations for administrator HTTP handlers.
type UserAdministration interface {
	ListUsers(ctx context.Context, page, pageSize int, filters identity.UserListFilters, sortBy, sortOrder string) ([]identity.User, int64, error)
	GetUser(ctx context.Context, id int64) (*identity.User, error)
	GetUserIncludeDeleted(ctx context.Context, id int64) (*identity.User, error)
	CreateUser(ctx context.Context, input *identity.CreateUserInput) (*identity.User, error)
	UpdateUser(ctx context.Context, id int64, input *identity.UpdateUserInput) (*identity.User, error)
	DeleteUser(ctx context.Context, id int64) error
	BatchUpdateConcurrency(ctx context.Context, userIDs []int64, value int, mode string) (int, error)
	BatchUpdateLimits(ctx context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error)
	UpdateUserBalance(ctx context.Context, userID int64, balance float64, operation string, notes string) (*identity.User, error)
	GetUserRPMStatus(ctx context.Context, userID int64) (*identity.UserRPMStatus, error)
	GetUserUsageStats(ctx context.Context, userID int64, period string) (any, error)
	GetUserBalanceHistory(ctx context.Context, userID int64, page, pageSize int, codeType string) ([]identity.RedeemCode, int64, float64, error)
	BindUserAuthIdentity(ctx context.Context, userID int64, input identity.AdminBindAuthIdentityInput) (*identity.AdminBoundAuthIdentity, error)
	ReplaceUserGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (*identity.ReplaceUserGroupResult, error)
}

// AdminUserHandler combines user administration with key listing and concurrency reads.
type AdminUserHandler[K any] struct {
	idempotencyhttp.Executor

	adminService UserAdministration
	listKeys     func(context.Context, int64, int, int, string, string) ([]K, int64, error)
	concurrency  func(context.Context, []identity.User) (map[int64]int, error)
	stepUp       func(*gin.Context) bool
}

// UserWithConcurrency wraps AdminUser with current concurrency info.
type UserWithConcurrency[K any] struct {
	dto.AdminUser[K]
	CurrentConcurrency int `json:"current_concurrency"`
}

// CreateUserRequest represents admin create user request.
type CreateUserRequest struct {
	Email         string   `json:"email" binding:"required,email"`
	Password      string   `json:"password" binding:"required,min=6"`
	Username      string   `json:"username"`
	Notes         string   `json:"notes"`
	Role          string   `json:"role" binding:"omitempty,oneof=admin user"`
	Balance       *float64 `json:"balance"`
	Concurrency   int      `json:"concurrency"`
	RPMLimit      int      `json:"rpm_limit"`
	APIKeyLimit   *int     `json:"api_key_limit" binding:"omitempty,gte=0,lte=2147483647"`
	AllowedGroups []int64  `json:"allowed_groups"`
	// DisabledPublicGroups lists public groups denied to this user.
	DisabledPublicGroups []int64 `json:"disabled_public_groups"`
}

// UpdateUserRequest contains administrator changes to a user.
// Pointer fields distinguish omitted values from zero or empty values.
type UpdateUserRequest struct {
	Email         string   `json:"email" binding:"omitempty,email"`
	Password      string   `json:"password" binding:"omitempty,min=6"`
	Username      *string  `json:"username"`
	Notes         *string  `json:"notes"`
	Role          string   `json:"role" binding:"omitempty,oneof=admin user"`
	Balance       *float64 `json:"balance"`
	Concurrency   *int     `json:"concurrency"`
	RPMLimit      *int     `json:"rpm_limit"`
	APIKeyLimit   *int     `json:"api_key_limit" binding:"omitempty,gte=0,lte=2147483647"`
	Status        string   `json:"status" binding:"omitempty,oneof=active disabled"`
	AllowedGroups *[]int64 `json:"allowed_groups"`
	// DisabledPublicGroups is nil to leave the list unchanged or empty to clear it.
	DisabledPublicGroups *[]int64 `json:"disabled_public_groups"`
	// GroupRates maps group IDs to user-specific rate multipliers.
	// A nil rate removes the override for that group.
	GroupRates map[int64]*float64 `json:"group_rates"`
}

// UpdateBalanceRequest represents balance update request.
type UpdateBalanceRequest struct {
	Balance   float64 `json:"balance" binding:"required,gt=0"`
	Operation string  `json:"operation" binding:"required,oneof=set add subtract"`
	Notes     string  `json:"notes"`
}

// BatchUpdateConcurrencyRequest sets or adds to user concurrency limits.
type BatchUpdateConcurrencyRequest struct {
	UserIDs     []int64 `json:"user_ids"`
	All         bool    `json:"all"`
	Concurrency int     `json:"concurrency"`
	Mode        string  `json:"mode" binding:"required,oneof=set add"`
}

type BindUserAuthIdentityRequest struct {
	ProviderType    string                              `json:"provider_type"`
	ProviderKey     string                              `json:"provider_key"`
	ProviderSubject string                              `json:"provider_subject"`
	Issuer          *string                             `json:"issuer"`
	Metadata        map[string]any                      `json:"metadata"`
	Channel         *BindUserAuthIdentityChannelRequest `json:"channel"`
}

type BindUserAuthIdentityChannelRequest struct {
	Channel        string         `json:"channel"`
	ChannelAppID   string         `json:"channel_app_id"`
	ChannelSubject string         `json:"channel_subject"`
	Metadata       map[string]any `json:"metadata"`
}

// ReplaceGroupRequest represents the request to replace a user's exclusive group.
type ReplaceGroupRequest struct {
	OldGroupID int64 `json:"old_group_id" binding:"required,gt=0"`
	NewGroupID int64 `json:"new_group_id" binding:"required,gt=0"`
}

// BatchUpdateLimitsRequest replaces concurrency and RPM limits for selected users.
type BatchUpdateLimitsRequest struct {
	UserIDs     []int64 `json:"user_ids"`
	All         bool    `json:"all"`
	Concurrency *int    `json:"concurrency" binding:"omitempty,min=0"`
	RPMLimit    *int    `json:"rpm_limit" binding:"omitempty,min=0"`
}

func NewAdminUserHandler[K any](users UserAdministration, keys func(context.Context, int64, int, int, string, string) ([]K, int64, error), concurrency func(context.Context, []identity.User) (map[int64]int, error), stepUp func(*gin.Context) bool) *AdminUserHandler[K] {
	return &AdminUserHandler[K]{adminService: users, listKeys: keys, concurrency: concurrency, stepUp: stepUp}
}

func (h *AdminUserHandler[K]) userResponse(u *identity.User) *dto.AdminUser[K] {
	return dto.AdminUserFromIdentity[K](u, nil)
}

func adminID(c *gin.Context) int64 {
	s, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		return 0
	}
	return s.UserID
}

// List handles listing all users with pagination
// GET /api/v1/admin/users
// Query params:
//   - status: filter by user status
//   - role: filter by user role
//   - search: matches email, username, notes, or API key content
//   - attr[{id}]: filter by custom attribute value, e.g. attr[1]=company
//   - group_name: fuzzy filter by allowed group name
//   - api_key_group_id: matches the exact group bound to a user API key
func (h *AdminUserHandler[K]) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)

	search := c.Query("search")
	// Limit search input by rune count so truncation preserves UTF-8.
	search = strings.TrimSpace(search)
	if runes := []rune(search); len(runes) > 100 {
		search = string(runes[:100])
	}

	filters := identity.UserListFilters{
		Status:     c.Query("status"),
		Role:       c.Query("role"),
		Search:     search,
		GroupName:  strings.TrimSpace(c.Query("group_name")),
		Attributes: ParseAttributeFilters(c),
	}
	if raw := strings.TrimSpace(c.Query("api_key_group_id")); raw != "" {
		if id, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && id > 0 {
			filters.APIKeyGroupID = id
		}
	}
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	if raw, ok := c.GetQuery("include_subscriptions"); ok {
		includeSubscriptions := response.ParseBoolQueryWithDefault(raw, true)
		filters.IncludeSubscriptions = &includeSubscriptions
	}

	users, total, err := h.adminService.ListUsers(c.Request.Context(), page, pageSize, filters, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Batch get current concurrency (nil map if unavailable)
	var loadInfo map[int64]int
	if len(users) > 0 && h.concurrency != nil {
		loadInfo, _ = h.concurrency(c.Request.Context(), users)
	}

	out := make([]UserWithConcurrency[K], len(users))
	for i := range users {
		out[i] = UserWithConcurrency[K]{
			AdminUser: *h.userResponse(&users[i]),
		}
		out[i].CurrentConcurrency = loadInfo[users[i].ID]
	}

	response.Paginated(c, out, total, page, pageSize)
}

// ParseAttributeFilters extracts attribute filters from query params
// Format: attr[{attributeID}]=value, e.g. attr[1]=company&attr[2]=developer.
func ParseAttributeFilters(c *gin.Context) map[int64]string {
	result := make(map[int64]string)

	for key, values := range c.Request.URL.Query() {
		if len(values) == 0 || values[0] == "" {
			continue
		}

		if len(key) > 5 && key[:5] == "attr[" && key[len(key)-1] == ']' {
			idStr := key[5 : len(key)-1]
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err == nil && id > 0 {
				result[id] = values[0]
			}
		}
	}

	return result
}

// GetByID handles getting a user by ID
// GET /api/v1/admin/users/:id
func (h *AdminUserHandler[K]) GetByID(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var user *identity.User
	if c.Query("include_deleted") == "true" {
		user, err = h.adminService.GetUserIncludeDeleted(c.Request.Context(), userID)
	} else {
		user, err = h.adminService.GetUser(c.Request.Context(), userID)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.userResponse(user))
}

// BindAuthIdentity manually binds a canonical auth identity to a user.
// POST /api/v1/admin/users/:id/auth-identities
func (h *AdminUserHandler[K]) BindAuthIdentity(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req BindUserAuthIdentityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := identity.AdminBindAuthIdentityInput{
		ProviderType:    req.ProviderType,
		ProviderKey:     req.ProviderKey,
		ProviderSubject: req.ProviderSubject,
		Issuer:          req.Issuer,
		Metadata:        req.Metadata,
	}
	if req.Channel != nil {
		input.Channel = &identity.AdminBindAuthIdentityChannelInput{
			Channel:        req.Channel.Channel,
			ChannelAppID:   req.Channel.ChannelAppID,
			ChannelSubject: req.Channel.ChannelSubject,
			Metadata:       req.Channel.Metadata,
		}
	}

	result, err := h.adminService.BindUserAuthIdentity(c.Request.Context(), userID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// Create handles creating a new user
// POST /api/v1/admin/users
func (h *AdminUserHandler[K]) Create(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Creating an administrator requires the configured step-up check.
	if req.Role == identity.RoleAdmin {
		if !h.stepUp(c) {
			return
		}
	}

	user, err := h.adminService.CreateUser(c.Request.Context(), &identity.CreateUserInput{
		Email:                req.Email,
		Password:             req.Password,
		Username:             req.Username,
		Notes:                req.Notes,
		Role:                 req.Role,
		Balance:              req.Balance,
		Concurrency:          req.Concurrency,
		RPMLimit:             req.RPMLimit,
		APIKeyLimit:          req.APIKeyLimit,
		AllowedGroups:        req.AllowedGroups,
		DisabledPublicGroups: req.DisabledPublicGroups,
		ActorAdminID:         adminID(c),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.userResponse(user))
}

// Update handles updating a user
// PUT /api/v1/admin/users/:id
func (h *AdminUserHandler[K]) Update(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Block self-demotion so the caller retains administrator access.
	// Demotion of another administrator is checked by the service.
	if req.Role == identity.RoleUser && userID == adminID(c) {
		response.BadRequest(c, "cannot demote yourself from admin")
		return
	}

	// Run step-up whenever the request sets the administrator role.
	// Reading the current role first would permit a concurrent demotion and unchecked re-promotion.
	if req.Role == identity.RoleAdmin {
		if !h.stepUp(c) {
			return
		}
	}

	// Pass pointer fields through so nil continues to mean omitted.
	user, err := h.adminService.UpdateUser(c.Request.Context(), userID, &identity.UpdateUserInput{
		Email:                req.Email,
		Password:             req.Password,
		Username:             req.Username,
		Notes:                req.Notes,
		Role:                 req.Role,
		Balance:              req.Balance,
		Concurrency:          req.Concurrency,
		RPMLimit:             req.RPMLimit,
		APIKeyLimit:          req.APIKeyLimit,
		Status:               req.Status,
		AllowedGroups:        req.AllowedGroups,
		DisabledPublicGroups: req.DisabledPublicGroups,
		GroupRates:           req.GroupRates,
		ActorAdminID:         adminID(c),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.userResponse(user))
}

// Delete handles deleting a user
// DELETE /api/v1/admin/users/:id
func (h *AdminUserHandler[K]) Delete(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	err = h.adminService.DeleteUser(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "User deleted successfully"})
}

// UpdateBalance handles updating user balance
// POST /api/v1/admin/users/:id/balance
func (h *AdminUserHandler[K]) UpdateBalance(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req UpdateBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	idempotencyPayload := struct {
		UserID int64                `json:"user_id"`
		Body   UpdateBalanceRequest `json:"body"`
	}{
		UserID: userID,
		Body:   req,
	}
	h.ExecuteAdminIdempotentJSON(c, "admin.users.balance.update", idempotencyPayload, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		user, execErr := h.adminService.UpdateUserBalance(ctx, userID, req.Balance, req.Operation, req.Notes)
		if execErr != nil {
			return nil, execErr
		}
		return h.userResponse(user), nil
	})
}

// BatchUpdateConcurrency updates concurrency limits for selected users.
// POST /api/v1/admin/users/batch-concurrency
func (h *AdminUserHandler[K]) BatchUpdateConcurrency(c *gin.Context) {
	var req BatchUpdateConcurrencyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !req.All && len(req.UserIDs) == 0 {
		response.BadRequest(c, "user_ids is required unless all=true")
		return
	}
	if len(req.UserIDs) > 500 {
		response.BadRequest(c, "user_ids cannot exceed 500")
		return
	}

	var userIDs []int64
	if req.All {
		page := 1
		const pageSize = 500
		for {
			users, _, err := h.adminService.ListUsers(c.Request.Context(), page, pageSize, identity.UserListFilters{}, "id", "asc")
			if err != nil {
				response.ErrorFrom(c, err)
				return
			}
			for _, user := range users {
				userIDs = append(userIDs, user.ID)
			}
			if len(users) < pageSize {
				break
			}
			page++
		}
	} else {
		userIDs = req.UserIDs
	}

	if len(userIDs) == 0 {
		response.Success(c, gin.H{"affected": 0})
		return
	}

	affected, err := h.adminService.BatchUpdateConcurrency(c.Request.Context(), userIDs, req.Concurrency, req.Mode)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"affected": affected})
}

// GetUserAPIKeys handles getting user's API keys
// GET /api/v1/admin/users/:id/api-keys
func (h *AdminUserHandler[K]) GetUserAPIKeys(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	page, pageSize := response.ParsePagination(c)
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")

	keys, total, err := h.listKeys(c.Request.Context(), userID, page, pageSize, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Paginated(c, keys, total, page, pageSize)
}

// GetUserUsage handles getting user's usage statistics
// GET /api/v1/admin/users/:id/usage
func (h *AdminUserHandler[K]) GetUserUsage(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	period := c.DefaultQuery("period", "month")

	stats, err := h.adminService.GetUserUsageStats(c.Request.Context(), userID, period)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, stats)
}

// GetBalanceHistory handles getting user's balance/concurrency change history
// GET /api/v1/admin/users/:id/balance-history
// Query params:
//   - type: filter by record type (balance, admin_balance, concurrency, admin_concurrency, subscription)
func (h *AdminUserHandler[K]) GetBalanceHistory(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	page, pageSize := response.ParsePagination(c)
	codeType := c.Query("type")

	codes, total, totalRecharged, err := h.adminService.GetUserBalanceHistory(c.Request.Context(), userID, page, pageSize, codeType)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Convert to admin DTO (includes notes field for admin visibility)
	out := make([]billingdto.AdminRedeemCode, 0, len(codes))
	for i := range codes {
		out = append(out, *billingdto.RedeemCodeFromServiceAdmin(&codes[i]))
	}

	// Custom response with total_recharged alongside pagination
	pages := max(int((total+int64(pageSize)-1)/int64(pageSize)), 1)
	response.Success(c, gin.H{
		"items":           out,
		"total":           total,
		"page":            page,
		"page_size":       pageSize,
		"pages":           pages,
		"total_recharged": totalRecharged,
	})
}

// ReplaceGroup handles replacing a user's exclusive group
// POST /api/v1/admin/users/:id/replace-group
func (h *AdminUserHandler[K]) ReplaceGroup(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	var req ReplaceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.adminService.ReplaceUserGroup(c.Request.Context(), userID, req.OldGroupID, req.NewGroupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"migrated_keys": result.MigratedKeys,
	})
}

// GetUserRPMStatus returns current-minute request counts and limits for a user.
// GET /api/v1/admin/users/:id/rpm-status
func (h *AdminUserHandler[K]) GetUserRPMStatus(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}

	status, err := h.adminService.GetUserRPMStatus(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, status)
}

// BatchUpdateLimits replaces concurrency and/or RPM limits for selected users.
// POST /api/v1/admin/users/batch-limits.
func (h *AdminUserHandler[K]) BatchUpdateLimits(c *gin.Context) {
	var req BatchUpdateLimitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.Concurrency == nil && req.RPMLimit == nil {
		response.BadRequest(c, "at least one of concurrency or rpm_limit is required")
		return
	}
	if !req.All && len(req.UserIDs) == 0 {
		response.BadRequest(c, "user_ids is required unless all=true")
		return
	}
	if !req.All && len(req.UserIDs) > 500 {
		response.BadRequest(c, "user_ids cannot exceed 500")
		return
	}

	userIDs := req.UserIDs
	if req.All {
		userIDs = nil
		page := 1
		const pageSize = 500
		for {
			users, _, err := h.adminService.ListUsers(c.Request.Context(), page, pageSize, identity.UserListFilters{}, "id", "asc")
			if err != nil {
				response.ErrorFrom(c, err)
				return
			}
			for _, user := range users {
				userIDs = append(userIDs, user.ID)
			}
			if len(users) < pageSize {
				break
			}
			page++
		}
	}

	if len(userIDs) == 0 {
		response.Success(c, gin.H{"affected": 0})
		return
	}

	affected, err := h.adminService.BatchUpdateLimits(
		c.Request.Context(),
		userIDs,
		req.Concurrency,
		req.RPMLimit,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"affected": affected})
}
