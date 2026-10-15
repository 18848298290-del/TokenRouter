package provider

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// grok_quota.go queries Grok billing and active quota observations and
// combines their partial results.
// Grok quota queries can obtain official billing data, active response headers,
// or both. Accepted billing signals end the query early; otherwise an active
// probe can supply rolling windows while the available billing result remains
// useful for display. An active failure can therefore return billing with a
// ProbeError and no overall error.
//
// Weekly and monthly billing run independently under one context and are joined
// before merging. Nonnil summaries count as available even with paired errors,
// while two missing summaries can persist a failed observation before returning
// an error. Active callbacks can persist snapshots and rate-limit effects
// before their transport later fails. Persisted describes observation writes
// separately from those effects. Shared flights use the runtime's cancellation
// ownership and copy mutable result fields for each waiter.

// GrokQuotaSnapshotExtraKey stores rolling quota observations separately from
// official billing summaries so display and scheduling can combine accepted sources.
const GrokQuotaSnapshotExtraKey = "grok_usage_snapshot"

// GrokQuotaOptions supplies observation transport, persistence, display, and
// health callbacks.
type GrokQuotaOptions struct {
	// Timeout is retained as supplied. Direct billing derives its shared deadline
	// from it, while RunProbeFlight adds five seconds for runtime ownership;
	// direct active transport receives its caller context.
	Timeout   time.Duration // billing and flight context budget
	Available func() bool   // network probe availability predicate
	// GetProvider supplies the row used for preparation and later optional model
	// scheduling. LoadGrokOAuthProvider rejects nil rows or errors before token
	// acquisition and upstream probing.
	GetProvider func(context.Context, int64) (*Record, error) // provider observation record reader
	// UpdateExtra receives the selected observation patch by provider ID. The
	// service passes no queried credential version, leaving identity comparison
	// to the supplied writer. Its error controls Persisted or a warning separately
	// from transport completion and subsequent rate-limit actions.
	UpdateExtra func(context.Context, int64, map[string]any) error // quota and billing snapshot writer
	// provider access-token acquisition callback
	Token        func(context.Context, *Record) (string, error)
	ResolveProxy func(context.Context, *Record) string // provider query proxy resolver
	// Active invokes callbacks serially with nonnil snapshots and finishes them
	// before returning. ProbeUsageDirect has no lock around captured result or
	// snapshot mutation, so callbacks cannot overlap or outlive this operation.
	// active header observation transport
	Active func(context.Context, *Record, string, string, string, func(*usageview.QuotaSnapshot, int)) error
	// Billing receives true for weekly and false for monthly queries concurrently.
	// It keeps the prepared record stable and responds to the shared context; the
	// caller waits for actual completion of both.
	// weekly or monthly billing transport
	Billing    func(context.Context, *Record, string, string, bool) (*usageview.BillingSummary, int, error)
	ProbeModel func() string // active probe model selector
	// ScheduleModels is called after accepted query paths when a provider reread
	// returns nil error. Its callback handles any returned row, including nil, as
	// the production discovery helper does.
	ScheduleModels func(*Record) // post-query model discovery scheduler
	// MergeBilling receives previous, weekly, and monthly summaries with
	// availability flags. When at least one part is available, it returns nonnil
	// assembly because the core sets its status fields.
	// partial billing observation assembler
	MergeBilling func(*usageview.BillingSummary, *usageview.BillingSummary, *usageview.BillingSummary, bool, bool) *usageview.BillingSummary
	// StampBilling can return the pointer used for persistence, local statistics,
	// and the returned result. Its selected metadata treatment remains the
	// supplied platform implementation's responsibility.
	// billing observation metadata stamper
	StampBilling func(*usageview.BillingSummary, int, string) *usageview.BillingSummary
	// StampQuota, ResetAt, and NormalizeResets operate on the active callback's
	// snapshot before optional observation persistence. Limit persistence or
	// observed recovery follows that write attempt independently.
	StampQuota func(*Record, *usageview.QuotaSnapshot, string) // rolling quota metadata stamper
	// provider rate-limit deadline resolver
	ResetAt func(*Record, *usageview.QuotaSnapshot, time.Time) (time.Time, bool)
	// exhausted window reset reconciler
	NormalizeResets func(*usageview.QuotaSnapshot, time.Time, time.Time)
	// PersistLimit and ClearLimit are separate side effects from UpdateExtra. A
	// failed snapshot write can still reach either path, with CanRecover selecting
	// the recovery branch.
	PersistLimit func(context.Context, *Record, time.Time) // provider-wide limit persistence callback
	// successful observation recovery predicate
	CanRecover       func(*Record, *usageview.QuotaSnapshot) bool
	ClearLimit       func(context.Context, *Record) // observed limit recovery callback
	MediaEligibility func(*Record) (bool, string)   // current media generation admission
	// LocalStats receives the assembled billing observation and one captured time
	// after persistence. Its three results represent local daily, weekly, and
	// billing-month totals returned by the probe.
	// local provider usage window totals
	LocalStats func(context.Context, int64, *usageview.BillingSummary, time.Time) (*WindowStats, *WindowStats, *WindowStats)
	MapStatus  func(int) int        // public upstream failure status mapper
	Warn       func(string, ...any) // observation write warning callback
}

