package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// benchmarkOpsMonitoringEnabled and benchmarkOpsAdvancedSettings retain each
// measured read result after serial benchmark iterations, keeping observable
// output outside the timed loop's local variables.
var (
	benchmarkOpsMonitoringEnabled bool
	benchmarkOpsAdvancedSettings  OpsAdvancedSettings
)

// opsRuntimeRefreshRepo supplies controlled preference reads and independent
// failure/call observations for polling tests.
type opsRuntimeRefreshRepo struct {
	// Settings embeds unsupported methods through a nil interface in these
	// fixtures. Tests call the supplied GetMultiple method; other embedded
	// operations require another implementation.
	Settings
	// mu protects values during reads and set updates. GetMultiple returns a new
	// selected map, while fail and calls are independent atomics sampled outside
	// that value-map lock.
	mu     sync.RWMutex      // fixture values map mutex
	values map[string]string // fixture persisted preference map
	fail   atomic.Bool       // injected preference read failure
	calls  atomic.Int64      // observed attempted preference reads
}

// OpsRuntimeSettingsRefreshHealth carries test-only independently sampled
// polling state and attempt counters.
type OpsRuntimeSettingsRefreshHealth struct {
	// Running, SuccessTotal, and FailureTotal are separate atomic observations
	// read by the test helper. They can reflect different instants, and Stop
	// joining establishes completion for assertions.
	Running      bool   `json:"running"`       // sampled worker running flag
	SuccessTotal uint64 `json:"success_total"` // cumulative successful preference reads
	FailureTotal uint64 `json:"failure_total"` // cumulative failed preference reads
}

// stubOpsRepoForUserErr captures user-list selection and supplies controlled
// diagnostic detail for serial fixtures.
type stubOpsRepoForUserErr struct {
	// OpsRepository embeds unavailable operations through a nil interface. These
	// fixtures supply ListErrorLogs and GetErrorLogByID only; callers reaching
	// other methods would panic.
	OpsRepository
	// gotFilter retains the supplied shallow filter pointer for assertions. Tests
	// leave pointed-to identities and category slices stable while inspecting it
	// after the call.
	gotFilter *OpsErrorLogFilter // retained supplied user-list filter

	// detailErrToReturn takes precedence over detailToReturn in GetErrorLogByID.
	// Successful detail fixtures configure a nonnil shared pointer, while error
	// fixtures need no detail allocation.
	detailToReturn    *OpsErrorLogDetail // shared configured diagnostic pointer
	detailErrToReturn error              // configured diagnostic lookup error
}

// TestSanitizeOpsUpstreamErrorsForQueueBoundsAndRedacts supplies twenty
// oversized upstream events with JSON credential fields. It protects sixteen
// serialized survivors, raw-list release, byte limits, and removal of the
// selected fixture credentials.
func TestSanitizeOpsUpstreamErrorsForQueueBoundsAndRedacts(t *testing.T) {
	entry := &OpsInsertErrorLogInput{}
	for range 20 {
		entry.UpstreamErrors = append(entry.UpstreamErrors, &OpsUpstreamErrorEvent{
			Platform:             strings.Repeat("p", 100),
			ProviderName:         strings.Repeat("a", 300),
			UpstreamStatusCode:   500,
			UpstreamURL:          strings.Repeat("u", 3000),
			UpstreamResponseBody: `{"authorization":"Bearer secret","message":"` + strings.Repeat("x", 10_000) + `"}`,
			Message:              strings.Repeat("m", 3000),
			Detail:               `{"api_key":"secret","detail":"` + strings.Repeat("y", 10_000) + `"}`,
		})
	}

	if err := SanitizeOpsUpstreamErrorsForQueue(entry); err != nil {
		t.Fatal(err)
	}
	if entry.UpstreamErrors != nil {
		t.Fatal("raw upstream event slice must be released before queueing")
	}
	if entry.UpstreamErrorsJSON == nil {
		t.Fatal("sanitized upstream event JSON is missing")
	}
	events, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 16 {
		t.Fatalf("event count = %d, want 16", len(events))
	}
	for _, event := range events {
		if len(event.Platform) > 32 || len(event.ProviderName) > 128 || len(event.UpstreamURL) > 2048 || len(event.Message) > 2048 {
			t.Fatalf("event fields were not bounded: %+v", event)
		}
		if len(event.UpstreamResponseBody) > OpsErrorLogQueueBodyMaxBytes || len(event.Detail) > OpsErrorLogQueueBodyMaxBytes {
			t.Fatal("event body/detail exceeded queue limit")
		}
		if strings.Contains(event.UpstreamResponseBody, "Bearer secret") || strings.Contains(event.Detail, `"secret"`) {
			t.Fatal("credential material was not redacted")
		}
	}
}

