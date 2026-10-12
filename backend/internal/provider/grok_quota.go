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

const GrokQuotaSnapshotExtraKey = "grok_usage_snapshot"

type GrokQuotaOptions struct {
	Timeout          time.Duration
	Available        func() bool
	GetProvider      func(context.Context, int64) (*Record, error)
	UpdateExtra      func(context.Context, int64, map[string]any) error
	Token            func(context.Context, *Record) (string, error)
	ResolveProxy     func(context.Context, *Record) string
	Active           func(context.Context, *Record, string, string, string, func(*usageview.QuotaSnapshot, int)) error
	Billing          func(context.Context, *Record, string, string, bool) (*usageview.BillingSummary, int, error)
	ProbeModel       func() string
	ScheduleModels   func(*Record)
	MergeBilling     func(*usageview.BillingSummary, *usageview.BillingSummary, *usageview.BillingSummary, bool, bool) *usageview.BillingSummary
	StampBilling     func(*usageview.BillingSummary, int, string) *usageview.BillingSummary
	StampQuota       func(*Record, *usageview.QuotaSnapshot, string)
	ResetAt          func(*Record, *usageview.QuotaSnapshot, time.Time) (time.Time, bool)
	NormalizeResets  func(*usageview.QuotaSnapshot, time.Time, time.Time)
	PersistLimit     func(context.Context, *Record, time.Time)
	CanRecover       func(*Record, *usageview.QuotaSnapshot) bool
	ClearLimit       func(context.Context, *Record)
	MediaEligibility func(*Record) (bool, string)
	LocalStats       func(context.Context, int64, *usageview.BillingSummary, time.Time) (*WindowStats, *WindowStats, *WindowStats)
	MapStatus        func(int) int
	Warn             func(string, ...any)
}
type GrokQuotaService struct {
	Options GrokQuotaOptions
	Runtime *ProbeRuntime
}

