package ops

import (
	"context"
	"maps"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// These platform and provider-state labels supply Ops fixtures with the same
// platform values used by routing capabilities.
const (
	PlatformOpenAI    = capability.PlatformOpenAI    // OpenAI fixture platform
	PlatformAnthropic = capability.PlatformAnthropic // Anthropic fixture platform
	StatusActive      = "active"                     // active provider state
	StatusError       = "error"                      // error provider state
)

// opsRepoMock is checked against the complete OpsRepository interface so
// fixtures expose every required repository method at compile time.
var _ OpsRepository = (*opsRepoMock)(nil)

// opsRepoMock supplies optional repository hooks and successful empty defaults
// for Ops tests.
type opsRepoMock struct {
	// single error insertion hook
	// InsertErrorLogFn overrides insertion when present. Its result is forwarded
	// directly; the default method returns a successful zero row ID.
	InsertErrorLogFn func(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error)
	// batch error insertion hook
	// BatchInsertErrorLogsFn overrides the batch result when present. The default
	// method reports input length, including any nil entries.
	BatchInsertErrorLogsFn func(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error)
	// batch system-log insertion hook
	// BatchInsertSystemLogsFn overrides system-log insertion when present. Input
	// pointers and the callback's result are passed through without copying.
	BatchInsertSystemLogsFn func(ctx context.Context, inputs []*OpsInsertSystemLogInput) (int64, error)
	// system-log listing hook
	// ListSystemLogsFn overrides listing when present. The default fixture returns
	// an empty log slice with fixed page metadata.
	ListSystemLogsFn func(ctx context.Context, filter *OpsSystemLogFilter) (*OpsSystemLogList, error)
	// request timing lookup hook
	// ListRequestTimingsFn overrides timing lookup when present. The callback
	// receives the supplied identifier slice directly and controls result
	// ownership.
	ListRequestTimingsFn func(ctx context.Context, clientRequestIDs []string) (map[string]*OpsRequestTiming, error)
	// system-log deletion hook
	// DeleteSystemLogsFn overrides deletion when present. An absent callback
	// returns zero affected rows and retains no deletion state.
	DeleteSystemLogsFn func(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error)
	// cleanup audit insertion hook
	// InsertSystemLogCleanupAuditFn overrides audit insertion when present. An
	// absent callback succeeds without retaining the supplied audit object.
	InsertSystemLogCleanupAuditFn func(ctx context.Context, input *OpsSystemLogCleanupAudit) error
}

// Setting aliases the persisted settings row type used by these Ops fixtures.
type Setting = settings.Setting

// runtimeSettingRepoStub stores test settings and records reads and successful
// single-key writes.
type runtimeSettingRepoStub struct {
	// values is allocated by newRuntimeSettingRepoStub and mutated directly by
	// fixture methods. Callers serialize access because the map has no internal
	// synchronization.
	values map[string]string // current setting string map
	// deleted records successful Delete calls after the key was present. A later
	// Set does not clear an existing deletion marker.
	deleted          map[string]bool // successfully deleted key markers
	setCalls         int             // successful single-key set count
	getValueCalls    int             // single-value read attempt count
	getMultipleCalls int             // multi-value read attempt count
	// getValueFn takes precedence over values lookup when configured. GetValue
	// counts the attempt before returning the hook's value or error.
	getValueFn func(key string) (string, error) // optional single-value read hook
	// setFn runs before the stored value changes. A hook error prevents both the
	// map write and successful set counter increment.
	setFn func(key, value string) error // optional single-key write hook
	// deleteFn runs before checking key presence. A hook error prevents map
	// deletion and marking the key as deleted.
	deleteFn func(key string) error // optional key deletion hook
}

// timePtr returns a pointer to its own copy of the supplied time value.
func timePtr(v time.Time) *time.Time { return &v }

// newLegacyShapeOpsService forwards repository, settings, options, provider,
// user, concurrency, and sink dependencies to NewOpsService. The four
// placeholder arguments are ignored; callers receive that constructor's
// initialized service with no log-control implementation.
func newLegacyShapeOpsService(repo OpsRepository, settings Settings, cfg *Options, a ProviderReader, u UserReader, c ConcurrencyReader, _ any, _ any, _ any, _ any, sink *OpsSystemLogSink) *OpsService {
	return NewOpsService(repo, settings, cfg, a, u, c, sink, nil)
}

// InsertErrorLog forwards to the configured single-insert hook when present and
// returns its result unchanged. With no hook, this fixture reports zero ID and
// success without storing the entry.
func (m *opsRepoMock) InsertErrorLog(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error) {
	if m.InsertErrorLogFn != nil {
		return m.InsertErrorLogFn(ctx, input)
	}
	return 0, nil
}

// BatchInsertErrorLogs delegates to its configured hook or reports the input
// slice length as a successful count. The default counts nil entries too, and
// stores none of the supplied observations.
func (m *opsRepoMock) BatchInsertErrorLogs(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error) {
	if m.BatchInsertErrorLogsFn != nil {
		return m.BatchInsertErrorLogsFn(ctx, inputs)
	}
	return int64(len(inputs)), nil
}

// ListErrorLogs returns an empty successful error page with fixed fixture
// pagination.
func (m *opsRepoMock) ListErrorLogs(ctx context.Context, filter *OpsErrorLogFilter) (*OpsErrorLogList, error) {
	return &OpsErrorLogList{Errors: []*OpsErrorLog{}, Page: 1, PageSize: 20}, nil
}

// GetErrorLogByID returns a new empty error detail for fixture lookups.
func (m *opsRepoMock) GetErrorLogByID(ctx context.Context, id int64) (*OpsErrorLogDetail, error) {
	return &OpsErrorLogDetail{}, nil
}

// ListRequestDetails returns an empty successful request-detail list with zero
// matching rows.
func (m *opsRepoMock) ListRequestDetails(ctx context.Context, filter *OpsRequestDetailFilter) ([]*OpsRequestDetail, int64, error) {
	return []*OpsRequestDetail{}, 0, nil
}

// BatchInsertSystemLogs forwards to its hook when present and otherwise reports
// the supplied slice length. The default performs no persistence or row
// filtering, while hooks receive the same input pointers.
func (m *opsRepoMock) BatchInsertSystemLogs(ctx context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
	if m.BatchInsertSystemLogsFn != nil {
		return m.BatchInsertSystemLogsFn(ctx, inputs)
	}
	return int64(len(inputs)), nil
}

// ListSystemLogs returns its hook's result when configured, with the filter
// passed directly. Without a hook it creates an empty page one with size fifty,
// regardless of requested pagination.
func (m *opsRepoMock) ListSystemLogs(ctx context.Context, filter *OpsSystemLogFilter) (*OpsSystemLogList, error) {
	if m.ListSystemLogsFn != nil {
		return m.ListSystemLogsFn(ctx, filter)
	}
	return &OpsSystemLogList{Logs: []*OpsSystemLog{}, Total: 0, Page: 1, PageSize: 50}, nil
}

// ListRequestTimings passes request identifiers to its hook or returns a new
// empty timing map. Hook results are returned directly, allowing individual
// tests to choose missing data and pointer sharing.
func (m *opsRepoMock) ListRequestTimings(ctx context.Context, clientRequestIDs []string) (map[string]*OpsRequestTiming, error) {
	if m.ListRequestTimingsFn != nil {
		return m.ListRequestTimingsFn(ctx, clientRequestIDs)
	}
	return map[string]*OpsRequestTiming{}, nil
}

// DeleteSystemLogs delegates deletion to its hook when configured. The default
// reports zero affected rows and success, leaving fixture state untouched so
// cleanup tests can supply their own write observations.
func (m *opsRepoMock) DeleteSystemLogs(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error) {
	if m.DeleteSystemLogsFn != nil {
		return m.DeleteSystemLogsFn(ctx, filter)
	}
	return 0, nil
}

// InsertSystemLogCleanupAudit invokes its audit hook when supplied and forwards
// the hook error. An absent hook accepts the call successfully, letting tests
// that do not inspect audit persistence use the fixture.
func (m *opsRepoMock) InsertSystemLogCleanupAudit(ctx context.Context, input *OpsSystemLogCleanupAudit) error {
	if m.InsertSystemLogCleanupAuditFn != nil {
		return m.InsertSystemLogCleanupAuditFn(ctx, input)
	}
	return nil
}

// UpdateErrorResolution accepts fixture resolution updates without retaining
// any resolution state.
func (m *opsRepoMock) UpdateErrorResolution(ctx context.Context, errorID int64, resolved bool, resolvedByUserID *int64, resolvedAt *time.Time) error {
	return nil
}

// GetWindowStats returns a new empty aggregate for default window-statistics
// fixtures.
func (m *opsRepoMock) GetWindowStats(ctx context.Context, filter *OpsDashboardFilter) (*OpsWindowStats, error) {
	return &OpsWindowStats{}, nil
}

// GetRealtimeTrafficSummary returns a new empty traffic summary for fixture
// reads.
func (m *opsRepoMock) GetRealtimeTrafficSummary(ctx context.Context, filter *OpsDashboardFilter) (*OpsRealtimeTrafficSummary, error) {
	return &OpsRealtimeTrafficSummary{}, nil
}

// GetDashboardOverview returns a new empty dashboard overview for fixture
// reads.
func (m *opsRepoMock) GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	return &OpsDashboardOverview{}, nil
}

