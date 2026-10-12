package search

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Manager tests separate supplier eligibility and quota ordering from actual
// HTTP execution. An inert quota fixture records confirmed and uncertain
// reservation outcomes, while direct helper cases check date clamping, proxy
// identity override, and storage-absent behavior.

// TestConfirmedReservationReleasesOnlyOnce releases one confirmed reservation
// concurrently after request cancellation. The fixture records one decrement,
// zero remaining usage, and a cleanup deadline detached from the canceled
// request.
func TestConfirmedReservationReleasesOnlyOnce(t *testing.T) {
	q := &quotaFixture{used: 1}
	manager := NewManager(nil, q, noSearchExecutor{}, nil)
	lease := &quotaReservation{manager: manager, config: ProviderConfig{Type: ProviderTypeBrave, QuotaLimit: 10}, acquired: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var wg sync.WaitGroup
	// Overlap sixteen releases of the same confirmed reservation, then join all
	// calls before reading the quota fixture's protected counters.
	for range 16 {
		wg.Go(func() { lease.release(ctx) })
	}
	wg.Wait()
	require.Equal(t, int64(0), q.used)
	require.Equal(t, int64(1), q.decrements)
	require.True(t, q.releaseDeadline)
}

// TestUncertainReservationDoesNotCompensate makes the fixture increment its
// counter but return an unknown-result error. Execution remains allowed with no
// confirmed reservation, and releasing that value issues no decrement.
func TestUncertainReservationDoesNotCompensate(t *testing.T) {
	q := &quotaFixture{uncertain: true}
	manager := NewManager(nil, q, noSearchExecutor{}, nil)
	allowed, acquired := manager.tryReserveQuota(context.Background(), ProviderConfig{Type: ProviderTypeBrave, QuotaLimit: 10})
	require.True(t, allowed)
	require.False(t, acquired)
	lease := &quotaReservation{manager: manager, config: ProviderConfig{Type: ProviderTypeBrave, QuotaLimit: 10}, acquired: acquired}
	lease.release(context.Background())
	require.Zero(t, q.decrements)
}

// TestNewManager_PreservesOrder constructs a Manager with Brave followed by
// Tavily. Returned configuration snapshots retain those supplier positions,
// protecting the order used by administrator testing and unweighted candidates.
func TestNewManager_PreservesOrder(t *testing.T) {
	configs := []ProviderConfig{
		{Type: "brave", APIKey: "k3"},
		{Type: "tavily", APIKey: "k1"},
	}
	m := newTestManager(configs, nil)
	require.Equal(t, "brave", m.ProviderConfigs()[0].Type)
	require.Equal(t, "tavily", m.ProviderConfigs()[1].Type)
}

// TestManager_SearchWithBestProvider_EmptyQuery submits empty and
// whitespace-only production queries. Both calls return the empty-query error
// before any candidate can reach the helper's absent executor.
func TestManager_SearchWithBestProvider_EmptyQuery(t *testing.T) {
	m := newTestManager([]ProviderConfig{{Type: "brave", APIKey: "k"}}, nil)
	_, _, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: ""})
	require.ErrorContains(t, err, "empty search query")

	_, _, err = m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "   "})
	require.ErrorContains(t, err, "empty search query")
}

// TestManager_SearchWithBestProvider_SkipEmptyAPIKey provides a candidate with
// no authentication key. Production search reports unavailable providers after
// filtering instead of attempting execution through the helper's absent
// executor.
func TestManager_SearchWithBestProvider_SkipEmptyAPIKey(t *testing.T) {
	m := newTestManager([]ProviderConfig{{Type: "brave", APIKey: ""}}, nil)
	_, _, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "test"})
	require.ErrorContains(t, err, "no available provider")
}

// TestManager_SearchWithBestProvider_SkipExpired provides a keyed candidate
// whose expiration is one hour earlier. Production search reports unavailable
// providers because the candidate is filtered before supplier execution.
func TestManager_SearchWithBestProvider_SkipExpired(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).Unix()
	m := newTestManager([]ProviderConfig{
		{Type: "brave", APIKey: "k", ExpiresAt: &past},
	}, nil)
	_, _, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "test"})
	require.ErrorContains(t, err, "no available provider")
}

// TestManager_GetUsage_NilRedis reads a supplier counter through a Manager with
// no quota state. It returns zero usage with nil error for storage-absent
// display and ranking.
func TestManager_GetUsage_NilRedis(t *testing.T) {
	m := newTestManager(nil, nil)
	used, err := m.GetUsage(context.Background(), "brave")
	require.NoError(t, err)
	require.Equal(t, int64(0), used)
}

// TestQuotaTTLFromSubscription_NilSubscription passes an absent subscription
// anchor to lifetime calculation. It expects the exact default duration,
// protecting the undated supplier-counter policy independently of monthly-date
// arithmetic.
func TestQuotaTTLFromSubscription_NilSubscription(t *testing.T) {
	ttl := quotaTTLFromSubscription(nil)
	require.Equal(t, defaultQuotaTTL, ttl)
}

