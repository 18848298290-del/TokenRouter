package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// crsShadowStore holds provider records for shadow tests.
type crsShadowStore struct {
	rows map[int64]*Record
}

// windowPairReader records query counts and simulates failure or cancellation.
type windowPairReader struct {
	LocalUsageStats
	pairs    int
	singles  int
	starts   []time.Time
	failPair bool
	cancel   context.CancelFunc
}

// lifecycleRefreshExecutor controls whether refresh exits on cancellation or waits for release.
type lifecycleRefreshExecutor struct {
	started      chan struct{}
	release      chan struct{}
	ignoreCancel bool
	calls        atomic.Int32
}

type lifecycleRefreshRepository struct{ writes atomic.Int32 }

func attachCNMonitorLimits(provider *Record, observedAt time.Time, limits []UpstreamUsageLimit) {
	if provider.Extra == nil {
		provider.Extra = make(map[string]any)
	}
	queryConfig, err := EffectiveUpstreamUsageConfig(provider)
	if err != nil {
		panic(err)
	}
	queryConfig.Adapter = CNUpstreamUsageAdapterName(provider)
	provider.Extra[CNUsageMonitorSnapshotExtraKey] = &CNUsageMonitorSnapshot{
		Version:       CNUsageMonitorSnapshotVersion,
		Adapter:       queryConfig.Adapter,
		IdentityHash:  CNUsageMonitorIdentityFingerprint(provider),
		Provider:      provider.Platform,
		Mode:          "limits",
		Unit:          "PERCENT",
		Limits:        limits,
		ObservedAt:    &observedAt,
		LastAttemptAt: observedAt,
	}
}

func cnCodingTestProvider(platform string) *Record {
	return &Record{
		ID:       1,
		Platform: platform,
		Type:     capability.ProviderTypeAPIKey,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"api_key":       "sk-test",
			"provider_mode": ProviderModeCoding,
		},
		Extra: map[string]any{},
	}
}

func newCRSShadowStore() *crsShadowStore {
	return &crsShadowStore{rows: make(map[int64]*Record)}
}

func (s *crsShadowStore) Create(_ context.Context, record *Record) error {
	record.ID = int64(len(s.rows) + 1)
	s.rows[record.ID] = record
	return nil
}

func (s *crsShadowStore) GetByID(_ context.Context, id int64) (*Record, error) {
	return s.rows[id], nil
}

func (s *crsShadowStore) ListShadowsByParent(_ context.Context, id int64) ([]*Record, error) {
	var rows []*Record
	for _, row := range s.rows {
		if row.ParentProviderID != nil && *row.ParentProviderID == id {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (s *crsShadowStore) Update(_ context.Context, record *Record) error {
	s.rows[record.ID] = record
	return nil
}

func (s *crsShadowStore) UpdateConfiguration(ctx context.Context, record *Record, _ ConfigurationChange) error {
	return s.Update(ctx, record)
}

// GetProviderWindowStatsPair records a combined query and can return an error or cancel its caller.
func (r *windowPairReader) GetProviderWindowStatsPair(_ context.Context, _ int64, a, b time.Time) (*WindowStats, *WindowStats, error) {
	r.pairs++
	r.starts = []time.Time{a, b}
	if r.cancel != nil {
		r.cancel()
		return nil, nil, context.Canceled
	}
	if r.failPair {
		return nil, nil, errors.New("pair unavailable")
	}
	value := &WindowStats{Requests: 7}
	return value, value, nil
}

// GetProviderWindowStats fails its second call to simulate a partially available window pair.
func (r *windowPairReader) GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error) {
	r.singles++
	if r.singles == 2 {
		return nil, errors.New("second unavailable")
	}
	return &WindowStats{Requests: 1}, nil
}

func (e *lifecycleRefreshExecutor) CacheKey(*Record) string { return "lifecycle:provider" }

func (e *lifecycleRefreshExecutor) CanRefresh(*Record) bool { return true }

func (e *lifecycleRefreshExecutor) NeedsRefresh(*Record, time.Duration) bool { return true }

func (e *lifecycleRefreshExecutor) Refresh(ctx context.Context, _ *Record) (map[string]any, error) {
	if e.calls.Add(1) == 1 {
		close(e.started)
	}
	if e.ignoreCancel {
		<-e.release
		return map[string]any{"access_token": "late"}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return map[string]any{"access_token": "new"}, nil
	}
}

func (*lifecycleRefreshRepository) GetByID(context.Context, int64) (*Record, error) {
	return &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Status: StatusActive}, nil
}

func (r *lifecycleRefreshRepository) UpdateOAuthCredentialsIfUnchanged(context.Context, CredentialVersion, map[string]any) (bool, error) {
	r.writes.Add(1)
	return true, nil
}

func waitUsageSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("查询未到达预期阶段")
	}
}
