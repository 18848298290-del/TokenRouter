package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// TimingHook records command durations for requests with an active Server-Timing collector.
type TimingHook struct{}

// NewClient creates a Redis client and optionally installs TimingHook.
func NewClient(options *redis.Options, enableTiming bool) *redis.Client {
	client := redis.NewClient(options)
	if enableTiming {
		client.AddHook(TimingHook{})
	}
	return client
}

// DialHook passes connection setup through without recording it.
func (TimingHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook records one command and its duration.
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

// ProcessPipelineHook records the pipeline duration and number of commands.
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