// GetThroughputTrend returns a new empty throughput response for fixture chart
// queries.
func (m *opsRepoMock) GetThroughputTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsThroughputTrendResponse, error) {
	return &OpsThroughputTrendResponse{}, nil
}

// GetLatencyHistogram returns a new empty latency response for fixture
// histogram queries.
func (m *opsRepoMock) GetLatencyHistogram(ctx context.Context, filter *OpsDashboardFilter, bucketBoundariesMS []int64) (*OpsLatencyHistogramResponse, error) {
	return &OpsLatencyHistogramResponse{}, nil
}

// GetErrorTrend returns a new empty error trend for fixture chart queries.
func (m *opsRepoMock) GetErrorTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsErrorTrendResponse, error) {
	return &OpsErrorTrendResponse{}, nil
}

// GetErrorDistribution returns a new empty distribution for fixture error
// grouping queries.
func (m *opsRepoMock) GetErrorDistribution(ctx context.Context, filter *OpsDashboardFilter) (*OpsErrorDistributionResponse, error) {
	return &OpsErrorDistributionResponse{}, nil
}

// GetTokenStats returns a new empty token-statistics response for fixture
// ranking queries.
func (m *opsRepoMock) GetTokenStats(ctx context.Context, filter *OpsTokenStatsFilter) (*OpsTokenStatsResponse, error) {
	return &OpsTokenStatsResponse{}, nil
}

