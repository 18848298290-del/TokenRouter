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

// OAuthUsageOptions supplies platform usage operations, a clock, jitter, and logging.
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

// OAuthUsageService coordinates platform queries, batch concurrency, passive usage, recovery, and shutdown.
type OAuthUsageService struct {
	providerRepo OAuthUsageReader
	cache        *OAuthUsageCache
	stats        *LocalUsageStatistics
	options      OAuthUsageOptions
	activity     operationActivity
}

// oauthAPIFlightResult ties successful and failed shared results to the queried credential identity.
type oauthAPIFlightResult struct {
	Response *ClaudeUsageResponse
	Err      error
	Identity string
}

// UsageSessionWindowWriter updates the window end separately after checking the observed identity and previous
// end.
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

// BeginDetached starts budgeted work independent of caller cancellation and tracked by service shutdown.
func (s *OAuthUsageService) BeginDetached(parent context.Context, budget time.Duration) (context.Context, func(), error) {
	ctx, finish, err := s.activity.begin(context.WithoutCancel(parent), ErrOAuthUsageStopped)
	// Work derived from an accepted query reports cancellation if shutdown wins this race.
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

	// Active Anthropic usage queries require OAuth credentials with profile scope.
	if provider.CanGetUsage() {
		var apiResp *ClaudeUsageResponse

		// Successful responses live for three minutes; errors live for one minute.
		if cache, ok := s.cache.LoadAPI(providerID); ok && cache.Identity == identity {
			age := s.options.Now().Sub(cache.Timestamp)
			if cache.Err != nil && age < OAuthUsageAPIErrorCacheTTL {
				return nil, cache.Err
			}
			if cache.Response != nil && age < OAuthUsageAPICacheTTL {
				apiResp = cache.Response
			}
		}

		// Coalesce cache misses into one upstream query per provider.
		if apiResp == nil {
			// Jitter spreads batch cache misses over time, up to 800 milliseconds.
			// The delay remains cancelable by the caller.
			jitter := time.Duration(s.options.Jitter(int64(OAuthUsageAPIQueryMaxJitter)))
			select {
			case <-time.After(jitter):
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			flightKey := fmt.Sprintf("usage:%d", providerID)
			result, flightErr, _ := s.cache.DoAPI(flightKey, func() (any, error) {
				// A preceding flight may have filled the cache while this call waited.
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
					// Cache errors briefly to limit repeated upstream failures, including 429 responses.
					s.cache.StoreAPI(providerID, &OAuthAPIUsageCache{
						Identity:  identity,
						Err:       fetchErr,
						Timestamp: s.options.Now(),
					})
					return oauthAPIFlightResult{Err: fetchErr, Identity: identity}, nil
				}
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

		// Recompute remaining seconds for each returned view.
		now := s.options.Now()
		usage := BuildUsageInfo(apiResp, &now, s.options.Now, s.options.Log)

		// Local window statistics have a separate one-minute cache.
		s.stats.AddWindowStats(ctx, provider, usage)

		// Publish actively queried values for subsequent passive reads.
		s.SyncActiveToPassive(ctx, provider, usage)

		// When the API omits Fable usage, use the passive 7d_oi header sample
		// to populate the seven-day Fable window.
		if usage.SevenDayFable == nil {
			usage.SevenDayFable = BuildPassiveUsageWindow(provider.Extra, "passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset", s.options.Now)
		}

		s.recoverProviderError(ctx, provider)
		return usage, nil
	}

	// Setup tokens lack profile scope, so usage is estimated from the stored session window.
	if provider.Type == ProviderTypeSetupToken {
		usage := EstimateSetupTokenUsage(provider, s.options.Now)
		s.stats.AddWindowStats(ctx, provider, usage)
		return usage, nil
	}

	// Remaining provider types do not support this usage query.
	return nil, fmt.Errorf("provider type %s does not support usage query", provider.Type)
}

// GetUsage loads a provider and dispatches its platform-specific usage query.
// Anthropic OAuth queries use a three-minute response cache and one-minute statistics cache.
// Anthropic setup tokens estimate the five-hour window from stored session state.
// The optional force flag requests a probe on platform paths that support it.
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

// GetUsageBatch queries unique positive provider IDs with bounded concurrency.
// Anthropic OAuth and setup-token providers use passive data; other providers use their platform query.
// Individual failures are returned by provider ID while the rest of the batch continues.
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

// GetPassiveUsage builds usage from stored samples and local statistics.
// It accepts Anthropic OAuth and setup-token providers.
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

	// Both credential types derive the five-hour window from the same stored session state.
	info := EstimateSetupTokenUsage(provider, s.options.Now)
	info.Source = "passive"

	// Prefer the recorded sample time over the current query time.
	if raw, ok := provider.Extra["passive_usage_sampled_at"]; ok {
		if str, ok := raw.(string); ok {
			if t, err := time.Parse(time.RFC3339, str); err == nil {
				info.UpdatedAt = &t
			}
		}
	}

	info.SevenDay = BuildPassiveUsageWindow(provider.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset", s.options.Now)

	// Fable uses the separately sampled 7d_oi header values.
	info.SevenDayFable = BuildPassiveUsageWindow(provider.Extra, "passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset", s.options.Now)

	s.stats.AddWindowStats(ctx, provider, info)

	return info, nil
}

// SyncActiveToPassive conditionally writes active usage into the stored passive snapshot.
// Identity changes prevent a result from updating another credential's usage.
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

	// EstimateSetupTokenUsage reads the five-hour reset from SessionWindowEnd.
	// Writing only Extra would leave passive views using the previous window's end.
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

// recoverProviderError conditionally clears recoverable errors for the observed credential identity.
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
