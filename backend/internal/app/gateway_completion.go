package app

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
)

// gateway_completion.go converts completion queue configuration into
// worker-pool options and binds completion telemetry.
// Completion recording uses configured queue capacity, task budgets, and
// overflow handling. Assembly starts from worker-pool defaults, applies fields
// according to their zero-value rules, and normalizes the result before binding
// completion telemetry to the pool.

// usageRecordPoolOptionsFromConfig returns defaults for nil cfg and normalized
// overrides otherwise. Positive capacity and step fields replace defaults;
// sample percentage, down threshold, and cooldown accept zero, while
// AutoScaleEnabled is copied directly and overflow policy is trimmed and
// lowercased.
func usageRecordPoolOptionsFromConfig(cfg *config.Config) completion.UsageRecordWorkerPoolOptions {
	opts := completion.DefaultOptions()
	if cfg == nil {
		return opts
	}
	if cfg.Gateway.UsageRecord.WorkerCount > 0 {
		opts.WorkerCount = cfg.Gateway.UsageRecord.WorkerCount
	}
	if cfg.Gateway.UsageRecord.QueueSize > 0 {
		opts.QueueSize = cfg.Gateway.UsageRecord.QueueSize
	}
	if cfg.Gateway.UsageRecord.TaskTimeoutSeconds > 0 {
		// Convert configured seconds into each completion task's execution budget.
		opts.TaskTimeout = time.Duration(cfg.Gateway.UsageRecord.TaskTimeoutSeconds) * time.Second
	}
	if policy := strings.TrimSpace(strings.ToLower(cfg.Gateway.UsageRecord.OverflowPolicy)); policy != "" {
		opts.OverflowPolicy = policy
	}
	if cfg.Gateway.UsageRecord.OverflowSamplePercent >= 0 {
		opts.OverflowSamplePercent = cfg.Gateway.UsageRecord.OverflowSamplePercent
	}
	opts.AutoScaleEnabled = cfg.Gateway.UsageRecord.AutoScaleEnabled
	if cfg.Gateway.UsageRecord.AutoScaleMinWorkers > 0 {
		opts.AutoScaleMinWorkers = cfg.Gateway.UsageRecord.AutoScaleMinWorkers
	}
	if cfg.Gateway.UsageRecord.AutoScaleMaxWorkers > 0 {
		opts.AutoScaleMaxWorkers = cfg.Gateway.UsageRecord.AutoScaleMaxWorkers
	}
	if cfg.Gateway.UsageRecord.AutoScaleUpQueuePercent > 0 {
		opts.AutoScaleUpPercent = cfg.Gateway.UsageRecord.AutoScaleUpQueuePercent
	}
	if cfg.Gateway.UsageRecord.AutoScaleDownQueuePercent >= 0 {
		opts.AutoScaleDownPercent = cfg.Gateway.UsageRecord.AutoScaleDownQueuePercent
	}
	if cfg.Gateway.UsageRecord.AutoScaleUpStep > 0 {
		opts.AutoScaleUpStep = cfg.Gateway.UsageRecord.AutoScaleUpStep
	}
	if cfg.Gateway.UsageRecord.AutoScaleDownStep > 0 {
		opts.AutoScaleDownStep = cfg.Gateway.UsageRecord.AutoScaleDownStep
	}
	if cfg.Gateway.UsageRecord.AutoScaleCheckIntervalSeconds > 0 {
		// Scaling checks use a duration expressed in configured seconds.
		opts.AutoScaleInterval = time.Duration(cfg.Gateway.UsageRecord.AutoScaleCheckIntervalSeconds) * time.Second
	}
	if cfg.Gateway.UsageRecord.AutoScaleCooldownSeconds >= 0 {
		// The cooldown duration spaces scaling changes in configured seconds.
		opts.AutoScaleCooldown = time.Duration(cfg.Gateway.UsageRecord.AutoScaleCooldownSeconds) * time.Second
	}
	return completion.NormalizeOptions(opts)
}

// provideUsageRecordWorkerPool constructs the bounded completion pool with
// application telemetry. The downstream constructor creates its pond pool
// immediately; lifecycle Start begins periodic scaling, and lifecycle
// StopContext waits before the task dependencies are closed.
func provideUsageRecordWorkerPool(cfg *config.Config) *completion.UsageRecordWorkerPool {
	opts := usageRecordPoolOptionsFromConfig(cfg)
	opts.Observe = telemetry.Completion
	return completion.NewUsageRecordWorkerPoolWithOptions(opts)
}
