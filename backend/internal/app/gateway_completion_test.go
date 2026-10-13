package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
)

// Completion-pool configuration treats zero values differently across capacity
// and scaling fields. The scenarios check disabled-scaling bounds, construction
// with a configured worker count, and preservation of the default options when
// the application configuration is absent.

// TestUsageRecordWorkerPool_OptionsFromConfig_AutoScaleDisabled supplies fixed
// capacity with conflicting scale bounds. It checks scaling is disabled, both
// bounds become the worker count, and the configured task timeout is converted
// to seconds.
func TestUsageRecordWorkerPool_OptionsFromConfig_AutoScaleDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.UsageRecord.WorkerCount = 64
	cfg.Gateway.UsageRecord.QueueSize = 128
	cfg.Gateway.UsageRecord.TaskTimeoutSeconds = 7
	cfg.Gateway.UsageRecord.OverflowPolicy = config.UsageRecordOverflowPolicyDrop
	cfg.Gateway.UsageRecord.OverflowSamplePercent = 0
	cfg.Gateway.UsageRecord.AutoScaleEnabled = false
	cfg.Gateway.UsageRecord.AutoScaleMinWorkers = 1
	cfg.Gateway.UsageRecord.AutoScaleMaxWorkers = 512

	opts := usageRecordPoolOptionsFromConfig(cfg)
	require.False(t, opts.AutoScaleEnabled)
	require.Equal(t, 64, opts.WorkerCount)
	require.Equal(t, 64, opts.AutoScaleMinWorkers)
	require.Equal(t, 64, opts.AutoScaleMaxWorkers)
	require.Equal(t, 7*time.Second, opts.TaskTimeout)
}

// TestNewUsageRecordWorkerPool_FromConfig constructs and starts a three-worker
// completion pool through application assembly. Its reported maximum
// concurrency must match configuration, and test cleanup stops the owned pool.
func TestNewUsageRecordWorkerPool_FromConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.UsageRecord.WorkerCount = 3
	cfg.Gateway.UsageRecord.QueueSize = 16
	cfg.Gateway.UsageRecord.TaskTimeoutSeconds = 2
	cfg.Gateway.UsageRecord.OverflowPolicy = config.UsageRecordOverflowPolicyDrop
	cfg.Gateway.UsageRecord.AutoScaleEnabled = false

	pool := provideUsageRecordWorkerPool(cfg)
	pool.Start()
	t.Cleanup(pool.Stop)

	stats := pool.Stats()
	require.Equal(t, 3, stats.MaxConcurrency)
}

// TestUsageRecordWorkerPool_OptionsFromConfig_NilConfig requests options
// without application configuration. It checks worker and queue capacity, task
// and overflow defaults, and enabled scaling with the default minimum and
// maximum worker bounds.
func TestUsageRecordWorkerPool_OptionsFromConfig_NilConfig(t *testing.T) {
	opts := usageRecordPoolOptionsFromConfig(nil)
	require.Equal(t, completion.DefaultOptions().WorkerCount, opts.WorkerCount)
	require.Equal(t, completion.DefaultOptions().QueueSize, opts.QueueSize)
	require.Equal(t, completion.DefaultOptions().TaskTimeout, opts.TaskTimeout)
	require.Equal(t, completion.DefaultOptions().OverflowPolicy, opts.OverflowPolicy)
	require.Equal(t, completion.DefaultOptions().OverflowSamplePercent, opts.OverflowSamplePercent)
	require.True(t, opts.AutoScaleEnabled)
	require.Equal(t, completion.DefaultOptions().AutoScaleMinWorkers, opts.AutoScaleMinWorkers)
	require.Equal(t, completion.DefaultOptions().AutoScaleMaxWorkers, opts.AutoScaleMaxWorkers)
}
