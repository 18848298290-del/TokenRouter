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

// Group administration accepts product and protocol policy independently of
// provider platforms. Strict JSON binding rejects retired fields, and the
// complete protocol selection takes precedence over a supplied legacy
// text-only selection. Optional update values distinguish retaining settings
// from replacing them with false, zero, or an empty collection.
//
// Duplicating a group also needs to recover a committed copy when HTTP
// idempotency state cannot report completion. The handler scopes the
// operation to the administrator, source group, and request key, then queries
// persisted duplication identity for in-progress or unavailable-store errors.
// A recovered group is returned with a recovery header, while a
// saved-response replay uses its replay header.

// CreateGroupRequest carries the product, routing, and compatibility inputs
// for creating a group.
type CreateGroupRequest struct {
	// Localization supplies editable display content; when omitted, GroupAdmin
	// builds source text from Name and Description at the initial content
	// revision.
	Localization  *locale.Update[routing.GroupCopy] `json:"localization"`            // localized display content input
	RoutingPolicy routing.GroupRoutingPolicy        `json:"routing_policy"`          // group model and feature policy
	Name          string                            `json:"name" binding:"required"` // group business name input
	Description   string                            `json:"description"`             // group description input
	// SchedulerType selects basic or advanced scheduling; an empty input is
	// normalized by GroupAdmin to the basic scheduler.
	SchedulerType string `json:"scheduler_type" binding:"omitempty,oneof=basic advanced"` // basic or advanced scheduler
	// AdvancedSchedulerOverrides supplies advanced scheduling values; absent
	// nested fields inherit corresponding gateway settings when the group uses
	// advanced scheduling.
	AdvancedSchedulerOverrides routing.GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"` // optional advanced scheduling values
	DisplayBrand               string                                  `json:"display_brand"`                // optional display brand input
	// SortOrder overrides automatic placement when supplied; omission lets
	// GroupAdmin choose the next display order within its creation transaction.
	SortOrder *int `json:"sort_order"` // manual group display order
	// RateMultiplier is passed to GroupAdmin, which requires a positive value
	// before storing the group or its provider bindings.
	RateMultiplier float64 `json:"rate_multiplier"` // user price scaling factor
	IsExclusive    bool    `json:"is_exclusive"`    // exclusive group membership flag
	// SessionIsolationEnabled selects rejection of designated sessions already
	// assigned to another group; GroupAdmin stores the selected group isolation
	// policy.
	SessionIsolationEnabled bool `json:"session_isolation_enabled"` // cross-group session isolation input

	// AllowImageGeneration and AllowBatchImageGeneration supply legacy
	// permissions when complete protocols are omitted or legacy protocols are
	// selected. GroupAdmin enables the legacy batch flag only when the image
	// flag is also true, then derives final mirrors from the accepted protocol
	// set.
	AllowImageGeneration      bool `json:"allow_image_generation"`       // legacy image permission input
	AllowBatchImageGeneration bool `json:"allow_batch_image_generation"` // legacy image-batch permission input

	ClaudeCodeOnly bool `json:"claude_code_only"` // Claude Code client restriction
	// FallbackGroupID selects an existing alternate for clients rejected by
	// ClaudeCodeOnly; GroupAdmin validates the supplied target before creating
	// the group.
	FallbackGroupID *int64 `json:"fallback_group_id"` // client restriction fallback group
	// FallbackGroupIDOnInvalidRequest selects an alternate for qualifying
	// invalid Anthropic requests; omitted or nonpositive values leave this
	// alternate unconfigured.
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"` // invalid Anthropic request fallback
	// UnavailableFallbackGroupID selects the preferred API-key alternate when
	// the group is unavailable; omitted or nonpositive values leave this
	// preference unconfigured.
	UnavailableFallbackGroupID *int64 `json:"unavailable_fallback_group_id"` // unavailable group fallback preference
	// ModelRouting and ModelRoutingEnabled supply a preferred-provider request.
	// GroupAdmin creation stores the map but leaves the stored switch false, so
	// these newly saved rules require a later enabling update.
	ModelRouting        map[string][]int64 `json:"model_routing"`         // model-pattern preferred provider lists
	ModelRoutingEnabled bool               `json:"model_routing_enabled"` // preferred provider routing switch
	// MCPXMLInject retains an explicit false value through its pointer;
	// GroupAdmin defaults this legacy setting to true when omitted.
	MCPXMLInject         *bool    `json:"mcp_xml_inject"`         // legacy Antigravity XML input
	SupportedModelScopes []string `json:"supported_model_scopes"` // legacy Antigravity family metadata
	// AllowedProtocols uses nil for GroupAdmin's creation defaults and an empty
	// array to disable client entries. LegacyAllowedClientProtocols is
	// considered when this complete field is absent.
	AllowedProtocols []protocol.ProtocolID `json:"allowed_protocols"` // complete client admission input
	// ProtocolFallbacks uses absent sources for automatic conversion, empty
	// target arrays for native-only execution, and ordered nonempty arrays to
	// restrict conversion choices.
	ProtocolFallbacks map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"` // ordered per-entry conversion restrictions
	// ResponsesImagePolicy is normalized by GroupAdmin; an empty value selects
	// inheritance from provider and global settings for Responses image
	// handling.
	ResponsesImagePolicy string `json:"responses_image_policy"` // Responses image tool policy
	// LegacyAllowedClientProtocols is selected when AllowedProtocols is absent;
	// a supplied complete protocol set takes precedence over this compatibility
	// input.
	LegacyAllowedClientProtocols []protocol.ProtocolID `json:"allowed_client_protocols"` // legacy text protocol input
	// AllowMessagesDispatch retains decoded compatibility input. GroupAdmin
	// creation derives the saved mirror from AllowedProtocols and does not apply
	// this boolean to Messages admission.
	AllowMessagesDispatch bool `json:"allow_messages_dispatch"` // legacy Messages permission input
	// AllowLive patches the Live entry during legacy protocol creation. Complete
	// AllowedProtocols input determines admission directly and overrides this
	// compatibility flag.
	AllowLive       bool `json:"allow_live"`        // legacy Live permission input
	ForceOpenAIFast bool `json:"force_openai_fast"` // legacy forced-priority input
	// OpenAIFastPolicy selects the named acceleration policy through GroupAdmin;
	// a nonblank named selection takes precedence over legacy ForceOpenAIFast
	// input.
	OpenAIFastPolicy *string `json:"openai_fast_policy"` // named OpenAI acceleration input

	// RequireOAuthOnly requests runtime candidates with OAuth or Setup Token
	// credentials. Provider-copy filtering excludes API Key records; runtime
	// qualification also rejects other credential types.
	RequireOAuthOnly bool `json:"require_oauth_only"` // OAuth or Setup Token requirement
	// RequirePrivacySet requests the provider privacy readiness check. OpenAI
	// and Antigravity use saved privacy flags, while other platforms pass that
	// check.
	RequirePrivacySet       bool                                 `json:"require_privacy_set"`       // provider privacy readiness requirement
	DefaultMappedModel      string                               `json:"default_mapped_model"`      // default mapped model input
	ModelsListConfig        routing.GroupModelsListConfig        `json:"models_list_config"`        // custom request-model catalog input
	AvailabilityProbeConfig routing.GroupAvailabilityProbeConfig `json:"availability_probe_config"` // active probe configuration input
	// RPMLimit supplies each user's allowance within this group. Nonpositive
	// values disable this group gate; user-group overrides take precedence, and
	// a positive user-wide RPM allowance still applies independently.
	RPMLimit int `json:"rpm_limit"` // group requests per minute
	// MaxReasoningEffort supplies the ceiling for effort in supported OpenAI or
	// Anthropic request fields; an empty input leaves that ceiling unconfigured.
	MaxReasoningEffort string `json:"max_reasoning_effort"` // requested reasoning effort ceiling
	// MaxReasoningEffortOverLimit selects downgrade or deny above the ceiling;
	// GroupAdmin normalizes an empty input to the downgrade action.
	MaxReasoningEffortOverLimit string `json:"max_reasoning_effort_over_limit"` // effort ceiling excess action
	// ReasoningEffortMappings supplies effort rewrites evaluated before the
	// ceiling, with optional exact, prefix, or suffix request-model conditions.
	ReasoningEffortMappings []routing.ReasoningEffortMapping `json:"reasoning_effort_mappings"` // model-scoped effort rewrite rules
	// CopyProvidersFromGroupIDs names existing source groups whose provider
	// associations are copied into the new group during the creation
	// transaction.
	CopyProvidersFromGroupIDs []int64 `json:"copy_providers_from_group_ids"` // source groups for provider bindings
}

