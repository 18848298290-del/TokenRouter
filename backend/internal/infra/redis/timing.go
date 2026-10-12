package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// Redis commands contribute timing only when their request context carries a
// collector. Hooks observe command and pipeline execution even when it fails,
// recording the whole pipeline interval with its command count while
// connection setup passes through the original dial hook.

// TimingHook records command durations for requests with an active Server-Timing collector.
type TimingHook struct{}

// NewClient constructs a Redis client and installs TimingHook when
// enableTiming is true. The caller owns connectivity checks and closing the
// returned client; constructing it does not perform a Ping.
func NewClient(options *redis.Options, enableTiming bool) *redis.Client {
	client := redis.NewClient(options)
	if enableTiming {
		client.AddHook(TimingHook{})
	}
	return client
}

// DialHook returns next unchanged so connection setup retains its original
// behavior.
func (TimingHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook returns a wrapper that records one completed Redis command for
// an active collector. It returns next's error unchanged and passes inactive
// contexts through directly, keeping command arguments out of recorded
// metrics.
func (TimingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if !servertiming.Active(ctx) {
			return next(ctx, cmd)
		}
		startedAt := time.Now()
		err := next(ctx, cmd)
		servertiming.Record(ctx, servertiming.MetricRedis, startedAt, time.Now(), 1)
		return err
	}
}

// ProcessPipelineHook returns a wrapper recording an active pipeline's
// interval and command count, including failures. Inactive contexts pass
// through directly; an empty pipeline's zero count follows Collector.Record's
// minimum-one counting rule.
func (TimingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if !servertiming.Active(ctx) {
			return next(ctx, cmds)
		}
		startedAt := time.Now()
		err := next(ctx, cmds)
		servertiming.Record(ctx, servertiming.MetricRedis, startedAt, time.Now(), len(cmds))
		return err
	}
}
