package routing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	wireprotocol "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// group_admin.go validates and applies administrative group mutations and
// provider membership changes.
// Group settings and provider memberships form one administrative mutation when
// creation or replacement copying is requested. Validation and source-provider
// reads occur before the transaction callback, while saving the group and
// binding its providers use the callback's shared operation context.
//
// Automatic display positions read the current tail under an optional
// transaction lock, and explicitly supplied positions bypass allocation.
// Compatibility protocol inputs are normalized into one authoritative admission
// set before persistence. Successful updates request group authentication-cache
// invalidation; deletion captures bound Key strings before their associations
// disappear so those snapshots can be invalidated afterward. Shared pricing
// remains owned by its separate configuration service.

// GroupSortOrderStep leaves gaps between automatically allocated display
// positions so administrators can insert intermediate positions during manual
// ordering.
const GroupSortOrderStep = 10

// ListGroups returns the selected group page and its total match count.
func (s *GroupAdmin) ListGroups(ctx context.Context, page, pageSize int, platform, status, search string, isExclusive *bool, sortBy, sortOrder string) ([]Group, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	groups, result, err := s.groupRepo.ListWithFilters(ctx, params, platform, status, search, isExclusive)
	if err != nil {
		return nil, 0, err
	}
	return groups, result.Total, nil
}

// GetAllGroups returns active groups from the configured group repository.
func (s *GroupAdmin) GetAllGroups(ctx context.Context) ([]Group, error) {
	return s.groupRepo.ListActive(ctx)
}

// GetAllGroupsIncludingInactive requests the first unfiltered administrative
// page with a size of 10,000. The PostgreSQL repository applies
// pagination.Limit's 1,000-row ceiling, so this convenience method can return a
// truncated collection.
func (s *GroupAdmin) GetAllGroupsIncludingInactive(ctx context.Context) ([]Group, error) {
	// This view requests one page; the PostgreSQL pagination limit further caps
	// it, so groups beyond that page are omitted from the unpaginated response.
	groups, _, err := s.groupRepo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10000}, "", "", "", nil)
	return groups, err
}

// GetGroup loads the requested group with repository-provided derived counts.
func (s *GroupAdmin) GetGroup(ctx context.Context, id int64) (*Group, error) {
	return s.groupRepo.GetByID(ctx, id)
}

