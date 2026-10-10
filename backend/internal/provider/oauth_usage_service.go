package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

var ErrOAuthUsageStopped = errors.New("oauth provider usage is stopped")

type OAuthUsageReader interface {
	GetByID(context.Context, int64) (*Record, error)
	GetByIDs(context.Context, []int64) ([]*Record, error)
}

// OAuthUsageOptions 提供当前平台观测能力；具体供应商 HTTP、身份和响应解析不进入核心。
type OAuthUsageOptions struct {
	Now              func() time.Time
	Jitter           func(int64) int64
	Log              func(string, ...any)
	Warn             func(string, ...any)
	OpenAI           OpenAIUsageOptions
	Gemini           GeminiUsageOptions
	Antigravity      AntigravityUsageOptions
	Grok             GrokUsageOptions
	Qoder            QoderUsageOptions
	Anthropic        func(context.Context, *Record) (*ClaudeUsageResponse, error)
	OpenAIQuotaPause func(context.Context, *Record, *UsageInfo)
}

// OAuthUsageService 拥有入口、并发批量、Anthropic 主被动查询、统一恢复与受控查询生命周期。
type OAuthUsageService struct {
	providerRepo OAuthUsageReader
	cache        *OAuthUsageCache
	stats        *LocalUsageStatistics
	options      OAuthUsageOptions
	activity     operationActivity
}

// oauthAPIFlightResult 连同负缓存保存来源，原提供商 key 的等待者不能消费另一身份结果。
type oauthAPIFlightResult struct {
	Response *ClaudeUsageResponse
	Err      error
	Identity string
}

// UsageSessionWindowWriter 保留窗口列的独立提交，并比较查询前身份及原窗口。
type UsageSessionWindowWriter interface {
	UpdateUsageSessionWindowEndIfUnchanged(context.Context, UsageObservationVersion, *time.Time, time.Time) (bool, error)
}

func NewOAuthUsageService(reader OAuthUsageReader, cache *OAuthUsageCache, stats *LocalUsageStatistics, options OAuthUsageOptions) *OAuthUsageService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = func(int64) int64 { return 0 }
	}
	if options.Log == nil {
		options.Log = func(string, ...any) {}
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if cache == nil {
		cache = NewOAuthUsageCache()
	}
	return &OAuthUsageService{providerRepo: reader, cache: cache, stats: stats, options: options}
}

func (s *OAuthUsageService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.activity.stop(ctx, "OAuth provider usage")
}

// BeginDetached 保留等待方独立取消，在资源 Stop 时仍取消并等待原本脱离调用方的查询。
func (s *OAuthUsageService) BeginDetached(parent context.Context, budget time.Duration) (context.Context, func(), error) {
	ctx, finish, err := s.activity.begin(context.WithoutCancel(parent), ErrOAuthUsageStopped)
	// 派生工作属于已接纳查询；停止与派生竞争时按取消结束，不重新解释为新入口拒绝。
	if err == ErrOAuthUsageStopped {
		return nil, nil, context.Canceled
	}
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	return ctx, func() { cancel(); finish() }, nil
}

func SupportsAnthropicPassiveUsage(provider *Record) bool {
	return provider != nil && provider.IsAnthropicOAuthOrSetupToken()
}

func batchUsageErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *OAuthUsageService) GetUsageForProvider(ctx context.Context, provider *Record, forceProbe bool) (*UsageInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()

	if provider == nil {
		return nil, fmt.Errorf("provider is required")
	}
	providerID := provider.ID
	identity := UsageCacheIdentity(provider)

	if provider.Platform == PlatformOpenAI && provider.Type == ProviderTypeOAuth {
		usage, err := s.GetOpenAIUsage(ctx, provider, forceProbe)
		if err == nil {
			s.options.OpenAIQuotaPause(ctx, provider, usage)
			s.recoverProviderError(ctx, provider)
		}
		return usage, err
	}

	if provider.Platform == PlatformGemini {
		usage, err := s.GetGeminiUsage(ctx, provider)
		if err == nil {
			s.recoverProviderError(ctx, provider)
		}
		return usage, err
	}

	// Antigravity 平台：使用 AntigravityQuotaFetcher 获取额度
	if provider.Platform == PlatformAntigravity {
		usage, err := s.GetAntigravityUsage(ctx, provider)
		if err == nil {
			s.recoverProviderError(ctx, provider)
		}
		return usage, err
	}

	if provider.Platform == PlatformGrok {
		usage, err := s.GetGrokUsage(ctx, provider, forceProbe)
		if err == nil && usage != nil && usage.Error == "" {
			s.recoverProviderError(ctx, provider)
		}
		return usage, err
	}

	if provider.Platform == PlatformQoder {
		usage, err := s.GetQoderUsage(ctx, provider, forceProbe)
		if err == nil && (usage == nil || (usage.Error == "" && usage.ErrorCode == "")) {
			s.recoverProviderError(ctx, provider)
		}
		return usage, err
	}

	// 只有 oauth 类型提供商可以通过 API 获取 usage（有 profile scope）
	if provider.CanGetUsage() {
		var apiResp *ClaudeUsageResponse

		// 1. 检查缓存（成功响应 3 分钟 / 错误响应 1 分钟）
		if cache, ok := s.cache.LoadAPI(providerID); ok && cache.Identity == identity {
			age := s.options.Now().Sub(cache.Timestamp)
			if cache.Err != nil && age < OAuthUsageAPIErrorCacheTTL {
				// 负缓存命中时返回缓存的错误。
				return nil, cache.Err
			}
			if cache.Response != nil && age < OAuthUsageAPICacheTTL {
				apiResp = cache.Response
			}
		}

		// 2. 如果没有有效缓存，通过 singleflight 从 API 获取（防止并发击穿）
		if apiResp == nil {
			// 随机延迟使批量请求分散发送，同一 TLS 指纹的请求错开发出。
			// 触发上游反滥用检测。延迟范围 0~800ms，仅在缓存未命中时生效。
			jitter := time.Duration(s.options.Jitter(int64(OAuthUsageAPIQueryMaxJitter)))
			select {
			case <-time.After(jitter):
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			flightKey := fmt.Sprintf("usage:%d", providerID)
			result, flightErr, _ := s.cache.DoAPI(flightKey, func() (any, error) {
				// 再次检查缓存（可能在等待 singleflight 期间被其他请求填充）
				if cache, ok := s.cache.LoadAPI(providerID); ok && cache.Identity == identity {
					age := s.options.Now().Sub(cache.Timestamp)
					if cache.Err != nil && age < OAuthUsageAPIErrorCacheTTL {
						return oauthAPIFlightResult{Err: cache.Err, Identity: identity}, nil
					}
					if cache.Response != nil && age < OAuthUsageAPICacheTTL {
						return oauthAPIFlightResult{Response: cache.Response, Identity: identity}, nil
					}
				}
				resp, fetchErr := s.options.Anthropic(ctx, provider)
				if fetchErr != nil {
					// 负缓存：缓存错误响应，防止后续请求重复触发 429
					s.cache.StoreAPI(providerID, &OAuthAPIUsageCache{
						Identity:  identity,
						Err:       fetchErr,
						Timestamp: s.options.Now(),
					})
					return oauthAPIFlightResult{Err: fetchErr, Identity: identity}, nil
				}
				// 缓存成功响应
				s.cache.StoreAPI(providerID, &OAuthAPIUsageCache{
					Identity:  identity,
					Response:  resp,
					Timestamp: s.options.Now(),
				})
				return oauthAPIFlightResult{Response: clonePointer(resp), Identity: identity}, nil
			})
			if flightErr != nil {
				return nil, flightErr
			}
			completed, ok := result.(oauthAPIFlightResult)
			if !ok || completed.Identity != identity {
				return nil, ErrUsageObservationChanged
			}
			if completed.Err != nil {
				return nil, completed.Err
			}
			apiResp = completed.Response
		}

		// 3. 构建 UsageInfo（每次都重新计算 RemainingSeconds）
		now := s.options.Now()
		usage := BuildUsageInfo(apiResp, &now, s.options.Now, s.options.Log)

		// 4. 添加窗口统计（有独立缓存，1 分钟）
		s.stats.AddWindowStats(ctx, provider, usage)

		// 5. 将主动查询结果同步到被动缓存，下次 passive 加载即为最新值
		s.SyncActiveToPassive(ctx, provider, usage)

		// 6. 上游 usage API 目前不一定下发 Fable 7d 窗口；缺失时回填被动采样
		// （7d_oi 响应头）的数据，7d F 进度条据此展示。
		if usage.SevenDayFable == nil {
			usage.SevenDayFable = BuildPassiveUsageWindow(provider.Extra, "passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset", s.options.Now)
		}

		s.recoverProviderError(ctx, provider)
		return usage, nil
	}

	// Setup Token 提供商：根据 session_window 推算（没有 profile scope，无法调用 usage API）
	if provider.Type == ProviderTypeSetupToken {
		usage := EstimateSetupTokenUsage(provider, s.options.Now)
		// 添加窗口统计
		s.stats.AddWindowStats(ctx, provider, usage)
		return usage, nil
	}

	// API Key 提供商不支持 usage 查询
	return nil, fmt.Errorf("provider type %s does not support usage query", provider.Type)
}

// GetUsage 获取提供商使用量
// OAuth 提供商通过 Anthropic API 查询用量（需要 profile scope），API 响应缓存十分钟，窗口统计缓存一分钟。
// Setup Token 提供商: 根据 session_window 推算 5h 窗口，7d 数据不可用（没有 profile scope）
// API Key 提供商: 不支持 usage 查询。
func (s *OAuthUsageService) GetUsage(ctx context.Context, providerID int64, force ...bool) (*UsageInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()

	forceProbe := len(force) > 0 && force[0]

	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get provider failed: %w", err)
	}

	return s.GetUsageForProvider(ctx, provider, forceProbe)
}