// GrokQuotaService combines official billing with lifecycle-tracked active
// quota observations.
type GrokQuotaService struct {
	// Options retains caller-provided callbacks and timeout. Configure each
	// reached dependency before use and keep callback state usable while
	// independent billing operations share the service.
	Options GrokQuotaOptions // retained observation dependency callbacks
	// Runtime owns shared probe admission, cancellation, and completion. Provide a
	// nonnil runtime for flight and stop operations; the production factory shares
	// it with observed-model tasks.
	Runtime *ProbeRuntime // shared on-demand probe owner
}

// GrokQuotaProbeResult carries upstream observations, local totals, and
// separate persistence diagnostics.
type GrokQuotaProbeResult struct {
	Source string `json:"source"`          // observation source display label
	Model  string `json:"model,omitempty"` // selected active probe model
	// Billing and Snapshot may originate from separate queries and are merged for
	// hybrid display. LocalUsage24h, LocalUsage7d, and LocalUsageMonthly travel
	// with billing; shared flight delivery copies known mutable result data.
	// optional official billing observation
	Billing *usageview.BillingSummary `json:"billing,omitempty"`
	// Snapshot retains the active transport's last callback pointer on direct
	// probes. RunProbeFlight clones it before waiter delivery; direct callers
	// keep shared callback data stable or clone it before independent edits.
	Snapshot *usageview.QuotaSnapshot `json:"snapshot,omitempty"` // optional rolling quota observation
	// local rolling daily totals
	LocalUsage24h *WindowStats `json:"local_usage_24h,omitempty"`
	// local rolling weekly totals
	LocalUsage7d *WindowStats `json:"local_usage_7d,omitempty"`
	// local billing month totals
	LocalUsageMonthly *WindowStats `json:"local_usage_monthly,omitempty"`
	// selected upstream response status
	StatusCode int `json:"status_code,omitempty"`
	// active callback header-presence flag
	HeadersObserved bool  `json:"headers_observed"`
	ResetSupported  bool  `json:"reset_supported"` // false for current probes
	FetchedAt       int64 `json:"fetched_at"`      // Unix-seconds
	// Persisted reports an accepted snapshot or billing write. Hybrid querying
	// combines the two flags with OR, so it does not assert every observation or
	// health side effect was written.
	Persisted bool `json:"persisted"` // accepted observation write flag
	// ProbeError can accompany a successful QueryQuota return when active probing
	// fails but billing remains available. Callers inspect it for that partial
	// observation outcome.
	// retained active probe diagnostic
	ProbeError string `json:"probe_error,omitempty"`
}

// GrokQuotaResetResult defines the administrator response shape for quota reset
// capability.
type GrokQuotaResetResult struct {
	Supported bool   `json:"supported"` // quota reset capability flag
	Code      string `json:"code"`      // machine-readable reset response label
	Message   string `json:"message"`   // administrator reset display explanation
}

// NewGrokQuotaService retains observation callbacks and the shared runtime for
// on-demand queries. It supplies no timeout or callback defaults; owners
// configure each reached dependency before invoking network or stop operations.
func NewGrokQuotaService(options GrokQuotaOptions, runtime *ProbeRuntime) *GrokQuotaService {
	return &GrokQuotaService{Options: options, Runtime: runtime}
}

