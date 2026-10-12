package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type freeQuotaReaderFixture struct {
	usage.UsageLogRepository
	calls atomic.Int64
}

func (f *freeQuotaReaderFixture) GetProviderWindowStatsBatch(_ context.Context, _ []int64, _ time.Time) (map[int64]*usage.ProviderStats, error) {
	f.calls.Add(1)
	return map[int64]*usage.ProviderStats{7: {Tokens: 480_000}}, nil
}

// TestFreeQuotaBindingPreservesCacheScopesAndTaskOwner checks that each selector fills its own cache on a miss.
func TestFreeQuotaBindingPreservesCacheScopesAndTaskOwner(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.Grok.FreeQuotaSoftGateEnabled = true
	cfg.Gateway.Grok.FreeQuotaTokenLimit = 500_000
	cfg.Gateway.Grok.FreeQuotaSoftGatePercent = 95
	cfg.Gateway.Grok.FreeQuotaWindowHours = 24
	cfg.Gateway.Grok.FreeQuotaStatsCacheSeconds = 60
	tasks := lifecycle.NewTasks()
	reader := &freeQuotaReaderFixture{}
	bound := provideSelectionFreeQuota(cfg, reader, tasks)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		require.NoError(t, tasks.Stop(cleanup))
	})
	candidates := []provider.FreeQuotaCandidate{{ID: 7, Eligible: true}}
	gates := []*provider.FreeQuotaGate{bound.Generic, bound.Compatible, bound.Advanced(), bound.Advanced()}
	for _, gate := range gates {
		require.Empty(t, gate.Blocked(candidates), "首次缺失保持放行")
		require.NoError(t, tasks.Wait(ctx))
		require.True(t, gate.Blocked(candidates)[7])
	}
	require.Equal(t, int64(4), reader.calls.Load())
	// After task shutdown, a cache miss returns no blocked providers and starts no refresh.
	require.NoError(t, tasks.Stop(ctx))
	require.Empty(t, bound.Generic.Blocked([]provider.FreeQuotaCandidate{{ID: 8, Eligible: true}}))
	require.Equal(t, int64(4), reader.calls.Load())
}