// GetUsageBatch 批量获取提供商使用量。
// Anthropic OAuth/SetupToken 统一走 passive 链路，其他提供商复用现有主动查询逻辑。
// 单个提供商失败不会中断整批请求，错误会按提供商返回。
func (s *OAuthUsageService) GetUsageBatch(ctx context.Context, providerIDs []int64, force bool) (map[int64]*UsageInfo, map[int64]string, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, nil, err
	}
	defer finish()

	uniqueIDs := make([]int64, 0, len(providerIDs))
	seen := make(map[int64]struct{}, len(providerIDs))
	for _, providerID := range providerIDs {
		if providerID <= 0 {
			continue
		}
		if _, ok := seen[providerID]; ok {
			continue
		}
		seen[providerID] = struct{}{}
		uniqueIDs = append(uniqueIDs, providerID)
	}

	usageByProvider := make(map[int64]*UsageInfo, len(uniqueIDs))
	errorsByProvider := make(map[int64]string)
	if len(uniqueIDs) == 0 {
		return usageByProvider, errorsByProvider, nil
	}

	providers, err := s.providerRepo.GetByIDs(ctx, uniqueIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("get providers failed: %w", err)
	}

	providersByID := make(map[int64]*Record, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		providersByID[provider.ID] = provider
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)

	for _, providerID := range uniqueIDs {
		id := providerID
		provider := providersByID[id]
		if provider == nil {
			mu.Lock()
			errorsByProvider[id] = ErrProviderNotFound.Error()
			mu.Unlock()
			continue
		}

		g.Go(func() error {
			var usage *UsageInfo
			var usageErr error
			if SupportsAnthropicPassiveUsage(provider) {
				usage, usageErr = s.GetPassiveUsageForProvider(gctx, provider)
			} else {
				usage, usageErr = s.GetUsageForProvider(gctx, provider, force)
			}

			mu.Lock()
			defer mu.Unlock()
			if usageErr != nil {
				errorsByProvider[id] = batchUsageErrorMessage(usageErr)
				return nil
			}
			usageByProvider[id] = usage
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, nil, err
	}

	return usageByProvider, errorsByProvider, nil
}

// GetPassiveUsage 从 Record.Extra 中的被动采样数据构建 UsageInfo，不调用外部 API。
// 仅适用于 Anthropic OAuth / SetupToken 提供商。
func (s *OAuthUsageService) GetPassiveUsage(ctx context.Context, providerID int64) (*UsageInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()

	provider, err := s.providerRepo.GetByID(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get provider failed: %w", err)
	}

	return s.GetPassiveUsageForProvider(ctx, provider)
}

func (s *OAuthUsageService) GetPassiveUsageForProvider(ctx context.Context, provider *Record) (*UsageInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrOAuthUsageStopped)
	if err != nil {
		return nil, err
	}
	defer finish()

	if !SupportsAnthropicPassiveUsage(provider) {
		return nil, fmt.Errorf("passive usage only supported for Anthropic OAuth/SetupToken providers")
	}

	// 复用 estimateSetupTokenUsage 构建 5h 窗口（OAuth 和 SetupToken 逻辑一致）
	info := EstimateSetupTokenUsage(provider, s.options.Now)
	info.Source = "passive"

	// 设置采样时间
	if raw, ok := provider.Extra["passive_usage_sampled_at"]; ok {
		if str, ok := raw.(string); ok {
			if t, err := time.Parse(time.RFC3339, str); err == nil {
				info.UpdatedAt = &t
			}
		}
	}

	// 构建 7d 窗口（从被动采样数据）
	info.SevenDay = BuildPassiveUsageWindow(provider.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset", s.options.Now)

	// 构建 7d Fable 窗口（从被动采样的 7d_oi 响应头数据）
	info.SevenDayFable = BuildPassiveUsageWindow(provider.Extra, "passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset", s.options.Now)

	// 添加窗口统计
	s.stats.AddWindowStats(ctx, provider, info)

	return info, nil
}

// SyncActiveToPassive 将主动查询的最新数据回写到 Extra 被动缓存，
// 这样下次被动加载时能看到最新值。
func (s *OAuthUsageService) SyncActiveToPassive(ctx context.Context, observed *Record, usage *UsageInfo) {
	if observed == nil || usage == nil {
		return
	}
	providerID := observed.ID
	version := ObserveUsageVersion(observed)
	extraUpdates := make(map[string]any, 4)

	if usage.FiveHour != nil {
		extraUpdates["session_window_utilization"] = usage.FiveHour.Utilization / 100
	}
	if usage.SevenDay != nil {
		extraUpdates["passive_usage_7d_utilization"] = usage.SevenDay.Utilization / 100
		if usage.SevenDay.ResetsAt != nil {
			extraUpdates["passive_usage_7d_reset"] = usage.SevenDay.ResetsAt.Unix()
		}
	}
	if usage.SevenDayFable != nil {
		extraUpdates["passive_usage_7d_oi_utilization"] = usage.SevenDayFable.Utilization / 100
		if usage.SevenDayFable.ResetsAt != nil {
			extraUpdates["passive_usage_7d_oi_reset"] = usage.SevenDayFable.ResetsAt.Unix()
		}
	}

	if len(extraUpdates) > 0 {
		extraUpdates["passive_usage_sampled_at"] = s.options.Now().UTC().Format(time.RFC3339)
		writer, ok := s.providerRepo.(UsageExtraWriter)
		if !ok {
			s.options.Warn("sync_active_to_passive_failed", "provider_id", providerID, "error", ErrUsageObservationWriterMissing)
			return
		}
		applied, err := writer.UpdateUsageExtraIfUnchanged(ctx, version, extraUpdates)
		if err != nil {
			s.options.Warn("sync_active_to_passive_failed", "provider_id", providerID, "error", err)
		} else if !applied {
			return
		}
	}

	// 5h ResetsAt 必须回写到 SessionWindowEnd column，estimateSetupTokenUsage
	// 读这个字段作为窗口结束时间；只塞 Extra 会让 UI 一直拿到上个窗口的过期时间。
	if usage.FiveHour != nil && usage.FiveHour.ResetsAt != nil {
		writer, ok := s.providerRepo.(UsageSessionWindowWriter)
		if !ok {
			s.options.Warn("sync_active_to_passive_session_window_end_failed", "provider_id", providerID, "error", ErrUsageObservationWriterMissing)
			return
		}
		if _, err := writer.UpdateUsageSessionWindowEndIfUnchanged(ctx, version, observed.SessionWindowEnd, *usage.FiveHour.ResetsAt); err != nil {
			s.options.Warn("sync_active_to_passive_session_window_end_failed", "provider_id", providerID, "error", err)
		}
	}
}

// recoverProviderError 仅适配本轮观察值与新条件恢复能力。
func (s *OAuthUsageService) recoverProviderError(ctx context.Context, value *Record) {
	writer, _ := s.providerRepo.(UsageRecoveryWriter)
	applied, err := RecoverUsageProviderError(ctx, value, writer)
	if err != nil {
		s.options.Log("[usage] failed to clear recoverable provider error for provider %d: %v", value.ID, err)
		return
	}
	if applied {
		value.Status = StatusActive
		value.ErrorMessage = ""
	}
}