// UpdateGroupRequest carries supplied group changes while optional fields
// retain omission information.
type UpdateGroupRequest struct {
	// Localization, RoutingPolicy, Description, SchedulerType,
	// AdvancedSchedulerOverrides, DisplayBrand, SortOrder, RateMultiplier, and
	// IsExclusive retain stored values when omitted or null. Supplied values are
	// passed to GroupAdmin for replacement and validation.
	Localization  *locale.Update[routing.GroupCopy] `json:"localization"`   // localized display content input
	RoutingPolicy *routing.GroupRoutingPolicy       `json:"routing_policy"` // group model and feature policy
	// Name replaces the stored business name when nonempty; an empty or omitted
	// string leaves that name unchanged in GroupAdmin.
	Name          string  `json:"name"`                                                    // group business name input
	Description   *string `json:"description"`                                             // group description input
	SchedulerType *string `json:"scheduler_type" binding:"omitempty,oneof=basic advanced"` // basic or advanced scheduler
	// AdvancedSchedulerOverrides retains the stored overrides when nil; an empty
	// object clears per-group overrides so advanced fields inherit gateway
	// settings.
	AdvancedSchedulerOverrides *routing.GroupAdvancedSchedulerOverrides `json:"advanced_scheduler_overrides"` // optional advanced scheduling values
	DisplayBrand               *string                                  `json:"display_brand"`                // optional display brand input
	SortOrder                  *int                                     `json:"sort_order"`                   // manual group display order
	// RateMultiplier is passed to GroupAdmin, which requires a positive value
	// before storing the group or its provider bindings.
	RateMultiplier *float64 `json:"rate_multiplier"` // user price scaling factor
	IsExclusive    *bool    `json:"is_exclusive"`    // exclusive group membership flag
	// SessionIsolationEnabled retains the target group's policy when nil. A
	// supplied true rejects designated sessions whose recorded first owner is
	// another group, and false permits that cross-group use.
	SessionIsolationEnabled *bool `json:"session_isolation_enabled"` // cross-group session isolation input
	// Status accepts active or inactive through JSON binding; an omitted or
	// empty string leaves the stored activation state unchanged.
	Status string `json:"status" binding:"omitempty,oneof=active inactive"` // active or inactive state patch

	// AllowImageGeneration and AllowBatchImageGeneration retain omitted
	// compatibility values. Legacy writes apply supplied flags to the protocol
	// selection; a false working image flag clears the batch mirror before the
	// batch patch, so enabling batches also requires image permission. Complete
	// AllowedProtocols input supplies the authoritative final mirrors.
	AllowImageGeneration      *bool `json:"allow_image_generation"`       // legacy image permission input
	AllowBatchImageGeneration *bool `json:"allow_batch_image_generation"` // legacy image-batch permission input

	// ClaudeCodeOnly retains the stored restriction when nil; a supplied false
	// clears the client restriction before the updated group is saved.
	ClaudeCodeOnly *bool `json:"claude_code_only"` // Claude Code client restriction
	// FallbackGroupID retains the stored alternate when nil; a positive ID
	// selects a validated group and zero or a negative ID clears it.
	FallbackGroupID *int64 `json:"fallback_group_id"` // client restriction fallback group
	// FallbackGroupIDOnInvalidRequest retains the stored alternate when nil; a
	// positive ID selects a validated group and zero or a negative ID clears it.
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"` // invalid Anthropic request fallback
	// UnavailableFallbackGroupID retains the stored alternate when nil; a
	// positive ID selects a validated group and zero or a negative ID clears it.
	UnavailableFallbackGroupID *int64 `json:"unavailable_fallback_group_id"` // unavailable group fallback preference
	// ModelRouting and ModelRoutingEnabled retain their respective settings
	// when omitted or null. A supplied map replaces all preferred-provider
	// rules, including an empty object that clears them; a supplied switch
	// controls whether exact or trailing-wildcard rules select ordered provider
	// IDs.
	ModelRouting        map[string][]int64 `json:"model_routing"`         // model-pattern preferred provider lists
	ModelRoutingEnabled *bool              `json:"model_routing_enabled"` // preferred provider routing switch
	// MCPXMLInject retains the stored legacy value when nil; a supplied boolean
	// replaces that retained configuration value during group administration.
	MCPXMLInject *bool `json:"mcp_xml_inject"` // legacy Antigravity XML input
	// SupportedModelScopes retains metadata when nil; a supplied empty array
	// clears the stored legacy Antigravity model-family selection.
	SupportedModelScopes *[]string `json:"supported_model_scopes"` // legacy Antigravity family metadata
	// AllowedProtocols retains the current selection when nil and replaces it
	// when supplied; an empty array disables every client entry controlled by
	// the group.
	AllowedProtocols *[]protocol.ProtocolID `json:"allowed_protocols"` // complete client admission input
	// ProtocolFallbacks retains the stored map when nil; a supplied map replaces
	// restrictions, with absent sources automatic and empty target arrays
	// native-only.
	ProtocolFallbacks map[protocol.ProtocolID][]protocol.ProtocolID `json:"protocol_fallbacks"` // ordered per-entry conversion restrictions
	// ResponsesImagePolicy retains the stored setting for an empty string;
	// supply inherit to select provider/global inheritance when clearing a prior
	// policy.
	ResponsesImagePolicy string `json:"responses_image_policy"` // Responses image tool policy
	// LegacyAllowedClientProtocols is selected when AllowedProtocols is absent;
	// a supplied complete protocol set takes precedence over this compatibility
	// input.
	LegacyAllowedClientProtocols *[]protocol.ProtocolID `json:"allowed_client_protocols"` // legacy text protocol input
	// AllowMessagesDispatch patches Messages only when both complete and legacy
	// protocol selections are absent. Nil retains the existing Messages entry.
	AllowMessagesDispatch *bool `json:"allow_messages_dispatch"` // legacy Messages permission input
	// AllowLive retains the stored mirror when nil; supplied values patch Live
	// only when complete protocols are omitted or a legacy text selection is
	// used.
	AllowLive *bool `json:"allow_live"` // legacy Live permission input
	// ForceOpenAIFast and OpenAIFastPolicy retain the stored policy when both
	// are nil. When at least one is supplied, a nonblank named policy wins. A
	// nil or blank named policy uses the supplied legacy boolean, treating an
	// absent boolean as false, which selects follow_request.
	ForceOpenAIFast  *bool   `json:"force_openai_fast"`  // legacy forced-priority input
	OpenAIFastPolicy *string `json:"openai_fast_policy"` // named OpenAI acceleration input

	// RequireOAuthOnly retains the stored restriction when nil; a supplied true
	// limits runtime candidates to OAuth or Setup Token credentials.
	RequireOAuthOnly *bool `json:"require_oauth_only"` // OAuth or Setup Token requirement
	// RequirePrivacySet retains the saved policy when nil. A supplied true
	// requires OpenAI and Antigravity privacy readiness; other platforms pass
	// that check.
	RequirePrivacySet *bool `json:"require_privacy_set"` // provider privacy readiness requirement
	// DefaultMappedModel retains the stored string when nil; a supplied empty
	// string clears this retained model setting during group administration.
	DefaultMappedModel *string `json:"default_mapped_model"` // default mapped model input
	// ModelsListConfig retains the stored custom list when nil. Supplied lists
	// are normalized, and an empty or disabled list leaves model candidates
	// unfiltered by this setting.
	ModelsListConfig *routing.GroupModelsListConfig `json:"models_list_config"` // custom request-model catalog input
	// AvailabilityProbeConfig retains stored probe settings when nil; a supplied
	// disabled configuration normalizes to a zero value, clearing its stored
	// probe details.
	AvailabilityProbeConfig *routing.GroupAvailabilityProbeConfig `json:"availability_probe_config"` // active probe configuration input
	// RPMLimit retains the stored per-user group allowance when nil. A
	// nonpositive value disables this group gate; user-group overrides take
	// precedence, and a positive user-wide allowance remains independently
	// applicable.
	RPMLimit *int `json:"rpm_limit"` // group requests per minute
	// MaxReasoningEffort retains the stored ceiling when nil; an empty string
	// clears the cap on effort from supported OpenAI or Anthropic request fields.
	MaxReasoningEffort *string `json:"max_reasoning_effort"` // requested reasoning effort ceiling
	// MaxReasoningEffortOverLimit retains the stored action when nil; an empty
	// string selects GroupAdmin's default downgrade action for excess effort.
	MaxReasoningEffortOverLimit *string `json:"max_reasoning_effort_over_limit"` // effort ceiling excess action
	// ReasoningEffortMappings retains stored rules when nil; a supplied empty
	// array clears them and a nonempty array replaces the entire mapping list.
	ReasoningEffortMappings *[]routing.ReasoningEffortMapping `json:"reasoning_effort_mappings"` // model-scoped effort rewrite rules
	// CopyProvidersFromGroupIDs replaces current provider bindings when nonempty
	// by copying associations from the named groups; an empty list leaves
	// current bindings unchanged.
	CopyProvidersFromGroupIDs []int64 `json:"copy_providers_from_group_ids"` // source groups for provider bindings
}

