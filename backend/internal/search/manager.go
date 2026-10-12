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

// Supplier quota is reserved immediately before each production search, while
// earlier usage observations influence candidate order. Candidates with
// positive observed remainder receive one randomized score; unlimited and
// nonpositive-remainder candidates follow in configuration order and still
// reach reservation when eligible.
//
// Executor failure releases only a confirmed reservation before cancellation
// and proxy policy decide whether another supplier can be tried. A shared
// request proxy ends supplier failover because all candidates would reuse it;
// configured supplier proxies can differ. Retirement closes idle connections
// while existing Manager holders retain copied settings. Shared operation
// completion includes quota cleanup, which receives an independent three-second
// context whose actual enforcement depends on storage client behavior.

// These durations govern configured-proxy failure markers and quota-key
// expiration, including the buffer after the next nominal subscription date.
const (
	proxyUnavailableTTL = 5 * time.Minute                  // configured proxy failure marker lifetime
	quotaTTLBuffer      = 24 * time.Hour                   // extra lifetime after monthly date
	defaultQuotaTTL     = 31*24*time.Hour + quotaTTLBuffer // undated supplier counter lifetime

)

// ErrProxyUnavailable identifies a connectivity-classified executor failure
// when req.ProxyURL is shared by every candidate; callers can then change the
// upstream provider.
var ErrProxyUnavailable = errors.New("websearch: proxy unavailable")

// weighted pairs one candidate configuration with its observed quota-ranking
// weight.
type weighted struct {
	// cfg shares optional date pointers with the immutable Manager snapshot.
	// Selection rearranges candidate values and leaves those retained dates
	// unchanged.
	cfg ProviderConfig
	// weight is positive observed quota remainder, or zero for unlimited or
	// exhausted candidates. Reservation later enforces the actual current limit.
	weight int64
}

// Manager retains copied supplier settings and coordinates search quota and
// completion.
type Manager struct {
	// configs is an immutable copied slice with copied optional timestamps.
	// Production selection reads it, and administrator testing retains its
	// configured supplier order.
	configs []ProviderConfig
	// state stores counters and proxy markers. Nil state permits limited searches
	// with unconfirmed reservation and reports zero usage or successful resets.
	state QuotaState
	// executor performs searches and idle cleanup. Supply it before eligible
	// search or retirement calls; cleanup needs to return before shared operation
	// completion.
	executor Executor
	// work counts admitted searches through quota release and retired-client
	// closing. ConfigService shares this group across generations so shutdown
	// waits for old requests.
	work *WorkGroup
	// retired requests idle cleanup after each finishing search. Manager admission
	// remains controlled by work, allowing retained pointers to start calls before
	// group stop.
	retired atomic.Bool
}

// quotaReservation remembers whether one attempted supplier call confirmed its
// quota increment.
type quotaReservation struct {
	// once guards release of acquired through manager using config. These values
	// belong to one supplier attempt; uncertain increments remain unacquired so
	// compensation cannot decrement another call.
	once     sync.Once      // single compensation attempt guard
	manager  *Manager       // owning supplier quota manager
	config   ProviderConfig // attempted supplier quota configuration
	acquired bool           // confirmed eligible quota increment
}

// NewManager copies supplier settings and optional timestamp pointers before
// retaining them. Nil work creates a tracker; nil state permits quota checks to
// fail open, and callers supply an executor for actual search or retirement.
func NewManager(configs []ProviderConfig, state QuotaState, executor Executor, work *WorkGroup) *Manager {
	if work == nil {
		work = NewWorkGroup()
	}
	return &Manager{configs: CloneProviderConfigs(configs), state: state, executor: executor, work: work}
}