// InsertSystemMetrics accepts fixture metric rows without retaining any stored
// measurements.
func (m *opsRepoMock) InsertSystemMetrics(ctx context.Context, input *OpsInsertSystemMetricsInput) error {
	return nil
}

// GetLatestSystemMetrics returns a new empty system snapshot for fixture
// queries.
func (m *opsRepoMock) GetLatestSystemMetrics(ctx context.Context, windowMinutes int) (*OpsSystemMetricsSnapshot, error) {
	return &OpsSystemMetricsSnapshot{}, nil
}

// UpsertJobHeartbeat accepts fixture job outcomes without retaining any
// heartbeat state.
func (m *opsRepoMock) UpsertJobHeartbeat(ctx context.Context, input *OpsUpsertJobHeartbeatInput) error {
	return nil
}

// ListJobHeartbeats returns an empty successful heartbeat list for fixture
// diagnostics.
func (m *opsRepoMock) ListJobHeartbeats(ctx context.Context) ([]*OpsJobHeartbeat, error) {
	return []*OpsJobHeartbeat{}, nil
}

// ListAlertRules returns an empty successful rule list for default alert
// fixtures.
func (m *opsRepoMock) ListAlertRules(ctx context.Context) ([]*OpsAlertRule, error) {
	return []*OpsAlertRule{}, nil
}

// CreateAlertRule returns the supplied rule pointer directly with no storage or
// generated identity. Tests retain ownership of that object, and changes
// through the returned pointer affect the input.
func (m *opsRepoMock) CreateAlertRule(ctx context.Context, input *OpsAlertRule) (*OpsAlertRule, error) {
	return input, nil
}

// UpdateAlertRule echoes the supplied rule pointer as a successful update
// result. The fixture performs no lookup or validation, so the input and
// returned rule continue sharing the same object.
func (m *opsRepoMock) UpdateAlertRule(ctx context.Context, input *OpsAlertRule) (*OpsAlertRule, error) {
	return input, nil
}

// DeleteAlertRule accepts fixture rule deletion requests without retaining any
// deletion state.
func (m *opsRepoMock) DeleteAlertRule(ctx context.Context, id int64) error {
	return nil
}

// ListAlertEvents returns an empty successful event list for fixture alert
// queries.
func (m *opsRepoMock) ListAlertEvents(ctx context.Context, filter *OpsAlertEventFilter) ([]*OpsAlertEvent, error) {
	return []*OpsAlertEvent{}, nil
}

// GetAlertEventByID returns a new empty alert event for fixture identity
// lookups.
func (m *opsRepoMock) GetAlertEventByID(ctx context.Context, eventID int64) (*OpsAlertEvent, error) {
	return &OpsAlertEvent{}, nil
}

// GetActiveAlertEvent reports successful absence of an active event for default
// fixtures.
func (m *opsRepoMock) GetActiveAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
	return nil, nil
}

// GetLatestAlertEvent reports successful absence of a latest event for default
// fixtures.
func (m *opsRepoMock) GetLatestAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error) {
	return nil, nil
}