// UpdateSortOrderRequest carries a nonempty batch of group display-order
// changes.
type UpdateSortOrderRequest struct {
	Updates []struct { // submitted group order changes
		ID        int64 `json:"id" binding:"required"` // target group identifier
		SortOrder int   `json:"sort_order"`            // new group display order
	} `json:"updates" binding:"required,min=1"`
}

// GroupAdministration supplies persisted group administration and recoverable
// duplication operations to HTTP handlers.
type GroupAdministration interface {
	// ListGroups returns a filtered page and its total matching group count.
	// HTTP callers pass an empty platform because groups are
	// platform-independent, and supply requested status, search, exclusivity,
	// and sorting options.
	ListGroups(context.Context, int, int, string, string, string, *bool, string, string) ([]routing.Group, int64, error)
	// GetAllGroups returns the active groups available to administrator
	// selection queries.
	GetAllGroups(context.Context) ([]routing.Group, error)
	// GetAllGroupsIncludingInactive returns administrator choices including
	// active and disabled groups. GroupAdmin requests one 10,000-row page; the
	// PostgreSQL reader caps it at 1,000, so callers needing every group use
	// paginated reads.
	GetAllGroupsIncludingInactive(context.Context) ([]routing.Group, error)
	// GetGroup reads the persisted group identified by the supplied numeric ID.
	GetGroup(context.Context, int64) (*routing.Group, error)
	// GetGroupModelsListCandidates returns suggestions for a new group or
	// eligible request models for an existing one. Nonpositive IDs select
	// suggestions in GroupAdmin; HTTP rejects negative IDs and passes an empty
	// platform, while an existing custom list can restrict results.
	GetGroupModelsListCandidates(context.Context, int64, string) ([]string, error)
	// CreateGroup validates and persists product settings and requested provider
	// associations. Nil protocol input uses creation defaults; binding work and
	// group creation share the configured mutation transaction in GroupAdmin.
	CreateGroup(context.Context, *routing.CreateGroupInput) (*routing.Group, error)
	// UpdateGroup applies supplied fields to the identified stored group and its
	// associations. Optional values retain omissions; nonempty source-group
	// lists request provider-binding replacement together with the group
	// mutation.
	UpdateGroup(context.Context, int64, *routing.UpdateGroupInput) (*routing.Group, error)
	// DuplicateGroup returns a disabled configuration copy with the source
	// group's provider bindings. Actor scope and operation key identify a
	// committed copy for retry recovery; the service stores that identity with
	// the copy.
	DuplicateGroup(context.Context, int64, string, string) (*routing.Group, error)
	// RecoverDuplicateGroup finds a committed copy for source group,
	// administrator scope, and operation key. A nil result means recovery found
	// no copy; an empty operation key cannot identify a stored duplication.
	RecoverDuplicateGroup(context.Context, int64, string, string) (*routing.Group, error)
	// DeleteGroup removes the identified group and its cascading associations
	// through administration.
	DeleteGroup(context.Context, int64) error
	// UpdateGroupSortOrders persists supplied display-order changes for the
	// listed group IDs.
	UpdateGroupSortOrders(context.Context, []routing.GroupSortOrderUpdate) error
}