// SearchWithBestProvider tries eligible candidates in quota-weighted order and
// returns the successful configured type. A nonnil context is checked before
// admission and each attempt; whitespace-only queries are rejected. Successful
// calls retain their confirmed increments.
//
// Failure releases the attempt's confirmed increment before inspecting
// cancellation. Canceled failures stop further attempts and proxy marking.
// Connectivity-classified errors with req.ProxyURL return ErrProxyUnavailable
// because that proxy is shared by all candidates; configured supplier proxy
// errors and ordinary errors can continue. Work completes after detached quota
// cleanup and retired-client idle closing return.
// @project-doc docs/architecture/gateway_request_lifecycle.md#moderation_search_boundaries
func (m *Manager) SearchWithBestProvider(ctx context.Context, req SearchRequest) (*SearchResponse, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	done, err := m.work.Begin()
	if err != nil {
		return nil, "", err
	}
	// Keep retired-client closing inside each registered call so shared shutdown
	// waits for the old executor's cleanup to return before completion.
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
			// Release this attempt's confirmed increment before checking cancellation so
			// canceled supplier failures still submit their detached-context quota
			// compensation.
			reservation.release(ctx)
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			if m.executor.IsProxyError(err) {
				m.markProxyUnavailable(ctx, cfg, req.ProxyURL)
				if req.ProxyURL != "" {
					// Every candidate would reuse the request proxy override, so return the
					// sentinel and let the gateway choose another upstream provider.
					slog.Warn("websearch: provider proxy error, aborting failover",
						"provider", cfg.Type, "error", err)
					return nil, "", fmt.Errorf("%w: %s", ErrProxyUnavailable, err.Error())
				}
				// A configured supplier proxy is specific to this candidate, so later
				// candidates can use a different proxy and continue the search.
				slog.Warn("websearch: provider proxy error, trying next provider",
					"provider", cfg.Type, "error", err)
				continue
			}
			// Continue through remaining eligible suppliers after an ordinary executor
			// error; the final failure reports exhausted attempts instead of this
			// candidate's error.
			slog.Warn("websearch: provider search failed",
				"provider", cfg.Type, "error", err)
			continue
		}
		return resp, cfg.Type, nil
	}
	return nil, "", fmt.Errorf("websearch: no available provider (all exhausted or failed)")
}

// filterAvailableProviders retains configured order while requiring a key,
// unexpired credentials, and an available configured proxy. A request proxy
// override bypasses stored proxy markers, and absent quota state permits proxy
// attempts.
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

// selectByQuotaWeight orders candidates by observed positive quota remainder
// and one random score per positive entry. Returned configurations share
// optional dates with the immutable Manager snapshot.
//
// Positive-remainder candidates sort first. Unlimited and nonpositive-remainder
// entries follow in relative configuration order, so an exhausted observation
// does not itself remove a candidate. Counts can change after weighting;
// tryReserveQuota makes the later admission decision. Weighting suppresses read
// errors while using each returned count, and its result authorizes no quota
// consumption by itself.
func (m *Manager) selectByQuotaWeight(ctx context.Context, candidates []ProviderConfig) []ProviderConfig {
	items := m.computeWeights(ctx, candidates)
	withQuota, withoutQuota := partitionByQuota(items)
	sortByStableRandomWeight(withQuota)
	return mergeWeightedResults(withQuota, withoutQuota, len(candidates))
}

// computeWeights combines each positive configured limit with its returned
// usage count, producing positive remainder or zero. Unlimited configurations
// begin at zero, and usage read errors are suppressed.
//
// The observations guide ordering while atomic reservation governs execution. A
// supplier can become exhausted between reading and attempting, or its counter
// can reset after observation. Each weighted value retains its configuration so
// later partitioning and sorting keep the candidate's identity attached to the
// observed remainder.
func (m *Manager) computeWeights(ctx context.Context, candidates []ProviderConfig) []weighted {
	items := make([]weighted, 0, len(candidates))
	for _, cfg := range candidates {
		w := int64(0)
		if cfg.QuotaLimit > 0 {
			// Use the observed count for ranking even when its read reports an error;
			// the later atomic reservation still decides quota eligibility.
			used, _ := m.GetUsage(ctx, cfg.Type)
			if remaining := cfg.QuotaLimit - used; remaining > 0 {
				w = remaining
			}
		}
		items = append(items, weighted{cfg: cfg, weight: w})
	}
	return items
}

