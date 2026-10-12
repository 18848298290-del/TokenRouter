package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy failure retention and quota counter lifetimes.
const (
	proxyUnavailableTTL = 5 * time.Minute
	quotaTTLBuffer      = 24 * time.Hour
	defaultQuotaTTL     = 31*24*time.Hour + quotaTTLBuffer // Used when the subscription date is unset.

)

// ErrProxyUnavailable indicates the search failed due to a proxy connectivity issue.
// Callers can switch the upstream provider when its shared proxy fails.
var ErrProxyUnavailable = errors.New("websearch: proxy unavailable")

// weighted is a provider candidate with computed quota weight.
type weighted struct {
	cfg    ProviderConfig
	weight int64
}

// Manager owns an immutable configuration snapshot that remains valid for in-flight requests after
// replacement.
type Manager struct {
	configs  []ProviderConfig
	state    QuotaState
	executor Executor
	work     *WorkGroup
	retired  atomic.Bool
}

type quotaReservation struct {
	once     sync.Once
	manager  *Manager
	config   ProviderConfig
	acquired bool
}

func NewManager(configs []ProviderConfig, state QuotaState, executor Executor, work *WorkGroup) *Manager {
	if work == nil {
		work = NewWorkGroup()
	}
	return &Manager{configs: CloneProviderConfigs(configs), state: state, executor: executor, work: work}
}

// SearchWithBestProvider tries providers in randomized quota-weighted order and returns the successful
// provider type.
// Failed searches attempt to release confirmed quota reservations; cancellation stops further attempts.
// Search-provider proxies with known IDs are marked unavailable for five minutes on connectivity errors.
// @project-doc docs/architecture/gateway_request_lifecycle.md#moderation_search_boundaries
func (m *Manager) SearchWithBestProvider(ctx context.Context, req SearchRequest) (*SearchResponse, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	done, err := m.work.Begin()
	if err != nil {
		return nil, "", err
	}
	defer done()
	defer m.closeIfRetired()
	if strings.TrimSpace(req.Query) == "" {
		return nil, "", fmt.Errorf("websearch: empty search query")
	}

	candidates := m.filterAvailableProviders(ctx, req.ProxyURL)
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("websearch: no available provider (all exhausted, expired, or proxy unavailable)")
	}

	selected := m.selectByQuotaWeight(ctx, candidates)

	for _, cfg := range selected {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		allowed, incremented := m.tryReserveQuota(ctx, cfg)
		if !allowed {
			continue
		}
		reservation := &quotaReservation{manager: m, config: cfg, acquired: incremented}
		resp, err := m.executor.Search(ctx, cfg, req)
		if err != nil {
			reservation.release(ctx)
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			if m.executor.IsProxyError(err) {
				m.markProxyUnavailable(ctx, cfg, req.ProxyURL)
				if req.ProxyURL != "" {
					// The request proxy is shared by all search providers, so another search
					// provider would use the same failing proxy. Ask the caller to switch upstream providers.
					slog.Warn("websearch: provider proxy error, aborting failover",
						"provider", cfg.Type, "error", err)
					return nil, "", fmt.Errorf("%w: %s", ErrProxyUnavailable, err.Error())
				}
				// A search-provider proxy failure permits trying the next search provider,
				// which may have a different proxy configuration.
				slog.Warn("websearch: provider proxy error, trying next provider",
					"provider", cfg.Type, "error", err)
				continue
			}
			slog.Warn("websearch: provider search failed",
				"provider", cfg.Type, "error", err)
			continue
		}
		return resp, cfg.Type, nil
	}
	return nil, "", fmt.Errorf("websearch: no available provider (all exhausted or failed)")
}

// filterAvailableProviders returns providers that have API keys, are not expired,
// and whose proxies are not marked unavailable.
func (m *Manager) filterAvailableProviders(ctx context.Context, providerProxyURL string) []ProviderConfig {
	var out []ProviderConfig
	for _, cfg := range m.configs {
		if !m.isProviderAvailable(cfg) {
			continue
		}
		proxyID := resolveProxyID(cfg, providerProxyURL)
		if proxyID > 0 && !m.isProxyAvailable(ctx, proxyID) {
			slog.Debug("websearch: proxy marked unavailable, skipping",
				"provider", cfg.Type, "proxy_id", proxyID)
			continue
		}
		out = append(out, cfg)
	}
	return out
}