// QueryQuota returns official billing when its accepted signals are
// authoritative, otherwise combining available billing with an active probe. An
// active failure can return billing with ProbeError and nil overall error;
// without billing that error is returned.
//
// A successful active result can receive billing and its local usage pointers,
// with persistence flags combined by OR. Model scheduling follows accepted
// quota paths after a successful reread. These observations and their writes
// remain separate operations, so a returned result is a display combination
// rather than a transaction across billing, headers, health, and discovery.
func (s *GrokQuotaService) QueryQuota(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	billingResult, billingErr := s.ProbeBilling(ctx, providerID)
	if billingErr == nil && billingResult != nil && GrokBillingHasAuthoritativeQuota(billingResult.Billing) {
		// Model discovery is auxiliary to this accepted billing result. A failed
		// reread skips scheduling while the already acquired quota remains
		// returnable.
		if acc, err := s.Options.GetProvider(ctx, providerID); err == nil {
			s.Options.ScheduleModels(acc)
		}
		return billingResult, nil
	}

	probeResult, probeErr := s.ProbeUsage(ctx, providerID)
	if probeErr != nil {
		// Billing remains useful when header probing fails. Return that accepted
		// observation with its active-probe diagnostic so display callers can use
		// partial data.
		if billingResult != nil && billingResult.Billing != nil {
			billingResult.ProbeError = probeErr.Error()
			return billingResult, nil
		}
		return nil, probeErr
	}
	if probeResult == nil {
		if billingErr != nil {
			return nil, billingErr
		}
		return nil, infraerrors.New(infraerrors.Category(502), "GROK_QUOTA_PROBE_EMPTY", "Grok quota probe returned no result")
	}
	if billingResult != nil {
		probeResult.Source = "hybrid_probe"
		probeResult.Billing = billingResult.Billing
		probeResult.LocalUsage24h = billingResult.LocalUsage24h
		probeResult.LocalUsage7d = billingResult.LocalUsage7d
		probeResult.LocalUsageMonthly = billingResult.LocalUsageMonthly
		probeResult.Persisted = probeResult.Persisted || billingResult.Persisted
	}
	// Discovery follows the accepted active/hybrid observation. Ignore its
	// prerequisite reread failure so this quota response stays usable while
	// model metadata can refresh later.
	if acc, err := s.Options.GetProvider(ctx, providerID); err == nil {
		s.Options.ScheduleModels(acc)
	}
	return probeResult, nil
}

// ProbeUsage coalesces active provider observations under a provider-ID runtime
// key and returns a copied result to each waiter. Caller cancellation affects
// that waiter while shared work uses its own timeout and shutdown ownership.
func (s *GrokQuotaService) ProbeUsage(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	return s.RunProbeFlight(ctx, "active:"+strconv.FormatInt(providerID, 10), func(sharedCtx context.Context) (*GrokQuotaProbeResult, error) {
		return s.ProbeUsageDirect(sharedCtx, providerID)
	})
}