// partitionByQuota returns positive-weight and nonpositive-weight partitions in
// input order. Each candidate occurs in one output slice; its configuration's
// optional pointers remain shared with the immutable Manager snapshot.
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

// sortByStableRandomWeight rearranges items by descending precomputed
// randomized score, returning immediately for at most one item. Each
// configuration remains paired with its sampled score through sorting.
//
// Scores multiply integer remainder by a random factor from one half up to one
// and a half. Sampling before sort makes repeated comparisons consistent;
// drawing inside the comparator could contradict earlier results. Float
// conversion can round large weights, and equal scores follow sort.Slice's
// chosen ordering.
func sortByStableRandomWeight(items []weighted) {
	if len(items) <= 1 {
		return
	}
	// entry keeps a candidate paired with its precomputed randomized sorting
	// score.
	type entry struct {
		// item and factor associate a candidate with one score sampled before
		// sorting. Comparisons read that fixed score so repeated comparisons remain
		// consistent.
		item   weighted // original weighted candidate value
		factor float64  // sampled randomized sorting score
	}
	// Sample scores before comparison so sorting decisions remain consistent even
	// when the comparator examines the same candidates repeatedly.
	entries := make([]entry, len(items))
	for i, item := range items {
		// Float conversion can round distinct large integer quota weights to the same
		// value.
		entries[i] = entry{item: item, factor: float64(item.weight) * (0.5 + rand.Float64())}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].factor > entries[j].factor
	})
	for i, e := range entries {
		items[i] = e.item
	}
}

// mergeWeightedResults returns positive-quota candidates before the remaining
// partition. Each partition's current order is retained, so callers sort the
// positive partition first and pass nonpositive candidates in configuration
// order.
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

// isProviderAvailable requires a nonempty key and optional expiration that has
// not been exceeded. Expiration uses current Unix seconds and a strict
// greater-than comparison, so equality remains eligible.
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

// markProxyUnavailable attempts to mark a configured supplier proxy for five
// minutes. Request overrides, nonpositive IDs, and nil state skip marking;
// storage errors are logged and suppressed.
func (m *Manager) markProxyUnavailable(ctx context.Context, cfg ProviderConfig, providerProxyURL string) {
	proxyID := resolveProxyID(cfg, providerProxyURL)
	if proxyID <= 0 || m.state == nil {
		return
	}
	// Report failed marker storage while allowing failover to continue; another
	// request may still choose this proxy until a marker is stored.
	if err := m.state.MarkProxy(ctx, proxyID, proxyUnavailableTTL); err != nil {
		slog.Warn("websearch: failed to mark proxy unavailable",
			"proxy_id", proxyID, "error", err)
	}
}

// isProxyAvailable permits attempts with nil state or a nonpositive proxy ID.
// Positive configured IDs delegate to state, whose Redis implementation permits
// attempts when reads fail or return empty.
func (m *Manager) isProxyAvailable(ctx context.Context, proxyID int64) bool {
	if m.state == nil || proxyID <= 0 {
		return true
	}
	return m.state.ProxyAvailable(ctx, proxyID)
}

// resolveProxyID returns zero when a nonempty request proxy overrides the
// supplier proxy. Zero skips configured marker lookup and writes; otherwise the
// configured ID is returned as supplied.
func resolveProxyID(cfg ProviderConfig, providerProxyURL string) int64 {
	if providerProxyURL != "" {
		// Skip stored identity when the caller selects a shared request proxy, whose
		// address is outside the configured search-provider proxy IDs.
		return 0
	}
	return cfg.ProxyID
}

