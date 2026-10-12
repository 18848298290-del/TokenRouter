package httpapi

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	groupdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// CreateGroupRequest contains the settings for a new group.
type CreateGroupRequest struct {
	Localization               *locale.Update[routing.GroupCopy]       `json:"localization"`
	RoutingPolicy              routing.GroupRoutingPolicy              `json:"routing_policy"`
	Name                       string                                  `json:"name" binding:"required"`
	Description                string                                  `json:"description"`
	SchedulerType              string                                  `json:"scheduler_type" binding:"omitempty,oneof=basic advanced"`
	AdvancedSchedulerOverrides routing.GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"`
	DisplayBrand               string                                  `json:"display_brand"`
	SortOrder                  *int                                    `json:"sort_order"`
	RateMultiplier             float64                                 `json:"rate_multiplier"`
	IsExclusive                bool                                    `json:"is_exclusive"`
	// Session isolation rejects explicit sessions already owned by another group.
	SessionIsolationEnabled bool `json:"session_isolation_enabled"`

	// Image permissions belong to the group independently of its shared pricing configuration.
	AllowImageGeneration      bool `json:"allow_image_generation"`
	AllowBatchImageGeneration bool `json:"allow_batch_image_generation"`

	ClaudeCodeOnly                  bool   `json:"claude_code_only"`
	FallbackGroupID                 *int64 `json:"fallback_group_id"`
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"`
	// UnavailableFallbackGroupID is the preferred API key fallback when this group is disabled.
	UnavailableFallbackGroupID *int64 `json:"unavailable_fallback_group_id"`
	// Legacy provider routing by model.
	ModelRouting        map[string][]int64 `json:"model_routing"`
	ModelRoutingEnabled bool               `json:"model_routing_enabled"`
	MCPXMLInject        *bool              `json:"mcp_xml_inject"`
	// Legacy Antigravity model-family metadata.
	SupportedModelScopes []string `json:"supported_model_scopes"`
	// Allowed client protocols; nil uses the creation defaults.
	AllowedProtocols             []protocol.ProtocolID                         `json:"allowed_protocols"`
	ProtocolFallbacks            map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"`
	ResponsesImagePolicy         string                                        `json:"responses_image_policy"`
	LegacyAllowedClientProtocols []protocol.ProtocolID                         `json:"allowed_client_protocols"`
	// Legacy OpenAI Messages compatibility switch.
	AllowMessagesDispatch bool `json:"allow_messages_dispatch"`
	AllowLive             bool `json:"allow_live"`
	// Whether to force Fast priority for OpenAI requests.
	ForceOpenAIFast bool `json:"force_openai_fast"`
	// When supplied, this policy takes precedence over the legacy boolean.
	OpenAIFastPolicy *string `json:"openai_fast_policy"`

	RequireOAuthOnly        bool                                 `json:"require_oauth_only"`
	RequirePrivacySet       bool                                 `json:"require_privacy_set"`
	DefaultMappedModel      string                               `json:"default_mapped_model"`
	ModelsListConfig        routing.GroupModelsListConfig        `json:"models_list_config"`
	AvailabilityProbeConfig routing.GroupAvailabilityProbeConfig `json:"availability_probe_config"`
	// Requests per minute; zero means unlimited.
	RPMLimit int `json:"rpm_limit"`
	// OpenAI/Codex reasoning-effort ceiling; empty means unlimited.
	MaxReasoningEffort string `json:"max_reasoning_effort"`
	// Action above the ceiling: downgrade (default) or deny.
	MaxReasoningEffortOverLimit string `json:"max_reasoning_effort_over_limit"`
	// Effort mappings can target exact model names, prefixes, or suffixes.
	ReasoningEffortMappings []routing.ReasoningEffortMapping `json:"reasoning_effort_mappings"`
	// Bind providers from these groups after creation.
	CopyProvidersFromGroupIDs []int64 `json:"copy_providers_from_group_ids"`
}

