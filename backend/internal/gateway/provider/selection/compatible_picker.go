package selection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

const (
	openAIProviderScheduleLayerLoadBalance    = "load_balance"
	openAIProviderScheduleLayerGuardianParent = "guardian_parent"
)

type openAIProviderSchedulerMetrics struct {
	schedulercore.PlatformMetrics
}

type compatiblePicker struct {
	service       *Compatible
	metrics       openAIProviderSchedulerMetrics
	stats         *schedulercore.RuntimeStats
	freeQuotaGate atomic.Pointer[providercore.FreeQuotaGate]
}

// pickerEngine 包含候选评分、计数和反馈操作。
type pickerEngine interface {
	Select(context.Context, schedulercore.PlatformSelectionInput) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error)
	ReportResult(int64, bool, *int, ...policy.FeedbackConfig)
	ReportSwitch()
	SnapshotMetrics() schedulercore.PlatformMetricsSnapshot
}

func (m *openAIProviderSchedulerMetrics) recordSwitch() {
	if m != nil {
		m.RecordSwitch()
	}
}

func newDefaultOpenAIProviderScheduler(service *Compatible, stats *schedulercore.RuntimeStats) pickerEngine {
	if stats == nil {
		stats = schedulercore.NewRuntimeStats(time.Now)
	}
	return &compatiblePicker{
		service: service,
		stats:   stats,
	}
}

func (s *compatiblePicker) Select(ctx context.Context, req schedulercore.PlatformSelectionInput) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	core, scope := s.platformSelector()
	v, decision, err := core.Select(ctx, cloneSelectionInput(req))
	return scope.restore(v), decision, err
}

// hasOpenAIProviderGroupMetadata 判断提供商是否声明了分组归属信息。
func hasOpenAIProviderGroupMetadata(provider *gatewayprovider.ExecutionProvider) bool {
	return provider != nil && (len(provider.Record.GroupIDs) > 0 || len(provider.Record.ProviderGroups) > 0)
}

// openAIStickyProviderMatchesGroup 校验粘性会话提供商是否仍属于当前请求分组。
func openAIStickyProviderMatchesGroup(provider *gatewayprovider.ExecutionProvider, groupID *int64) bool {
	if provider == nil {
		return false
	}
	if groupID == nil || *groupID <= 0 {
		return false
	}
	if slices.Contains(provider.Record.GroupIDs, *groupID) {
		return true
	}
	for _, providerGroup := range provider.Record.ProviderGroups {
		if providerGroup.GroupID == *groupID {
			return true
		}
	}
	return false
}

func (s *compatiblePicker) isProviderTransportCompatible(provider *gatewayprovider.ExecutionProvider, requiredTransport egress.OpenAIUpstreamTransport) bool {
	if requiredTransport == egress.OpenAIUpstreamTransportAny || requiredTransport == egress.OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || s.service == nil {
		return false
	}
	return s.service.isOpenAIProviderTransportCompatible(provider, requiredTransport)
}

func (s *compatiblePicker) lookupShadowParentProvider(ctx context.Context, id int64) *gatewayprovider.ExecutionProvider {
	if s == nil || s.service == nil {
		return nil
	}
	if s.service.schedulerSnapshot != nil {
		if provider, err := readSnapshotProvider(ctx, s.service.schedulerSnapshot, id); err == nil && provider != nil {
			return provider
		}
	}
	if s.service.providerRepo == nil {
		return nil
	}
	provider, _ := s.service.providerRepo.GetByID(ctx, id)
	return provider
}