// TestQuotaTTLFromSubscription_ZeroSubscription passes a pointer whose stored
// subscription value is zero. It expects the same default lifetime used for an
// absent anchor rather than a Unix-epoch monthly date.
func TestQuotaTTLFromSubscription_ZeroSubscription(t *testing.T) {
	zero := int64(0)
	ttl := quotaTTLFromSubscription(&zero)
	require.Equal(t, defaultQuotaTTL, ttl)
}

// TestQuotaTTLFromSubscription_ValidSubscription anchors a subscription ten
// days before the current clock. Its lifetime falls within the asserted
// fifteen-to-twenty-five-day bounds plus the configured quota buffer.
func TestQuotaTTLFromSubscription_ValidSubscription(t *testing.T) {
	// Use a recent subscription so the next monthly anchor falls within the
	// asserted broad bounds despite different calendar month lengths.
	sub := time.Now().Add(-10 * 24 * time.Hour).Unix()
	ttl := quotaTTLFromSubscription(&sub)
	require.Greater(t, ttl, 15*24*time.Hour) // At least fifteen days remain.
	require.Less(t, ttl, 25*24*time.Hour+quotaTTLBuffer)
}

// TestNextMonthlyReset_SubscribedRecentPast uses the current month's tenth day
// as an anchor. The returned date is no earlier than the sampled now and stays
// below its asserted future bound.
func TestNextMonthlyReset_SubscribedRecentPast(t *testing.T) {
	// The tenth exists in every month, so this fixture needs no day clamping.
	now := time.Now().UTC()
	sub := time.Date(now.Year(), now.Month(), 10, 0, 0, 0, 0, time.UTC)
	next := nextMonthlyReset(sub)
	require.True(t, next.After(now) || next.Equal(now), "next reset should be in the future or now")
	require.True(t, next.Before(now.AddDate(0, 1, 1)))
}