// selectByQuotaWeight ranks candidates with positive remaining quota ahead of other candidates.
// Unlimited and exhausted candidates follow in configuration order.
// Positive weights are multiplied by a random factor; reservation enforces the actual limit.
func (m *Manager) selectByQuotaWeight(ctx context.Context, candidates []ProviderConfig) []ProviderConfig {
	items := m.computeWeights(ctx, candidates)
	withQuota, withoutQuota := partitionByQuota(items)
	sortByStableRandomWeight(withQuota)
	return mergeWeightedResults(withQuota, withoutQuota, len(candidates))
}

func (m *Manager) computeWeights(ctx context.Context, candidates []ProviderConfig) []weighted {
	items := make([]weighted, 0, len(candidates))
	for _, cfg := range candidates {
		w := int64(0)
		if cfg.QuotaLimit > 0 {
			used, _ := m.GetUsage(ctx, cfg.Type)
			if remaining := cfg.QuotaLimit - used; remaining > 0 {
				w = remaining
			}
		}
		items = append(items, weighted{cfg: cfg, weight: w})
	}
	return items
}

func partitionByQuota(items []weighted) (withQuota, withoutQuota []weighted) {
	for _, item := range items {
		if item.weight > 0 {
			withQuota = append(withQuota, item)
		} else {
			withoutQuota = append(withoutQuota, item)
		}
	}
	return
}

// sortByStableRandomWeight samples each candidate's score once before sorting
// so comparisons remain transitive during that sort.
func sortByStableRandomWeight(items []weighted) {
	if len(items) <= 1 {
		return
	}
	type entry struct {
		item   weighted
		factor float64
	}
	entries := make([]entry, len(items))
	for i, item := range items {
		entries[i] = entry{item: item, factor: float64(item.weight) * (0.5 + rand.Float64())}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].factor > entries[j].factor
	})
	for i, e := range entries {
		items[i] = e.item
	}
}

func mergeWeightedResults(withQuota, withoutQuota []weighted, capacity int) []ProviderConfig {
	result := make([]ProviderConfig, 0, capacity)
	for _, item := range withQuota {
		result = append(result, item.cfg)
	}
	for _, item := range withoutQuota {
		result = append(result, item.cfg)
	}
	return result
}

func (m *Manager) isProviderAvailable(cfg ProviderConfig) bool {
	if cfg.APIKey == "" {
		return false
	}
	if cfg.ExpiresAt != nil && time.Now().Unix() > *cfg.ExpiresAt {
		slog.Info("websearch: provider expired, skipping",
			"provider", cfg.Type, "expires_at", *cfg.ExpiresAt)
		return false
	}
	return true
}

// markProxyUnavailable records a failure for a search-provider proxy with a known ID.
func (m *Manager) markProxyUnavailable(ctx context.Context, cfg ProviderConfig, providerProxyURL string) {
	proxyID := resolveProxyID(cfg, providerProxyURL)
	if proxyID <= 0 || m.state == nil {
		return
	}
	if err := m.state.MarkProxy(ctx, proxyID, proxyUnavailableTTL); err != nil {
		slog.Warn("websearch: failed to mark proxy unavailable",
			"proxy_id", proxyID, "error", err)
	}
}

// isProxyAvailable reports whether a proxy can be tried; missing storage allows the attempt.
func (m *Manager) isProxyAvailable(ctx context.Context, proxyID int64) bool {
	if m.state == nil || proxyID <= 0 {
		return true
	}
	return m.state.ProxyAvailable(ctx, proxyID)
}

// resolveProxyID returns the search-provider proxy ID unless a request-level proxy overrides it.
func resolveProxyID(cfg ProviderConfig, providerProxyURL string) int64 {
	if providerProxyURL != "" {
		return 0 // The request-level proxy has no ID in the search configuration.
	}
	return cfg.ProxyID
}

// tryReserveQuota returns whether the request may proceed and whether an increment
// was confirmed. Storage errors allow the request with no confirmed reservation.
func (m *Manager) tryReserveQuota(ctx context.Context, cfg ProviderConfig) (bool, bool) {
	if cfg.QuotaLimit <= 0 {
		return true, false
	}
	if m.state == nil {
		slog.Warn("websearch: Redis unavailable, quota check skipped", "provider", cfg.Type)
		return true, false
	}
	newVal, err := m.state.Increment(ctx, cfg.Type, quotaTTLFromSubscription(cfg.SubscribedAt))
	if err != nil {
		slog.Warn("websearch: quota Lua INCR failed, allowing request",
			"provider", cfg.Type, "error", err)
		return true, false
	}
	if newVal > cfg.QuotaLimit {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		decrErr := m.state.Decrement(cleanup, cfg.Type)
		cancel()
		if decrErr != nil {
			slog.Warn("websearch: quota over-limit DECR failed",
				"provider", cfg.Type, "error", decrErr)
		}
		slog.Info("websearch: provider quota exhausted",
			"provider", cfg.Type, "used", newVal, "limit", cfg.QuotaLimit)
		return false, false
	}
	return true, true
}