// isProviderRequestCompatibleReason 返回提供商是否兼容，并在拒绝时标明具体门禁原因。
func (s *compatiblePicker) isProviderRequestCompatibleReason(ctx context.Context, provider *gatewayprovider.ExecutionProvider, req schedulercore.PlatformSelectionInput) (bool, string) {
	if s != nil && s.service != nil && !s.service.shadowProtocolsAllowed(ctx, provider) {
		return false, "parent_protocol_unavailable"
	}
	if !gatewayprovider.ExecutionModelPolicy(provider).AllowsProtocol(ctx) {
		return false, "protocol_unavailable"
	}
	if provider == nil {
		return false, "provider_nil"
	}
	if reason := s.service.candidateEligibilityReason(ctx, provider, req.Platform, requestRoutingModel(req), req.RequireCompact, req.RequiredCapability); reason != "" {
		return false, reason
	}
	if req.RequirePrivacySet && !provider.View().IsPrivacySet() {
		return false, "privacy_not_set"
	}
	if s != nil && s.service != nil && s.service.isOpenAIProviderRequestRuntimeBlocked(provider, requestRoutingModel(req)) {
		return false, "runtime_blocked"
	}
	if s != nil && s.service != nil && s.service.isOpenAIProxyStreamQuarantined(ctx, provider) {
		return false, "proxy_stream_quarantined"
	}

	if paused, decision := gatewayprovider.OpenAIQuotaPause(ctx, provider); paused {
		reason := "quota_auto_pause"
		if decision.Window != "" {
			reason += "_" + decision.Window
		}
		return false, reason
	}

	if !providercore.ParentHealthyForShadow(gatewayprovider.ExecutionRecord(provider), func(id int64) *providercore.Record {
		return gatewayprovider.ExecutionRecord(s.lookupShadowParentProvider(ctx, id))
	}) {
		return false, "shadow_parent_unhealthy"
	}
	if !gatewayprovider.ExecutionModelPolicy(provider).SupportsCompatibleRouting(ctx, requestRoutingModel(req)) {
		return false, "model_not_supported"
	}
	if req.GroupID != nil && s != nil && s.service != nil &&
		s.service.NeedsUpstreamGroupRestriction(ctx, req.GroupID) &&
		s.service.UpstreamRoutingModelRestricted(ctx, *req.GroupID, provider, requestRoutingModel(req), req.RequireCompact) {
		return false, "group_upstream_restricted"
	}
	if !providerSupportsOpenAICapabilities(ctx, provider, req.RequiredCapability, req.RequiredImageCapability) {
		return false, "capability_mismatch"
	}
	return true, ""
}

func (s *compatiblePicker) ReportResult(providerID int64, success bool, firstTokenMs *int, feedback ...policy.FeedbackConfig) {
	if s == nil || s.stats == nil {
		return
	}
	s.stats.Report(providerID, success, firstTokenMs, feedback...)
}

func (s *compatiblePicker) ReportSwitch() {
	if s == nil {
		return
	}
	s.metrics.recordSwitch()
}

func (s *compatiblePicker) SnapshotMetrics() schedulercore.PlatformMetricsSnapshot {
	if s == nil {
		return schedulercore.PlatformMetricsSnapshot{}
	}
	return s.metrics.Snapshot(s.stats.Size())
}

// groupUsesAdvancedScheduler 只依据最终解析后的分组决定调度模式。
func (s *Compatible) groupUsesAdvancedScheduler(ctx context.Context, groupID *int64) bool {
	if s == nil || groupID == nil || *groupID <= 0 {
		return false
	}
	if group, ok := requeststate.GroupFromContext(ctx); ok && routing.IsGroupContextValid(group) && group.ID == *groupID {
		return group.UsesAdvancedScheduler()
	}
	if s.schedulerSnapshot == nil {
		return false
	}
	group, err := s.readSchedulingGroup(ctx, *groupID)
	return err == nil && group != nil && group.UsesAdvancedScheduler()
}

func (s *Compatible) getOpenAIProviderScheduler(ctx context.Context, groupID *int64) pickerEngine {
	if s == nil {
		return nil
	}
	if !s.groupUsesAdvancedScheduler(ctx, groupID) {
		return nil
	}
	s.pickerOnce.Do(func() {
		if s.openaiProviderStats == nil {
			s.openaiProviderStats = schedulercore.NewRuntimeStats(time.Now)
		}
		if s.picker == nil {
			s.picker = newDefaultOpenAIProviderScheduler(s, s.openaiProviderStats)
		}
	})
	return s.picker
}

