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

// CRS fixtures retain the exact Record pointers supplied to storage, so callers
// observe mutations made through borrowed references. Refresh fixtures can hold
// an exchange after cancellation until a test releases it, letting shutdown
// tests distinguish a returned timeout from a completed exchange and suppressed
// credential writes.

// crsShadowStore retains shared record addresses for CRS shadow test fixtures.
type crsShadowStore struct {
	// rows retains original record pointers supplied by Create and Update. GetByID
	// and shadow queries expose those same addresses to tests.
	rows map[int64]*Record
}

// windowPairReader controls paired usage queries and records their fallback
// observations.
type windowPairReader struct {
	LocalUsageStats             // unused usage interface methods
	pairs           int         // combined window query count
	singles         int         // single window query count
	starts          []time.Time // last requested window starts
	failPair        bool        // combined query failure selection
	// cancel is invoked by a paired query before its failure selection. Nil leaves
	// that cancellation scenario disabled for the fixture.
	cancel context.CancelFunc
}

// lifecycleRefreshExecutor controls cancellation and release of test refresh
// exchanges.
type lifecycleRefreshExecutor struct {
	// started closes on the first Refresh call, while release lets waiting
	// exchanges complete. Tests observe started before requesting shutdown, then
	// close release to unblock delayed work.
	started chan struct{} // first exchange start signal
	release chan struct{} // test-controlled exchange completion signal
	// ignoreCancel makes Refresh wait for release after shutdown cancellation.
	// False lets the exchange choose release or the context error.
	ignoreCancel bool
	calls        atomic.Int32 // atomic refresh invocation count
}

// lifecycleRefreshRepository records attempted credential writes after
// controlled refresh exchanges.
type lifecycleRefreshRepository struct {
	writes atomic.Int32 // atomic credential write count
}

// attachCNMonitorLimits installs a CN limit snapshot matching the provider's
// current usage identity. It retains the supplied limits slice, copies the
// observation time value, and panics when fixture query configuration is
// invalid.
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

// cnCodingTestProvider creates an active API Key fixture using the selected CN
// coding platform.
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

// newCRSShadowStore creates an empty shared-record store for CRS shadow
// fixtures.
func newCRSShadowStore() *crsShadowStore {
	return &crsShadowStore{rows: make(map[int64]*Record)}
}

// Create assigns a fixture ID and retains the supplied record pointer in rows.
// Later caller mutations affect the same stored address, so this fixture models
// shared in-memory records rather than persistence isolation.
func (s *crsShadowStore) Create(_ context.Context, record *Record) error {
	record.ID = int64(len(s.rows) + 1)
	s.rows[record.ID] = record
	return nil
}

// GetByID returns the stored record address or nil when the fixture has no
// matching ID. Returned records remain shared with rows and can be changed by
// the test.
func (s *crsShadowStore) GetByID(_ context.Context, id int64) (*Record, error) {
	return s.rows[id], nil
}

// ListShadowsByParent selects stored children matching the parent identifier.
// Returned pointers remain shared with rows, and an unmatched parent returns
// nil records without treating that absence as an error.
func (s *crsShadowStore) ListShadowsByParent(_ context.Context, id int64) ([]*Record, error) {
	var rows []*Record
	for _, row := range s.rows {
		if row.ParentProviderID != nil && *row.ParentProviderID == id {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// Update stores the supplied record address under its provider ID. It performs
// no clone, so fixture readers and the caller observe subsequent mutations
// through that shared pointer.
func (s *crsShadowStore) Update(_ context.Context, record *Record) error {
	s.rows[record.ID] = record
	return nil
}

// UpdateConfiguration delegates to the fixture's record replacement and ignores
// the field selection. This test store keeps shared addresses and supplies no
// latest-row merge or transaction behavior.
func (s *crsShadowStore) UpdateConfiguration(ctx context.Context, record *Record, _ ConfigurationChange) error {
	return s.Update(ctx, record)
}

// GetProviderWindowStatsPair records both window starts and returns one shared
// stats pointer for both results. Cancellation runs before the configured pair
// failure and returns context.Canceled to exercise query short-circuiting.
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

// GetProviderWindowStats returns one request except on its second call, which
// fails. The counters let paired-query tests detect two single-window fallbacks
// and distinguish a partial fallback result.
func (r *windowPairReader) GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error) {
	r.singles++
	if r.singles == 2 {
		return nil, errors.New("second unavailable")
	}
	return &WindowStats{Requests: 1}, nil
}

// CacheKey returns the fixed lock identity used by lifecycle refresh tests.
func (e *lifecycleRefreshExecutor) CacheKey(*Record) string { return "lifecycle:provider" }

// CanRefresh accepts every supplied provider for controlled lifecycle refresh
// tests.
func (e *lifecycleRefreshExecutor) CanRefresh(*Record) bool { return true }

// NeedsRefresh reports every fixture provider due for its controlled refresh
// exchange.
func (e *lifecycleRefreshExecutor) NeedsRefresh(*Record, time.Duration) bool { return true }

// Refresh signals the first invocation and waits for test release or
// cancellation. With ignoreCancel enabled, it waits for release even after
// context cancellation and returns a late token to exercise shutdown write
// suppression.
//
// The invocation counter prevents repeated closure of started when calls
// overlap. The cooperative path returns the context error when cancellation
// wins, or a new token when release wins. Tests control the channels so the
// caller can observe an exchange that remains active across its shutdown
// budget.
func (e *lifecycleRefreshExecutor) Refresh(ctx context.Context, _ *Record) (map[string]any, error) {
	// Multiple exchanges can share this fixture. Count atomically so exactly
	// the first invocation closes the start channel and later invocations
	// cannot close it again.
	if e.calls.Add(1) == 1 {
		close(e.started)
	}
	// Hold the exchange across cancellation to reproduce an upstream call that
	// outlives shutdown, then let the test release its late credential response
	// deliberately.
	if e.ignoreCancel {
		<-e.release
		return map[string]any{"access_token": "late"}, nil
	}
	// The cooperative fixture returns as soon as cancellation wins over
	// release, allowing shutdown tests to distinguish cancelled exchange work
	// from late successful work.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return map[string]any{"access_token": "new"}, nil
	}
}

// GetByID returns a fresh active OpenAI OAuth fixture for refresh shutdown
// tests.
func (*lifecycleRefreshRepository) GetByID(context.Context, int64) (*Record, error) {
	return &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Status: StatusActive}, nil
}

// UpdateOAuthCredentialsIfUnchanged counts the attempted conditional credential
// write and reports success.
func (r *lifecycleRefreshRepository) UpdateOAuthCredentialsIfUnchanged(context.Context, CredentialVersion, map[string]any) (bool, error) {
	r.writes.Add(1)
	return true, nil
}

// waitUsageSignal waits up to one second for a test phase signal. It marks the
// caller as the failure location and fails the test when coordination does not
// reach the expected phase.
func waitUsageSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	// Bound the phase wait so broken coordination fails the test promptly.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("查询未到达预期阶段")
	}
}