// UpdateGroupRequest contains changes to an existing group.
type UpdateGroupRequest struct {
	Localization               *locale.Update[routing.GroupCopy]        `json:"localization"`
	RoutingPolicy              *routing.GroupRoutingPolicy              `json:"routing_policy"`
	Name                       string                                   `json:"name"`
	Description                *string                                  `json:"description"`
	SchedulerType              *string                                  `json:"scheduler_type" binding:"omitempty,oneof=basic advanced"`
	AdvancedSchedulerOverrides *routing.GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"`
	DisplayBrand               *string                                  `json:"display_brand"`
	SortOrder                  *int                                     `json:"sort_order"`
	RateMultiplier             *float64                                 `json:"rate_multiplier"`
	IsExclusive                *bool                                    `json:"is_exclusive"`
	// Nil leaves session isolation unchanged.
	SessionIsolationEnabled *bool  `json:"session_isolation_enabled"`
	Status                  string `json:"status" binding:"omitempty,oneof=active inactive"`

	// Image permissions belong to the group independently of its shared pricing configuration.
	AllowImageGeneration      *bool `json:"allow_image_generation"`
	AllowBatchImageGeneration *bool `json:"allow_batch_image_generation"`

	ClaudeCodeOnly                  *bool  `json:"claude_code_only"`
	FallbackGroupID                 *int64 `json:"fallback_group_id"`
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"`
	// UnavailableFallbackGroupID is the preferred API key fallback when this group is disabled.
	UnavailableFallbackGroupID *int64 `json:"unavailable_fallback_group_id"`
	// Legacy provider routing by model.
	ModelRouting        map[string][]int64 `json:"model_routing"`
	ModelRoutingEnabled *bool              `json:"model_routing_enabled"`
	MCPXMLInject        *bool              `json:"mcp_xml_inject"`
	// Legacy Antigravity model-family metadata.
	SupportedModelScopes *[]string `json:"supported_model_scopes"`
	// Nil leaves protocols unchanged; an empty array disables all client protocols.
	AllowedProtocols             *[]protocol.ProtocolID                        `json:"allowed_protocols"`
	ProtocolFallbacks            map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"`
	ResponsesImagePolicy         string                                        `json:"responses_image_policy"`
	LegacyAllowedClientProtocols *[]protocol.ProtocolID                        `json:"allowed_client_protocols"`
	// Legacy OpenAI Messages compatibility switch.
	AllowMessagesDispatch *bool `json:"allow_messages_dispatch"`
	AllowLive             *bool `json:"allow_live"`
	// Whether to force Fast priority for OpenAI requests.
	ForceOpenAIFast *bool `json:"force_openai_fast"`
	// When supplied, this policy takes precedence over the legacy boolean.
	OpenAIFastPolicy *string `json:"openai_fast_policy"`

	RequireOAuthOnly        *bool                                 `json:"require_oauth_only"`
	RequirePrivacySet       *bool                                 `json:"require_privacy_set"`
	DefaultMappedModel      *string                               `json:"default_mapped_model"`
	ModelsListConfig        *routing.GroupModelsListConfig        `json:"models_list_config"`
	AvailabilityProbeConfig *routing.GroupAvailabilityProbeConfig `json:"availability_probe_config"`
	// Requests per minute; zero means unlimited and nil leaves the limit unchanged.
	RPMLimit *int `json:"rpm_limit"`
	// OpenAI/Codex reasoning-effort ceiling; empty clears it and nil leaves it unchanged.
	MaxReasoningEffort *string `json:"max_reasoning_effort"`
	// Action above the ceiling; empty means downgrade and nil leaves it unchanged.
	MaxReasoningEffortOverLimit *string `json:"max_reasoning_effort_over_limit"`
	// Nil leaves mappings unchanged; an empty array clears them, otherwise replaces them.
	ReasoningEffortMappings *[]routing.ReasoningEffortMapping `json:"reasoning_effort_mappings"`
	// Replace current provider bindings with providers from these groups.
	CopyProvidersFromGroupIDs []int64 `json:"copy_providers_from_group_ids"`
}

// UpdateSortOrderRequest represents the request to update group sort orders.
type UpdateSortOrderRequest struct {
	Updates []struct {
		ID        int64 `json:"id" binding:"required"`
		SortOrder int   `json:"sort_order"`
	} `json:"updates" binding:"required,min=1"`
}