// ensureOpenAIProviderScheduler 仅供结果统计和诊断初始化调度器实例。
// 实际提供商选择仍必须经 getOpenAIProviderScheduler 按分组模式门控。
func (s *Compatible) ensureOpenAIProviderScheduler() pickerEngine {
	if s == nil {
		return nil
	}
	s.pickerOnce.Do(func() {
		if s.openaiProviderStats == nil {
			s.openaiProviderStats = schedulercore.NewRuntimeStats(time.Now)
		}
		if s.picker == nil {
			s.picker = newDefaultOpenAIProviderScheduler(s, s.openaiProviderStats)
		}
	})
	return s.picker
}

func (s *Compatible) SelectProviderWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport egress.OpenAIUpstreamTransport,
	requireCompact bool,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	return s.selectProviderWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, "", "", requireCompact, "", false)
}

// SelectProviderWithSchedulerForCapability 按能力要求调度提供商。
// previousResponseCanMove 表示首包 input 可自行重建工具续链，previous_response_id 允许跨提供商迁移
// （开启粘性加权时，绑定提供商获得评分加成）。
func (s *Compatible) SelectProviderWithSchedulerForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport egress.OpenAIUpstreamTransport,
	requiredCapability providercore.OpenAIEndpointCapability,
	requireCompact bool,
	previousResponseCanMove bool,
	platformOverride ...string,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	platform := ""
	if len(platformOverride) > 0 {
		platform = platformOverride[0]
	}
	return s.selectProviderWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, "", requireCompact, platform, previousResponseCanMove)
}

// SelectProviderWithSchedulerForCapabilityAndRoutingModel 同时保留客户端模型 R 与已解析的提供商层模型。
// 该入口供 /v1/messages 使用，使分组白名单按 R/C 检查，提供商能力与提供商映射按 D 检查。
func (s *Compatible) SelectProviderWithSchedulerForCapabilityAndRoutingModel(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	routingModel string,
	excludedIDs map[int64]struct{},
	requiredTransport egress.OpenAIUpstreamTransport,
	requiredCapability providercore.OpenAIEndpointCapability,
	requireCompact bool,
	previousResponseCanMove bool,
	platformOverride ...string,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	platform := ""
	if len(platformOverride) > 0 {
		platform = platformOverride[0]
	}
	routingModel = strings.TrimSpace(routingModel)
	if routingModel == "" {
		routingModel = s.resolveGroupRoutingModel(ctx, groupID, requestedModel)
	}
	return s.selectProviderWithSchedulerForRouting(ctx, groupID, previousResponseID, sessionHash, requestedModel, routingModel, excludedIDs, requiredTransport, requiredCapability, "", requireCompact, platform, previousResponseCanMove)
}

func (s *Compatible) SelectProviderWithSchedulerForImages(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability providercore.OpenAIImagesCapability,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	ctx = context.WithValue(ctx, imageModelRequiredKey{}, true)
	selection, decision, err := s.selectProviderWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, egress.OpenAIUpstreamTransportHTTPSSE, "", requiredCapability, false, "", false)
	if err == nil && selection != nil && selection.Provider != nil {
		return selection, decision, nil
	}

	if requiredCapability == providercore.OpenAIImagesCapabilityNative {
		return s.selectProviderWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, egress.OpenAIUpstreamTransportHTTPSSE, "", providercore.OpenAIImagesCapabilityBasic, false, "", false)
	}
	return selection, decision, err
}

func (s *Compatible) selectProviderWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport egress.OpenAIUpstreamTransport,
	requiredCapability providercore.OpenAIEndpointCapability,
	requiredImageCapability providercore.OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	routingModel := s.resolveGroupRoutingModel(ctx, groupID, requestedModel)
	return s.selectProviderWithSchedulerForRouting(ctx, groupID, previousResponseID, sessionHash, requestedModel, routingModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove)
}

