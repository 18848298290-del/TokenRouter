package app

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Waiting counters need a lifetime derived from both sticky-session and
// fallback waiting budgets. Cache assembly chooses the longer whole-second
// budget and falls back to the concurrency slot lifetime when neither produces
// a positive value, while occupied-slot expiry remains configured in minutes.
// Subsecond budgets therefore take the slot-lifetime path after truncation.
//
// Snapshot, concurrency, session, and message-queue services share application
// Redis resources and diagnostic callbacks. Concurrency construction attempts
// stale-process cleanup before periodic lifecycle startup, reporting failure
// while keeping the service available. Feedback, settings, parameters, and
// sticky statistics are constructed once for shared selection and administrator
// diagnostics.

// schedulerSharedState retains feedback and parameter state shared by scheduler
// consumers.
type schedulerSharedState struct {
	// Settings and Parameters share one settings runtime built before parameter
	// reads are bound. Feedback and Sticky retain the same observation counters across platform selectors and administrator diagnostics.
	Feedback   *scheduler.RuntimeStats    // provider execution feedback
	Settings   *scheduler.SettingsRuntime // effective runtime settings
	Parameters *scheduler.Parameters      // group parameter resolver
	Sticky     *scheduler.StickyStats     // affinity compatibility counters
}

// provideSchedulerCache constructs the provider snapshot cache with optional
// chunk settings. Nil cfg leaves zero options for cache normalization; supplied
// MGET and write chunk sizes are copied from scheduling configuration.
func provideSchedulerCache(rdb *redis.Client, cfg *config.Config) *schedulerredis.SnapshotCache {
	options := schedulerredis.SnapshotCacheOptions{}
	if cfg != nil {
		options.MGetChunkSize = cfg.Gateway.Scheduling.SnapshotMGetChunkSize
		options.WriteChunkSize = cfg.Gateway.Scheduling.SnapshotWriteChunkSize
	}
	return schedulerredis.NewSnapshotCache(rdb, codec.ProviderCodec{}, options)
}

// provideSchedulerSnapshot binds cached snapshots, durable outbox, and
// provider/group sources. Nil cfg leaves option defaults to SnapshotService;
// supplied fallback, polling, rebuild, lag, and backlog values are captured for
// its lifecycle.
func provideSchedulerSnapshot(cache scheduler.SnapshotCache, outbox scheduler.SchedulerOutboxRepository, providers *providerpostgres.ProviderStore, groups *routingpostgres.GroupStore, cfg *config.Config) *scheduler.SnapshotService {
	var options *scheduler.SnapshotOptions
	if cfg != nil {
		v := cfg.Gateway.Scheduling
		options = &scheduler.SnapshotOptions{
			DbFallbackEnabled: v.DbFallbackEnabled, DbFallbackMaxQPS: v.DbFallbackMaxQPS, DbFallbackTimeoutSeconds: v.DbFallbackTimeoutSeconds,
			OutboxPollIntervalSeconds: v.OutboxPollIntervalSeconds, FullRebuildIntervalSeconds: v.FullRebuildIntervalSeconds, OutboxLagWarnSeconds: v.OutboxLagWarnSeconds,
			OutboxLagRebuildSeconds: v.OutboxLagRebuildSeconds, OutboxLagRebuildFailures: v.OutboxLagRebuildFailures, OutboxBacklogRebuildRows: v.OutboxBacklogRebuildRows,
		}
	}
	return scheduler.NewSnapshotService(cache, outbox, schedulerProviderSource{ProviderStore: providers}, schedulerGroupSource{GroupStore: groups}, options,
		scheduler.SnapshotBindings{
			ProviderNotFound: provider.ErrProviderNotFound, GroupNotFound: routing.ErrGroupNotFound, Diagnostics: scheduler.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
		})
}

// provideConcurrencyCache constructs slot and wait counters from required
// scheduling configuration. It chooses the longer sticky or fallback wait,
// truncating its seconds into an integer used by the wait-counter cache.
//
// A nonpositive result selects slot TTL minutes multiplied by sixty, while slot
// expiry separately receives the configured minutes. A positive subsecond wait
// can truncate to zero and take that same fallback, so callers account for
// whole-second precision when configuring the counter lifetime.
func provideConcurrencyCache(rdb *redis.Client, cfg *config.Config) scheduler.ConcurrencyCache {
	// Wait counters cover the longer configured waiting path. Whole seconds
	// are used by the Redis cache, with the slot-lifetime fallback applied below after nonpositive truncation.
	ttl := int(cfg.Gateway.Scheduling.StickySessionWaitTimeout.Seconds())
	if cfg.Gateway.Scheduling.FallbackWaitTimeout > cfg.Gateway.Scheduling.StickySessionWaitTimeout {
		ttl = int(cfg.Gateway.Scheduling.FallbackWaitTimeout.Seconds())
	}
	if ttl <= 0 {
		ttl = cfg.Gateway.ConcurrencySlotTTLMinutes * 60
	}
	return schedulerredis.NewConcurrencyCache(rdb, cfg.Gateway.ConcurrencySlotTTLMinutes, ttl)
}