// GroupAdministration defines group management operations used by HTTP handlers.
type GroupAdministration interface {
	ListGroups(context.Context, int, int, string, string, string, *bool, string, string) ([]routing.Group, int64, error)
	GetAllGroups(context.Context) ([]routing.Group, error)
	GetAllGroupsIncludingInactive(context.Context) ([]routing.Group, error)
	GetGroup(context.Context, int64) (*routing.Group, error)
	GetGroupModelsListCandidates(context.Context, int64, string) ([]string, error)
	CreateGroup(context.Context, *routing.CreateGroupInput) (*routing.Group, error)
	UpdateGroup(context.Context, int64, *routing.UpdateGroupInput) (*routing.Group, error)
	DuplicateGroup(context.Context, int64, string, string) (*routing.Group, error)
	RecoverDuplicateGroup(context.Context, int64, string, string) (*routing.Group, error)
	DeleteGroup(context.Context, int64) error
	UpdateGroupSortOrders(context.Context, []routing.GroupSortOrderUpdate) error
}

// GroupHandler decodes HTTP input and delegates group rules to GroupAdministration.
type GroupHandler struct {
	idempotencyhttp.Executor

	adminService GroupAdministration
	resources    GroupResources
}

func NewGroupHandler(admin GroupAdministration, resources ...GroupResources) *GroupHandler {
	handler := &GroupHandler{adminService: admin}
	if len(resources) > 0 {
		handler.resources = resources[0]
	}
	return handler
}

// List returns groups with filtering and pagination.
// GET /api/v1/admin/groups
func (h *GroupHandler) List(c *gin.Context) {
	page, pageSize := httpx.ParsePagination(c)
	if _, supplied := c.GetQuery("platform"); supplied {
		httpx.ErrorFrom(c, infraerrors.BadRequest("REMOVED_GROUP_FIELD", "groups no longer have a platform"))
		return
	}
	platform := ""
	status := c.Query("status")
	search := c.Query("search")

	search = strings.TrimSpace(search)
	if len(search) > 100 {
		search = search[:100]
	}
	isExclusiveStr := c.Query("is_exclusive")
	sortBy := c.DefaultQuery("sort_by", "sort_order")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	var isExclusive *bool
	if isExclusiveStr != "" {
		val := isExclusiveStr == "true"
		isExclusive = &val
	}

	groups, total, err := h.adminService.ListGroups(c.Request.Context(), page, pageSize, platform, status, search, isExclusive, sortBy, sortOrder)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	outGroups := make([]groupdto.AdminGroup[struct{}], 0, len(groups))
	for i := range groups {
		outGroups = append(outGroups, *groupdto.AdminGroupFromRouting[struct{}](&groups[i]))
	}
	httpx.Paginated(c, outGroups, total, page, pageSize)
}

// GetAll returns enabled groups without pagination.
// With ?include_inactive=true it also returns disabled groups for administration.
//
// GET /api/v1/admin/groups/all
func (h *GroupHandler) GetAll(c *gin.Context) {
	if _, supplied := c.GetQuery("platform"); supplied {
		httpx.ErrorFrom(c, infraerrors.BadRequest("REMOVED_GROUP_FIELD", "groups no longer have a platform"))
		return
	}
	includeInactive := c.Query("include_inactive") == "true"

	var groups []routing.Group
	var err error

	if includeInactive {
		groups, err = h.adminService.GetAllGroupsIncludingInactive(c.Request.Context())
	} else {
		groups, err = h.adminService.GetAllGroups(c.Request.Context())
	}

	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	outGroups := make([]groupdto.AdminGroup[struct{}], 0, len(groups))
	for i := range groups {
		outGroups = append(outGroups, *groupdto.AdminGroupFromRouting[struct{}](&groups[i]))
	}
	httpx.Success(c, outGroups)
}

// GetByID returns a group by ID.
// GET /api/v1/admin/groups/:id
func (h *GroupHandler) GetByID(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid group ID")
		return
	}

	group, err := h.adminService.GetGroup(c.Request.Context(), groupID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, groupdto.AdminGroupFromRouting[struct{}](group))
}

// GetModelsListCandidates returns model IDs eligible for a custom /v1/models list.
// GET /api/v1/admin/groups/:id/models-list-candidates
func (h *GroupHandler) GetModelsListCandidates(c *gin.Context) {
	if _, supplied := c.GetQuery("platform"); supplied {
		httpx.ErrorFrom(c, infraerrors.BadRequest("REMOVED_GROUP_FIELD", "groups no longer have a platform"))
		return
	}
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID < 0 {
		httpx.BadRequest(c, "Invalid group ID")
		return
	}

	models, err := h.adminService.GetGroupModelsListCandidates(
		c.Request.Context(),
		groupID,
		"",
	)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{"models": models})
}