// GetGroupModelsListCandidates returns model suggestions for new groups and
// eligible request names for saved groups. Nonpositive IDs invoke
// DefaultModels, a missing provider reader yields an empty slice, and enabled
// custom lists intersect resolved candidates in administrator order. Policy
// failures and cancellation absorbed by RequestableResolver yield empty
// candidates with no administrative error after repository reads succeed.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_resolution
func (s *GroupAdmin) GetGroupModelsListCandidates(ctx context.Context, id int64, _ string) ([]string, error) {
	if id <= 0 {
		return s.options.DefaultModels(""), nil
	}
	group, err := s.groupRepo.GetByIDLite(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.providerRepo == nil {
		return []string{}, nil
	}
	providers, err := s.providerRepo.ListSchedulableByGroupID(ctx, id)
	if err != nil {
		return nil, err
	}
	// The resolver represents failed eligibility as a restricted empty catalog.
	// Consuming Models leaves unavailable editor choices empty after the group
	// and provider reads have succeeded.
	resolution := s.options.ModelResolver.ResolveWithProviders(ctx, &id, "", nil, providers)
	if resolution.Err != nil {
		return nil, resolution.Err
	}
	candidates := make([]string, 0, len(resolution.Models))
	for _, model := range resolution.Models {
		candidates = append(candidates, model.ID)
	}
	sort.Strings(candidates)
	if group.CustomModelsListEnabled() {
		candidates = FilterModelsListCandidates(candidates, group.ModelsListConfig.Models)
	}
	return candidates, nil
}

// SanitizeGroupOpenAIFast updates saved acceleration fields to their effective
// named policy. Nil groups are ignored; ForceOpenAIFast mirrors force_priority
// alone, while provider execution determines whether the policy applies to its
// route.
func SanitizeGroupOpenAIFast(group *Group) {
	if group == nil {
		return
	}
	group.OpenAIFastPolicy = group.EffectiveOpenAIFastPolicy()
	group.ForceOpenAIFast = group.OpenAIFastPolicy == GroupOpenAIFastPolicyForcePriority
}

// CreateGroup builds and saves an active group after validating routing,
// scheduler, effort, fallback, protocol, and probe settings. Callers supply a
// non-nil input and configure Mutate to complete the coordinated write through
// the transaction owner.
//
// Source groups and distinct provider IDs are read before entering that
// mutation. Within its operation context, automatic sorting acquires the
// optional ordering lock, creation saves the group, and provider binding
// follows it so binding failure can roll back creation. Legacy media input is
// applied after original protocol validation, and returned configuration uses
// the normalized working value.
func (s *GroupAdmin) CreateGroup(ctx context.Context, input *CreateGroupInput) (*Group, error) {
	if err := ValidateGroupRoutingPolicy(input.RoutingPolicy); err != nil {
		return nil, err
	}
	fastPolicy, policyErr := ResolveGroupOpenAIFastPolicyInput(input.OpenAIFastPolicy, input.ForceOpenAIFast)
	if policyErr != nil {
		return nil, policyErr
	}
	if input.RateMultiplier <= 0 {
		return nil, errors.New("rate_multiplier must be > 0")
	}

	platform := ""
	schedulerType, err := NormalizeGroupSchedulerType(input.SchedulerType)
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_SCHEDULER_TYPE", "%v", err)
	}
	if err := s.ValidateAdvancedOverrides(ctx, input.AdvancedSchedulerOverrides); err != nil {
		return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_ADVANCED_SCHEDULER_OVERRIDES", "%v", err)
	}
	allowedClientProtocols := input.AllowedProtocols
	if allowedClientProtocols == nil {
		allowedClientProtocols = capability.DefaultGroupClientProtocols(platform)
	}

	maxReasoningEffort, err := NormalizeMaxReasoningEffortForPlatform(PlatformOpenAI, input.MaxReasoningEffort)
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_MAX_REASONING_EFFORT", "%v", err)
	}
	maxReasoningEffortOverLimit, err := NormalizeMaxReasoningEffortOverLimitForPlatform(PlatformOpenAI, input.MaxReasoningEffortOverLimit)
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_MAX_REASONING_EFFORT_OVER_LIMIT", "%v", err)
	}
	reasoningEffortMappings, err := NormalizeReasoningEffortMappings(PlatformOpenAI, input.ReasoningEffortMappings)
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_REASONING_EFFORT_MAPPING", "%v", err)
	}

	if input.FallbackGroupID != nil {
		if err := s.ValidateFallbackGroup(ctx, 0, *input.FallbackGroupID); err != nil {
			return nil, err
		}
	}
	unavailableFallbackGroupID := input.UnavailableFallbackGroupID
	if unavailableFallbackGroupID != nil && *unavailableFallbackGroupID <= 0 {
		unavailableFallbackGroupID = nil
	}
	if unavailableFallbackGroupID != nil {
		if err := s.ValidateUnavailableFallbackGroup(ctx, 0, platform, *unavailableFallbackGroupID); err != nil {
			return nil, err
		}
	}
	fallbackOnInvalidRequest := input.FallbackGroupIDOnInvalidRequest
	if fallbackOnInvalidRequest != nil && *fallbackOnInvalidRequest <= 0 {
		fallbackOnInvalidRequest = nil
	}
	if fallbackOnInvalidRequest != nil {
		if err := s.ValidateFallbackGroupOnInvalidRequest(ctx, 0, platform, *fallbackOnInvalidRequest); err != nil {
			return nil, err
		}
	}

	mcpXMLInject := true
	if input.MCPXMLInject != nil {
		mcpXMLInject = *input.MCPXMLInject
	}

	allowImageGeneration := input.AllowImageGeneration
	allowBatchImageGeneration := input.AllowBatchImageGeneration && allowImageGeneration

	// Read source membership before opening the mutation so validation and
	// copy-read failures return before allocating or writing the destination
	// group.
	var providerIDsToCopy []int64
	if len(input.CopyProvidersFromGroupIDs) > 0 {
		seen := make(map[int64]struct{})
		uniqueSourceGroupIDs := make([]int64, 0, len(input.CopyProvidersFromGroupIDs))
		for _, srcGroupID := range input.CopyProvidersFromGroupIDs {
			if _, exists := seen[srcGroupID]; !exists {
				seen[srcGroupID] = struct{}{}
				uniqueSourceGroupIDs = append(uniqueSourceGroupIDs, srcGroupID)
			}
		}

		for _, srcGroupID := range uniqueSourceGroupIDs {
			_, err := s.groupRepo.GetByIDLite(ctx, srcGroupID)
			if err != nil {
				return nil, fmt.Errorf("source group %d not found: %w", srcGroupID, err)
			}
		}

		var err error
		providerIDsToCopy, err = s.groupRepo.GetProviderIDsByGroupIDs(ctx, uniqueSourceGroupIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to get providers from source groups: %w", err)
		}
	}
	availabilityProbeConfig, err := NormalizeGroupAvailabilityProbeConfigForAdminWrite(input.AvailabilityProbeConfig)
	if err != nil {
		return nil, err
	}

	sortOrder := 0
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	group := &Group{
		Name:        input.Name,
		Description: input.Description,

		SchedulerType:                   schedulerType,
		AdvancedSchedulerOverrides:      policy.CloneGroupAdvancedSchedulerOverrides(input.AdvancedSchedulerOverrides),
		DisplayBrand:                    strings.TrimSpace(input.DisplayBrand),
		SortOrder:                       sortOrder,
		RateMultiplier:                  input.RateMultiplier,
		IsExclusive:                     input.IsExclusive,
		SessionIsolationEnabled:         input.SessionIsolationEnabled,
		Status:                          StatusActive,
		RoutingPolicy:                   input.RoutingPolicy.Clone(),
		AllowImageGeneration:            allowImageGeneration,
		AllowBatchImageGeneration:       allowBatchImageGeneration,
		ClaudeCodeOnly:                  input.ClaudeCodeOnly,
		FallbackGroupID:                 input.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: fallbackOnInvalidRequest,
		UnavailableFallbackGroupID:      unavailableFallbackGroupID,
		ModelRouting:                    input.ModelRouting,
		MCPXMLInject:                    mcpXMLInject,
		SupportedModelScopes:            input.SupportedModelScopes,
		AllowedProtocols:                allowedClientProtocols,
		ProtocolFallbacks:               input.ProtocolFallbacks,
		ResponsesImagePolicy:            input.ResponsesImagePolicy,
		AllowLive:                       input.AllowLive,
		ForceOpenAIFast:                 input.ForceOpenAIFast,
		OpenAIFastPolicy:                fastPolicy,
		RequireOAuthOnly:                input.RequireOAuthOnly,
		RequirePrivacySet:               input.RequirePrivacySet,
		DefaultMappedModel:              input.DefaultMappedModel,
		ModelsListConfig:                NormalizeGroupModelsListConfig(input.ModelsListConfig),
		AvailabilityProbeConfig:         availabilityProbeConfig,
		RPMLimit:                        input.RPMLimit,
		MaxReasoningEffort:              maxReasoningEffort,
		MaxReasoningEffortOverLimit:     maxReasoningEffortOverLimit,
		ReasoningEffortMappings:         reasoningEffortMappings,
	}
	SanitizeGroupMessagesDispatchFields(group)
	SanitizeGroupOpenAIFast(group)
	SanitizeGroupReasoningEffortPolicy(group)
	if group.ProtocolFallbacks == nil {
		group.ProtocolFallbacks = capability.DefaultProtocolFallbacks(platform)
	}
	if input.Localization != nil {
		next, err := locale.Prepare(locale.Original(GroupCopy{DisplayName: group.Name, Description: group.Description}), *input.Localization, ValidateGroupCopy)
		if err != nil {
			return nil, err
		}
		group.Localization = GroupLocalization(next)
	} else {
		group.Localization = GroupLocalization(GroupContent(group))
	}
	var legacy *LegacyGroupProtocolPatch
	if input.AllowedProtocols == nil || input.LegacyProtocolInput {
		legacy = &LegacyGroupProtocolPatch{Image: &group.AllowImageGeneration, Batch: &group.AllowBatchImageGeneration, Live: &group.AllowLive}
	}
	if err := NormalizeGroupProtocolPolicy(group, legacy); err != nil {
		return nil, err
	}
	if err := ValidateGroupProbeProtocol(group); err != nil {
		return nil, err
	}

	// Filter copied memberships using credential type before binding; API-key
	// source records cannot be carried into a group configured for OAuth-only
	// admission.
	if group.RequireOAuthOnly && len(providerIDsToCopy) > 0 {
		providers, err := s.providerRepo.GetByIDs(ctx, providerIDsToCopy)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch providers for oauth filter: %w", err)
		}
		oauthIDs := make(map[int64]struct{}, len(providers))
		for _, acc := range providers {
			if acc.Type != capability.ProviderTypeAPIKey {
				oauthIDs[acc.ID] = struct{}{}
			}
		}
		var filtered []int64
		for _, aid := range providerIDsToCopy {
			if _, ok := oauthIDs[aid]; ok {
				filtered = append(filtered, aid)
			}
		}
		providerIDsToCopy = filtered
	}

	// Allocate the optional tail position, create the row, and bind copied
	// providers through the same mutation context so a later binding failure can
	// roll back creation.
	if err := s.options.Mutate(ctx, func(opCtx context.Context) error {
		if input.SortOrder == nil {
			resolvedSortOrder, err := s.NextGroupSortOrder(opCtx)
			if err != nil {
				return err
			}
			group.SortOrder = resolvedSortOrder
		}
		if err := s.groupRepo.Create(opCtx, group); err != nil {
			return err
		}
		if len(providerIDsToCopy) > 0 {
			if err := s.groupRepo.BindProvidersToGroup(opCtx, group.ID, providerIDsToCopy); err != nil {
				return fmt.Errorf("failed to bind providers to new group: %w", err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if len(providerIDsToCopy) > 0 {
		group.ProviderCount = int64(len(providerIDsToCopy))
	}

	return group, nil
}

// NextGroupSortOrder computes the display position following the current
// highest saved position. Call inside the creation transaction so an optional
// repository lock protects the tail read and subsequent insertion together.
//
// An empty repository starts at zero; other allocations add GroupSortOrderStep
// and reject detected integer wraparound. A nil sort-order repository leaves
// the read without that serialization lock, so callers requiring concurrent
// allocation protection provide the repository and the same operation context
// through creation.
func (s *GroupAdmin) NextGroupSortOrder(ctx context.Context) (int, error) {
	if s.groupSortOrderRepo != nil {
		if err := s.groupSortOrderRepo.LockGroupSortOrder(ctx); err != nil {
			return 0, fmt.Errorf("lock group sort order: %w", err)
		}
	}

	groups, _, err := s.groupRepo.ListWithFilters(
		ctx,
		pagination.PaginationParams{
			Page:      1,
			PageSize:  1,
			SortBy:    "sort_order",
			SortOrder: "desc",
		},
		"",
		"",
		"",
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("load last group sort order: %w", err)
	}
	if len(groups) == 0 {
		return 0, nil
	}

	// Leave fixed spacing after the saved tail while detecting signed integer
	// wraparound before an automatic position is returned to creation.
	next := groups[0].SortOrder + GroupSortOrderStep
	if next < groups[0].SortOrder {
		return 0, errors.New("group sort order overflow")
	}
	return next, nil
}

// ValidateFallbackGroup checks the configured client-restriction chain before
// it is saved. Pass zero for currentGroupID during creation; the immediate
// target must accept clients beyond Claude Code, and every reachable reference
// must exist.
//
// The visited-ID set detects repeated targets and a path that returns to the
// current group. Traversal follows saved FallbackGroupID links until a target
// has none, so validation considers the entire chain rather than only direct
// self-reference. Group activity and request-time permissions are checked by
// their consuming routing paths.
func (s *GroupAdmin) ValidateFallbackGroup(ctx context.Context, currentGroupID, fallbackGroupID int64) error {
	if currentGroupID > 0 && currentGroupID == fallbackGroupID {
		return fmt.Errorf("cannot set self as fallback group")
	}

	visited := map[int64]struct{}{}
	nextID := fallbackGroupID
	for {
		if _, seen := visited[nextID]; seen {
			return fmt.Errorf("fallback group cycle detected")
		}
		visited[nextID] = struct{}{}
		if currentGroupID > 0 && nextID == currentGroupID {
			return fmt.Errorf("fallback group cycle detected")
		}

		fallbackGroup, err := s.groupRepo.GetByIDLite(ctx, nextID)
		if err != nil {
			return fmt.Errorf("fallback group not found: %w", err)
		}

		if nextID == fallbackGroupID && fallbackGroup.ClaudeCodeOnly {
			return fmt.Errorf("fallback group cannot have claude_code_only enabled")
		}

		if fallbackGroup.FallbackGroupID == nil {
			return nil
		}
		nextID = *fallbackGroup.FallbackGroupID
	}
}

// ValidateFallbackGroupOnInvalidRequest checks an existing invalid-request
// target with no invalid-request fallback of its own. Pass zero for creation;
// self-reference is rejected for a saved group, and the retained platform
// parameter does not restrict the target.
func (s *GroupAdmin) ValidateFallbackGroupOnInvalidRequest(ctx context.Context, currentGroupID int64, platform string, fallbackGroupID int64) error {
	if currentGroupID > 0 && currentGroupID == fallbackGroupID {
		return fmt.Errorf("cannot set self as invalid request fallback group")
	}

	fallbackGroup, err := s.groupRepo.GetByIDLite(ctx, fallbackGroupID)
	if err != nil {
		return fmt.Errorf("fallback group not found: %w", err)
	}
	if fallbackGroup.FallbackGroupIDOnInvalidRequest != nil {
		return fmt.Errorf("fallback group cannot have invalid request fallback configured")
	}
	return nil
}

// UpdateGroup applies supplied settings to a loaded working group, validates
// the result, and saves it through Mutate. Omitted fields retain their
// configured values according to UpdateGroupInput, while explicit empty or
// nonpositive values can clear selected optional settings.
//
// Legacy text updates carry forward existing nontext entries before
// original-set validation and switch patching. Provider-copy sources are
// checked and read before the mutation; a nonempty source list triggers
// membership clearing and replacement even when its eligible ID set is empty.
// Settings and memberships use the same operation context, and authentication
// invalidation is requested after Mutate returns success. When Mutate reuses an
// enclosing transaction, that transaction's owner still controls completion.
//
// GetByID supplies the object mutated during preparation. Repositories provide
// a disposable working value because a later validation or persistence error
// leaves earlier in-memory edits applied.
func (s *GroupAdmin) UpdateGroup(ctx context.Context, id int64, input *UpdateGroupInput) (*Group, error) {
	group, err := s.groupRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if input.Localization != nil {
		current := GroupContent(group)
		next, err := locale.Prepare(current, *input.Localization, ValidateGroupCopy)
		if err != nil {
			return nil, err
		}
		group.Localization = GroupLocalization(next)
	}
	previousAllowedProtocols := group.EffectiveAllowedProtocols()

	if input.Name != "" {
		group.Name = input.Name
	}
	if input.Description != nil {
		group.Description = *input.Description
	}
	if input.SchedulerType != nil {
		schedulerType, normalizeErr := NormalizeGroupSchedulerType(*input.SchedulerType)
		if normalizeErr != nil {
			return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_SCHEDULER_TYPE", "%v", normalizeErr)
		}
		group.SchedulerType = schedulerType
	}
	if input.AdvancedSchedulerOverrides != nil {
		if validationErr := s.ValidateAdvancedOverrides(ctx, *input.AdvancedSchedulerOverrides); validationErr != nil {
			return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_ADVANCED_SCHEDULER_OVERRIDES", "%v", validationErr)
		}
		group.AdvancedSchedulerOverrides = policy.CloneGroupAdvancedSchedulerOverrides(*input.AdvancedSchedulerOverrides)
	}
	if input.AllowedProtocols != nil {
		group.AllowedProtocols = append([]wireprotocol.ProtocolID{}, *input.AllowedProtocols...)
	} else {
		group.AllowedProtocols = previousAllowedProtocols
	}
	if input.DisplayBrand != nil {
		group.DisplayBrand = strings.TrimSpace(*input.DisplayBrand)
	}
	if input.SortOrder != nil {
		group.SortOrder = *input.SortOrder
	}
	if input.RateMultiplier != nil {
		if *input.RateMultiplier <= 0 {
			return nil, errors.New("rate_multiplier must be > 0")
		}
		group.RateMultiplier = *input.RateMultiplier
	}
	if input.IsExclusive != nil {
		group.IsExclusive = *input.IsExclusive
	}
	if input.SessionIsolationEnabled != nil {
		group.SessionIsolationEnabled = *input.SessionIsolationEnabled
	}
	if input.Status != "" {
		group.Status = input.Status
	}
	if input.RoutingPolicy != nil {
		if err := ValidateGroupRoutingPolicy(*input.RoutingPolicy); err != nil {
			return nil, err
		}
		group.RoutingPolicy = input.RoutingPolicy.Clone()
	}
	if input.AllowImageGeneration != nil {
		group.AllowImageGeneration = *input.AllowImageGeneration
	}
	if input.AllowBatchImageGeneration != nil {
		group.AllowBatchImageGeneration = *input.AllowBatchImageGeneration
	}
	if !group.AllowImageGeneration {
		group.AllowBatchImageGeneration = false
	}
	if input.ClaudeCodeOnly != nil {
		group.ClaudeCodeOnly = *input.ClaudeCodeOnly
	}
	if input.FallbackGroupID != nil {
		if *input.FallbackGroupID > 0 {
			if err := s.ValidateFallbackGroup(ctx, id, *input.FallbackGroupID); err != nil {
				return nil, err
			}
			group.FallbackGroupID = input.FallbackGroupID
		} else {
			group.FallbackGroupID = nil
		}
	}
	fallbackOnInvalidRequest := group.FallbackGroupIDOnInvalidRequest
	if input.FallbackGroupIDOnInvalidRequest != nil {
		if *input.FallbackGroupIDOnInvalidRequest > 0 {
			fallbackOnInvalidRequest = input.FallbackGroupIDOnInvalidRequest
		} else {
			fallbackOnInvalidRequest = nil
		}
	}
	if fallbackOnInvalidRequest != nil {
		if err := s.ValidateFallbackGroupOnInvalidRequest(ctx, id, "", *fallbackOnInvalidRequest); err != nil {
			return nil, err
		}
	}
	group.FallbackGroupIDOnInvalidRequest = fallbackOnInvalidRequest
	unavailableFallbackGroupID := group.UnavailableFallbackGroupID
	if input.UnavailableFallbackGroupID != nil {
		if *input.UnavailableFallbackGroupID > 0 {
			unavailableFallbackGroupID = input.UnavailableFallbackGroupID
		} else {
			unavailableFallbackGroupID = nil
		}
	}
	if unavailableFallbackGroupID != nil {
		if err := s.ValidateUnavailableFallbackGroup(ctx, id, "", *unavailableFallbackGroupID); err != nil {
			return nil, err
		}
	}
	group.UnavailableFallbackGroupID = unavailableFallbackGroupID

	if input.ModelRouting != nil {
		group.ModelRouting = input.ModelRouting
	}
	if input.ModelRoutingEnabled != nil {
		group.ModelRoutingEnabled = *input.ModelRoutingEnabled
	}
	if input.MCPXMLInject != nil {
		group.MCPXMLInject = *input.MCPXMLInject
	}

	if input.SupportedModelScopes != nil {
		group.SupportedModelScopes = *input.SupportedModelScopes
	}

	if input.AllowLive != nil {
		group.AllowLive = *input.AllowLive
	}
	if input.OpenAIFastPolicy != nil || input.ForceOpenAIFast != nil {
		legacyForce := input.ForceOpenAIFast != nil && *input.ForceOpenAIFast
		policy, err := ResolveGroupOpenAIFastPolicyInput(input.OpenAIFastPolicy, legacyForce)
		if err != nil {
			return nil, err
		}
		group.OpenAIFastPolicy = policy
	}
	if input.RequireOAuthOnly != nil {
		group.RequireOAuthOnly = *input.RequireOAuthOnly
	}
	if input.RequirePrivacySet != nil {
		group.RequirePrivacySet = *input.RequirePrivacySet
	}
	if input.DefaultMappedModel != nil {
		group.DefaultMappedModel = *input.DefaultMappedModel
	}
	if input.ModelsListConfig != nil {
		group.ModelsListConfig = NormalizeGroupModelsListConfig(*input.ModelsListConfig)
	}
	if input.AvailabilityProbeConfig != nil {
		config, err := NormalizeGroupAvailabilityProbeConfigForAdminWrite(*input.AvailabilityProbeConfig)
		if err != nil {
			return nil, err
		}
		group.AvailabilityProbeConfig = config
	}
	if input.RPMLimit != nil {
		group.RPMLimit = *input.RPMLimit
	}
	if input.MaxReasoningEffort != nil {
		maxReasoningEffort, err := NormalizeMaxReasoningEffortForPlatform(PlatformOpenAI, *input.MaxReasoningEffort)
		if err != nil {
			return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_MAX_REASONING_EFFORT", "%v", err)
		}
		group.MaxReasoningEffort = maxReasoningEffort
	}
	if input.MaxReasoningEffortOverLimit != nil {
		maxReasoningEffortOverLimit, err := NormalizeMaxReasoningEffortOverLimitForPlatform(PlatformOpenAI, *input.MaxReasoningEffortOverLimit)
		if err != nil {
			return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_MAX_REASONING_EFFORT_OVER_LIMIT", "%v", err)
		}
		group.MaxReasoningEffortOverLimit = maxReasoningEffortOverLimit
	}
	if input.ReasoningEffortMappings != nil {
		reasoningEffortMappings, err := NormalizeReasoningEffortMappings(PlatformOpenAI, *input.ReasoningEffortMappings)
		if err != nil {
			return nil, infraerrors.Newf(infraerrors.Category(400), "INVALID_REASONING_EFFORT_MAPPING", "%v", err)
		}
		group.ReasoningEffortMappings = reasoningEffortMappings
	}
	SanitizeGroupMessagesDispatchFields(group)
	SanitizeGroupOpenAIFast(group)
	SanitizeGroupReasoningEffortPolicy(group)
	// A legacy text-only selection cannot express media entries, so carry forward
	// existing nontext protocols before applying the supplied compatibility
	// switches and validation.
	if input.LegacyProtocolInput {
		for _, protocol := range previousAllowedProtocols {
			if protocol != wireprotocol.ProtocolAnthropicMessages && protocol != wireprotocol.ProtocolOpenAIResponses && protocol != wireprotocol.ProtocolOpenAIChatCompletions && protocol != wireprotocol.ProtocolGeminiGenerateContent {
				group.AllowedProtocols = append(group.AllowedProtocols, protocol)
			}
		}
	}
	if input.ProtocolFallbacks != nil {
		group.ProtocolFallbacks = input.ProtocolFallbacks
	}
	if input.ResponsesImagePolicy != "" {
		group.ResponsesImagePolicy = input.ResponsesImagePolicy
	}
	var legacy *LegacyGroupProtocolPatch
	if input.AllowedProtocols == nil || input.LegacyProtocolInput {
		legacy = &LegacyGroupProtocolPatch{Image: input.AllowImageGeneration, Live: input.AllowLive}
		if input.AllowBatchImageGeneration != nil {
			legacy.Batch = &group.AllowBatchImageGeneration
		}
		if input.AllowedProtocols == nil {
			legacy.Messages = input.AllowMessagesDispatch
		}
	}
	if err := NormalizeGroupProtocolPolicy(group, legacy); err != nil {
		return nil, err
	}
	if err := ValidateGroupProbeProtocol(group); err != nil {
		return nil, err
	}

	// A nonempty source-group request replaces memberships even when copied
	// eligibility is empty, so the mutation clears current bindings before
	// optionally inserting the selected IDs.
	var providerIDsToCopy []int64
	if len(input.CopyProvidersFromGroupIDs) > 0 {
		seen := make(map[int64]struct{})
		uniqueSourceGroupIDs := make([]int64, 0, len(input.CopyProvidersFromGroupIDs))
		for _, srcGroupID := range input.CopyProvidersFromGroupIDs {
			if srcGroupID == id {
				return nil, fmt.Errorf("cannot copy providers from self")
			}
			if _, exists := seen[srcGroupID]; !exists {
				seen[srcGroupID] = struct{}{}
				uniqueSourceGroupIDs = append(uniqueSourceGroupIDs, srcGroupID)
			}
		}

		for _, srcGroupID := range uniqueSourceGroupIDs {
			_, err := s.groupRepo.GetByIDLite(ctx, srcGroupID)
			if err != nil {
				return nil, fmt.Errorf("source group %d not found: %w", srcGroupID, err)
			}
		}

		providerIDsToCopy, err = s.groupRepo.GetProviderIDsByGroupIDs(ctx, uniqueSourceGroupIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to get providers from source groups: %w", err)
		}

		// Apply the destination's authentication restriction to source IDs before
		// replacement so API-key memberships are excluded even when the source group
		// allowed them.
		if group.RequireOAuthOnly && len(providerIDsToCopy) > 0 {
			providers, err := s.providerRepo.GetByIDs(ctx, providerIDsToCopy)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch providers for oauth filter: %w", err)
			}
			oauthIDs := make(map[int64]struct{}, len(providers))
			for _, acc := range providers {
				if acc.Type != capability.ProviderTypeAPIKey {
					oauthIDs[acc.ID] = struct{}{}
				}
			}
			var filtered []int64
			for _, aid := range providerIDsToCopy {
				if _, ok := oauthIDs[aid]; ok {
					filtered = append(filtered, aid)
				}
			}
			providerIDsToCopy = filtered
		}
	}

	// Save settings before clearing and rebinding on the same mutation context.
	// Returning a membership error lets the transaction owner roll back all
	// participant writes together.
	if err := s.options.Mutate(ctx, func(opCtx context.Context) error {
		if err := s.groupRepo.Update(opCtx, group); err != nil {
			return err
		}
		if len(input.CopyProvidersFromGroupIDs) > 0 {
			if _, err := s.groupRepo.DeleteProviderGroupsByGroupID(opCtx, id); err != nil {
				return fmt.Errorf("failed to clear existing provider bindings: %w", err)
			}
			if len(providerIDsToCopy) > 0 {
				if err := s.groupRepo.BindProvidersToGroup(opCtx, id, providerIDsToCopy); err != nil {
					return fmt.Errorf("failed to bind providers to group: %w", err)
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Invalidation follows the successful mutation callback. Later Key reads can
	// load its changed settings once the transaction owner has committed the
	// participant writes.
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, id)
	}

	return group, nil
}

// DeleteGroup removes a group through repository cascade cleanup and requests
// invalidation of its previously bound Keys. A configured authentication
// invalidator requires a Key reader before deletion begins.
//
// Bound Key strings are read first because their group associations may
// disappear during cascade cleanup. A failed Key read is tolerated and leaves
// no captured Keys, while cascade failure returns before invalidation. After
// successful removal, each captured string is used to expire its authentication
// snapshot; the repository's returned user IDs are not used by this operation.
func (s *GroupAdmin) DeleteGroup(ctx context.Context, id int64) error {
	var groupKeys []string
	if s.authCacheInvalidator != nil {
		keys, err := s.apiKeyRepo.ListKeysByGroupID(ctx, id)
		// A Key-list failure leaves deletion available, but only successfully
		// captured strings can be individually invalidated after their group bindings
		// disappear.
		if err == nil {
			groupKeys = keys
		}
	}

	// Credential eviction follows a successful cascade result. Returning a
	// cascade error skips that phase, leaving the caller to resolve the storage
	// outcome before another deletion attempt.
	_, err := s.groupRepo.DeleteCascade(ctx, id)
	if err != nil {
		return err
	}
	if s.authCacheInvalidator != nil {
		for _, key := range groupKeys {
			s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, key)
		}
	}

	return nil
}

// UpdateGroupSortOrders forwards administrative display positions to the group
// repository.
func (s *GroupAdmin) UpdateGroupSortOrders(ctx context.Context, updates []GroupSortOrderUpdate) error {
	return s.groupRepo.UpdateSortOrders(ctx, updates)
}