type GrokQuotaProbeResult struct {
	Source            string                    `json:"source"`
	Model             string                    `json:"model,omitempty"`
	Billing           *usageview.BillingSummary `json:"billing,omitempty"`
	Snapshot          *usageview.QuotaSnapshot  `json:"snapshot,omitempty"`
	LocalUsage24h     *WindowStats              `json:"local_usage_24h,omitempty"`
	LocalUsage7d      *WindowStats              `json:"local_usage_7d,omitempty"`
	LocalUsageMonthly *WindowStats              `json:"local_usage_monthly,omitempty"`
	StatusCode        int                       `json:"status_code,omitempty"`
	HeadersObserved   bool                      `json:"headers_observed"`
	ResetSupported    bool                      `json:"reset_supported"`
	FetchedAt         int64                     `json:"fetched_at"`
	Persisted         bool                      `json:"persisted"`
	ProbeError        string                    `json:"probe_error,omitempty"`
}
type GrokQuotaResetResult struct {
	Supported bool   `json:"supported"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

func NewGrokQuotaService(options GrokQuotaOptions, runtime *ProbeRuntime) *GrokQuotaService {
	return &GrokQuotaService{Options: options, Runtime: runtime}
}

// QueryQuota prefers authoritative billing quota and otherwise combines billing with an active header probe.
func (s *GrokQuotaService) QueryQuota(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	billingResult, billingErr := s.ProbeBilling(ctx, providerID)
	if billingErr == nil && billingResult != nil && GrokBillingHasAuthoritativeQuota(billingResult.Billing) {
		if acc, err := s.Options.GetProvider(ctx, providerID); err == nil {
			s.Options.ScheduleModels(acc)
		}
		return billingResult, nil
	}

	probeResult, probeErr := s.ProbeUsage(ctx, providerID)
	if probeErr != nil {
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
	if acc, err := s.Options.GetProvider(ctx, providerID); err == nil {
		s.Options.ScheduleModels(acc)
	}
	return probeResult, nil
}

func (s *GrokQuotaService) ProbeUsage(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	return s.RunProbeFlight(ctx, "active:"+strconv.FormatInt(providerID, 10), func(sharedCtx context.Context) (*GrokQuotaProbeResult, error) {
		return s.ProbeUsageDirect(sharedCtx, providerID)
	})
}

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
		if limited {
			s.Options.PersistLimit(ctx, provider, resetAt)
		} else if s.Options.CanRecover(provider, snapshot) {
			s.Options.ClearLimit(ctx, provider)
		}

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

// ProbeBilling queries xAI billing for provider usage display and coalesces concurrent probes.
func (s *GrokQuotaService) ProbeBilling(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	return s.RunProbeFlight(ctx, "billing:"+strconv.FormatInt(providerID, 10), func(sharedCtx context.Context) (*GrokQuotaProbeResult, error) {
		return s.ProbeBillingDirect(sharedCtx, providerID)
	})
}

// ProbeMediaEligibility refreshes billing and evaluates the reloaded provider snapshot.
// It returns a probe error when billing remains unobserved; known ineligibility is an ordinary result.
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

func (s *GrokQuotaService) ProbeBillingDirect(ctx context.Context, providerID int64) (*GrokQuotaProbeResult, error) {
	provider, token, proxyURL, err := s.PrepareProbe(ctx, providerID)
	if err != nil {
		return nil, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, s.Options.Timeout)
	defer cancel()
	type billingResult struct {
		summary *usageview.BillingSummary
		status  int
		err     error
	}
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
	previous, _ := ParseGrokBillingSnapshot(provider.Extra)
	if !weeklyOK && !monthlyOK {
		probeErr := s.MergeGrokBillingProbeErrors(weekly.status, monthly.status, weekly.err, monthly.err)
		billing := s.Options.MergeBilling(previous, nil, nil, false, false)
		if billing == nil {
			billing = &usageview.BillingSummary{Partial: true, FailedWindows: []string{"weekly", "monthly"}}
		}
		billing.WeeklyStatusCode = weekly.status
		billing.MonthlyStatusCode = monthly.status
		billing = s.Options.StampBilling(billing, s.PreferBillingObservationStatus(weekly.status, monthly.status), "billing_probe")
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
	persistErr := s.Options.UpdateExtra(ctx, provider.ID, map[string]any{
		GrokUsageBillingExtraKey: billing,
	})
	if persistErr != nil {
		s.Options.Warn("grok_billing_persist_failed", "provider_id", provider.ID, "error", persistErr)
	}
	now := time.Now().UTC()
	localUsage24h, localUsage7d, localUsageMonthly := s.Options.LocalStats(ctx, provider.ID, billing, now)
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

// PreferBillingObservationStatus prioritizes 403 because it determines media ineligibility.
func (s *GrokQuotaService) PreferBillingObservationStatus(weeklyStatus, monthlyStatus int) int {
	if weeklyStatus == 403 || monthlyStatus == 403 {
		return 403
	}
	if weeklyStatus != 0 {
		return weeklyStatus
	}
	return monthlyStatus
}

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

// StopContext stops the shared runtime and waits for probes and model sync within the caller's budget.
func (s *GrokQuotaService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.Runtime.StopContext(ctx)
}

func (s *GrokQuotaService) MergeGrokBillingProbeErrors(weeklyStatus, monthlyStatus int, weeklyErr, monthlyErr error) error {
	weeklyKey := s.GrokBillingProbeErrorKey(weeklyStatus, weeklyErr)
	monthlyKey := s.GrokBillingProbeErrorKey(monthlyStatus, monthlyErr)
	if weeklyKey == monthlyKey {
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

func (s *GrokQuotaService) GrokBillingProbeErrorKey(status int, err error) string {
	if err != nil {
		return strconv.Itoa(status) + ":" + strconv.Itoa(int(infraerrors.CategoryOf(err))) + ":" + infraerrors.Reason(err)
	}
	return strconv.Itoa(status) + ":empty"
}

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

func (s *GrokQuotaService) ResetQuota(ctx context.Context, providerID int64) (*GrokQuotaResetResult, error) {
	if _, err := s.LoadGrokOAuthProvider(ctx, providerID); err != nil {
		return nil, err
	}
	return nil, infraerrors.New(infraerrors.Category(501), "GROK_QUOTA_RESET_UNSUPPORTED", "xAI does not expose a Grok subscription quota reset endpoint for OAuth providers")
}

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