// provideConcurrency constructs shared slot management and immediately attempts
// stale-process cleanup. Cleanup uses Background and reports failure as a
// warning; a supplied cfg sets the provider-load batch cache lifetime in
// milliseconds before the service is returned.
func provideConcurrency(cache scheduler.ConcurrencyCache, cfg *config.Config) *scheduler.ConcurrencyService {
	core := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	// Stale slots can remain from a prior process. Attempt their cleanup during
	// construction, then warn on failure while leaving periodic lifecycle maintenance available.
	if err := core.CleanupStaleProcessSlots(context.Background()); err != nil {
		logging.LegacyPrintf("service.concurrency", "Warning: startup cleanup stale process slots failed: %v", err)
	}
	if cfg != nil {
		// Provider load reuse receives the configured cache lifetime in milliseconds.
		core.SetProviderLoadBatchCacheTTL(time.Duration(cfg.Gateway.Scheduling.LoadBatchCacheTTLMS) * time.Millisecond)
	}
	return core
}

// provideSessionCache constructs session admission with a five-minute idle
// input by default. A positive configured idle timeout replaces it; nil cfg or
// nonpositive minutes retain that default for the Redis adapter.
func provideSessionCache(rdb *redis.Client, cfg *config.Config) scheduler.SessionLimitCache {
	minutes := 5
	if cfg != nil && cfg.Gateway.SessionIdleTimeoutMinutes > 0 {
		minutes = cfg.Gateway.SessionIdleTimeoutMinutes
	}
	return schedulerredis.NewSessionLimitCache(rdb, minutes)
}

// provideMessageQueue binds user queue and RPM storage with required configured
// lock/delay options.
func provideMessageQueue(cache scheduler.UserMsgQueueCache, rpm scheduler.RPMCache, cfg *config.Config) *scheduler.UserMessageQueueService {
	v := cfg.Gateway.UserMessageQueue
	return scheduler.NewUserMessageQueueService(cache, rpm, &scheduler.MessageQueueOptions{LockTTLMs: v.LockTTLMs, MinDelayMs: v.MinDelayMs, MaxDelayMs: v.MaxDelayMs}, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
}

// provideSelectionSnapshots returns a cache reader or nil when SnapshotService
// is absent. This keeps optional source capability as a nil interface instead
// of wrapping an unavailable service for selection.
func provideSelectionSnapshots(source *scheduler.SnapshotService) selection.Snapshots {
	if source == nil {
		return nil
	}
	return schedulerredis.NewSnapshotReader(source)
}

// schedulerParameterDefaults returns built-in inputs for nil cfg and configured
// weights otherwise. A positive configured Top-K replaces its default; runtime
// EWMA values pass through, and sticky escape is normalized by scheduler
// policy.
func schedulerParameterDefaults(cfg *config.Config) scheduler.ParameterDefaults {
	defaults := scheduler.DefaultParameters()
	if cfg == nil {
		return defaults
	}
	value := cfg.Gateway.AdvancedScheduler
	if value.LBTopK > 0 {
		defaults.TopK = value.LBTopK
	}
	weights := value.ScoreWeights
	defaults.Weights = policy.ScoreWeights{Priority: weights.Priority, Load: weights.Load, Queue: weights.Queue, ErrorRate: weights.ErrorRate, TTFT: weights.TTFT, Reset: weights.Reset, QuotaHeadroom: weights.QuotaHeadroom, Previous: weights.PreviousResponse, SessionSticky: weights.SessionSticky}
	defaults.Runtime.EwmaErrorRateAlpha = value.EWMAErrorRateAlpha
	defaults.Runtime.EwmaTTFTAlpha = value.EWMATTFTAlpha
	defaults.Runtime.StickyEscape = policy.NormalizeStickyEscape(policy.StickyEscapeConfig{Enabled: value.StickyEscapeEnabled, TtftMs: float64(value.StickyEscapeTTFTMs), ErrorRate: value.StickyEscapeErrorRate})
	return defaults
}

// provideSchedulerSharedState constructs feedback, settings, sticky statistics,
// and parameter reads once. Parameters retains the constructed Settings
// instance with the supplied repository and process defaults, sharing
// publication inputs across later consumers.
func provideSchedulerSharedState(cfg *config.Config, source settings.Repository) *schedulerSharedState {
	state := &schedulerSharedState{Feedback: scheduler.NewRuntimeStats(time.Now), Settings: scheduler.NewSettingsRuntime(scheduler.Diagnostics{
		Logf: logging.LegacyPrintf, Event: logging.Event,
	},
	), Sticky: &scheduler.StickyStats{}}
	state.Parameters = scheduler.NewParameters(state.Settings, source, schedulerParameterDefaults(cfg))
	return state
}
