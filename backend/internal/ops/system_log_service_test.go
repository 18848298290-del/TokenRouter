package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestOpsServiceListSystemLogs_DefaultClampAndSuccess supplies page zero and an
// oversized page size to a repository hook. It checks the filter becomes page
// one and size two hundred, with one returned row and matching total.
func TestOpsServiceListSystemLogs_DefaultClampAndSuccess(t *testing.T) {
	var gotFilter *OpsSystemLogFilter
	repo := &opsRepoMock{
		ListSystemLogsFn: func(ctx context.Context, filter *OpsSystemLogFilter) (*OpsSystemLogList, error) {
			gotFilter = filter
			return &OpsSystemLogList{
				Logs:     []*OpsSystemLog{{ID: 1, Level: "warn", Message: "x"}},
				Total:    1,
				Page:     filter.Page,
				PageSize: filter.PageSize,
			}, nil
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	out, err := svc.ListSystemLogs(context.Background(), &OpsSystemLogFilter{
		Page:     0,
		PageSize: 999,
	})
	if err != nil {
		t.Fatalf("ListSystemLogs() error: %v", err)
	}
	if gotFilter == nil {
		t.Fatalf("expected repository to receive filter")
	}
	if gotFilter.Page != 1 || gotFilter.PageSize != 200 {
		t.Fatalf("filter normalized unexpectedly: page=%d pageSize=%d", gotFilter.Page, gotFilter.PageSize)
	}
	if out.Total != 1 || len(out.Logs) != 1 {
		t.Fatalf("unexpected result: %+v", out)
	}
}

// TestOpsServiceListSystemLogs_MonitoringDisabled uses a hard-disabled Ops
// service with a repository fixture. It checks this disabled configuration
// makes the service return a listing error.
func TestOpsServiceListSystemLogs_MonitoringDisabled(t *testing.T) {
	svc := newLegacyShapeOpsService(
		&opsRepoMock{},
		nil,
		&Options{Ops: RuntimeOptions{Enabled: false}},
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	_, err := svc.ListSystemLogs(context.Background(), &OpsSystemLogFilter{})
	if err == nil {
		t.Fatalf("expected disabled error")
	}
}

// TestOpsServiceListSystemLogs_NilRepoReturnsEmpty requests logs with no
// repository and no filter. It checks an allocated empty result with zero total
// and fixed page-one, size-fifty metadata.
func TestOpsServiceListSystemLogs_NilRepoReturnsEmpty(t *testing.T) {
	svc := newLegacyShapeOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	out, err := svc.ListSystemLogs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListSystemLogs() error: %v", err)
	}
	if out == nil || out.Page != 1 || out.PageSize != 50 || out.Total != 0 || len(out.Logs) != 0 {
		t.Fatalf("unexpected nil-repo result: %+v", out)
	}
}

// TestOpsServiceListSystemLogs_RepoErrorMapped injects a database read failure
// through the listing hook. It checks the service returns an error containing
// the system-log list failure identifier.
func TestOpsServiceListSystemLogs_RepoErrorMapped(t *testing.T) {
	repo := &opsRepoMock{
		ListSystemLogsFn: func(ctx context.Context, filter *OpsSystemLogFilter) (*OpsSystemLogList, error) {
			return nil, errors.New("db down")
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.ListSystemLogs(context.Background(), &OpsSystemLogFilter{})
	if err == nil {
		t.Fatalf("expected mapped internal error")
	}
	if !strings.Contains(err.Error(), "OPS_SYSTEM_LOG_LIST_FAILED") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOpsServiceCleanupSystemLogs_SuccessAndAudit injects a three-row deletion
// result and captures the audit input. It checks the count and retained host,
// client request, user, and Key conditions reach that audit.
func TestOpsServiceCleanupSystemLogs_SuccessAndAudit(t *testing.T) {
	var audit *OpsSystemLogCleanupAudit
	repo := &opsRepoMock{
		DeleteSystemLogsFn: func(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error) {
			return 3, nil
		},
		InsertSystemLogCleanupAuditFn: func(ctx context.Context, input *OpsSystemLogCleanupAudit) error {
			audit = input
			return nil
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	userID := int64(7)
	apiKeyID := int64(8)
	now := time.Now().UTC()
	filter := &OpsSystemLogCleanupFilter{
		StartTime:       &now,
		Host:            "api-node-1",
		Level:           "warn",
		RequestID:       "req-1",
		ClientRequestID: "creq-1",
		UserID:          &userID,
		APIKeyID:        &apiKeyID,
		Query:           "timeout",
	}

	deleted, err := svc.CleanupSystemLogs(context.Background(), filter, 99)
	if err != nil {
		t.Fatalf("CleanupSystemLogs() error: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted=%d, want 3", deleted)
	}
	if audit == nil {
		t.Fatalf("expected cleanup audit")
	}
	if !strings.Contains(audit.Conditions, `"host":"api-node-1"`) {
		t.Fatalf("audit conditions should include host: %s", audit.Conditions)
	}
	if !strings.Contains(audit.Conditions, `"client_request_id":"creq-1"`) {
		t.Fatalf("audit conditions should include client_request_id: %s", audit.Conditions)
	}
	if !strings.Contains(audit.Conditions, `"user_id":7`) {
		t.Fatalf("audit conditions should include user_id: %s", audit.Conditions)
	}
	if !strings.Contains(audit.Conditions, `"api_key_id":8`) {
		t.Fatalf("audit conditions should include api_key_id: %s", audit.Conditions)
	}
}

// TestOpsServiceCleanupSystemLogs_RepoUnavailableAndInvalidOperator exercises
// absent storage and a nonpositive operator on separate fixtures. It checks
// both conditions reject cleanup before a deletion result is accepted.
func TestOpsServiceCleanupSystemLogs_RepoUnavailableAndInvalidOperator(t *testing.T) {
	svc := newLegacyShapeOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if _, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{RequestID: "r"}, 1); err == nil {
		t.Fatalf("expected repo unavailable error")
	}

	svc = newLegacyShapeOpsService(&opsRepoMock{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if _, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{RequestID: "r"}, 0); err == nil {
		t.Fatalf("expected invalid operator error")
	}
}

// TestOpsServiceCleanupSystemLogs_FilterRequired injects the repository's
// missing-filter diagnostic for an empty filter. It checks the service returns
// a filter-related error through its diagnostic-based application-error
// mapping.
func TestOpsServiceCleanupSystemLogs_FilterRequired(t *testing.T) {
	repo := &opsRepoMock{
		DeleteSystemLogsFn: func(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error) {
			return 0, errors.New("cleanup requires at least one filter condition")
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{}, 1)
	if err == nil {
		t.Fatalf("expected filter required error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "filter") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOpsServiceCleanupSystemLogs_InvalidRange supplies an end time one hour
// before the start. It checks the service rejects that ordered-range violation
// for the supplied reversed endpoints in this deletion fixture.
func TestOpsServiceCleanupSystemLogs_InvalidRange(t *testing.T) {
	repo := &opsRepoMock{}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	start := time.Now().UTC()
	end := start.Add(-time.Hour)
	_, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{
		StartTime: &start,
		EndTime:   &end,
	}, 1)
	if err == nil {
		t.Fatalf("expected invalid range error")
	}
}

// TestOpsServiceCleanupSystemLogs_NoRowsAndInternalError first injects a
// classified missing-row failure, then a generic failure. It checks the former
// becomes successful zero and the latter remains an error.
func TestOpsServiceCleanupSystemLogs_NoRowsAndInternalError(t *testing.T) {
	repo := &opsRepoMock{
		DeleteSystemLogsFn: func(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error) {
			return 0, ErrRowNotFound
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	deleted, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{
		RequestID: "req-1",
	}, 1)
	if err != nil || deleted != 0 {
		t.Fatalf("expected no rows shortcut, deleted=%d err=%v", deleted, err)
	}

	repo.DeleteSystemLogsFn = func(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error) {
		return 0, errors.New("boom")
	}
	if _, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{
		RequestID: "req-1",
	}, 1); err == nil {
		t.Fatalf("expected internal cleanup error")
	}
}

// TestOpsServiceCleanupSystemLogs_AuditFailureIgnored supplies a successful
// five-row deletion followed by audit failure. It checks cleanup still returns
// five and no error, protecting the separately attempted audit outcome.
func TestOpsServiceCleanupSystemLogs_AuditFailureIgnored(t *testing.T) {
	repo := &opsRepoMock{
		DeleteSystemLogsFn: func(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error) {
			return 5, nil
		},
		InsertSystemLogCleanupAuditFn: func(ctx context.Context, input *OpsSystemLogCleanupAudit) error {
			return errors.New("audit down")
		},
	}
	svc := newLegacyShapeOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	deleted, err := svc.CleanupSystemLogs(context.Background(), &OpsSystemLogCleanupFilter{
		RequestID: "r1",
	}, 1)
	if err != nil || deleted != 5 {
		t.Fatalf("audit failure should not break cleanup, deleted=%d err=%v", deleted, err)
	}
}

// TestMarshalSystemLogCleanupConditions_NilAndMarshalError checks a nil filter
// and ordinary timestamp/user conditions. Its values are serializable, so
// despite the name the test exercises empty-input and successful encoding
// cases.
func TestMarshalSystemLogCleanupConditions_NilAndMarshalError(t *testing.T) {
	if got := marshalSystemLogCleanupConditions(nil); got != "{}" {
		t.Fatalf("nil filter should return {}, got %s", got)
	}

	now := time.Now().UTC()
	userID := int64(1)
	filter := &OpsSystemLogCleanupFilter{
		StartTime: &now,
		EndTime:   &now,
		UserID:    &userID,
	}
	got := marshalSystemLogCleanupConditions(filter)
	if !strings.Contains(got, `"start_time"`) || !strings.Contains(got, `"user_id":1`) {
		t.Fatalf("unexpected marshal payload: %s", got)
	}
}

// TestOpsServiceGetSystemLogSinkHealth reads an unbound sink and a constructed
// sink fixture. It checks zero health for absence and configured capacity for
// construction, without asserting successful ingestion.
func TestOpsServiceGetSystemLogSinkHealth(t *testing.T) {
	svc := newLegacyShapeOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	health := svc.GetSystemLogSinkHealth()
	if health.QueueCapacity != 0 || health.QueueDepth != 0 {
		t.Fatalf("unexpected health for nil sink: %+v", health)
	}

	sink := newTestSystemLogSink(&opsRepoMock{})
	svc = newLegacyShapeOpsService(&opsRepoMock{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, sink)
	health = svc.GetSystemLogSinkHealth()
	if health.QueueCapacity <= 0 {
		t.Fatalf("expected non-zero queue capacity: %+v", health)
	}
}