// TestNextMonthlyReset_SubscribedLongAgo anchors six months earlier on the
// first day. The returned reset lies after current UTC time and before the
// test's roughly one-month upper bound.
func TestNextMonthlyReset_SubscribedLongAgo(t *testing.T) {
	// Anchor the subscription to the first day six months ago.
	sub := time.Now().UTC().AddDate(0, -6, 0)
	sub = time.Date(sub.Year(), sub.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := nextMonthlyReset(sub)
	require.True(t, next.After(time.Now().UTC()))
	// The next monthly date falls within roughly one month.
	require.True(t, next.Before(time.Now().UTC().AddDate(0, 1, 1)))
}

// TestNextMonthlyReset_FutureSubscription supplies a subscription five days
// ahead of current UTC time. The result remains in the future; this assertion
// does not constrain its exact calendar day.
func TestNextMonthlyReset_FutureSubscription(t *testing.T) {
	sub := time.Now().UTC().AddDate(0, 0, 5)
	next := nextMonthlyReset(sub)
	require.True(t, next.After(time.Now().UTC()))
}

// TestAddMonthsClamped_Jan31ToFeb adds one month to January thirty-first in
// 2026. Month and day assertions place the result on February twenty-eighth
// rather than a normalized March date.
func TestAddMonthsClamped_Jan31ToFeb(t *testing.T) {
	sub := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	next := addMonthsClamped(sub, 1)
	require.Equal(t, time.Month(2), next.Month())
	require.Equal(t, 28, next.Day()) // February 2026 has 28 days.
}

// TestAddMonthsClamped_Jan31ToFebLeapYear adds one month to January
// thirty-first in leap-year 2028. Month and day assertions retain February and
// select its twenty-ninth day as the clamp.
func TestAddMonthsClamped_Jan31ToFebLeapYear(t *testing.T) {
	sub := time.Date(2028, 1, 31, 0, 0, 0, 0, time.UTC)
	next := addMonthsClamped(sub, 1)
	require.Equal(t, time.Month(2), next.Month())
	require.Equal(t, 29, next.Day()) // February 2028 has 29 days.
}

// TestAddMonthsClamped_Mar31ToApr adds one month to March thirty-first in 2026.
// The result remains in April and clamps to its thirtieth day instead of
// spilling into May.
func TestAddMonthsClamped_Mar31ToApr(t *testing.T) {
	sub := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	next := addMonthsClamped(sub, 1)
	require.Equal(t, time.Month(4), next.Month())
	require.Equal(t, 30, next.Day()) // April has 30 days.
}

// TestAddMonthsClamped_NormalDay adds one month to January fifteenth in 2026.
// The result advances into February while retaining the fifteenth because that
// calendar day exists in the target month.
func TestAddMonthsClamped_NormalDay(t *testing.T) {
	sub := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	next := addMonthsClamped(sub, 1)
	require.Equal(t, time.Month(2), next.Month())
	require.Equal(t, 15, next.Day()) // The fifteenth exists in the target month.
}

// TestIsProviderAvailable_EmptyAPIKey checks an entry with empty credentials
// directly against the eligibility helper. The result is false, protecting key
// presence independently of quota or proxy filtering.
func TestIsProviderAvailable_EmptyAPIKey(t *testing.T) {
	m := newTestManager(nil, nil)
	require.False(t, m.isProviderAvailable(ProviderConfig{APIKey: ""}))
}

// TestIsProviderAvailable_Expired checks a keyed entry whose expiration is one
// hour earlier. The eligibility helper returns false before search ordering or
// atomic quota admission can consider it.
func TestIsProviderAvailable_Expired(t *testing.T) {
	m := newTestManager(nil, nil)
	past := time.Now().Add(-1 * time.Hour).Unix()
	require.False(t, m.isProviderAvailable(ProviderConfig{APIKey: "k", ExpiresAt: &past}))
}

// TestIsProviderAvailable_Valid checks both a future expiration and an omitted
// expiration with keys present. Both candidates remain available, protecting
// optional expiration behavior used before search selection.
func TestIsProviderAvailable_Valid(t *testing.T) {
	m := newTestManager(nil, nil)
	future := time.Now().Add(1 * time.Hour).Unix()
	require.True(t, m.isProviderAvailable(ProviderConfig{APIKey: "k", ExpiresAt: &future}))
	require.True(t, m.isProviderAvailable(ProviderConfig{APIKey: "k"})) // No expiration is configured.
}

// TestResolveProxyID_ProviderProxyOverrides uses a configured proxy ID with and
// without a request override. The override yields zero marker identity; absence
// returns the configured ID unchanged.
func TestResolveProxyID_ProviderProxyOverrides(t *testing.T) {
	cfg := ProviderConfig{ProxyID: 42}
	require.Equal(t, int64(0), resolveProxyID(cfg, "http://provider-proxy:8080"))
	require.Equal(t, int64(42), resolveProxyID(cfg, ""))
}

// TestIsProxyAvailable_NilRedis checks a positive configured proxy ID with
// absent quota state. It remains available, protecting Manager's storage-absent
// policy before a supplier attempt is ranked.
func TestIsProxyAvailable_NilRedis(t *testing.T) {
	m := newTestManager(nil, nil)
	require.True(t, m.isProxyAvailable(context.Background(), 42))
}

// TestIsProxyAvailable_ZeroID checks zero proxy identity through a Manager
// without storage. It is accepted as available, matching the
// no-configured-marker identity used by direct or overridden proxy selection.
func TestIsProxyAvailable_ZeroID(t *testing.T) {
	m := newTestManager(nil, nil)
	require.True(t, m.isProxyAvailable(context.Background(), 0))
}

// TestSelectByQuotaWeight_NoQuotaLast mixes an unlimited candidate with a
// positive-limit candidate while storage reports zero. The positive remainder
// ranks first, and the unlimited candidate stays in the returned list.
func TestSelectByQuotaWeight_NoQuotaLast(t *testing.T) {
	m := newTestManager(nil, nil)
	candidates := []ProviderConfig{
		{Type: "brave", APIKey: "k1", QuotaLimit: 0},
		{Type: "tavily", APIKey: "k2", QuotaLimit: 100},
	}
	result := m.selectByQuotaWeight(context.Background(), candidates)
	require.Len(t, result, 2)
	require.Equal(t, "tavily", result[0].Type)
	require.Equal(t, "brave", result[1].Type)
}

// TestSelectByQuotaWeight_AllNoQuota supplies two unlimited candidates to
// selection. The returned list retains both entries; this test asserts count
// and does not assert their individual output positions.
func TestSelectByQuotaWeight_AllNoQuota(t *testing.T) {
	m := newTestManager(nil, nil)
	candidates := []ProviderConfig{
		{Type: "brave", APIKey: "k1", QuotaLimit: 0},
		{Type: "tavily", APIKey: "k2", QuotaLimit: 0},
	}
	result := m.selectByQuotaWeight(context.Background(), candidates)
	require.Len(t, result, 2)
}

// TestSelectByQuotaWeight_Empty supplies no candidates to quota selection. The
// returned collection is empty, allowing production search's earlier
// eligibility check to report the unavailable-provider result separately.
func TestSelectByQuotaWeight_Empty(t *testing.T) {
	m := newTestManager(nil, nil)
	result := m.selectByQuotaWeight(context.Background(), nil)
	require.Empty(t, result)
}

// TestManager_ResetUsage_NilRedis resets a literal Brave counter through a
// Manager without state. It returns nil error, protecting the configured
// storage-absent administrator reset behavior.
func TestManager_ResetUsage_NilRedis(t *testing.T) {
	m := newTestManager(nil, nil)
	err := m.ResetUsage(context.Background(), "brave")
	require.NoError(t, err)
}

// newTestManager copies configs into a Manager with nil state and executor. Its
// second argument is ignored; tests use it for pure selection or rejection
// paths that cannot execute suppliers.
func newTestManager(configs []ProviderConfig, _ any) *Manager {
	return NewManager(configs, nil, nil, nil)
}
