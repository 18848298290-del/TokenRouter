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

var (
	benchmarkOpsMonitoringEnabled bool
	benchmarkOpsAdvancedSettings  OpsAdvancedSettings
)

type opsRuntimeRefreshRepo struct {
	Settings
	mu     sync.RWMutex
	values map[string]string
	fail   atomic.Bool
	calls  atomic.Int64
}

// OpsRuntimeSettingsRefreshHealth exposes refresh state for tests.
type OpsRuntimeSettingsRefreshHealth struct {
	Running      bool   `json:"running"`
	SuccessTotal uint64 `json:"success_total"`
	FailureTotal uint64 `json:"failure_total"`
}

type stubOpsRepoForUserErr struct {
	OpsRepository // Unimplemented methods on the embedded nil interface panic.
	gotFilter     *OpsErrorLogFilter

	// Results returned by GetErrorLogByID.
	detailToReturn    *OpsErrorLogDetail
	detailErrToReturn error
}

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

func (r *opsRuntimeRefreshRepo) set(key, value string) {
	r.mu.Lock()
	r.values[key] = value
	r.mu.Unlock()
}

func waitForOpsRefresh(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

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

func TestOpsRuntimeSettingsBackgroundRefreshConverges(t *testing.T) {
	repo := &opsRuntimeRefreshRepo{values: map[string]string{SettingKeyOpsMonitoringEnabled: "false"}}
	svc := &OpsService{settingRepo: repo}
	svc.initRuntimeSettings(context.Background())
	if svc.IsMonitoringEnabled(context.Background()) {
		t.Fatal("initial monitoring state = true, want false")
	}

	repo.set(SettingKeyOpsMonitoringEnabled, "true")
	svc.startRuntimeSettingsRefresh(context.Background(), 5*time.Millisecond, 0, 50*time.Millisecond)
	t.Cleanup(svc.StopRuntimeSettingsRefresh)
	waitForOpsRefresh(t, time.Second, func() bool {
		return svc.IsMonitoringEnabled(context.Background()) && svc.RuntimeSettingsRefreshHealth().SuccessTotal > 0
	})
}

// TestOpsRuntimeSettingsRefreshFailuresKeepLastKnownGoodSnapshot checks cached values after repeated failures.
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
		defer svc.StopRuntimeSettingsRefresh()

		// Advance the virtual clock and wait for each refresh to finish.
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

func TestOpsServiceRecordErrorBatch_SkipsFallbackAfterContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
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

func strPtr(v string) *string {
	return &v
}

// RuntimeSettingsRefreshHealth reads refresh state and success/failure counters.
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

func (s *stubOpsRepoForUserErr) GetErrorLogByID(ctx context.Context, id int64) (*OpsErrorLogDetail, error) {
	if s.detailErrToReturn != nil {
		return nil, s.detailErrToReturn
	}
	return s.detailToReturn, nil
}

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
	// Queries are scoped to the authenticated user.
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
