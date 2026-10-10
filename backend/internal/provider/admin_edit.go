package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

func (s *Admin) UpdateProvider(ctx context.Context, id int64, input *UpdateProviderInput) (*Record, error) {
	provider, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	var expected *CredentialVersion
	if input.ExpectedCredentials != nil {
		frozen := CloneCredentialVersion(*input.ExpectedCredentials)
		expected = &frozen
		if !MatchesCredentialVersion(provider, frozen) {
			return nil, ErrRefreshProviderStateChanged
		}
	}
	credentialInput := input.Credentials
	if input.PatchCredentials {
		credentialInput = CloneValues(input.Credentials)
		copy := *input
		copy.Credentials = MergeCredentials(provider.Credentials, CloneValues(credentialInput))
		input = &copy
	}
	var extraPatch map[string]any
	if input.PatchExtra {
		extraPatch = CloneValues(input.Extra)
		copy := *input
		copy.Extra = CloneValues(provider.Extra)
		if copy.Extra == nil {
			copy.Extra = map[string]any{}
		}
		maps.Copy(copy.Extra, extraPatch)
		input = &copy
	}
	var computeResetAt, normalizeWindowAt *time.Time
	previousCNUsageIdentity := CNUsageMonitorIdentityFingerprint(provider)
	originalQoderSite, originalQoderSiteErr := s.options.Credentials.Site(provider)
	originalQoderPAT := strings.TrimSpace(provider.GetCredential("pat"))
	normalizedExtra, shouldReplaceExtra := NormalizeDeprecatedProviderExtraUpdate(input.Extra)
	if shouldReplaceExtra {
		if IsOpenAIAPIKeyProvider(provider) && HasOpenAIConfigurationPatch(nil, normalizedExtra) {
			if err := NormalizeOpenAIAPIKeyConfigurationPatch(nil, normalizedExtra); err != nil {
				return nil, err
			}
		}
		normalizedExtra, err = NormalizeGrokMediaEligibilityUpdateExtra(provider, input, normalizedExtra)
		if err != nil {
			return nil, err
		}
		if err := ValidateUpstreamRequestIDHeaderExtra(normalizedExtra); err != nil {
			return nil, err
		}
	}
	previousOllamaUsageIdentity := OllamaCloudUsageIdentity(provider)
	// 安全/身份不变量(影子提供商):通用更新路径被 edit/re-auth/refresh/batch 共用,
	// 必须在此守住,否则仅在创建时的保证可被这些路径绕过。
	if provider.IsCredentialShadow() {
		// 影子凭据由母提供商管理，不能独立写入。
		if !IsAllowedSparkShadowCredentialsUpdate(input.Credentials) {
			return nil, infraerrors.Newf(infraerrors.CategoryBadRequest, "SPARK_SHADOW_NO_CREDENTIALS",
				"spark shadow providers do not hold auth credentials; only model mapping can be configured on the shadow provider")
		}
		// 影子类型必须保持 OAuth。认证转换、ChatGPT Header 和 WS 决策依赖该类型，
		// 改成 API Key 会使请求按错误协议转发。
		if input.Type != "" && input.Type != provider.Type {
			return nil, infraerrors.Newf(infraerrors.CategoryBadRequest, "SPARK_SHADOW_IMMUTABLE_TYPE",
				"spark shadow provider type cannot be changed; it must remain an OpenAI OAuth shadow")
		}
	} else if input.Type != "" && input.Type != provider.Type && input.Type != ProviderTypeOAuth {
		// 有 Spark 影子的母提供商必须保持 OpenAI OAuth，否则影子无法解析共用凭据。
		// 修改母提供商类型前必须先删除影子。
		shadows, serr := s.providerRepo.ListShadowsByParent(ctx, id)
		if serr != nil {
			return nil, serr
		}
		if len(shadows) > 0 {
			return nil, infraerrors.New(infraerrors.CategoryBadRequest, "SPARK_SHADOW_PARENT_IMMUTABLE_TYPE",
				"cannot change provider type while it has a spark shadow; delete the shadow first")
		}
	}
	wasOveragesEnabled := provider.IsOveragesEnabled()
	var change ConfigurationChange

	if input.Name != "" {
		provider.Name = input.Name
		change.Fields |= ConfigName
	}
	if input.Type != "" {
		provider.Type = input.Type
		change.Fields |= ConfigType
	}
	if input.Notes != nil {
		provider.Notes = NormalizeProviderNotes(input.Notes)
		change.Fields |= ConfigNotes
	}
	if provider.IsCredentialShadow() && input.Credentials != nil {
		provider.Credentials = SanitizeSparkShadowCredentials(input.Credentials)
	} else if len(input.Credentials) > 0 {
		incomingCredentials := PreserveProtocolCredentials(provider.Credentials, input.Credentials)
		if IsOpenAIAPIKeyProvider(provider) {
			// 先规范化本次增量，请求中的兼容别名按本次输入覆盖已保存的配置。
			if err := NormalizeOpenAIAPIKeyConfigurationPatch(incomingCredentials, nil); err != nil {
				return nil, err
			}
		}
		// 前端拿到脱敏后的凭据，提交完整对象时会省略敏感字段。
		// 更新时从当前记录补齐这些字段。
		provider.Credentials = MergePreservingSensitiveCreds(provider.Credentials, incomingCredentials)
		// 校验并规范化请求头覆写配置（header 名小写化、格式检查）
		if err := egress.NormalizeHeaderOverrideCredentials(provider.Credentials); err != nil {
			return nil, err
		}
		// 移除不得与 OAuth token 一同保存的 SSO 和密码残留。
		provider.Credentials = SanitizeStoredCredentials(provider.Platform, provider.Credentials)
		if err := ValidateGeminiThirdPartyBaseURL(provider); err != nil {
			return nil, err
		}
	}
	// Extra 为 nil 时保持当前值，空 map 表示清空。
	// 关闭配额限制时前端会删除 quota_* 键并提交 extra:{}，此时也必须落库；只有废弃键时则不替换。
	if shouldReplaceExtra {
		DiscardDeprecatedProviderExtra(normalizedExtra)
		if err := NormalizeUpstreamUsageExtra(normalizedExtra); err != nil {
			return nil, err
		}
		// 请求省略查询配置键时，整份 Extra 替换也保持已保存的查询配置。
		if _, provided := input.Extra[UpstreamUsageQueryExtraKey]; !provided {
			if value, exists := provider.Extra[UpstreamUsageQueryExtraKey]; exists {
				if normalized, ok := NormalizedUpstreamUsageConfigValue(value); ok {
					normalizedExtra[UpstreamUsageQueryExtraKey] = normalized
				}
			}
		}
		delete(normalizedExtra, OllamaCloudUsageSessionExtraKey)
		delete(normalizedExtra, OllamaCloudUsageAutoRefreshExtraKey)
		delete(normalizedExtra, OllamaCloudUsageSnapshotExtraKey)
		delete(normalizedExtra, CNUsageMonitorSnapshotExtraKey)
		// 保留配额用量和专用服务受管字段，防止普通提供商编辑意外覆盖。
		for _, key := range []string{
			"quota_used",
			"quota_daily_used",
			"quota_daily_start",
			"quota_weekly_used",
			"quota_weekly_start",
			"grok_billing_snapshot",
			OllamaCloudUsageSessionExtraKey,
			OllamaCloudUsageAutoRefreshExtraKey,
			OllamaCloudUsageSnapshotExtraKey,
			CNUsageMonitorSnapshotExtraKey,
		} {
			if v, ok := provider.Extra[key]; ok {
				normalizedExtra[key] = v
			}
		}
		if IsOpenAIAPIKeyProvider(provider) {
			// 请求省略能力字段时，保持已保存的管理员设置。
			_, continuationProvided := input.Extra[ExtraKeyResponsesContinuationSupported]
			if !continuationProvided {
				if value, ok := provider.Extra[ExtraKeyResponsesContinuationSupported]; ok {
					normalizedExtra[ExtraKeyResponsesContinuationSupported] = value
				}
			}
		}
		normalizedExtra = PrepareCodexFingerprintExtraForUpdate(provider, normalizedExtra, s.options.Creation.NewSeed)
		provider.Extra = normalizedExtra
		if provider.Platform == PlatformAntigravity && wasOveragesEnabled && !provider.IsOveragesEnabled() {
			delete(provider.Extra, "antigravity_credits_overages") // 清理旧版 overages 运行态
			// 清除 AICredits 限流 key
			if rawLimits, ok := provider.Extra["model_rate_limits"].(map[string]any); ok {
				delete(rawLimits, "AICredits")
			}
		}
		if provider.Platform == PlatformAntigravity && !wasOveragesEnabled && provider.IsOveragesEnabled() {
			delete(provider.Extra, "model_rate_limits")
			delete(provider.Extra, "antigravity_credits_overages") // 清理旧版 overages 运行态
		}
		// 校验并预计算固定时间重置的下次重置时间
		if err := ValidateQuotaResetConfig(provider.Extra, s.options.Creation.LoadLocation); err != nil {
			return nil, err
		}
		resetNow := s.options.Creation.Now()
		computeResetAt = &resetNow
		ComputeQuotaResetAt(provider.Extra, resetNow, s.options.Creation.LoadLocation)
		windowNow := s.options.Creation.Now()
		normalizeWindowAt = &windowNow
		NormalizeFixedQuotaWindows(provider.Extra, windowNow, s.options.Creation.LoadLocation)
	}
	if input.Extra == nil {
		provider.Extra = PrepareCodexFingerprintExtraForUpdate(provider, provider.Extra, s.options.Creation.NewSeed)
	}
	// 影子代理由母提供商同步，不能独立编辑，否则两次同步之间会出现出站代理不一致。
	if input.ProxyID != nil && !provider.IsCredentialShadow() {
		// 0 表示清除代理，前端通过这个值提交清除操作。
		if *input.ProxyID == 0 {
			provider.ProxyID = nil
		} else {
			provider.ProxyID = input.ProxyID
		}
		provider.Proxy = nil // 清除关联对象，防止 GORM Save 时根据 Proxy.ID 覆盖 ProxyID
	}
	DiscardDeprecatedProviderExtra(provider.Extra)
	ApplyLegacyProtocolPatch(provider, input.Credentials, input.Extra)
	if err := NormalizeCNProviderCredentials(provider, false); err != nil {
		return nil, err
	}
	if err := NormalizeOpenAIAPIKeyConfiguration(provider); err != nil {
		return nil, err
	}
	if err := NormalizeProviderProtocols(provider); err != nil {
		return nil, err
	}
	if provider.Extra != nil {
		if !IsOllamaCloudUsageProvider(provider) {
			delete(provider.Extra, OllamaCloudUsageSessionExtraKey)
			delete(provider.Extra, OllamaCloudUsageAutoRefreshExtraKey)
			delete(provider.Extra, OllamaCloudUsageSnapshotExtraKey)
		} else if !reflect.DeepEqual(previousOllamaUsageIdentity, OllamaCloudUsageIdentity(provider)) {
			delete(provider.Extra, OllamaCloudUsageSessionExtraKey)
			delete(provider.Extra, OllamaCloudUsageAutoRefreshExtraKey)
			delete(provider.Extra, OllamaCloudUsageSnapshotExtraKey)
		}
	}
	// 只在指针非 nil 时更新 Concurrency（支持设置为 0）
	if input.Concurrency != nil {
		provider.Concurrency = NormalizeProviderConcurrency(provider.Platform, provider.Type, *input.Concurrency)
		change.Fields |= ConfigConcurrency
	}
	// 只在指针非 nil 时更新 Priority（支持设置为 0）
	if input.Priority != nil {
		provider.Priority = *input.Priority
		change.Fields |= ConfigPriority
	}
	if input.RateMultiplier != nil {
		if *input.RateMultiplier < 0 {
			return nil, errors.New("rate_multiplier must be >= 0")
		}
		provider.RateMultiplier = input.RateMultiplier
		change.Fields |= ConfigRateMultiplier
	}
	if input.LoadFactor != nil {
		if *input.LoadFactor <= 0 {
			provider.LoadFactor = nil // 0 或负数表示清除
		} else if *input.LoadFactor > 10000 {
			return nil, errors.New("load_factor must be <= 10000")
		} else {
			provider.LoadFactor = input.LoadFactor
		}
		change.Fields |= ConfigLoadFactor
	}
	if input.Status != "" {
		provider.Status = input.Status
		change.Fields |= ConfigStatus
	}
	if input.ExpiresAt != nil {
		if *input.ExpiresAt <= 0 {
			provider.ExpiresAt = nil
		} else {
			expiresAt := time.Unix(*input.ExpiresAt, 0)
			provider.ExpiresAt = &expiresAt
		}
		change.Fields |= ConfigExpiresAt
	}
	if input.AutoPauseOnExpired != nil {
		provider.AutoPauseOnExpired = *input.AutoPauseOnExpired
		change.Fields |= ConfigAutoPauseOnExpired
	}

	// 先验证分组是否存在（在任何写操作之前）
	if input.GroupIDs != nil {
		if err := s.ValidateGroupIDs(ctx, *input.GroupIDs); err != nil {
			return nil, err
		}
	}

	deferQoderPATValidation := false
	if provider.IsQoder() && originalQoderSiteErr == nil && originalQoderPAT != "" && originalQoderPAT == strings.TrimSpace(provider.GetCredential("pat")) {
		if currentSite, siteErr := s.options.Credentials.Site(provider); siteErr == nil {
			deferQoderPATValidation = currentSite != originalQoderSite
		}
	}
	s.attachProxyForValidation(ctx, provider)
	if previousCNUsageIdentity != CNUsageMonitorIdentityFingerprint(provider) && provider.Extra != nil {
		delete(provider.Extra, CNUsageMonitorSnapshotExtraKey)
	}
	if err := s.options.Credentials.ValidateEdit(ctx, provider, deferQoderPATValidation); err != nil {
		return nil, err
	}
	change.ExtraPatch = extraPatch
	change.ExpectedCredentials = expected
	change.NormalizeProtocols = true
	change.PreserveSensitive = !provider.IsCredentialShadow()
	change.CredentialInput = credentialInput
	change.ProtocolExtra = input.Extra
	change.ComputeResetAt = computeResetAt
	change.NormalizeWindowAt = normalizeWindowAt
	if input.PatchCredentials {
		change.CredentialPatch = CloneValues(credentialInput)
	}
	if (provider.IsCredentialShadow() && input.Credentials != nil) || len(input.Credentials) > 0 {
		change.Fields |= ConfigCredentials
	}
	if input.ProxyID != nil && !provider.IsCredentialShadow() {
		change.Fields |= ConfigProxyID
	}
	if shouldReplaceExtra {
		change.Fields |= ConfigExtra
		if _, provided := input.Extra[UpstreamUsageQueryExtraKey]; !provided {
			change.PreserveExtraKeys = append(change.PreserveExtraKeys, UpstreamUsageQueryExtraKey)
		}
		if _, provided := input.Extra[ExtraKeyResponsesContinuationSupported]; !provided && IsOpenAIAPIKeyProvider(provider) {
			change.PreserveExtraKeys = append(change.PreserveExtraKeys, ExtraKeyResponsesContinuationSupported)
		}
	}
	if err := WriteConfiguration(ctx, s.providerRepo, provider, change); err != nil {
		return nil, err
	}

	// 将 proxy 变更传播到 spark 影子提供商（同步；Update 内部已触发调度快照）。
	// 影子自身 proxy 不可独立编辑(见上),故对影子的更新不触发传播。
	if input.ProxyID != nil && !provider.IsCredentialShadow() {
		if err := s.propagateProxyToShadows(ctx, id, provider.ProxyID); err != nil {
			return nil, err
		}
	}

	// 绑定分组
	if input.GroupIDs != nil {
		if err := s.providerRepo.BindGroups(ctx, provider.ID, *input.GroupIDs); err != nil {
			return nil, err
		}
	}

	// 重新查询完整数据，包括更新后的 Proxy 关联对象。
	updated, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// UpdateProviderExtra 按键合并 Extra JSONB，其余运行状态和配置字段保持当前值。
func (s *Admin) UpdateProviderExtra(ctx context.Context, id int64, updates map[string]any) error {
	if err := validateResponsesWSConnectionModePatch(updates); err != nil {
		return err
	}
	updates = SanitizedCodexFingerprintExtraUpdates(updates)
	DiscardDeprecatedProviderExtra(updates)
	if err := NormalizeUpstreamUsageExtra(updates); err != nil {
		return err
	}
	delete(updates, OllamaCloudUsageSessionExtraKey)
	delete(updates, OllamaCloudUsageAutoRefreshExtraKey)
	delete(updates, OllamaCloudUsageSnapshotExtraKey)
	delete(updates, CNUsageMonitorSnapshotExtraKey)
	if HasOpenAIConfigurationPatch(nil, updates) {
		provider, err := s.providerRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if !IsOpenAIAPIKeyProvider(provider) {
			return infraerrors.BadRequest(
				"OPENAI_CONFIGURATION_TARGET_INVALID",
				"OpenAI text protocol and continuation configuration only applies to OpenAI API Key providers",
			)
		}
		if err := NormalizeOpenAIAPIKeyConfigurationPatch(nil, updates); err != nil {
			return err
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return s.providerRepo.UpdateExtra(ctx, id, updates)
}

// BulkUpdateProviders 在单次请求中更新多个提供商。
// 凭据和 extra 使用键级合并，不覆盖整个对象。
func (s *Admin) BulkUpdateProviders(ctx context.Context, input *BulkUpdateProvidersInput) (*BulkUpdateProvidersResult, error) {
	if err := validateResponsesWSConnectionModePatch(input.Extra); err != nil {
		return nil, err
	}
	// 受管会话状态只能通过专用类型接口更新，废弃提供商扩展字段直接丢弃。
	input.Extra = SanitizedCodexFingerprintExtraUpdates(input.Extra)
	DiscardDeprecatedProviderExtra(input.Extra)
	if err := NormalizeUpstreamUsageExtra(input.Extra); err != nil {
		return nil, err
	}
	delete(input.Extra, OllamaCloudUsageSessionExtraKey)
	delete(input.Extra, OllamaCloudUsageAutoRefreshExtraKey)
	delete(input.Extra, OllamaCloudUsageSnapshotExtraKey)
	delete(input.Extra, CNUsageMonitorSnapshotExtraKey)

	if len(input.ProviderIDs) == 0 && input.Filters != nil {
		providerIDs, err := s.resolveBulkUpdateTargetIDs(ctx, input.Filters)
		if err != nil {
			return nil, err
		}
		input.ProviderIDs = providerIDs
	}

	result := &BulkUpdateProvidersResult{
		SuccessIDs: make([]int64, 0, len(input.ProviderIDs)),
		FailedIDs:  make([]int64, 0, len(input.ProviderIDs)),
		Results:    make([]BulkUpdateProviderResult, 0, len(input.ProviderIDs)),
	}

	if len(input.ProviderIDs) == 0 {
		return result, nil
	}
	if input.GroupIDs != nil {
		if err := s.ValidateGroupIDs(ctx, *input.GroupIDs); err != nil {
			return nil, err
		}
	}
	openAISettings, err := normalizeBulkOpenAISettings(input)
	if err != nil {
		return nil, err
	}

	// 一次预取全部目标提供商，供凭据和代理校验共用。
	var cachedTargets []*Record
	hasOpenAIConfigPatch := HasOpenAIConfigurationPatch(input.Credentials, input.Extra)
	if len(input.Credentials) > 0 || input.ProxyID != nil || hasOpenAIConfigPatch {
		loaded, err := s.providerRepo.GetByIDs(ctx, input.ProviderIDs)
		if err != nil {
			return nil, err
		}
		cachedTargets = loaded
	}
	if hasOpenAIConfigPatch {
		targetsByID := make(map[int64]*Record, len(cachedTargets))
		for _, provider := range cachedTargets {
			if provider != nil {
				targetsByID[provider.ID] = provider
			}
		}
		for _, providerID := range input.ProviderIDs {
			provider, ok := targetsByID[providerID]
			if !ok || provider == nil {
				return nil, invalidBulkOpenAITarget(providerID, "provider does not exist")
			}
			if !IsOpenAIAPIKeyProvider(provider) {
				return nil, infraerrors.BadRequest(
					"OPENAI_CONFIGURATION_TARGET_INVALID",
					"OpenAI text protocol and continuation configuration can only be bulk-updated on OpenAI API Key providers",
				)
			}
		}
		if err := validateBulkOpenAISettingsTargets(input, openAISettings, targetsByID); err != nil {
			return nil, err
		}
		if err := NormalizeOpenAIAPIKeyConfigurationPatch(input.Credentials, input.Extra); err != nil {
			return nil, err
		}
	}
	// 批量写入凭据时，目标不能包含影子。
	// ProviderIDs 包含请求传入的 ID 和筛选结果中的 ID。
	if len(input.Credentials) > 0 {
		for _, acc := range cachedTargets {
			if acc != nil && acc.IsCredentialShadow() {
				return nil, infraerrors.Newf(infraerrors.CategoryBadRequest, "SPARK_SHADOW_NO_CREDENTIALS",
					"spark shadow provider %d cannot hold credentials; manage credentials on the parent provider", acc.ID)
			}
		}
	}

	// 批量代理更新拒绝影子提供商，影子的代理通过母提供商更新。
	// 含影子时拒绝整批更新，要求调用方先将影子移出目标集合。
	if input.ProxyID != nil {
		for _, acc := range cachedTargets {
			if acc != nil && acc.IsCredentialShadow() {
				return nil, infraerrors.Newf(infraerrors.CategoryBadRequest, "SPARK_SHADOW_PROXY_INHERITED",
					"spark shadow provider %d proxy is inherited from its parent and cannot be set in bulk; manage it on the parent provider", acc.ID)
			}
		}
	}

	if input.RateMultiplier != nil {
		if *input.RateMultiplier < 0 {
			return nil, errors.New("rate_multiplier must be >= 0")
		}
	}

	// 校验并规范化请求头覆写配置（批量路径为 JSONB 顶层 key 合并，直接校验增量即可）
	if err := egress.NormalizeHeaderOverrideCredentials(input.Credentials); err != nil {
		return nil, err
	}
	// 批量更新可能混合平台，因此始终移除临时 SSO、密码和 cookie 字段。
	if input.Credentials != nil {
		input.Credentials = SanitizeStoredCredentials("", input.Credentials)
	}
	protocolUpdates := map[int64]map[string]any{}
	if len(input.Credentials) > 0 || hasOpenAIConfigPatch {
		for _, provider := range cachedTargets {
			if provider == nil {
				continue
			}
			prospective := *provider
			prospective.Credentials = maps.Clone(provider.Credentials)
			if prospective.Credentials == nil {
				prospective.Credentials = make(map[string]any, len(input.Credentials))
			}
			maps.Copy(prospective.Credentials, input.Credentials)
			if err := NormalizeCNProviderCredentials(&prospective, false); err != nil {
				return nil, err
			}
			prospective.Extra = maps.Clone(provider.Extra)
			if prospective.Extra == nil {
				prospective.Extra = map[string]any{}
			}
			maps.Copy(prospective.Extra, input.Extra)
			ApplyLegacyProtocolPatch(&prospective, input.Credentials, input.Extra)
			if err := NormalizeProviderProtocols(&prospective); err != nil {
				return nil, err
			}
			protocolUpdates[provider.ID] = map[string]any{UpstreamProtocolsKey: prospective.Credentials[UpstreamProtocolsKey]}
			if urls, exists := prospective.Credentials["api_base_urls"]; exists {
				protocolUpdates[provider.ID]["api_base_urls"] = urls
			}
			if err := ValidateGeminiThirdPartyBaseURL(&prospective); err != nil {
				return nil, err
			}
		}
	}

	// Prepare bulk updates for columns and JSONB fields.
	repoUpdates := ProviderBulkUpdate{
		ProtocolUpdates:            protocolUpdates,
		Credentials:                input.Credentials,
		Extra:                      input.Extra,
		EnsureCodexFingerprintSeed: ShouldEnsureCodexFingerprintSeedForExtraUpdates(input.Extra),
	}
	if input.Name != "" {
		repoUpdates.Name = &input.Name
	}
	if input.ProxyID != nil {
		repoUpdates.ProxyID = input.ProxyID
	}
	if input.Concurrency != nil {
		repoUpdates.Concurrency = input.Concurrency
	}
	if input.Priority != nil {
		repoUpdates.Priority = input.Priority
	}
	if input.RateMultiplier != nil {
		repoUpdates.RateMultiplier = input.RateMultiplier
	}
	if input.LoadFactor != nil {
		if *input.LoadFactor <= 0 {
			repoUpdates.LoadFactor = nil // 0 或负数表示清除
		} else if *input.LoadFactor > 10000 {
			return nil, errors.New("load_factor must be <= 10000")
		} else {
			repoUpdates.LoadFactor = input.LoadFactor
		}
	}
	if input.Status != "" {
		repoUpdates.Status = &input.Status
	}
	if input.Schedulable != nil {
		repoUpdates.Schedulable = input.Schedulable
	}

	// Run bulk update for column/jsonb fields first.
	if _, err := s.providerRepo.BulkUpdate(ctx, input.ProviderIDs, repoUpdates); err != nil {
		return nil, err
	}

	// 将 proxy 变更传播到每个目标提供商的 spark 影子提供商
	if repoUpdates.ProxyID != nil {
		var effectiveProxyID *int64
		if *repoUpdates.ProxyID != 0 {
			effectiveProxyID = repoUpdates.ProxyID
		}
		for _, providerID := range input.ProviderIDs {
			if err := s.propagateProxyToShadows(ctx, providerID, effectiveProxyID); err != nil {
				return nil, err
			}
		}
	}

	// Handle group bindings per provider (requires individual operations).
	for _, providerID := range input.ProviderIDs {
		entry := BulkUpdateProviderResult{ProviderID: providerID}

		if input.GroupIDs != nil {
			if err := s.providerRepo.BindGroups(ctx, providerID, *input.GroupIDs); err != nil {
				entry.Success = false
				entry.Error = err.Error()
				result.Failed++
				result.FailedIDs = append(result.FailedIDs, providerID)
				result.Results = append(result.Results, entry)
				continue
			}
		}

		entry.Success = true
		result.Success++
		result.SuccessIDs = append(result.SuccessIDs, providerID)
		result.Results = append(result.Results, entry)
	}

	return result, nil
}

func (s *Admin) resolveBulkUpdateTargetIDs(ctx context.Context, filters *BulkUpdateProviderFilters) ([]int64, error) {
	if filters == nil {
		return nil, nil
	}

	groupID := int64(0)
	switch strings.TrimSpace(filters.Group) {
	case "":
	case "ungrouped":
		groupID = ProviderListGroupUngrouped
	default:
		parsedGroupID, err := strconv.ParseInt(strings.TrimSpace(filters.Group), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid group filter: %w", err)
		}
		groupID = parsedGroupID
	}

	const pageSize = 500
	page := 1
	providerIDs := make([]int64, 0, pageSize)

	for {
		providers, total, err := s.ListProviders(
			ctx,
			page,
			pageSize,
			filters.Platform,
			filters.Type,
			filters.Status,
			filters.Search,
			groupID,
			filters.PrivacyMode,
			"",
			"",
		)
		if err != nil {
			return nil, err
		}
		for _, provider := range providers {
			providerIDs = append(providerIDs, provider.ID)
		}
		if int64(len(providerIDs)) >= total || len(providers) == 0 {
			return providerIDs, nil
		}
		page++
	}
}