// GroupHandler binds group administration, linked-resource readers, and HTTP
// idempotency execution.
type GroupHandler struct {
	idempotencyhttp.Executor // shared HTTP idempotency executor

	adminService GroupAdministration // stored group administration operations
	resources    GroupResources      // injected linked-resource HTTP dependencies
}

// NewGroupHandler binds group administration and the first optional resource
// bundle. Omitted resources leave zero-valued dependencies, so callers supply
// each dependency used by registered routes and bind idempotency before
// serving writes.
func NewGroupHandler(admin GroupAdministration, resources ...GroupResources) *GroupHandler {
	handler := &GroupHandler{adminService: admin}
	if len(resources) > 0 {
		handler.resources = resources[0]
	}
	return handler
}

// List returns administrator group rows with standard pagination and
// requested filters. It rejects a supplied platform parameter, caps trimmed
// search by bytes, defaults sorting to ascending sort_order, and interprets
// nonempty is_exclusive as true only when spelled true.
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
	// Cap filter bytes so administrative search input stays bounded.
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

// GetAll returns administrator group choices through the active-group reader
// by default. include_inactive=true selects the broader administration
// reader, and any supplied platform parameter is rejected before reading
// groups.
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

// GetByID parses the route's numeric group ID and returns its administrator
// DTO. Parse failures become bad requests; administration determines whether
// the parsed ID identifies an available stored group.
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