// selectProviderWithSchedulerForRouting 首次调度仍把隔离代理当作不可用；只有因容量耗尽
// 失败且熔断器确实在隔离代理时，才用同一提供商层模型重跑一次并忽略隔离。这样健康代理
// 始终优先，同时避免共享代理场景被熔断器清空全部容量。
func (s *Compatible) selectProviderWithSchedulerForRouting(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	routingModel string,
	excludedIDs map[int64]struct{},
	requiredTransport egress.OpenAIUpstreamTransport,
	requiredCapability providercore.OpenAIEndpointCapability,
	requiredImageCapability providercore.OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	if forced, ok := apikey.ForcePlatformFromContext(ctx); ok && strings.TrimSpace(forced) != "" {
		platform = forced
	}
	originalGroupID := derefGroupID(groupID)
	resolvedCtx, resolvedGroupID, err := s.resolveOpenAISchedulerGroup(ctx, groupID)
	if err != nil {
		return nil, schedulercore.PlatformDecision{}, err
	}
	ctx = resolvedCtx
	groupID = resolvedGroupID
	if derefGroupID(groupID) != originalGroupID {
		routingModel = s.resolveGroupRoutingModel(ctx, groupID, requestedModel)
	}
	// 模型优先提供商在当前组内先尝试；没有可用候选时再使用完整池。
	if _, configured := ctx.Value(preferredProvidersKey{}).(map[int64]struct{}); !configured && previousResponseID == "" {
		if group, ok := requeststate.GroupFromContext(ctx); ok && group != nil {
			ids := group.GetRoutingProviderIDs(routingModel)
			if len(ids) > 0 {
				preferred := make(map[int64]struct{}, len(ids))
				for _, id := range ids {
					preferred[id] = struct{}{}
				}
				result, decision, preferredErr := s.selectProviderWithSchedulerForRouting(context.WithValue(ctx, preferredProvidersKey{}, preferred), groupID, previousResponseID, sessionHash, requestedModel, routingModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove)
				if preferredErr == nil {
					return result, decision, preferredErr
				}
			}
		}
	}
	excludedIDs = cloneExcludedProviderIDs(excludedIDs)
	var selection *gatewayprovider.SelectionResult
	var decision schedulercore.PlatformDecision
	for {
		selection, decision, err = s.selectProviderWithSchedulerForRoutingOnce(ctx, groupID, previousResponseID, sessionHash, requestedModel, routingModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove)
		if err != nil || selection == nil || selection.Provider == nil || s.generic == nil || s.generic.checkAndRegisterSession(ctx, selection.Provider, sessionHash) {
			break
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		if excludedIDs == nil {
			excludedIDs = map[int64]struct{}{}
		}
		excludedIDs[selection.Provider.Record.ID] = struct{}{}
	}

	if err == nil || openAIProxyStreamQuarantineBypassed(ctx) {
		return selection, decision, err
	}
	if !errors.Is(err, schedulercore.ErrNoAvailableProviders) && !errors.Is(err, schedulercore.ErrNoAvailableCompactProviders) {
		return selection, decision, err
	}

	if platform != "" && strings.TrimSpace(platform) != capability.PlatformOpenAI {
		return selection, decision, err
	}
	blocked := s.getOpenAIProxyStreamCircuit().ActiveBlockCount(time.Now())
	if blocked == 0 {
		return selection, decision, err
	}
	s.logOpenAIProxyStreamQuarantineFailOpen(requestedModel, blocked)
	return s.selectProviderWithSchedulerForRouting(withOpenAIProxyStreamQuarantineBypass(ctx), groupID, previousResponseID, sessionHash, requestedModel, routingModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove)
}

// resolveOpenAISchedulerGroup 复核入口已经授权的分组，保持计划与提供商池一致。
func (s *Compatible) resolveOpenAISchedulerGroup(ctx context.Context, groupID *int64) (context.Context, *int64, error) {
	var read func(context.Context, int64) (*routing.Group, error)
	if s != nil && s.schedulingGroups != nil {
		read = s.readSchedulingGroup
	}
	group, err := currentSelectionGroup(ctx, groupID, read)
	if err != nil {
		return ctx, nil, err
	}
	if group != nil {
		ctx = requeststate.WithGroup(ctx, group)
	}
	return ctx, groupID, nil
}

// withOpenAIGroupPrivacyRequirement 在一次调度请求内缓存分组隐私资格，避免重试重复查询。
func (s *Compatible) withOpenAIGroupPrivacyRequirement(ctx context.Context, groupID *int64) context.Context {
	// 父 context 为 nil 时 panic。
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	hints := requeststate.ExecutionHintsFromContext(ctx)
	hints.GroupPrivacyGroupID = derefGroupID(groupID)
	hints.GroupPrivacyRequirement = requeststate.Hint[bool]{Set: true, Value: s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID)}
	return requeststate.WithExecutionHints(ctx, hints)
}