// tryReserveQuota reports whether a supplier may be attempted and whether its
// increment is confirmed. Nonpositive limits bypass counters; missing state or
// increment errors permit execution with no confirmed reservation.
//
// A returned count above the limit rejects execution after one Decrement call
// using a detached three-second context. Decrement errors are logged and leave
// compensation unconfirmed, while rejection still returns false for both
// results. Counts within the limit confirm a reservation that failed execution
// can release once. Uncertain increments receive no speculative decrement.
func (m *Manager) tryReserveQuota(ctx context.Context, cfg ProviderConfig) (bool, bool) {
	if cfg.QuotaLimit <= 0 {
		return true, false
	}
	// Permit execution when quota storage is absent, keeping reservation
	// unconfirmed so later release cannot decrement another supplier call.
	if m.state == nil {
		slog.Warn("websearch: Redis unavailable, quota check skipped", "provider", cfg.Type)
		return true, false
	}
	newVal, err := m.state.Increment(ctx, cfg.Type, quotaTTLFromSubscription(cfg.SubscribedAt))
	if err != nil {
		// A failed reply cannot establish whether Redis incremented; allow execution
		// and avoid speculative compensation against an unknown reservation.
		slog.Warn("websearch: quota Lua INCR failed, allowing request",
			"provider", cfg.Type, "error", err)
		return true, false
	}
	// Reject an over-limit increment after one detached-context compensation
	// attempt; request cancellation should not prevent submitting this
	// supplier-counter decrement.
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

// rollbackQuota invokes Decrement once for a positive-limit supplier with
// configured state. It supplies a three-second context detached from request
// cancellation, logs errors, and requires the adapter to return for cleanup
// completion. Storage clients can retry that invocation internally.
func (m *Manager) rollbackQuota(ctx context.Context, cfg ProviderConfig) {
	if cfg.QuotaLimit <= 0 || m.state == nil {
		return
	}
	// Separate quota cleanup from canceled request signals; the adapter receives a
	// three-second context budget whose I/O enforcement depends on its client.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := m.state.Decrement(cleanup, cfg.Type); err != nil {
		slog.Warn("websearch: quota rollback DECR failed",
			"provider", cfg.Type, "error", err)
	}
}

// TestSearch tries keyed, unexpired suppliers in configuration order and
// returns the successful configured type. A nonnil context is checked before
// admission and each candidate, and whitespace-only queries are rejected.
//
// Administrator testing skips quota reservation and configured proxy markers.
// Every executor error continues, including connectivity errors; failure on the
// last candidate yields the generic unavailable error even when that call
// caused cancellation. The executor still applies request proxy overrides, and
// retired-client cleanup completes before shared operation accounting ends.
func (m *Manager) TestSearch(ctx context.Context, req SearchRequest) (*SearchResponse, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	done, err := m.work.Begin()
	if err != nil {
		return nil, "", err
	}
	// Keep retired-client closing inside each registered call so shared shutdown
	// waits for the old executor's cleanup to return before completion.
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
			// Administrator tests continue on every executor error, including proxy
			// faults; only the next iteration's context check reports cancellation here.
			continue
		}
		return resp, cfg.Type, nil
	}
	return nil, "", fmt.Errorf("websearch: no available provider")
}

// GetUsage reads the literal supplier counter through configured state. Nil
// state returns zero with nil error; other calls expose the storage count and
// error to their consumer.
func (m *Manager) GetUsage(ctx context.Context, providerType string) (int64, error) {
	if m.state == nil {
		return 0, nil
	}
	return m.state.Usage(ctx, providerType)
}

// ResetUsage deletes the literal supplier counter through configured state. Nil
// state returns success, while storage errors propagate; the supplier string
// passes through without validation or trimming.
func (m *Manager) ResetUsage(ctx context.Context, providerType string) error {
	if m.state == nil {
		return nil
	}
	return m.state.Reset(ctx, providerType)
}