// Create creates a group.
// POST /api/v1/admin/groups
func (h *GroupHandler) Create(c *gin.Context) {
	var req CreateGroupRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	legacyProtocolInput := req.AllowedProtocols == nil && req.LegacyAllowedClientProtocols != nil
	if legacyProtocolInput {
		req.AllowedProtocols = req.LegacyAllowedClientProtocols
	}

	group, err := h.adminService.CreateGroup(c.Request.Context(), &routing.CreateGroupInput{
		Localization:                    req.Localization,
		Name:                            req.Name,
		Description:                     req.Description,
		SchedulerType:                   req.SchedulerType,
		AdvancedSchedulerOverrides:      req.AdvancedSchedulerOverrides,
		DisplayBrand:                    req.DisplayBrand,
		SortOrder:                       req.SortOrder,
		RateMultiplier:                  req.RateMultiplier,
		IsExclusive:                     req.IsExclusive,
		SessionIsolationEnabled:         req.SessionIsolationEnabled,
		RoutingPolicy:                   req.RoutingPolicy,
		AllowImageGeneration:            req.AllowImageGeneration,
		AllowBatchImageGeneration:       req.AllowBatchImageGeneration,
		ClaudeCodeOnly:                  req.ClaudeCodeOnly,
		FallbackGroupID:                 req.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: req.FallbackGroupIDOnInvalidRequest,
		UnavailableFallbackGroupID:      req.UnavailableFallbackGroupID,
		ModelRouting:                    req.ModelRouting,
		ModelRoutingEnabled:             req.ModelRoutingEnabled,
		MCPXMLInject:                    req.MCPXMLInject,
		SupportedModelScopes:            req.SupportedModelScopes,
		LegacyProtocolInput:             legacyProtocolInput,
		AllowedProtocols:                req.AllowedProtocols,
		ProtocolFallbacks:               req.ProtocolFallbacks,
		ResponsesImagePolicy:            req.ResponsesImagePolicy,
		AllowMessagesDispatch:           req.AllowMessagesDispatch,
		AllowLive:                       req.AllowLive,
		ForceOpenAIFast:                 req.ForceOpenAIFast,
		OpenAIFastPolicy:                req.OpenAIFastPolicy,
		RequireOAuthOnly:                req.RequireOAuthOnly,
		RequirePrivacySet:               req.RequirePrivacySet,
		DefaultMappedModel:              req.DefaultMappedModel,
		ModelsListConfig:                req.ModelsListConfig,
		AvailabilityProbeConfig:         req.AvailabilityProbeConfig,
		RPMLimit:                        req.RPMLimit,
		MaxReasoningEffort:              req.MaxReasoningEffort,
		MaxReasoningEffortOverLimit:     req.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:         req.ReasoningEffortMappings,
		CopyProvidersFromGroupIDs:       req.CopyProvidersFromGroupIDs,
	})
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, groupdto.AdminGroupFromRouting[struct{}](group))
}

// Duplicate creates a disabled copy with the source group's provider bindings.
// POST /api/v1/admin/groups/:id/duplicate
func (h *GroupHandler) Duplicate(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		httpx.BadRequest(c, "Invalid group ID")
		return
	}
	actorScope := idempotencyhttp.AdminActorScope(c)

	result, err := h.ExecuteAdminIdempotent(
		c,
		"admin.groups.duplicate",
		struct {
			GroupID int64 `json:"group_id"`
		}{GroupID: groupID},
		h.DefaultWriteIdempotencyTTL(),
		func(ctx context.Context) (any, error) {
			group, execErr := h.adminService.DuplicateGroup(ctx, groupID, actorScope, c.GetHeader("Idempotency-Key"))
			if execErr != nil {
				return nil, execErr
			}
			return groupdto.AdminGroupFromRouting[struct{}](group), nil
		},
	)
	if err != nil {
		reason := infraerrors.Reason(err)
		if reason == infraerrors.Reason(idempotency.ErrIdempotencyInProgress) || reason == infraerrors.Reason(idempotency.ErrIdempotencyStoreUnavail) {
			recovered, recoverErr := h.adminService.RecoverDuplicateGroup(c.Request.Context(), groupID, actorScope, c.GetHeader("Idempotency-Key"))
			if recoverErr != nil {
				slog.Warn("group_duplicate_recovery_failed", "group_id", groupID, "actor_scope", actorScope, "reason", reason, "error", recoverErr)
			} else if recovered != nil {
				c.Header("X-Idempotency-Recovered", "true")
				httpx.Success(c, groupdto.AdminGroupFromRouting[struct{}](recovered))
				return
			}
		}
		httpx.ErrorFrom(c, err)
		return
	}

	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	httpx.Success(c, result.Data)
}