func (s *Compatible) openAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	// 父 context 为 nil 时 panic。
	if ctx == nil {
		panic("nil context")
	}
	hints := requeststate.ExecutionHintsFromContext(ctx)
	if hints.GroupPrivacyRequirement.Set && hints.GroupPrivacyGroupID == derefGroupID(groupID) {
		return hints.GroupPrivacyRequirement.Value
	}
	return s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID)
}

func (s *Compatible) loadOpenAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if s == nil || groupID == nil || s.schedulerSnapshot == nil {
		return false
	}
	group, err := s.readSchedulingGroup(ctx, *groupID)
	if err != nil {
		return true
	}
	return group != nil && group.RequirePrivacySet
}

func (s *Compatible) selectProviderWithSchedulerForRoutingOnce(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	routingModel string,
	excludedIDs map[int64]struct{},
	requiredTransport egress.OpenAIUpstreamTransport,
	requiredCapability providercore.OpenAIEndpointCapability,
	requiredImageCapability providercore.OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
) (*gatewayprovider.SelectionResult, schedulercore.PlatformDecision, error) {
	ctx = s.withCandidatePolicy(ctx, groupID, sessionHash)
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	platform = strings.TrimSpace(platform)
	decision := schedulercore.PlatformDecision{}
	preserveGuardianParentBinding := requeststate.PreserveGuardianParentBinding(ctx, sessionHash)
	guardianParentProviderID := int64(0)
	if strings.TrimSpace(previousResponseID) == "" {
		guardianParentProviderID = s.resolveOpenAIGuardianParentProviderID(ctx, groupID)
	}
	scheduler := s.getOpenAIProviderScheduler(ctx, groupID)
	if scheduler == nil {
		decision.Layer = openAIProviderScheduleLayerLoadBalance
		if guardianParentProviderID > 0 {
			fallbackScheduler := &compatiblePicker{service: s, stats: schedulercore.NewRuntimeStats(time.Now)}
			selection, _, err := fallbackScheduler.selectBySessionHash(ctx, schedulercore.PlatformSelectionInput{
				GroupID:                 groupID,
				Platform:                platform,
				SessionHash:             sessionHash,
				StickyProviderID:        guardianParentProviderID,
				PreserveStickyBinding:   true,
				RequestedModel:          requestedModel,
				RoutingModel:            routingModel,
				RequiredTransport:       string(requiredTransport),
				RequiredCapability:      requiredCapability,
				RequiredImageCapability: requiredImageCapability,
				RequireCompact:          requireCompact,
				RequirePrivacySet:       s.openAIGroupRequiresPrivacySet(ctx, groupID),
				ExcludedIDs:             excludedIDs,
			})
			if err != nil {
				return nil, decision, err
			}
			if selection != nil && selection.Provider != nil {
				decision.Layer = openAIProviderScheduleLayerGuardianParent
				decision.StickySessionHit = true
				decision.SelectedProviderID = selection.Provider.Record.ID
				decision.SelectedProviderType = selection.Provider.Record.Type
				return selection, decision, nil
			}
		}
		legacySessionHash := sessionHash
		if preserveGuardianParentBinding {
			legacySessionHash = ""
		}
		effectiveExcludedIDs := cloneExcludedProviderIDs(excludedIDs)
		for {
			selection, err := s.selectProviderWithLoadAwarenessForRouting(ctx, groupID, platform, legacySessionHash, requestedModel, routingModel, effectiveExcludedIDs, requireCompact, requiredCapability)
			if err != nil {
				return nil, decision, err
			}
			if selection == nil || selection.Provider == nil {
				return selection, decision, nil
			}
			if (requiredTransport == egress.OpenAIUpstreamTransportAny ||
				requiredTransport == egress.OpenAIUpstreamTransportHTTPSSE ||
				s.isOpenAIProviderTransportCompatible(selection.Provider, requiredTransport)) &&
				providerSupportsOpenAICapabilities(ctx, selection.Provider, requiredCapability, requiredImageCapability) {
				return selection, decision, nil
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			if effectiveExcludedIDs == nil {
				effectiveExcludedIDs = make(map[int64]struct{})
			}
			if _, exists := effectiveExcludedIDs[selection.Provider.Record.ID]; exists {
				return nil, decision, schedulercore.ErrNoAvailableProviders
			}
			effectiveExcludedIDs[selection.Provider.Record.ID] = struct{}{}
		}
	}

	if s.CheckGroupModelRestriction(ctx, groupID, requestedModel) {
		slog.Warn("group model restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, decision, fmt.Errorf("%w supporting model: %s (group model restriction)", schedulercore.ErrNoAvailableProviders, requestedModel)
	}

	var stickyProviderID int64
	if sessionHash != "" && s.cache != nil {
		if providerID, err := s.getStickySessionProviderID(ctx, groupID, sessionHash); err == nil && providerID > 0 {
			stickyProviderID = providerID
		}
	}
	effectiveSettings := s.advancedSchedulerEffectiveSettingsForRequest(ctx, groupID)
	stickyWeighted := effectiveSettings.StickyWeightedEnabled
	subscriptionPriority := effectiveSettings.SubscriptionPriorityEnabled
	stickyPreviousProviderID := int64(0)
	if stickyWeighted && previousResponseCanMove && strings.TrimSpace(previousResponseID) != "" && (platform == "" || platform == capability.PlatformOpenAI) {
		stickyPreviousProviderID = s.ResolveProviderIDByPreviousResponseIDForScheduler(ctx, groupID, previousResponseID, routingModel, excludedIDs, requiredCapability, requireCompact)
	}

	selection, decision, selectErr := scheduler.Select(ctx, schedulercore.PlatformSelectionInput{
		GroupID:                  groupID,
		Platform:                 platform,
		SessionHash:              sessionHash,
		StickyProviderID:         stickyProviderID,
		GuardianParentProviderID: guardianParentProviderID,
		StickyPreviousProviderID: stickyPreviousProviderID,
		StickyWeighted:           stickyWeighted,
		SubscriptionPriority:     subscriptionPriority,
		PreserveStickyBinding:    preserveGuardianParentBinding,
		RequirePrivacySet:        s.openAIGroupRequiresPrivacySet(ctx, groupID),
		PreviousResponseID:       previousResponseID,
		PreviousResponseCanMove:  previousResponseCanMove,
		RequestedModel:           requestedModel,
		RoutingModel:             routingModel,
		RequiredTransport:        string(requiredTransport),
		RequiredCapability:       requiredCapability,
		RequiredImageCapability:  requiredImageCapability,
		RequireCompact:           requireCompact,
		ExcludedIDs:              excludedIDs,
	})
	if selection != nil {
		selection.AdvancedScheduler = true
		feedback := effectiveSettings.Feedback
		selection.AdvancedSchedulerFeedback = &feedback
	}
	return selection, decision, selectErr
}

func providerSupportsOpenAICapabilities(ctx context.Context, provider *gatewayprovider.ExecutionProvider, requiredCapability providercore.OpenAIEndpointCapability, requiredImageCapability providercore.OpenAIImagesCapability) bool {
	if provider == nil {
		return false
	}
	if !gatewayprovider.SupportsRequestCapability(ctx, provider, requiredCapability) {
		return false
	}
	if requiredImageCapability != "" && provider.View().IsGrok() {
		return gatewayprovider.SupportsRequestCapability(ctx, provider, providercore.OpenAIEndpointCapabilityGrokMediaGeneration)
	}
	return provider.View().SupportsOpenAIImageCapability(requiredImageCapability)
}

func cloneExcludedProviderIDs(excludedIDs map[int64]struct{}) map[int64]struct{} {
	if len(excludedIDs) == 0 {
		return nil
	}
	cloned := make(map[int64]struct{}, len(excludedIDs))
	for id := range excludedIDs {
		cloned[id] = struct{}{}
	}
	return cloned
}

func (s *Compatible) isOpenAIProviderTransportCompatible(provider *gatewayprovider.ExecutionProvider, requiredTransport egress.OpenAIUpstreamTransport) bool {
	if requiredTransport == egress.OpenAIUpstreamTransportAny || requiredTransport == egress.OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || provider == nil {
		return false
	}
	if requiredTransport == egress.OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		if provider.View().IsGrok() {
			return true
		}
		return provider.Route.Protocol() == protocol.ProtocolOpenAIResponses || provider.View().SupportsResponsesWS() ||
			(provider.View().IsOpenAI() && slices.Contains(provider.View().UpstreamProtocols(), capability.ProtocolOpenAIResponses))
	}
	return s.ResolveTransport(provider).Transport == requiredTransport
}

func (s *Compatible) ReportOpenAIProviderScheduleResult(providerOrID any, model string, success bool, firstTokenMs *int, observedErr ...error) bool {
	var provider *gatewayprovider.ExecutionProvider
	var providerID int64
	switch value := providerOrID.(type) {
	case *gatewayprovider.ExecutionProvider:
		provider = value
		if provider != nil {
			providerID = provider.Record.ID
		}
	case int64:
		providerID = value
	case int:
		providerID = int64(value)
	}
	if provider == nil && providerID == 0 {
		return false
	}

	healthTripped := false
	if provider != nil && s != nil && s.healthObserver != nil {
		if !success && len(observedErr) > 0 && observedErr[0] != nil {
			healthTripped = s.ObserveOpenAIProviderHealthFailure(context.Background(), provider, observedErr[0])
		}
	}
	if success {
		s.runtimeBlockState().ResetRetry(providerID)
		s.clearOpenAIProviderModelTransientState(providerID, providercore.NormalizeTransientModel(model))
	}
	if provider == nil {
		s.reportOpenAIProviderScheduleResult(false, providerID, model, success, firstTokenMs)
		return healthTripped
	}
	if s == nil || s.healthObserver == nil {
		return healthTripped
	}
	scheduler := s.ensureOpenAIProviderScheduler()
	if scheduler == nil {
		return healthTripped
	}
	scheduler.ReportResult(providerID, success, firstTokenMs)
	return healthTripped
}

// ObserveOpenAIProviderHealthFailure 记录已经写出响应后无法进入调度反馈的失败。
func (s *Compatible) ObserveOpenAIProviderHealthFailure(ctx context.Context, provider *gatewayprovider.ExecutionProvider, observedErr error) bool {
	if s == nil || s.healthObserver == nil || provider == nil || observedErr == nil {
		return false
	}
	status, body, eligible := gatewayprovider.ClassifyOpenAIAPIKeyHealthFailure(observedErr)
	record := gatewayprovider.ExecutionRecord(provider)
	handled := s.healthObserver.Core.ApplyAPIKeyHealthFailure(ctx, record, status, body, eligible)
	provider.Record.TempUnschedulableUntil = record.TempUnschedulableUntil
	provider.Record.TempUnschedulableReason = record.TempUnschedulableReason
	return handled
}

// ReportOpenAIProviderScheduleResultForSelection 按本次实际选择模式写入反馈。
// 基础分组仍清理请求临时状态，但不会污染高级调度统计。
func (s *Compatible) ReportOpenAIProviderScheduleResultForSelection(selection *gatewayprovider.SelectionResult, providerID int64, model string, success bool, firstTokenMs *int) {
	if selection != nil && selection.AdvancedScheduler && selection.AdvancedSchedulerFeedback != nil {
		s.reportOpenAIProviderScheduleResultWithFeedback(providerID, model, success, firstTokenMs, *selection.AdvancedSchedulerFeedback)
		return
	}
	s.reportOpenAIProviderScheduleResult(selection != nil && selection.AdvancedScheduler, providerID, model, success, firstTokenMs)
}

func (s *Compatible) reportOpenAIProviderScheduleResult(advanced bool, providerID int64, model string, success bool, firstTokenMs *int) {
	if success {
		s.clearOpenAIProviderModelTransientState(providerID, providercore.NormalizeTransientModel(model))
	}
	if !advanced {
		return
	}
	scheduler := s.ensureOpenAIProviderScheduler()
	if scheduler == nil {
		return
	}
	scheduler.ReportResult(providerID, success, firstTokenMs)
}

func (s *Compatible) reportOpenAIProviderScheduleResultWithFeedback(providerID int64, model string, success bool, firstTokenMs *int, feedback policy.FeedbackConfig) {
	if success {
		s.clearOpenAIProviderModelTransientState(providerID, providercore.NormalizeTransientModel(model))
	}
	scheduler := s.ensureOpenAIProviderScheduler()
	if scheduler == nil {
		return
	}
	scheduler.ReportResult(providerID, success, firstTokenMs, feedback)
}

func (s *Compatible) RecordOpenAIProviderSwitch() {
	if s == nil || s.healthObserver == nil {
		return
	}
	scheduler := s.ensureOpenAIProviderScheduler()
	if scheduler != nil {
		scheduler.ReportSwitch()
	}
}

// RecordOpenAIProviderSwitchForSelection 只记录高级调度请求的提供商切换。
func (s *Compatible) RecordOpenAIProviderSwitchForSelection(selection *gatewayprovider.SelectionResult) {
	s.recordOpenAIProviderSwitch(selection != nil && selection.AdvancedScheduler)
}

func (s *Compatible) recordOpenAIProviderSwitch(advanced bool) {
	if !advanced {
		return
	}
	scheduler := s.ensureOpenAIProviderScheduler()
	if scheduler == nil {
		return
	}
	scheduler.ReportSwitch()
}

func (s *Compatible) SnapshotOpenAIProviderSchedulerMetrics() schedulercore.PlatformMetricsSnapshot {
	scheduler := s.ensureOpenAIProviderScheduler()
	if scheduler == nil {
		return schedulercore.PlatformMetricsSnapshot{}
	}
	return scheduler.SnapshotMetrics()
}

func (s *Compatible) SessionStickyTTL() time.Duration {
	if s != nil &&
		s.options.StickyTTL >
			0 {
		return s.options.StickyTTL
	}
	return time.Hour
}

// selectBySessionHash 调用共享粘性选择器执行基础调度回退。
func (s *compatiblePicker) selectBySessionHash(ctx context.Context, req schedulercore.PlatformSelectionInput) (*gatewayprovider.SelectionResult, bool, error) {
	core, scope := s.platformSelector()
	value, escaped, err := core.SelectBySessionHash(ctx, cloneSelectionInput(req))
	return scope.restore(value), escaped, err
}

// openAIQuotaHeadroomFactor 旧候选只转换额度观测；评分信号使用提供商模块唯一实现。
func openAIQuotaHeadroomFactor(value *gatewayprovider.ExecutionProvider, now time.Time) float64 {
	return providercore.OpenAIQuotaHeadroomFactor(gatewayprovider.ExecutionRecord(value), now)
}

// requestRoutingModel 返回提供商层模型，未提供时使用客户端模型。
func requestRoutingModel(input schedulercore.PlatformSelectionInput) string {
	if model := strings.TrimSpace(input.RoutingModel); model != "" {
		return model
	}
	return input.RequestedModel
}

// cloneSelectionInput 复制本次选择输入和排除集合。
func cloneSelectionInput(input schedulercore.PlatformSelectionInput) schedulercore.PlatformSelectionInput {
	input.ExcludedIDs = maps.Clone(input.ExcludedIDs)
	return input
}