// GetModelsListCandidates returns model choices for the route's group. Zero
// selects new-group suggestions, negative or malformed IDs return bad
// requests, and a supplied platform parameter is rejected because group model
// choices are platform-independent.
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

// Create strictly decodes a group creation request and returns its
// administrator DTO after administration succeeds. Product defaults,
// validation, provider associations, and persistence belong to GroupAdmin
// before the normalized settings are mapped for the response.
//
// If AllowedProtocols is absent and a legacy text selection is present, the
// handler passes that selection with LegacyProtocolInput so GroupAdmin can
// apply compatibility rules. A supplied complete selection, including an
// empty array, takes precedence.
func (h *GroupHandler) Create(c *gin.Context) {
	var req CreateGroupRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Complete protocol input takes precedence, including an empty array; select
	// legacy text input when the complete field is absent.
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

// Duplicate returns a disabled group copy identified by a positive source ID
// and the administrator's request scope. The idempotency executor can return
// a saved response; the service also stores duplication identity for the same
// source, actor, and key.
//
// When execution reports an in-progress operation or unavailable idempotency
// storage, the handler attempts to load the committed copy before returning
// that original error. A found copy produces X-Idempotency-Recovered, while
// ordinary response replay produces X-Idempotency-Replayed; recovery read
// failures are logged and the original execution error remains the response.
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
			GroupID int64 `json:"group_id"` // source group operation identity
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
		// A copy may be committed even when idempotency state cannot report
		// completion; use its persisted source/actor/key identity before returning
		// the executor error.
		if reason == infraerrors.Reason(idempotency.ErrIdempotencyInProgress) || reason == infraerrors.Reason(idempotency.ErrIdempotencyStoreUnavail) {
			recovered, recoverErr := h.adminService.RecoverDuplicateGroup(c.Request.Context(), groupID, actorScope, c.GetHeader("Idempotency-Key"))
			if recoverErr != nil {
				// A failed recovery read cannot confirm completion. Keep the executor's
				// original error so the client sees the unresolved idempotency state.
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

// Update strictly decodes supplied changes for the route's numeric group ID
// and returns its administrator DTO. Optional request values preserve
// unchanged settings, false values, and empty collections for GroupAdmin to
// interpret.
//
// A legacy text protocol selection is used when the complete protocol field
// is absent, and the compatibility marker lets GroupAdmin retain non-text
// entry permissions. Supplied complete protocol input takes precedence over
// the legacy selection.
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
	// Complete protocol input takes precedence, including an empty array; select
	// legacy text input when the complete field is absent.
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

// Delete parses the route's numeric group ID and asks administration to
// remove it. Parse failures become bad requests, administration failures use
// the standard error response, and successful removal returns the deletion
// message.
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

// UpdateSortOrder strictly decodes a nonempty list of group display-order
// changes. It forwards each ID and order in request order to administration
// and reports success after the batch update completes.
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