// Update updates a group.
// PUT /api/v1/admin/groups/:id
func (h *GroupHandler) Update(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid group ID")
		return
	}

	var req UpdateGroupRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	legacyProtocolInput := req.AllowedProtocols == nil && req.LegacyAllowedClientProtocols != nil
	if legacyProtocolInput {
		req.AllowedProtocols = req.LegacyAllowedClientProtocols
	}

	group, err := h.adminService.UpdateGroup(c.Request.Context(), groupID, &routing.UpdateGroupInput{
		Localization:                    req.Localization,
		Name:                            req.Name,
		Description:                     req.Description,
		SchedulerType:                   req.SchedulerType,
		AdvancedSchedulerOverrides:      req.AdvancedSchedulerOverrides,
		DisplayBrand:                    req.DisplayBrand,
		SortOrder:                       req.SortOrder,
		RateMultiplier:                  req.RateMultiplier,
		IsExclusive:                     req.IsExclusive,
		SessionIsolationEnabled:         req.SessionIsolationEnabled,
		Status:                          req.Status,
		RoutingPolicy:                   req.RoutingPolicy,
		AllowImageGeneration:            req.AllowImageGeneration,
		AllowBatchImageGeneration:       req.AllowBatchImageGeneration,
		ClaudeCodeOnly:                  req.ClaudeCodeOnly,
		FallbackGroupID:                 req.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: req.FallbackGroupIDOnInvalidRequest,
		UnavailableFallbackGroupID:      req.UnavailableFallbackGroupID,
		ModelRouting:                    req.ModelRouting,
		ModelRoutingEnabled:             req.ModelRoutingEnabled,
		MCPXMLInject:                    req.MCPXMLInject,
		SupportedModelScopes:            req.SupportedModelScopes,
		LegacyProtocolInput:             legacyProtocolInput,
		AllowedProtocols:                req.AllowedProtocols,
		ProtocolFallbacks:               req.ProtocolFallbacks,
		ResponsesImagePolicy:            req.ResponsesImagePolicy,
		AllowMessagesDispatch:           req.AllowMessagesDispatch,
		AllowLive:                       req.AllowLive,
		ForceOpenAIFast:                 req.ForceOpenAIFast,
		OpenAIFastPolicy:                req.OpenAIFastPolicy,
		RequireOAuthOnly:                req.RequireOAuthOnly,
		RequirePrivacySet:               req.RequirePrivacySet,
		DefaultMappedModel:              req.DefaultMappedModel,
		ModelsListConfig:                req.ModelsListConfig,
		AvailabilityProbeConfig:         req.AvailabilityProbeConfig,
		RPMLimit:                        req.RPMLimit,
		MaxReasoningEffort:              req.MaxReasoningEffort,
		MaxReasoningEffortOverLimit:     req.MaxReasoningEffortOverLimit,
		ReasoningEffortMappings:         req.ReasoningEffortMappings,
		CopyProvidersFromGroupIDs:       req.CopyProvidersFromGroupIDs,
	})
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, groupdto.AdminGroupFromRouting[struct{}](group))
}

// Delete deletes a group.
// DELETE /api/v1/admin/groups/:id
func (h *GroupHandler) Delete(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.BadRequest(c, "Invalid group ID")
		return
	}

	err = h.adminService.DeleteGroup(c.Request.Context(), groupID)
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{"message": "Group deleted successfully"})
}

// UpdateSortOrder updates group sort order in a batch.
// PUT /api/v1/admin/groups/sort-order
func (h *GroupHandler) UpdateSortOrder(c *gin.Context) {
	var req UpdateSortOrderRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	updates := make([]routing.GroupSortOrderUpdate, 0, len(req.Updates))
	for _, u := range req.Updates {
		updates = append(updates, routing.GroupSortOrderUpdate{
			ID:        u.ID,
			SortOrder: u.SortOrder,
		})
	}

	if err := h.adminService.UpdateGroupSortOrders(c.Request.Context(), updates); err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	httpx.Success(c, gin.H{"message": "Sort order updated successfully"})
}