// GetMultiple counts the attempt before checking context cancellation and the
// injected failure flag. On success it copies selected string values under the
// read lock into a new map; fixture callers supply nonnil ctx.
func (r *opsRuntimeRefreshRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	r.calls.Add(1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if r.fail.Load() {
		return nil, errors.New("settings unavailable")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// set changes one fixture preference under the values mutex. The map must be
// initialized by the fixture; background GetMultiple calls then see that update
// when they next acquire the read lock.
func (r *opsRuntimeRefreshRepo) set(key, value string) {
	r.mu.Lock()
	r.values[key] = value
	r.mu.Unlock()
}

// waitForOpsRefresh polls a supplied condition with millisecond waits until an
// elapsed-time deadline, then fatals on failure. Conditions can read
// independent atomic health values; the helper owns no cancellation of the
// worker being observed.
func waitForOpsRefresh(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		// Yield between checks so the independently scheduled worker can publish
		// progress before the fixture deadline expires.
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

// TestOpsRuntimeSettingsSnapshotLoadsOnceAndServesHotPath initializes two
// stored preferences and performs one thousand snapshot reads. It protects
// loaded values and exactly one initial bulk read with no hot-path single
// reads.
func TestOpsRuntimeSettingsSnapshotLoadsOnceAndServesHotPath(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values[SettingKeyOpsMonitoringEnabled] = "false"
	repo.values[SettingKeyOpsAdvancedSettings] = `{"ignore_context_canceled":false,"auto_refresh_interval_seconds":45}`

	svc := &OpsService{settingRepo: repo}
	svc.initRuntimeSettings(context.Background())
	if repo.getMultipleCalls != 1 {
		t.Fatalf("startup GetMultiple calls = %d, want 1", repo.getMultipleCalls)
	}

	for range 1000 {
		if svc.IsMonitoringEnabled(context.Background()) {
			t.Fatal("monitoring enabled, want false")
		}
		cfg, err := svc.GetOpsAdvancedSettings(context.Background())
		if err != nil {
			t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
		}
		if cfg.AutoRefreshIntervalSec != 45 {
			t.Fatalf("AutoRefreshIntervalSec = %d, want 45", cfg.AutoRefreshIntervalSec)
		}
	}
	if repo.getValueCalls != 0 || repo.getMultipleCalls != 1 {
		t.Fatalf("hot path touched repository: get=%d get_multiple=%d", repo.getValueCalls, repo.getMultipleCalls)
	}
}

// TestOpsRuntimeSettingsAdministrativeUpdatesAreImmediatelyVisible changes
// monitoring and advanced preferences directly in memory. It protects immediate
// snapshot visibility and retention of monitoring state while the advanced
// component changes.
func TestOpsRuntimeSettingsAdministrativeUpdatesAreImmediatelyVisible(t *testing.T) {
	svc := &OpsService{}
	svc.initRuntimeSettings(context.Background())

	svc.SetMonitoringEnabled(false)
	if svc.IsMonitoringEnabled(context.Background()) {
		t.Fatal("monitoring update was not visible")
	}

	cfg := defaultOpsAdvancedSettings()
	cfg.IgnoreNoAvailableProviders = true
	svc.storeAdvancedSettingsSnapshot(cfg)
	got, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if !got.IgnoreNoAvailableProviders {
		t.Fatal("advanced settings update was not visible")
	}
	if svc.IsMonitoringEnabled(context.Background()) {
		t.Fatal("advanced update overwrote monitoring setting")
	}
}

// TestOpsRuntimeSettingsBackgroundRefreshConverges changes a fixture preference
// after initialization and starts short-interval polling. It protects eventual
// updated monitoring plus a recorded success and stops the worker through test
// cleanup.
func TestOpsRuntimeSettingsBackgroundRefreshConverges(t *testing.T) {
	repo := &opsRuntimeRefreshRepo{values: map[string]string{SettingKeyOpsMonitoringEnabled: "false"}}
	svc := &OpsService{settingRepo: repo}
	svc.initRuntimeSettings(context.Background())
	if svc.IsMonitoringEnabled(context.Background()) {
		t.Fatal("initial monitoring state = true, want false")
	}

	repo.set(SettingKeyOpsMonitoringEnabled, "true")
	svc.startRuntimeSettingsRefresh(context.Background(), 5*time.Millisecond, 0, 50*time.Millisecond)
	// Stop and join the background refresh after the test completes or fatals,
	// releasing the worker while its fixture map remains available.
	t.Cleanup(svc.StopRuntimeSettingsRefresh)
	waitForOpsRefresh(t, time.Second, func() bool {
		return svc.IsMonitoringEnabled(context.Background()) && svc.RuntimeSettingsRefreshHealth().SuccessTotal > 0
	})
}

// TestOpsRuntimeSettingsRefreshFailuresKeepLastKnownGoodSnapshot injects three
// failed reads under a virtual clock after loading valid preferences. It
// protects failure counting and retained monitoring/advanced values across
// those failures.
func TestOpsRuntimeSettingsRefreshFailuresKeepLastKnownGoodSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &opsRuntimeRefreshRepo{values: map[string]string{
			SettingKeyOpsMonitoringEnabled: "false",
			SettingKeyOpsAdvancedSettings:  `{"ignore_no_available_providers":true}`,
		}}
		svc := &OpsService{settingRepo: repo}
		svc.initRuntimeSettings(context.Background())
		repo.fail.Store(true)
		svc.startRuntimeSettingsRefresh(context.Background(), 5*time.Millisecond, 0, 50*time.Millisecond)
		// Join the polling worker when the virtual-clock callback exits, so its
		// retained handles and timers do not outlive the failure assertions.
		defer svc.StopRuntimeSettingsRefresh()

		// Wait until the worker is durably blocked, then advance one configured
		// interval per iteration and wait again so each injected refresh failure is
		// counted deterministically.
		synctest.Wait()
		for range 3 {
			time.Sleep(5 * time.Millisecond)
			synctest.Wait()
		}
		if got := svc.RuntimeSettingsRefreshHealth().FailureTotal; got != 3 {
			t.Fatalf("refresh failures = %d, want 3", got)
		}
		if svc.IsMonitoringEnabled(context.Background()) {
			t.Fatal("failed refresh overwrote last known monitoring state")
		}
		if !svc.OpsAdvancedSettingsSnapshot().IgnoreNoAvailableProviders {
			t.Fatal("failed refresh overwrote last known advanced settings")
		}
	})
}

// TestOpsRuntimeSettingsRefreshStopEndsLifecycle starts polling and waits for
// success before stopping. It protects no additional fixture reads during the
// subsequent observation interval, a false running flag, and repeated stop
// completion.
func TestOpsRuntimeSettingsRefreshStopEndsLifecycle(t *testing.T) {
	repo := &opsRuntimeRefreshRepo{values: map[string]string{}}
	svc := &OpsService{settingRepo: repo}
	svc.initRuntimeSettings(context.Background())
	svc.startRuntimeSettingsRefresh(context.Background(), 5*time.Millisecond, 0, 50*time.Millisecond)
	waitForOpsRefresh(t, time.Second, func() bool {
		return svc.RuntimeSettingsRefreshHealth().SuccessTotal > 0
	})

	svc.StopRuntimeSettingsRefresh()
	callsAfterStop := repo.calls.Load()
	time.Sleep(20 * time.Millisecond)
	if got := repo.calls.Load(); got != callsAfterStop {
		t.Fatalf("refresh continued after Stop: before=%d after=%d", callsAfterStop, got)
	}
	if svc.RuntimeSettingsRefreshHealth().Running {
		t.Fatal("refresh health still reports running after Stop")
	}
	// Stopping an already stopped refresh loop is safe.
	svc.StopRuntimeSettingsRefresh()
}

// BenchmarkOpsRuntimeSettingsSnapshotRead measures cached monitoring and
// advanced-value reads after initialization. It reports allocations and retains
// results globally; storage initialization remains outside the timed benchmark
// iterations.
func BenchmarkOpsRuntimeSettingsSnapshotRead(b *testing.B) {
	svc := &OpsService{}
	svc.initRuntimeSettings(context.Background())
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkOpsMonitoringEnabled = svc.IsMonitoringEnabled(ctx)
		benchmarkOpsAdvancedSettings = svc.OpsAdvancedSettingsSnapshot()
	}
}