// quotaTTLFromSubscription returns lifetime until the next UTC subscription
// date plus twenty-four hours. Nil or zero selects thirty-two days. A date that
// passes before the later time sample selects that default lifetime.
//
// This date guides a Redis key's lifetime rather than directly resetting usage.
// Counters reset when an expired key is recreated, and the buffer carries usage
// beyond the nominal date. Lua can renew expiration when the resulting count
// becomes one or the existing key has no TTL.
func quotaTTLFromSubscription(subscribedAt *int64) time.Duration {
	if subscribedAt == nil || *subscribedAt == 0 {
		return defaultQuotaTTL
	}
	next := nextMonthlyReset(time.Unix(*subscribedAt, 0).UTC())
	ttl := time.Until(next) + quotaTTLBuffer
	if ttl <= quotaTTLBuffer {
		// The clock can pass the selected date between samples; choose the default
		// duration so that stale date does not shorten counter lifetime.
		ttl = defaultQuotaTTL
	}
	return ttl
}

// nextMonthlyReset selects the next subscription-day midnight UTC after the
// current sample. A zero input returns one calendar month from now with its
// time of day; a future subscription can supply the first returned midnight.
//
// Elapsed months are counted from the subscription month and clamped at zero.
// addMonthsClamped handles days absent from the target month. A candidate after
// now is returned; otherwise the following month is selected, keeping the
// intended expiration anchor ahead of this clock sample.
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

// addMonthsClamped adds a nonnegative month count and returns the input
// calendar day at midnight UTC. Callers convert subscription timestamps to UTC
// first because the input's calendar date supplies the anchor.
//
// The target year and month are normalized before its final day is calculated.
// If the anchor day is absent, clamping before time.Date keeps the result
// inside that target month. January thirty-first therefore becomes February
// twenty-eighth in 2026 or February twenty-ninth in 2028, rather than
// normalizing into March.
func addMonthsClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	targetMonth := time.Month(int(m) + months)
	targetYear := y + int(targetMonth-1)/12
	targetMonth = (targetMonth-1)%12 + 1
	// Use the final target-month day when the subscription day is absent,
	// preventing calendar normalization from drifting into another month.
	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(targetYear, targetMonth, d, 0, 0, 0, 0, time.UTC)
}

// ProviderConfigs returns a slice with copied optional timestamps from the
// retained Manager snapshot. Callers can mutate copied elements and pointed-to
// dates independently of the active configuration.
func (m *Manager) ProviderConfigs() []ProviderConfig { return CloneProviderConfigs(m.configs) }

// Retire marks the Manager for repeated idle-client closing and immediately
// asks its executor to close current idle connections. WorkGroup retains
// admission control; holders can still search before shutdown.
func (m *Manager) Retire() { m.retired.Store(true); m.executor.CloseIdle() }

// closeIfRetired closes currently cached idle connections after a retired
// Manager call. Search defers it before releasing WorkGroup accounting so
// shutdown waits for the executor's close callback to return.
func (m *Manager) closeIfRetired() {
	if m.retired.Load() {
		m.executor.CloseIdle()
	}
}

// release attempts compensation once when this reservation confirmed an
// eligible increment. Unconfirmed attempts leave counters untouched. A
// confirmed reservation delegates to detached-context decrement; failure is
// logged while the guard still prevents another release attempt.
//
// sync.Once coordinates overlapping cleanup paths so one reservation submits
// one adapter release call. That call returns before later callers return from
// Do, but a storage client can retry its command after a failed reply. Completion
// means this invocation finished; it does not establish one applied Redis
// decrement, and a failed operation leaves counter restoration unconfirmed.
func (r *quotaReservation) release(ctx context.Context) {
	// Serialize overlapping releases through one compensation attempt; failed
	// decrement remains logged rather than reopening this reservation for another
	// counter change.
	r.once.Do(func() {
		if r.acquired {
			r.manager.rollbackQuota(ctx, r.config)
		}
	})
}
