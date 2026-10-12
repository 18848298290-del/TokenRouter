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

// schedulerSharedState holds scheduling feedback, runtime parameters, and sticky-session statistics shared across platforms.
type schedulerSharedState struct {
	Feedback   *scheduler.RuntimeStats
	Settings   *scheduler.SettingsRuntime
	Parameters *scheduler.Parameters
	Sticky     *scheduler.StickyStats
}

func provideSchedulerCache(rdb *redis.Client, cfg *config.Config) *schedulerredis.SnapshotCache {
	options := schedulerredis.SnapshotCacheOptions{}
	if cfg != nil {
		options.MGetChunkSize = cfg.Gateway.Scheduling.SnapshotMGetChunkSize
		options.WriteChunkSize = cfg.Gateway.Scheduling.SnapshotWriteChunkSize
	}
	return schedulerredis.NewSnapshotCache(rdb, codec.ProviderCodec{}, options)
}

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

func provideConcurrencyCache(rdb *redis.Client, cfg *config.Config) scheduler.ConcurrencyCache {
	ttl := int(cfg.Gateway.Scheduling.StickySessionWaitTimeout.Seconds())
	if cfg.Gateway.Scheduling.FallbackWaitTimeout > cfg.Gateway.Scheduling.StickySessionWaitTimeout {
		ttl = int(cfg.Gateway.Scheduling.FallbackWaitTimeout.Seconds())
	}
	if ttl <= 0 {
		ttl = cfg.Gateway.ConcurrencySlotTTLMinutes * 60
	}
	return schedulerredis.NewConcurrencyCache(rdb, cfg.Gateway.ConcurrencySlotTTLMinutes, ttl)
}

func provideConcurrency(cache scheduler.ConcurrencyCache, cfg *config.Config) *scheduler.ConcurrencyService {
	core := scheduler.NewConcurrencyService(cache, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	// Clean up slots left by a previous process now; lifecycle startup starts periodic cleanup.
	if err := core.CleanupStaleProcessSlots(context.Background()); err != nil {
		logging.LegacyPrintf("service.concurrency", "Warning: startup cleanup stale process slots failed: %v", err)
	}
	if cfg != nil {
		core.SetProviderLoadBatchCacheTTL(time.Duration(cfg.Gateway.Scheduling.LoadBatchCacheTTLMS) * time.Millisecond)
	}
	return core
}

func provideSessionCache(rdb *redis.Client, cfg *config.Config) scheduler.SessionLimitCache {
	minutes := 5
	if cfg != nil && cfg.Gateway.SessionIdleTimeoutMinutes > 0 {
		minutes = cfg.Gateway.SessionIdleTimeoutMinutes
	}
	return schedulerredis.NewSessionLimitCache(rdb, minutes)
}

func provideMessageQueue(cache scheduler.UserMsgQueueCache, rpm scheduler.RPMCache, cfg *config.Config) *scheduler.UserMessageQueueService {
	v := cfg.Gateway.UserMessageQueue
	return scheduler.NewUserMessageQueueService(cache, rpm, &scheduler.MessageQueueOptions{LockTTLMs: v.LockTTLMs, MinDelayMs: v.MinDelayMs, MaxDelayMs: v.MaxDelayMs}, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
}

// provideSelectionSnapshots returns a snapshot adapter, or a nil interface when the source is absent.
func provideSelectionSnapshots(source *scheduler.SnapshotService) selection.Snapshots {
	if source == nil {
		return nil
	}
	return schedulerredis.NewSnapshotReader(source)
}

// schedulerParameterDefaults overlays process configuration on scheduler defaults, retaining default TopK when configured nonpositive.
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

func provideSchedulerSharedState(cfg *config.Config, source settings.Repository) *schedulerSharedState {
	state := &schedulerSharedState{Feedback: scheduler.NewRuntimeStats(time.Now), Settings: scheduler.NewSettingsRuntime(scheduler.Diagnostics{
		Logf: logging.LegacyPrintf, Event: logging.Event,
	},
	), Sticky: &scheduler.StickyStats{}}
	state.Parameters = scheduler.NewParameters(state.Settings, source, schedulerParameterDefaults(cfg))
	return state
}