// ProbeUsageDirect prepares an OAuth row and invokes active transport with a
// model and synchronous observation callback. The callback requires nonnil
// snapshots and may run before transport eventually returns an error.
//
// Accepted statuses persist observed headers, while headerless 200 also records
// a completed attempt. Snapshot write failure only changes Persisted;
// rate-limit persistence or observed recovery remains independently reachable.
// A later transport error discards the direct result even if callback effects
// already happened. A nil transport error with no observation callback yields
// nil result, which RunProbeFlight rejects separately.
func (s *GrokQuotaService) ProbeUsageDirect(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	provider, token, proxyURL, err := s.PrepareProbe(ctx, providerID)
	if err != nil {
		return nil, err
	}
	probeModel := s.Options.ProbeModel()
	var result *GrokQuotaProbeResult
	err = s.Options.Active(ctx, provider, token, proxyURL, probeModel, func(snapshot *usageview.QuotaSnapshot, statusCode int) {
		s.Options.StampQuota(provider, snapshot, probeModel)
		resetAt, limited := s.Options.ResetAt(provider, snapshot, time.Now())
		if limited {
			// Pass the selected provider deadline to the exhausted-window
			// normalizer before persistence so stored evidence can match the
			// scheduling pause derived from this observation.
			s.Options.NormalizeResets(snapshot, resetAt, time.Now())
		}
		// Preserve earlier observations on authentication, transport, or server failure.
		// Persist observed headers for statuses below 400 or 429. A headerless 200 also records
		// a completed probe so the UI can distinguish it from an unobserved provider.
		persistErr := error(nil)
		persisted := false
		shouldPersist := statusCode < 400 || statusCode == 429
		if shouldPersist && (snapshot.HeadersObserved || statusCode == 200) {
			persistErr = s.Options.UpdateExtra(ctx, provider.ID, map[string]any{
				GrokQuotaSnapshotExtraKey: snapshot,
			})
			persisted = persistErr == nil
		}
		// Health maintenance follows the observation write attempt independently.
		// Failed snapshot storage cannot erase the upstream limit or recovery
		// evidence received by this callback.
		if limited {
			s.Options.PersistLimit(ctx, provider, resetAt)
		} else if s.Options.CanRecover(provider, snapshot) {
			s.Options.ClearLimit(ctx, provider)
		}

		// Capture response construction time in epoch seconds, separately
		// from the header timestamps already stamped on the quota snapshot.
		result = &GrokQuotaProbeResult{
			Source: "active_probe",

			Model: probeModel,

			Snapshot: snapshot,

			StatusCode: statusCode,

			HeadersObserved: snapshot.HeadersObserved,

			ResetSupported: false,

			FetchedAt: time.Now().Unix(),

			Persisted: persisted,
		}
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ProbeBilling coalesces provider-ID billing work through the shared runtime
// and returns detached known result fields. Each waiter can cancel
// independently, while direct billing joins its weekly/monthly callbacks within
// their shared operation.
func (s *GrokQuotaService) ProbeBilling(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	return s.RunProbeFlight(ctx, "billing:"+strconv.FormatInt(providerID, 10), func(sharedCtx context.Context) (*GrokQuotaProbeResult, error) {
		return s.ProbeBillingDirect(sharedCtx, providerID)
	})
}

// ProbeMediaEligibility attempts billing, reloads the Grok OAuth row, and
// returns current media eligibility and reason. A probe error is returned only
// when the reloaded decision still says billing_unobserved; known rejection
// remains an ordinary result.
func (s *GrokQuotaService) ProbeMediaEligibility(ctx context.Context, providerID int64) (bool, string, error) {
	_, probeErr := s.ProbeBilling(ctx, providerID)
	provider, err := s.LoadGrokOAuthProvider(ctx, providerID)
	if err != nil {
		return false, "billing_probe_failed", err
	}
	eligible, reason := s.Options.MediaEligibility(provider)
	if reason == "billing_unobserved" && probeErr != nil {
		return false, reason, probeErr
	}
	return eligible, reason, nil
}

// ProbeBillingDirect prepares one OAuth row and joins concurrent weekly and
// monthly billing under the supplied timeout context. A nonnil summary
// identifies an available part even when its paired error is nonnil; callbacks
// keep shared inputs stable and finish before merging.
//
// Two missing parts produce merged/stamped failure data and attempt to persist
// it before returning the selected probe error. One or both available parts
// produce a result even when observation persistence fails, with Persisted
// carrying that distinction and local totals computed afterward. The join waits
// for actual callbacks, so context expiry does not force an ignoring dependency
// to return.
func (s *GrokQuotaService) ProbeBillingDirect(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	provider, token, proxyURL, err := s.PrepareProbe(ctx, providerID)
	if err != nil {
		return nil, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, s.Options.Timeout)
	defer cancel()
	// billingResult retains one concurrently fetched window's data and failure
	// observations until the weekly/monthly completion barrier has joined both.
	type billingResult struct {
		// summary, status, and err are written by one window callback and read
		// after the join. A nonnil summary marks availability even when the
		// paired error is present.
		summary *usageview.BillingSummary // returned billing window observation
		status  int                       // observed upstream response status
		err     error                     // accompanying billing query failure
	}
	// Fetch each billing window into separate storage and join actual callback
	// completion before inspecting presence or merging data; both calls share
	// this one context budget.
	var weekly, monthly billingResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		weekly.summary, weekly.status, weekly.err = s.Options.Billing(probeCtx, provider, token, proxyURL, true)
	}()
	go func() {
		defer wg.Done()
		monthly.summary, monthly.status, monthly.err = s.Options.Billing(probeCtx, provider, token, proxyURL, false)
	}()
	wg.Wait()

	weeklyOK := weekly.summary != nil
	monthlyOK := monthly.summary != nil
	// Malformed retained billing contributes no previous source. Continue
	// merging fresh weekly/monthly observations instead of failing their
	// already completed query results.
	previous, _ := ParseGrokBillingSnapshot(provider.Extra)
	if !weeklyOK && !monthlyOK {
		probeErr := s.MergeGrokBillingProbeErrors(weekly.status, monthly.status, weekly.err, monthly.err)
		billing := s.Options.MergeBilling(previous, nil, nil, false, false)
		// An unavailable merge result still needs failed-window evidence to
		// retain for display. Build that placeholder before stamping both
		// observed statuses and attempting persistence.
		if billing == nil {
			billing = &usageview.BillingSummary{Partial: true, FailedWindows: []string{"weekly", "monthly"}}
		}
		billing.WeeklyStatusCode = weekly.status
		billing.MonthlyStatusCode = monthly.status
		billing = s.Options.StampBilling(billing, s.PreferBillingObservationStatus(weekly.status, monthly.status), "billing_probe")
		// Retain failed-window evidence even though this probe returns an error.
		// Persistence failure is diagnostic and leaves the selected upstream
		// error as the caller's result.
		if persistErr := s.Options.UpdateExtra(ctx, provider.ID, map[string]any{GrokUsageBillingExtraKey: billing}); persistErr != nil {
			s.Options.Warn("grok_billing_failure_persist_failed", "provider_id", provider.ID, "error", persistErr)
		}
		return nil, probeErr
	}
	statusCode := s.PreferSuccessfulBillingStatus(weekly.status, monthly.status, weeklyOK, monthlyOK)
	billing := s.Options.MergeBilling(previous, weekly.summary, monthly.summary, weeklyOK, monthlyOK)
	billing.WeeklyStatusCode = weekly.status
	billing.MonthlyStatusCode = monthly.status
	billing = s.Options.StampBilling(billing, statusCode, "billing_probe")
	// Persist the assembled billing observation before reading local totals.
	// Storage failure is reported separately because completed upstream data can
	// still be returned to the administrator.
	persistErr := s.Options.UpdateExtra(ctx, provider.ID, map[string]any{
		GrokUsageBillingExtraKey: billing,
	})
	// A completed billing observation remains useful when its write fails.
	// Report persistence separately, then compute display totals from the
	// assembled data already available.
	if persistErr != nil {
		s.Options.Warn("grok_billing_persist_failed", "provider_id", provider.ID, "error", persistErr)
	}
	now := time.Now().UTC()
	localUsage24h, localUsage7d, localUsageMonthly := s.Options.LocalStats(ctx, provider.ID, billing, now)
	// Return the captured time supplied to LocalStats in epoch-seconds form.
	// The response timestamp and local-window callback therefore receive the
	// same clock reading.
	return &GrokQuotaProbeResult{
		Source: "billing_probe",

		Billing: billing,

		LocalUsage24h: localUsage24h,

		LocalUsage7d: localUsage7d,

		LocalUsageMonthly: localUsageMonthly,

		StatusCode: statusCode,

		FetchedAt: now.Unix(),

		Persisted: persistErr == nil,
	}, nil
}

// PreferBillingObservationStatus selects one status for a failed weekly/monthly
// billing observation. Either 403 wins so retained failure data carries
// forbidden access even when the other window reports a different failure.
// Otherwise a nonzero weekly status keeps first-window priority and monthly
// supplies the zero-weekly case.
//
// ProbeBillingDirect also stores both original statuses on the summary. Those
// fields retain each window's display and media-admission evidence after this
// helper reduces two observations to one status.
func (s *GrokQuotaService) PreferBillingObservationStatus(weeklyStatus, monthlyStatus int) int {
	if weeklyStatus == 403 || monthlyStatus == 403 {
		return 403
	}
	if weeklyStatus != 0 {
		return weeklyStatus
	}
	return monthlyStatus
}

// RunProbeFlight uses a caller-supplied key to share runtime work with a
// timeout of Options.Timeout plus five seconds. Nil service is an error;
// callers supply a nonnil runtime and probe callback.
//
// Runtime admission and waiting own caller versus shared cancellation. A
// successful shared value must be a nonnil GrokQuotaProbeResult, then
// CloneGrokQuotaProbeResult detaches its known mutable fields for this waiter.
// Direct helpers can return nil, but that cannot become a successful flight
// result. Key identity determines which callbacks share work, so callers
// distinguish different provider and probe kinds.
func (s *GrokQuotaService) RunProbeFlight(
	ctx context.Context,
	key string,
	probe func(context.Context) (*GrokQuotaProbeResult, error),
) (*GrokQuotaProbeResult, error) {
	if s == nil {
		return nil, infraerrors.New(infraerrors.Category(500), "GROK_QUOTA_NOT_CONFIGURED", "grok quota service is not configured")
	}
	value, err := s.Runtime.Run(ctx, key, s.Options.Timeout+5*time.Second, func(shared context.Context) (any, error) {
		return probe(shared)
	})
	if err != nil {
		return nil, err
	}
	result, ok := value.(*GrokQuotaProbeResult)
	if !ok || result == nil {
		return nil, infraerrors.New(infraerrors.Category(500), "GROK_QUOTA_PROBE_RESULT_INVALID", "invalid Grok quota probe result")
	}
	return CloneGrokQuotaProbeResult(result), nil
}

// StopContext stops the retained probe runtime and waits within the caller's
// shutdown budget. Nil service succeeds; the configured runtime also owns its
// registered background model work and retains its first stop result.
func (s *GrokQuotaService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.Runtime.StopContext(ctx)
}

// MergeGrokBillingProbeErrors chooses a public failure for two unavailable
// billing parts using status, category, and reason keys. Equal keys prefer an
// existing weekly then monthly error, or synthesize status/empty failures.
//
// Different keys log both observations and return one combined 502 with
// statuses in metadata. Matching error text alone is not the comparison:
// ordinary messages outside the key can differ while one existing error still
// wins. Callers use this after availability has been decided from summary
// presence.
func (s *GrokQuotaService) MergeGrokBillingProbeErrors(weeklyStatus, monthlyStatus int, weeklyErr, monthlyErr error) error {
	weeklyKey := s.GrokBillingProbeErrorKey(weeklyStatus, weeklyErr)
	monthlyKey := s.GrokBillingProbeErrorKey(monthlyStatus, monthlyErr)
	if weeklyKey == monthlyKey {
		// Matching status/category/reason identifies one public failure. Prefer
		// an existing error to retain its diagnostic metadata; absent errors
		// require a synthesized status or empty-data result.
		switch {
		case weeklyErr != nil:
			return weeklyErr
		case monthlyErr != nil:
			return monthlyErr
		case weeklyStatus == 429:
			return infraerrors.New(infraerrors.Category(429), "GROK_QUOTA_PROBE_UPSTREAM_ERROR", "billing rate limited")
		case weeklyStatus != 0 && weeklyStatus != 200:
			return infraerrors.New(infraerrors.Category(s.Options.MapStatus(weeklyStatus)), "GROK_QUOTA_PROBE_UPSTREAM_ERROR", "xAI billing endpoints returned the same upstream error")
		default:
			return infraerrors.New(infraerrors.Category(502), "GROK_QUOTA_BILLING_EMPTY", "xAI billing endpoints returned no quota data")
		}
	}
	s.Options.Warn("grok_quota_probe_parts_failed", "weekly_status", weeklyStatus, "weekly_error", weeklyErr, "monthly_status", monthlyStatus, "monthly_error", monthlyErr)
	return infraerrors.New(infraerrors.Category(502), "GROK_QUOTA_PROBE_PARTS_FAILED", "weekly and monthly billing probes failed differently").WithMetadata(map[string]string{
		"weekly_status": strconv.Itoa(weeklyStatus), "monthly_status": strconv.Itoa(monthlyStatus),
	})
}

// GrokBillingProbeErrorKey identifies an observation by status plus application
// error category and reason, or an empty-data suffix. The returned key omits
// error message text so error merging compares classified outcomes.
func (s *GrokQuotaService) GrokBillingProbeErrorKey(status int, err error) string {
	if err != nil {
		return strconv.Itoa(status) + ":" + strconv.Itoa(int(infraerrors.CategoryOf(err))) + ":" + infraerrors.Reason(err)
	}
	return strconv.Itoa(status) + ":empty"
}

// PreferSuccessfulBillingStatus selects an available 2xx weekly part before an
// available 2xx monthly part. Availability comes from nonnil summary presence,
// so a success status with absent data cannot win this initial selection. If
// neither part supplies both conditions, nonzero weekly status wins even when
// its part is unavailable; monthly handles zero weekly status.
//
// The caller also retains individual status fields while stamping this combined
// result.
func (s *GrokQuotaService) PreferSuccessfulBillingStatus(weeklyStatus, monthlyStatus int, weeklyOK, monthlyOK bool) int {
	if weeklyOK && weeklyStatus >= 200 && weeklyStatus < 300 {
		return weeklyStatus
	}
	if monthlyOK && monthlyStatus >= 200 && monthlyStatus < 300 {
		return monthlyStatus
	}
	if weeklyStatus != 0 {
		return weeklyStatus
	}
	return monthlyStatus
}

// ResetQuota validates that the requested stored row is Grok OAuth and then
// returns the public unsupported-reset error. Provider loading errors take
// precedence; no upstream reset or provider write is attempted here.
func (s *GrokQuotaService) ResetQuota(ctx context.Context, providerID int64) (*GrokQuotaResetResult, error) {
	if _, err := s.LoadGrokOAuthProvider(ctx, providerID); err != nil {
		return nil, err
	}
	return nil, infraerrors.New(infraerrors.Category(501), "GROK_QUOTA_RESET_UNSUPPORTED", "xAI does not expose a Grok subscription quota reset endpoint for OAuth providers")
}

// PrepareProbe checks configured availability, loads a Grok OAuth row, resolves
// its proxy, and acquires nonblank token text. Token failures become public
// acquisition errors; the returned row and strings are supplied to the chosen
// transport.
func (s *GrokQuotaService) PrepareProbe(ctx context.Context, providerID int64) (*Record, string, string, error) {
	if s == nil || s.Options.Available == nil || !s.Options.Available() {
		return nil, "", "", infraerrors.New(infraerrors.Category(500), "GROK_QUOTA_NOT_CONFIGURED", "grok quota service is not configured")
	}
	provider, err := s.LoadGrokOAuthProvider(ctx, providerID)
	if err != nil {
		return nil, "", "", err
	}
	proxyURL := s.Options.ResolveProxy(ctx, provider)

	token, err := s.Options.Token(ctx, provider)
	if err != nil {
		return nil, "", "", infraerrors.Newf(infraerrors.Category(502), "GROK_QUOTA_TOKEN_UNAVAILABLE", "failed to acquire access token: %v", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, "", "", infraerrors.New(infraerrors.Category(502), "GROK_QUOTA_TOKEN_UNAVAILABLE", "access token is empty")
	}

	return provider, token, proxyURL, nil
}

// LoadGrokOAuthProvider reads the requested row and validates Grok platform and
// OAuth type. Missing reader or service reports configuration failure, and
// failed or nil lookup becomes the public provider-not-found result.
func (s *GrokQuotaService) LoadGrokOAuthProvider(ctx context.Context, providerID int64) (*Record, error) {
	if s == nil || s.Options.GetProvider == nil {
		return nil, infraerrors.New(infraerrors.Category(500), "GROK_QUOTA_NOT_CONFIGURED", "grok quota service is not configured")
	}
	provider, err := s.Options.GetProvider(ctx, providerID)
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.Category(404), "GROK_QUOTA_PROVIDER_NOT_FOUND", "provider not found: %v", err)
	}
	if provider == nil {
		return nil, infraerrors.New(infraerrors.Category(404), "GROK_QUOTA_PROVIDER_NOT_FOUND", "provider not found")
	}
	if provider.Platform != PlatformGrok {
		return nil, infraerrors.New(infraerrors.Category(400), "GROK_QUOTA_INVALID_PLATFORM", "provider is not a Grok provider")
	}
	if provider.Type != ProviderTypeOAuth {
		return nil, infraerrors.New(infraerrors.Category(400), "GROK_QUOTA_INVALID_TYPE", "provider is not an OAuth provider")
	}
	return provider, nil
}