// TestOpsServiceRecordErrorBatch_SanitizesAndBatches supplies two entries with
// missing defaults and selected JSON/query credentials. It protects one
// captured prepared batch, explicit status cleanup, raw-list release, and
// selected redaction/default values.
func TestOpsServiceRecordErrorBatch_SanitizesAndBatches(t *testing.T) {
	t.Parallel()

	var captured []*OpsInsertErrorLogInput
	repo := &opsRepoMock{
		BatchInsertErrorLogsFn: func(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error) {
			captured = append(captured, inputs...)
			return int64(len(inputs)), nil
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	msg := " upstream failed: https://example.com?access_token=secret-value "
	detail := `{"authorization":"Bearer secret-token"}`
	entries := []*OpsInsertErrorLogInput{
		{
			ErrorBody:            `{"error":"bad","access_token":"secret"}`,
			UpstreamStatusCode:   intPtr(-10),
			UpstreamErrorMessage: strPtr(msg),
			UpstreamErrorDetail:  strPtr(detail),
			UpstreamErrors: []*OpsUpstreamErrorEvent{
				{
					ProviderID:         -2,
					UpstreamStatusCode: 429,
					Message:            " token leaked ",
					Detail:             `{"refresh_token":"secret"}`,
				},
			},
		},
		{
			ErrorPhase: "upstream",
			ErrorType:  "upstream_error",
			CreatedAt:  time.Now().UTC(),
		},
	}

	require.NoError(t, svc.RecordErrorBatch(context.Background(), entries))
	require.Len(t, captured, 2)

	first := captured[0]
	require.Equal(t, "internal", first.ErrorPhase)
	require.Equal(t, "api_error", first.ErrorType)
	require.Nil(t, first.UpstreamStatusCode)
	require.NotNil(t, first.UpstreamErrorMessage)
	require.NotContains(t, *first.UpstreamErrorMessage, "secret-value")
	require.Contains(t, *first.UpstreamErrorMessage, "access_token=***")
	require.NotNil(t, first.UpstreamErrorDetail)
	require.NotContains(t, *first.UpstreamErrorDetail, "secret-token")
	require.NotContains(t, first.ErrorBody, "secret")
	require.Nil(t, first.UpstreamErrors)
	require.NotNil(t, first.UpstreamErrorsJSON)
	require.NotContains(t, *first.UpstreamErrorsJSON, "secret")
	require.Contains(t, *first.UpstreamErrorsJSON, "[REDACTED]")

	second := captured[1]
	require.Equal(t, "upstream", second.ErrorPhase)
	require.Equal(t, "upstream_error", second.ErrorType)
	require.False(t, second.CreatedAt.IsZero())
}

// TestOpsServiceRecordErrorBatch_DoesNotFallbackToSingleInsertsWhenBatchFails
// injects a failed two-entry repository batch. It protects one batch call,
// returned failure, and zero single-row replay attempts afterward.
func TestOpsServiceRecordErrorBatch_DoesNotFallbackToSingleInsertsWhenBatchFails(t *testing.T) {
	t.Parallel()

	var (
		batchCalls  int
		singleCalls int
	)
	repo := &opsRepoMock{
		BatchInsertErrorLogsFn: func(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error) {
			batchCalls++
			return 0, errors.New("batch failed")
		},
		InsertErrorLogFn: func(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error) {
			singleCalls++
			return int64(singleCalls), nil
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	err := svc.RecordErrorBatch(context.Background(), []*OpsInsertErrorLogInput{
		{ErrorMessage: "first"},
		{ErrorMessage: "second"},
	})
	require.Error(t, err)
	require.Equal(t, 1, batchCalls)
	require.Zero(t, singleCalls)
}

// TestOpsServiceRecordErrorBatch_SkipsFallbackAfterContextCanceled injects an
// already canceled context and a repository error. It protects returned
// cancellation, one batch attempt, and no single-row replay for that supplied
// fixture.
func TestOpsServiceRecordErrorBatch_SkipsFallbackAfterContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before service execution so the fixture's batch failure returns that
	// context error; preparation and the attempted batch still occur as supplied.
	cancel()
	var (
		batchCalls  int
		singleCalls int
	)
	repo := &opsRepoMock{
		BatchInsertErrorLogsFn: func(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error) {
			batchCalls++
			return 0, ctx.Err()
		},
		InsertErrorLogFn: func(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error) {
			singleCalls++
			return 0, ctx.Err()
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	err := svc.RecordErrorBatch(ctx, []*OpsInsertErrorLogInput{
		{ErrorMessage: "first"},
		{ErrorMessage: "second"},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, batchCalls)
	require.Equal(t, 0, singleCalls)
}

// TestOpsServiceRecordErrorPersistsExplicitProviderAuthStatusZero supplies
// retained inference followed by credential-failure evidence. It protects
// provider_auth precedence, explicit upstream zero, cleared stale detail, and
// both diagnostic events in serialized history.
func TestOpsServiceRecordErrorPersistsExplicitProviderAuthStatusZero(t *testing.T) {
	t.Parallel()

	var captured *OpsInsertErrorLogInput
	repo := &opsRepoMock{
		InsertErrorLogFn: func(_ context.Context, input *OpsInsertErrorLogInput) (int64, error) {
			captured = input
			return 1, nil
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	staleStatus := 403
	staleMessage := "stale inference message"
	staleDetail := "stale inference detail"

	err := svc.RecordError(context.Background(), &OpsInsertErrorLogInput{
		ErrorPhase:           "upstream",
		ErrorType:            "upstream_error",
		ErrorOwner:           "provider",
		ErrorSource:          "upstream_http",
		UpstreamStatusCode:   &staleStatus,
		UpstreamErrorMessage: &staleMessage,
		UpstreamErrorDetail:  &staleDetail,
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{Stage: "inference", UpstreamStatusCode: 403, Message: staleMessage, Detail: staleDetail},
			{
				Stage: "provider_auth", Scope: "provider",
				Reason: "grok_oauth_credential_revoked", Message: "Grok OAuth credentials require provider action",
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, "provider_auth", captured.ErrorPhase)
	require.Equal(t, "provider", captured.ErrorOwner)
	require.Equal(t, "gateway", captured.ErrorSource)
	require.NotNil(t, captured.UpstreamStatusCode)
	require.Zero(t, *captured.UpstreamStatusCode)
	require.NotNil(t, captured.UpstreamErrorMessage)
	require.Equal(t, "Grok OAuth credentials require provider action", *captured.UpstreamErrorMessage)
	require.Nil(t, captured.UpstreamErrorDetail)
	require.Nil(t, captured.UpstreamErrors)
	require.NotNil(t, captured.UpstreamErrorsJSON)
	require.Contains(t, *captured.UpstreamErrorsJSON, `"upstream_status_code":403`)
	require.Contains(t, *captured.UpstreamErrorsJSON, `"stage":"provider_auth"`)
}

// strPtr allocates a pointer to the supplied fixture string value.
func strPtr(v string) *string {
	return &v
}

// RuntimeSettingsRefreshHealth returns test-only separately loaded polling
// state and attempt counters, with zero fields for nil service. Values are not
// a common snapshot; tests use Stop joining where consistent completion
// matters.
func (s *OpsService) RuntimeSettingsRefreshHealth() OpsRuntimeSettingsRefreshHealth {
	if s == nil {
		return OpsRuntimeSettingsRefreshHealth{}
	}
	return OpsRuntimeSettingsRefreshHealth{
		Running:      s.runtimeRefreshRunning.Load(),
		SuccessTotal: s.runtimeRefreshSuccess.Load(),
		FailureTotal: s.runtimeRefreshFailure.Load(),
	}
}

// TestIsSensitiveKey_TokenBudgetKeysNotRedacted supplies telemetry budget names
// and selected credential names. It protects the listed allowlist decisions and
// credential matching, including the tested case-folded Authorization spelling.
func TestIsSensitiveKey_TokenBudgetKeysNotRedacted(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"max_tokens",
		"max_output_tokens",
		"max_input_tokens",
		"max_completion_tokens",
		"max_tokens_to_sample",
		"budget_tokens",
		"prompt_tokens",
		"completion_tokens",
		"input_tokens",
		"output_tokens",
		"total_tokens",
		"token_count",
	} {
		if isSensitiveKey(key) {
			t.Fatalf("expected key %q to NOT be treated as sensitive", key)
		}
	}

	for _, key := range []string{
		"authorization",
		"Authorization",
		"access_token",
		"refresh_token",
		"id_token",
		"session_token",
		"token",
		"client_secret",
		"private_key",
		"signature",
	} {
		if !isSensitiveKey(key) {
			t.Fatalf("expected key %q to be treated as sensitive", key)
		}
	}
}

// TestSanitizeAndTrimJSONPayload_PreservesTokenBudgetFields supplies nested
// thinking budgets and an access token in a fitting JSON body. It protects
// retained numeric budget fields and replacement of the selected credential
// value.
func TestSanitizeAndTrimJSONPayload_PreservesTokenBudgetFields(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"model":"claude-3","max_tokens":123,"thinking":{"type":"enabled","budget_tokens":456},"access_token":"abc","messages":[{"role":"user","content":"hi"}]}`)
	out, _, _ := sanitizeAndTrimJSONPayload(raw, 10*1024)
	if out == "" {
		t.Fatalf("expected non-empty sanitized output")
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal sanitized output: %v", err)
	}

	if got, ok := decoded["max_tokens"].(float64); !ok || got != 123 {
		t.Fatalf("expected max_tokens=123, got %#v", decoded["max_tokens"])
	}

	thinking, ok := decoded["thinking"].(map[string]any)
	if !ok || thinking == nil {
		t.Fatalf("expected thinking object to be preserved, got %#v", decoded["thinking"])
	}
	if got, ok := thinking["budget_tokens"].(float64); !ok || got != 456 {
		t.Fatalf("expected thinking.budget_tokens=456, got %#v", thinking["budget_tokens"])
	}

	if got := decoded["access_token"]; got != "[REDACTED]" {
		t.Fatalf("expected access_token to be redacted, got %#v", got)
	}
}

// TestShrinkToEssentials_IncludesThinking supplies a thinking configuration and
// a two-element conversation. It protects the presence of the thinking field in
// the selected essential map; it does not assert conversation truncation or
// deep copying.
func TestShrinkToEssentials_IncludesThinking(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"model":      "claude-3",
		"max_tokens": 100,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 200,
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "first"},
			map[string]any{"role": "user", "content": "last"},
		},
	}

	out := shrinkToEssentials(root)
	if _, ok := out["thinking"]; !ok {
		t.Fatalf("expected thinking to be included in essentials: %#v", out)
	}
}

// ListErrorLogs retains the supplied filter and returns one synthetic error
// with fixed pagination metadata. It ignores context and predicate execution,
// so tests inspect the captured service selection rather than database
// ownership enforcement.
func (s *stubOpsRepoForUserErr) ListErrorLogs(ctx context.Context, f *OpsErrorLogFilter) (*OpsErrorLogList, error) {
	s.gotFilter = f
	return &OpsErrorLogList{
		Errors: []*OpsErrorLog{{
			Phase: "request", Type: "rate_limit_error",
			Model: "m", RequestedModel: "rm", StatusCode: 429,
			Message: "secret", UserEmail: "a@b.c",
		}},
		Total: 1, Page: 1, PageSize: 20,
	}, nil
}

// GetErrorLogByID returns the configured error first, otherwise the shared
// detail pointer. Context and requested ID are ignored; successful user-detail
// fixtures provide nonnil data for the service's ownership comparison.
func (s *stubOpsRepoForUserErr) GetErrorLogByID(ctx context.Context, id int64) (*OpsErrorLogDetail, error) {
	if s.detailErrToReturn != nil {
		return nil, s.detailErrToReturn
	}
	return s.detailToReturn, nil
}

// TestListUserErrorRequests_ForcesScopeAndRedacts supplies a positive user
// identity plus a conflicting administrator-style filter. It protects forced
// selection fields, retained key identity, unchanged caller scalars, and
// returned category/requested-model display values through a stub.
func TestListUserErrorRequests_ForcesScopeAndRedacts(t *testing.T) {
	stub := &stubOpsRepoForUserErr{}
	svc := &OpsService{opsRepo: stub}
	uid := int64(42)
	kid := int64(7)
	in := &OpsErrorLogFilter{UserID: nil, View: "errors", Phase: "upstream", APIKeyID: &kid}
	out, err := svc.ListUserErrorRequests(context.Background(), uid, in)
	if err != nil {
		t.Fatal(err)
	}
	// The stub captures a positive caller identity rather than running SQL. Verify
	// the service overwrites the selection before checking its other user-facing
	// restrictions.
	if stub.gotFilter.UserID == nil || *stub.gotFilter.UserID != uid {
		t.Fatalf("UserID not forced: %+v", stub.gotFilter.UserID)
	}
	// Include business-limit and balance errors.
	if stub.gotFilter.View != "all" {
		t.Fatalf("View not forced to all: %q", stub.gotFilter.View)
	}
	// Exclude count_tokens probes.
	if !stub.gotFilter.ExcludeCountTokens {
		t.Fatal("ExcludeCountTokens not forced")
	}
	// Clear Phase so it cannot bypass status filtering or conflict with category phases.
	if stub.gotFilter.Phase != "" {
		t.Fatalf("Phase not cleared: %q", stub.gotFilter.Phase)
	}
	// Retain APIKeyID; the repository combines it with UserID for ownership checks.
	if stub.gotFilter.APIKeyID == nil || *stub.gotFilter.APIKeyID != kid {
		t.Fatalf("APIKeyID should be preserved, got %v", stub.gotFilter.APIKeyID)
	}
	// The shallow copy must keep the caller's scalar filters unchanged.
	if in.View != "errors" || in.UserID != nil || in.Phase != "upstream" {
		t.Fatalf("caller filter was mutated: View=%q UserID=%v Phase=%q", in.View, in.UserID, in.Phase)
	}
	// Check the sanitized category and requested model.
	if len(out.Items) != 1 || out.Items[0].Category != "rate_limit" || out.Items[0].Model != "rm" {
		t.Fatalf("bad item: %+v", out.Items)
	}
}

// TestGetUserErrorRequestDetail_OwnershipEnforced supplies one saved owner's
// diagnostic and calls as another user then the owner. It protects not-found
// concealment for the other user and retained body/status/message for the
// owner.
func TestGetUserErrorRequestDetail_OwnershipEnforced(t *testing.T) {
	ownerUID := int64(999)
	callerUID := int64(1)
	upstreamStatus := 503

	detail := &OpsErrorLogDetail{
		OpsErrorLog: OpsErrorLog{
			ID:              42,
			Phase:           "upstream",
			Type:            "api_error",
			Model:           "gpt-4",
			RequestedModel:  "gpt-4-turbo",
			InboundEndpoint: "/v1/chat/completions",
			StatusCode:      502,
			Platform:        "openai",
			Message:         "upstream failed",
			UserID:          &ownerUID,
		},
		ErrorBody:          `{"error":"upstream"}`,
		UpstreamStatusCode: &upstreamStatus,
	}

	stub := &stubOpsRepoForUserErr{detailToReturn: detail}
	svc := &OpsService{opsRepo: stub}

	// A different user receives NotFound and no details.
	got, err := svc.GetUserErrorRequestDetail(context.Background(), callerUID, 42)
	if err == nil {
		t.Fatal("expected error for unauthorized access, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil detail for unauthorized access, got %+v", got)
	}
	// Other users' records are reported as missing.
	if !apperror.IsNotFound(err) {
		t.Fatalf("expected NotFound error, got: %v", err)
	}

	// The owner receives the request details.
	got2, err2 := svc.GetUserErrorRequestDetail(context.Background(), ownerUID, 42)
	if err2 != nil {
		t.Fatalf("expected no error for legitimate access, got %v", err2)
	}
	if got2 == nil {
		t.Fatal("expected non-nil detail for legitimate access")
		return
	}
	if got2.ID != 42 {
		t.Errorf("want ID=42, got %d", got2.ID)
	}
	if got2.ErrorBody != `{"error":"upstream"}` {
		t.Errorf("want ErrorBody=%q, got %q", `{"error":"upstream"}`, got2.ErrorBody)
	}
	if got2.UpstreamStatusCode == nil || *got2.UpstreamStatusCode != 503 {
		t.Errorf("want UpstreamStatusCode=503, got %v", got2.UpstreamStatusCode)
	}
	if got2.Message != "upstream failed" {
		t.Errorf("want Message=%q, got %q", "upstream failed", got2.Message)
	}
}

// TestGetUserErrorRequestDetail_NotFound injects a missing-row repository error
// for a positive identity. It protects a failed user-detail lookup with nil
// returned detail for that supplied absence.
func TestGetUserErrorRequestDetail_NotFound(t *testing.T) {
	stub := &stubOpsRepoForUserErr{detailErrToReturn: ErrRowNotFound}
	svc := &OpsService{opsRepo: stub}

	got, err := svc.GetUserErrorRequestDetail(context.Background(), 1, 999)
	if err == nil {
		t.Fatal("expected error for not found, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil detail, got %+v", got)
	}
}

// TestGetUserErrorRequestDetail_InvalidID supplies zero and negative error
// identities through a configured service. It protects rejection before a
// successful detail lookup would use the stub's unset detail pointer.
func TestGetUserErrorRequestDetail_InvalidID(t *testing.T) {
	stub := &stubOpsRepoForUserErr{}
	svc := &OpsService{opsRepo: stub}

	_, err := svc.GetUserErrorRequestDetail(context.Background(), 1, 0)
	if err == nil {
		t.Fatal("expected error for id=0")
	}
	_, err = svc.GetUserErrorRequestDetail(context.Background(), 1, -5)
	if err == nil {
		t.Fatal("expected error for id=-5")
	}
}