// CreateAlertEvent returns its input event directly as a successful fixture
// result. No identity is assigned or stored, and callers observe subsequent
// mutation through either reference to that event.
func (m *opsRepoMock) CreateAlertEvent(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
	return event, nil
}

// UpdateAlertEventStatus accepts fixture state transitions without retaining
// any updated event.
func (m *opsRepoMock) UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error {
	return nil
}

// UpdateAlertEventEmailSent accepts fixture email-delivery flags without
// retaining their resulting state.
func (m *opsRepoMock) UpdateAlertEventEmailSent(ctx context.Context, eventID int64, emailSent bool) error {
	return nil
}

// CreateAlertSilence returns the supplied silence pointer directly and performs
// no persistence or identity assignment. This lets tests use their own silence
// object while retaining shared ownership of the returned value.
func (m *opsRepoMock) CreateAlertSilence(ctx context.Context, input *OpsAlertSilence) (*OpsAlertSilence, error) {
	return input, nil
}

// IsAlertSilenced reports false and success for every default fixture silence
// query.
func (m *opsRepoMock) IsAlertSilenced(ctx context.Context, ruleID int64, platform string, groupID *int64, region *string, now time.Time) (bool, error) {
	return false, nil
}

// UpsertHourlyMetrics accepts fixture hourly aggregation requests without
// retaining any rollup data.
func (m *opsRepoMock) UpsertHourlyMetrics(ctx context.Context, startTime, endTime time.Time, ignoredStatusCodes []int) error {
	return nil
}

// UpsertDailyMetrics accepts fixture daily aggregation requests without
// retaining any rollup data.
func (m *opsRepoMock) UpsertDailyMetrics(ctx context.Context, startTime, endTime time.Time) error {
	return nil
}

// GetLatestHourlyBucketStart reports no recorded hourly bucket for default
// aggregation fixtures.
func (m *opsRepoMock) GetLatestHourlyBucketStart(ctx context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// GetLatestDailyBucketDate reports no recorded daily bucket for default
// aggregation fixtures.
func (m *opsRepoMock) GetLatestDailyBucketDate(ctx context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// newRuntimeSettingRepoStub allocates independent value and deletion-marker
// maps for one test fixture.
func newRuntimeSettingRepoStub() *runtimeSettingRepoStub {
	return &runtimeSettingRepoStub{
		values:  map[string]string{},
		deleted: map[string]bool{},
	}
}

// Get builds a new Setting from GetValue's result, forwarding any read error.
// The returned row has the requested key and stored string, while GetValue
// performs hook selection and read accounting.
func (s *runtimeSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	value, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

// GetValue increments its attempt counter before invoking the optional hook or
// looking up values. It ignores context, and an absent map key returns
// ErrSettingNotFound instead of an empty successful value.
func (s *runtimeSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	s.getValueCalls++
	if s.getValueFn != nil {
		return s.getValueFn(key)
	}
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

// Set invokes its optional hook, then stores the supplied string and increments
// successful single-key writes. A hook error leaves both unchanged; callers
// allocate values before invoking this unsynchronized fixture method.
func (s *runtimeSettingRepoStub) Set(_ context.Context, key, value string) error {
	if s.setFn != nil {
		if err := s.setFn(key, value); err != nil {
			return err
		}
	}
	s.values[key] = value
	s.setCalls++
	return nil
}

// GetMultiple counts a batch-read attempt and returns a new map containing only
// requested keys present in values. It ignores context and read hooks, so
// absent keys remain omitted and do not cause errors.
func (s *runtimeSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.getMultipleCalls++
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple merges supplied string settings into values and leaves
// unmentioned keys intact. It ignores context, single-key hooks, and setCalls;
// callers serialize access to the fixture's existing map.
func (s *runtimeSettingRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	maps.Copy(s.values, settings)
	return nil
}

// GetAll returns a new map containing the fixture's current string settings.
// Its map can be changed independently, while context and read hooks are
// ignored for this complete fixture snapshot.
func (s *runtimeSettingRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	maps.Copy(out, s.values)
	return out, nil
}

// Delete runs its optional hook before requiring the key to exist and removing
// it from values. A successful deletion marks deleted[key]; a missing key or
// hook error returns before recording that marker.
func (s *runtimeSettingRepoStub) Delete(_ context.Context, key string) error {
	if s.deleteFn != nil {
		if err := s.deleteFn(key); err != nil {
			return err
		}
	}
	if _, ok := s.values[key]; !ok {
		return ErrSettingNotFound
	}
	delete(s.values, key)
	s.deleted[key] = true
	return nil
}