func (m *Manager) rollbackQuota(ctx context.Context, cfg ProviderConfig) {
	if cfg.QuotaLimit <= 0 || m.state == nil {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := m.state.Decrement(cleanup, cfg.Type); err != nil {
		slog.Warn("websearch: quota rollback DECR failed",
			"provider", cfg.Type, "error", err)
	}
}

// TestSearch tries keyed, unexpired providers in configuration order until one succeeds.
// Administrator tests bypass quota reservation and proxy-availability filtering.
func (m *Manager) TestSearch(ctx context.Context, req SearchRequest) (*SearchResponse, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	done, err := m.work.Begin()
	if err != nil {
		return nil, "", err
	}
	defer done()
	defer m.closeIfRetired()
	if strings.TrimSpace(req.Query) == "" {
		return nil, "", fmt.Errorf("websearch: empty search query")
	}
	for _, cfg := range m.configs {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if !m.isProviderAvailable(cfg) {
			continue
		}
		resp, err := m.executor.Search(ctx, cfg, req)
		if err != nil {
			continue
		}
		return resp, cfg.Type, nil
	}
	return nil, "", fmt.Errorf("websearch: no available provider")
}

// GetUsage returns the current usage count for the given provider.
func (m *Manager) GetUsage(ctx context.Context, providerType string) (int64, error) {
	if m.state == nil {
		return 0, nil
	}
	return m.state.Usage(ctx, providerType)
}

// ResetUsage deletes the Redis quota key for the given provider, resetting usage to 0.
func (m *Manager) ResetUsage(ctx context.Context, providerType string) error {
	if m.state == nil {
		return nil
	}
	return m.state.Reset(ctx, providerType)
}

// quotaTTLFromSubscription returns the time to the next subscription date plus a 24-hour buffer.
// An unset date uses 32 days. Usage resets when the counter expires and is recreated,
// so the nominal monthly date is not an exact reset deadline.
func quotaTTLFromSubscription(subscribedAt *int64) time.Duration {
	if subscribedAt == nil || *subscribedAt == 0 {
		return defaultQuotaTTL
	}
	next := nextMonthlyReset(time.Unix(*subscribedAt, 0).UTC())
	ttl := time.Until(next) + quotaTTLBuffer
	if ttl <= quotaTTLBuffer {
		// Use the default lifetime if the computed date has already passed.
		ttl = defaultQuotaTTL
	}
	return ttl
}

// nextMonthlyReset returns the next subscription date at midnight UTC.
// The subscription day anchors each month; a future subscription can be the first returned date.
// Days beyond the target month are clamped to its last day; a zero input returns one month from now.
func nextMonthlyReset(subscribedAt time.Time) time.Time {
	now := time.Now().UTC()
	if subscribedAt.IsZero() {
		return now.AddDate(0, 1, 0)
	}
	months := max((now.Year()-subscribedAt.Year())*12+int(now.Month()-subscribedAt.Month()), 0)
	candidate := addMonthsClamped(subscribedAt, months)
	if candidate.After(now) {
		return candidate
	}
	return addMonthsClamped(subscribedAt, months+1)
}

// addMonthsClamped adds nonnegative months, clamps the day to the target month, and returns midnight UTC.
// For example, January 31 plus one month becomes February 28 in a non-leap year.
func addMonthsClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	targetMonth := time.Month(int(m) + months)
	targetYear := y + int(targetMonth-1)/12
	targetMonth = (targetMonth-1)%12 + 1
	// Last day of the target month
	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(targetYear, targetMonth, d, 0, 0, 0, 0, time.UTC)
}

func (m *Manager) ProviderConfigs() []ProviderConfig { return CloneProviderConfigs(m.configs) }
func (m *Manager) Retire()                           { m.retired.Store(true); m.executor.CloseIdle() }

func (m *Manager) closeIfRetired() {
	if m.retired.Load() {
		m.executor.CloseIdle()
	}
}

func (r *quotaReservation) release(ctx context.Context) {
	r.once.Do(func() {
		if r.acquired {
			r.manager.rollbackQuota(ctx, r.config)
		}
	})
}
