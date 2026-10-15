package ops

import (
	"context"
	"time"
)

// port.go defines persistence operations and record shapes for errors, system
// logs, metrics, and background-job diagnostics.
// Ops observations keep the platform request ID separate from caller
// correlation text and saved provider/group dimensions. Nullable measurements
// distinguish absent evidence from recorded zero, while each PostgreSQL encoder
// determines which zero values persist. Input preparation and queued ownership
// belong to the ingestion service; these persistence shapes are read by
// ingestion, dashboard, cleanup, and background jobs.

// OpsRepository supplies Ops observation persistence, dashboard reads, alert
// history, and aggregation storage.
type OpsRepository interface {
	// InsertErrorLog persists one nonnil prepared error observation and returns
	// its stored identity. OpsService supplies classification defaults, time, and
	// body preparation; the PostgreSQL adapter performs nullable encoding and
	// reports write failures.
	InsertErrorLog(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error)
	// BatchInsertErrorLogs persists prepared nonnil observations in one PostgreSQL
	// transaction, skipping nil members and returning zero for empty input.
	// Callers keep entries stable until the operation returns.
	//
	// A returned count on failure can include executed rows whose transaction was
	// rolled back, so only successful completion establishes a committed count.
	// Commit errors are returned without a guaranteed persistence outcome, and
	// callers choose their own retry policy rather than treating the count as
	// durable progress.
	BatchInsertErrorLogs(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error)
	// ListErrorLogs returns matching error observations with total and page
	// metadata. PostgreSQL defaults page one/size twenty, caps size at five
	// hundred, and reads total separately from the ordered page; live writes can
	// change their relative observations.
	ListErrorLogs(ctx context.Context, filter *OpsErrorLogFilter) (*OpsErrorLogList, error)
	// GetErrorLogByID loads stored diagnostics for one positive error identity.
	// PostgreSQL returns a no-rows error when absent, preserves nullable upstream
	// zero, and normalizes retained account_auth phases for current readers.
	GetErrorLogByID(ctx context.Context, id int64) (*OpsErrorLogDetail, error)
	// ListRequestDetails returns combined usage and final-client-error rows with a
	// separately read total. Nil filter defaults a recent hour and normalizes
	// pagination without mutating the filter. Usage/error pairs can remain
	// separate observations for one request.
	//
	// SLAOnly restricts the error branch alone, while request lookup, dimensions,
	// and duration conditions apply to the combined data. PostgreSQL count runs
	// before sort validation and paging, and equal creation times have no error-ID
	// tie breaker. Callers use the returned error rather than companion count values
	// when the operation fails.
	ListRequestDetails(ctx context.Context, filter *OpsRequestDetailFilter) ([]*OpsRequestDetail, int64, error)
	// BatchInsertSystemLogs persists valid nonnil system observations in one
	// PostgreSQL COPY transaction and skips blank level/message entries. Callers
	// keep inputs stable through submission. Timestamp/component/metadata defaults
	// are supplied by that adapter.
	//
	// Rows are submitted, COPY is flushed and closed, and then the transaction
	// commits. Counts returned on error describe earlier submission, which can be
	// rolled back and does not establish persistence. No per-row replay is
	// provided here, so the sink owns failure backoff and dropped-batch
	// accounting.
	BatchInsertSystemLogs(ctx context.Context, inputs []*OpsInsertSystemLogInput) (int64, error)
	// ListSystemLogs returns indexed system observations, total, and page
	// metadata. PostgreSQL defaults page one/size fifty, caps size at two hundred,
	// and tolerates unusable optional metadata while total and page run as
	// separate reads.
	ListSystemLogs(ctx context.Context, filter *OpsSystemLogFilter) (*OpsSystemLogList, error)
	// ListRequestTimings resolves stored access-log timing measurements for
	// supplied correlation strings. PostgreSQL trims and deduplicates bounded IDs,
	// selects its latest matching identity row, and omits rows with no usable
	// timing evidence; callers join by returned keys.
	ListRequestTimings(ctx context.Context, clientRequestIDs []string) (map[string]*OpsRequestTiming, error)
	// DeleteSystemLogs removes observations matching every effective filter
	// predicate and returns affected rows. PostgreSQL requires at least one
	// predicate to reject unrestricted deletion; the owning service records audit
	// independently, so an audit failure does not restore logs.
	DeleteSystemLogs(ctx context.Context, filter *OpsSystemLogCleanupFilter) (int64, error)
	// InsertSystemLogCleanupAudit persists administrator conditions and the
	// returned deletion count with its timestamp. PostgreSQL supplies a current
	// time when absent; the service calls it after an independent successful
	// deletion.
	InsertSystemLogCleanupAudit(ctx context.Context, input *OpsSystemLogCleanupAudit) error

	// UpdateErrorResolution persists resolution state, optional actor, and
	// resolution time for a positive error identity. PostgreSQL uses supplied
	// nonzero time, current UTC time for resolved with no time, and NULL
	// otherwise; missing-row detection belongs to the prior service read.
	UpdateErrorResolution(ctx context.Context, errorID int64, resolved bool, resolvedByUserID *int64, resolvedAt *time.Time) error

	// GetWindowStats returns raw usage, final-client-error, and token counts for
	// nonzero ordered endpoints spanning at most a day. PostgreSQL allows equal
	// endpoints and independent usage/error reads; this result supports
	// short-window realtime consumers.
	GetWindowStats(ctx context.Context, filter *OpsDashboardFilter) (*OpsWindowStats, error)
	// GetRealtimeTrafficSummary returns raw average, peak, and current traffic
	// rates for a positive window of at most an hour. PostgreSQL counts all
	// final-client failures except probes, keeps full-duration denominators, and
	// reads the preceding current minute independently.
	GetRealtimeTrafficSummary(ctx context.Context, filter *OpsDashboardFilter) (*OpsRealtimeTrafficSummary, error)

	// GetDashboardOverview returns counts, SLA ratios, latency measurements, and
	// rates under the resolved source mode. PostgreSQL automatic mode merges
	// covered stable hours with raw fragments and retries the entire raw range if
	// that path fails.
	//
	// Counts are additive while combined latency percentiles are approximations
	// using success or measured first-token weights. Current and peak continue
	// reading raw sources. Recognized optional deadlines permit missing
	// measurements or estimated nonpositive rates, while ordinary query errors
	// reject the result. Callers supply normalized filter policy and treat
	// retained GroupID as read-only.
	GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error)
	// GetThroughputTrend returns raw usage/client-failure buckets plus independent
	// switch counts and selection-dependent breakdowns. PostgreSQL normalizes
	// supported minute/five-minute/hour sizes, fills matching empty slots, and
	// divides full bucket seconds even for partial requested endpoints.
	GetThroughputTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsThroughputTrendResponse, error)
	// GetLatencyHistogram returns raw usage duration buckets using five increasing
	// positive millisecond cutoffs. PostgreSQL normalizes default boundaries,
	// excludes missing durations, uses strict less-than cutoff comparisons, and
	// restores absent SQL groups as zero counts.
	GetLatencyHistogram(ctx context.Context, filter *OpsDashboardFilter, bucketBoundariesMS []int64) (*OpsLatencyHistogramResponse, error)
	// GetErrorTrend returns raw client-failure categories and provider-status
	// observations for supported chart buckets. PostgreSQL excludes count-token
	// probes and fills missing matching timeline slots; recovered provider
	// observations can contribute independently of final-error totals.
	GetErrorTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsErrorTrendResponse, error)
	// GetErrorDistribution groups raw final-client failures by effective saved
	// status. PostgreSQL returns at most twenty groups, and response Total sums
	// the displayed groups; a nonnull upstream zero stays a distinct grouping
	// value.
	GetErrorDistribution(ctx context.Context, filter *OpsDashboardFilter) (*OpsErrorDistributionResponse, error)
	// GetTokenStats returns raw execution-model usage statistics in one supplied
	// limiting mode. OpsService normalizes pagination or TopN; PostgreSQL
	// preserves total for an empty page and keeps absent rate/first-token
	// measurements as nil pointers.
	GetTokenStats(ctx context.Context, filter *OpsTokenStatsFilter) (*OpsTokenStatsResponse, error)

	// InsertSystemMetrics persists a nonnil sample, preserving explicit nullable
	// zero/false for dedicated measurement encoders. PostgreSQL defaults
	// nonpositive window to one minute and zero timestamp to now, with no
	// conflict-based per-window deduplication.
	InsertSystemMetrics(ctx context.Context, input *OpsInsertSystemMetricsInput) error
	// GetLatestSystemMetrics returns the latest global sample for a supplied
	// window size, defaulting nonpositive size to one minute. PostgreSQL requires
	// null platform/group, propagates missing-row errors, and imposes no freshness
	// cutoff or ID tie breaker.
	GetLatestSystemMetrics(ctx context.Context, windowMinutes int) (*OpsSystemMetricsSnapshot, error)

	// UpsertJobHeartbeat inserts or updates one nonempty job name using
	// omission-aware values. Missing times, duration zero, and blank text retain
	// prior values on conflict; a present success time clears stored errors.
	//
	// LastResult changes on successful conflict updates when supplied, while
	// failed conflict updates retain the previous result. First insertion stores
	// supplied fields directly. Timestamps are not compared for monotonicity, and
	// data persistence remains separate from heartbeat persistence, so a caller
	// must not treat heartbeat failure as rollback of its completed work.
	UpsertJobHeartbeat(ctx context.Context, input *OpsUpsertJobHeartbeatInput) error
	// ListJobHeartbeats returns saved per-job timestamps, duration, result, and
	// error data. The PostgreSQL reader preserves absence through pointers and
	// sorts by job name; these persisted records do not directly report a
	// currently running worker.
	ListJobHeartbeats(ctx context.Context) ([]*OpsJobHeartbeat, error)

	// ListAlertRules returns saved rules in repository order for evaluator or
	// administrator reads. PostgreSQL sorts descending IDs and retains scalar
	// fields when optional filters cannot be decoded, with last-trigger absence
	// represented as nil.
	ListAlertRules(ctx context.Context) ([]*OpsAlertRule, error)
	// CreateAlertRule persists a nonnil complete rule and returns the saved row.
	// Administrator validation supplies supported metrics and ranges before
	// PostgreSQL trims text and serializes Filters; JSON encoding failures occur
	// before insertion.
	CreateAlertRule(ctx context.Context, input *OpsAlertRule) (*OpsAlertRule, error)
	// UpdateAlertRule replaces complete editable fields for a positive rule
	// identity and returns the saved row. PostgreSQL leaves creation/last-trigger
	// values unchanged, and a missing row yields an error for the service to
	// classify.
	UpdateAlertRule(ctx context.Context, input *OpsAlertRule) (*OpsAlertRule, error)
	// DeleteAlertRule deletes one positive rule identity, reporting a missing row
	// for administrator classification. PostgreSQL does not remove existing events
	// or silences through this statement, so their observation identities remain
	// independent.
	DeleteAlertRule(ctx context.Context, id int64) error

	// ListAlertEvents returns saved events under supplied criteria and an optional
	// complete timestamp/ID cursor. PostgreSQL caps its normalized limit at five
	// hundred, orders descending time then ID, and returns no separate total
	// count.
	ListAlertEvents(ctx context.Context, filter *OpsAlertEventFilter) ([]*OpsAlertEvent, error)
	// GetAlertEventByID loads a positive stored event identity. PostgreSQL returns
	// nil with nil error for absence and retains explicit zero measurements;
	// OpsService turns that absence into the administrator not-found result.
	GetAlertEventByID(ctx context.Context, eventID int64) (*OpsAlertEvent, error)
	// GetActiveAlertEvent returns the latest saved firing event for a positive
	// rule identity. PostgreSQL absence becomes nil with nil error, and this
	// independent read neither reserves insertion nor establishes one active event
	// per rule.
	GetActiveAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error)
	// GetLatestAlertEvent returns the latest event for a positive rule identity
	// regardless of its status. PostgreSQL absence becomes nil with nil error; the
	// evaluator uses FiredAt for cooldown and chooses its own error policy.
	GetLatestAlertEvent(ctx context.Context, ruleID int64) (*OpsAlertEvent, error)
	// CreateAlertEvent persists a supplied observation and returns the stored
	// event with generated identity. PostgreSQL serializes dimensions before
	// writing, keeps supplied FiredAt, and inserts independently of earlier
	// active/cooldown reads.
	CreateAlertEvent(ctx context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error)
	// UpdateAlertEventStatus writes supplied resolution status and nullable time
	// for a positive event identity. OpsService validates administrator state
	// names; PostgreSQL accepts nonblank text, turns zero time into NULL, and does
	// not check affected-row count.
	UpdateAlertEventStatus(ctx context.Context, eventID int64, status string, resolvedAt *time.Time) error
	// UpdateAlertEventEmailSent writes the supplied marker for a positive event
	// identity independently of mail callback execution. PostgreSQL ignores
	// affected-row count, so successful marker persistence does not establish
	// actual recipient delivery.
	UpdateAlertEventEmailSent(ctx context.Context, eventID int64, emailSent bool) error

	// CreateAlertSilence persists a positive rule, nonblank platform, nonzero
	// deadline, and exact nullable scope. PostgreSQL accepts past deadlines, turns
	// group zero and blank region into NULL, and returns independent optional
	// scalar pointers.
	CreateAlertSilence(ctx context.Context, input *OpsAlertSilence) (*OpsAlertSilence, error)
	// IsAlertSilenced checks whether an exact saved scope has a deadline strictly
	// after now. PostgreSQL supplies current UTC time for zero now and matches
	// nullable group/region as values; missing dimensions are not populated-scope
	// wildcards.
	IsAlertSilenced(ctx context.Context, ruleID int64, platform string, groupID *int64, region *string, now time.Time) (bool, error)

	// UpsertHourlyMetrics refreshes saved hourly dimensions from raw usage and
	// non-probe errors for aligned UTC endpoints supplied by aggregation.
	// PostgreSQL returns success for zero/unordered ranges, and uses the supplied
	// ignored-status classification when writing each observed dimension.
	//
	// The observed-dimension UPSERT and missing global-zero insertion are separate
	// commits. The latter preserves conflicts, so error there leaves earlier
	// refreshed data and absent new observations do not delete old dimensions.
	// These saved global markers support count-based coverage, while percentile
	// columns retain request statistics for later approximate combination.
	UpsertHourlyMetrics(ctx context.Context, startTime, endTime time.Time, ignoredStatusCodes []int) error
	// UpsertDailyMetrics refreshes saved daily dimensions from hourly summaries in
	// an aggregation-supplied whole-day UTC range. PostgreSQL sums counts, weights
	// low latency percentiles/means, and selects high-percentile maxima rather
	// than computing request-level quantiles; missing new dimensions are not
	// deleted.
	UpsertDailyMetrics(ctx context.Context, startTime, endTime time.Time) error
	// GetLatestHourlyBucketStart returns the greatest saved hourly timestamp
	// across dimensions plus whether a value exists. PostgreSQL uses false for
	// empty storage; aggregation treats it as a start hint, separate from
	// global-hour coverage verification.
	GetLatestHourlyBucketStart(ctx context.Context) (time.Time, bool, error)
	// GetLatestDailyBucketDate returns the greatest saved date across dimensions
	// as UTC midnight plus existence. PostgreSQL returns false for empty storage;
	// aggregation uses it as a recent-window start hint rather than continuous
	// historical coverage.
	GetLatestDailyBucketDate(ctx context.Context) (time.Time, bool, error)
}

// OpsInsertErrorLogInput carries one mutable request diagnostic before
// repository encoding. OpsService prepares the supplied object in place, and
// OpsErrorLogQueue retains that same pointer after its own preparation. Callers
// stop changing the entry and reachable mutable values after queue submission,
// including submissions rejected later by admission.
type OpsInsertErrorLogInput struct {
	// RequestID names the platform request while ClientRequestID carries caller
	// correlation. Both can be empty independently, and exact local lookup
	// precedence is resolved by repository query rules.
	RequestID       string // platform request correlation identity
	ClientRequestID string // caller request correlation text

	// UserID, APIKeyID, ProviderID, and GroupID retain captured associations read
	// by query filters. Nil or zero encodes SQL NULL, nonzero values remain
	// supplied, and these pointers do not establish authorization by themselves.
	UserID     *int64  // captured user association identity
	APIKeyID   *int64  // captured API-key association identity
	ProviderID *int64  // captured selected provider identity
	GroupID    *int64  // captured request group identity
	ClientIP   *string // captured client IP address

	Platform         string // saved execution platform dimension
	Model            string // saved execution model dimension
	RequestPath      string // observed inbound request path
	Stream           bool   // observed streaming request flag
	InboundEndpoint  string // normalized client endpoint path
	UpstreamEndpoint string // normalized provider endpoint path
	RequestedModel   string // client model before mapping
	UpstreamModel    string // model submitted to provider
	// RequestType uses usage modes 0=unknown, 1=sync, 2=stream, 3=ws_v2, 4=cyber,
	// 5=live. Nil encodes NULL, while explicit zero survives; direct storage
	// performs no enumeration validation.
	RequestType *int16 // saved request mode enumeration
	UserAgent   string // captured client agent text

	// ErrorPhase is trimmed and defaults to internal in service preparation. A
	// last nonnil provider_auth event can replace the phase, owner, source,
	// status, and text.
	ErrorPhase string // captured diagnostic request phase
	// ErrorType is trimmed and defaults to api_error during service preparation.
	// Direct repository callers supply a database-compatible nonempty category
	// themselves.
	ErrorType string // normalized diagnostic error category
	Severity  string // captured diagnostic severity label
	// StatusCode holds captured final-client status or a logical stream failure
	// chosen by the producer. PostgreSQL encodes zero as NULL; displayed upstream
	// status is selected separately.
	StatusCode int // captured final or logical status
	// IsBusinessLimited is the captured exclusion signal. Query-time ignored
	// statuses and upstream context add eligibility rules, so false does not by
	// itself establish SLA counting.
	IsBusinessLimited bool // captured SLA exclusion decision
	IsCountTokens     bool // observed count-token probe flag

	ErrorMessage string // captured diagnostic error message
	// ErrorBody receives JSON-key credential redaction and bounded shrinking
	// during service preparation. Non-JSON text is bounded as supplied, so
	// producers prepare sensitive text before submission.
	ErrorBody string // prepared diagnostic response body

	ErrorSource string // captured failure source label
	ErrorOwner  string // captured failure owner label

	// UpstreamStatusCode retains explicit zero for provider_auth. Service
	// preparation clears nonpositive values for other phases, while PostgreSQL
	// preserves any nonnil supplied integer.
	UpstreamStatusCode *int // selected upstream diagnostic status
	// UpstreamErrorMessage and UpstreamErrorDetail are selected diagnostic text.
	// Preparation sanitizes credential queries in the message and credential keys
	// in JSON detail, bounds size, and clears empty text. Plain detail remains
	// bounded text; provider_auth can replace both.
	UpstreamErrorMessage *string // selected upstream diagnostic message
	UpstreamErrorDetail  *string // selected upstream diagnostic detail
	// UpstreamErrors supplies newest nonnil top-level attribution before
	// preparation scans at most its last sixteen raw entries into
	// UpstreamErrorsJSON. Nil and empty diagnostic events are skipped; copied
	// event values are serialized and the raw slice is released from this entry.
	UpstreamErrors []*OpsUpstreamErrorEvent // retained upstream attempt observations
	// UpstreamErrorsJSON is replaced when raw UpstreamErrors are present. With an
	// empty raw list, service preparation leaves a supplied serialized value
	// unchanged, so the caller supplies prepared JSON.
	UpstreamErrorsJSON *string // serialized upstream attempt observations

	// AuthLatencyMs, RoutingLatencyMs, UpstreamLatencyMs, ResponseLatencyMs, and
	// TimeToFirstTokenMs contain captured millisecond measurements. PostgreSQL's
	// general integer encoder maps nil and zero to NULL, while negative supplied
	// values remain valid.
	AuthLatencyMs      *int64 // authentication duration in milliseconds
	RoutingLatencyMs   *int64 // routing duration in milliseconds
	UpstreamLatencyMs  *int64 // upstream duration in milliseconds
	ResponseLatencyMs  *int64 // response duration in milliseconds
	TimeToFirstTokenMs *int64 // first-token delay in milliseconds

	// CreatedAt records capture time. prepareErrorLogInput supplies time.Now when
	// it is zero; direct repository insertion stores the supplied timestamp and
	// provides no such default.
	CreatedAt time.Time // diagnostic observation capture instant

	// APIKeyPrefix retains producer-supplied credential evidence after key
	// metadata changes. Gateway capture supplies up to eight leading key bytes;
	// direct builders can pass another supplied snapshot.
	APIKeyPrefix string // saved credential identification prefix
}

// OpsInsertSystemMetricsInput carries one caller-owned observation for scalar
// metrics persistence.
type OpsInsertSystemMetricsInput struct {
	// CreatedAt defaults to current UTC time when zero in PostgreSQL insertion. A
	// supplied nonzero timestamp is stored as given, and WindowMinutes nonpositive
	// defaults to one.
	CreatedAt     time.Time // system sample capture instant
	WindowMinutes int       // sample observation window minutes

	// Platform and GroupID select saved sample dimensions; nil identifies global
	// data. PostgreSQL trims blank platform to NULL and maps group zero to NULL,
	// while latest-global reads require both NULL.
	Platform *string // optional saved platform dimension
	GroupID  *int64  // optional saved group dimension

	// SuccessCount, ErrorCountTotal, BusinessLimitedCount, and ErrorCountSLA are
	// direct nonnullable counters. The collector supplies usage/final-error
	// categories, and persistence accepts their given values without recomputing
	// classification.
	SuccessCount         int64 // saved usage observation count
	ErrorCountTotal      int64 // final client failure count
	BusinessLimitedCount int64 // SLA-excluded client failure count
	ErrorCountSLA        int64 // SLA-countable client failure count

	// UpstreamErrorCountExcl429529, Upstream429Count, and Upstream529Count retain
	// countable provider status categories, separately from final-client totals;
	// the collector's metrics query can include eligible recovered provider
	// observations.
	UpstreamErrorCountExcl429529 int64 // countable other provider observations
	Upstream429Count             int64 // countable provider 429 observations
	Upstream529Count             int64 // countable provider 529 observations

	TokenConsumed       int64 // input/output/cache token total
	ProviderSwitchCount int64 // recorded failover attempt count

	// QPS and TPS carry nullable per-second rates. Nil means unavailable, and
	// explicit zero survives PostgreSQL's floating-point encoder independently of
	// the stored request/token counters.
	QPS *float64 // requests per second
	TPS *float64 // tokens per second

	// DurationP50Ms, DurationP90Ms, DurationP95Ms, DurationP99Ms, DurationAvgMs,
	// and DurationMaxMs retain nullable millisecond statistics. Nil is absent,
	// while valid zero survives the dedicated measurement encoders.
	DurationP50Ms *int     // request duration median milliseconds
	DurationP90Ms *int     // request duration 90th percentile milliseconds
	DurationP95Ms *int     // request duration 95th percentile milliseconds
	DurationP99Ms *int     // request duration 99th percentile milliseconds
	DurationAvgMs *float64 // request duration average milliseconds
	DurationMaxMs *int     // request duration maximum milliseconds

	// TTFTP50Ms, TTFTP90Ms, TTFTP95Ms, TTFTP99Ms, TTFTAvgMs, and TTFTMaxMs retain
	// nullable first-token millisecond statistics. Their nil-versus-zero encoding
	// is independent of success counts.
	TTFTP50Ms *int     // first-token latency median milliseconds
	TTFTP90Ms *int     // first-token latency 90th percentile milliseconds
	TTFTP95Ms *int     // first-token latency 95th percentile milliseconds
	TTFTP99Ms *int     // first-token latency 99th percentile milliseconds
	TTFTAvgMs *float64 // first-token latency average milliseconds
	TTFTMaxMs *int     // first-token latency maximum milliseconds

	// CPUUsagePercent, MemoryUsedMB, MemoryTotalMB, MemoryUsagePercent, and
	// DiskUsagePercent carry nullable host observations. Dedicated encoders retain
	// measured zero, while the two disk-size fields follow their separate
	// zero-as-missing rules.
	CPUUsagePercent    *float64 // observed CPU usage percentage
	MemoryUsedMB       *int64   // observed used memory MiB
	MemoryTotalMB      *int64   // observed memory capacity MiB
	MemoryUsagePercent *float64 // observed memory usage percentage
	// DiskUsedMB uses the general integer encoder in PostgreSQL insertion, so nil
	// or zero becomes NULL; nonzero negative values remain supplied.
	DiskUsedMB *int64 // observed used disk MiB
	// DiskTotalMB uses the general integer encoder in PostgreSQL insertion, so nil
	// or zero becomes NULL; nonzero negative values remain supplied.
	DiskTotalMB      *int64   // observed disk capacity MiB
	DiskUsagePercent *float64 // observed disk usage percentage

	// DBOK and RedisOK carry each dependency's health-check observation. Dedicated
	// boolean encoding leaves nil unavailable and retains a measured false value
	// independently of connection counts.
	DBOK    *bool // database health observation boolean
	RedisOK *bool // Redis health observation boolean

	// RedisConnTotal and RedisConnIdle share collector pool-stat availability:
	// both are nil when that read is unavailable. Successful values, including
	// zero, retain separate total and idle counts.
	RedisConnTotal *int // observed Redis connection count
	RedisConnIdle  *int // observed idle Redis connections

	// DBConnActive and DBConnIdle carry SQL pool counts supplied by the collector.
	// Dedicated integer encoders preserve zero independently of the database
	// health-check observation in DBOK.
	DBConnActive *int // observed active database connections
	DBConnIdle   *int // observed idle database connections
	// DBConnWaiting is accepted by metric persistence and returned by
	// latest-sample reads. The current OpsMetricsCollector supplies no
	// waiting-count field, leaving its input nil and storage absent for those samples.
	DBConnWaiting *int // supplied database waiting-count observation

	// GoroutineCount and ConcurrencyQueueDepth are runtime observations encoded
	// independently. The collector supplies goroutines, while unavailable
	// scheduler queue-depth observation leaves its pointer nil rather than
	// assigning zero.
	GoroutineCount        *int // observed runtime goroutine count
	ConcurrencyQueueDepth *int // observed provider wait depth
}

// OpsInsertSystemLogInput carries one prepared system event for transactional
// batch persistence.
type OpsInsertSystemLogInput struct {
	// CreatedAt defaults to current UTC time when zero in PostgreSQL COPY
	// preparation. Stored values are converted to UTC; callers keep the
	// observation stable during submission.
	CreatedAt time.Time // log event capture instant
	// Host is captured by the logging sink from process metadata. Historical input
	// can omit it, and the PostgreSQL string encoder stores blank host as NULL.
	Host string // logging process host label
	// Level, Component, and Message are trimmed by PostgreSQL COPY preparation.
	// Blank level or message drops the input, level folds case, and blank
	// component selects app.
	Level     string // indexed log severity label
	Component string // indexed event component label
	Message   string // indexed event message text
	// RequestID and ClientRequestID retain distinct correlation values. Empty or
	// blank text becomes SQL NULL through the string encoder; query-time local-ID
	// precedence determines alias expansion.
	RequestID       string // platform request correlation identity
	ClientRequestID string // caller request correlation text
	// UserID, APIKeyID, and ProviderID are captured associations carried through
	// telemetry attributes. Nil or zero encodes NULL, and nonzero values remain
	// supplied rather than deriving identity from current metadata.
	UserID     *int64 // captured user association identity
	APIKeyID   *int64 // captured API-key association identity
	ProviderID *int64 // captured provider association identity
	Platform   string // captured execution platform label
	Model      string // captured execution model label
	// ExtraJSON supplies caller-prepared object text for the JSONB column. Empty
	// text becomes an object during COPY; database validation can reject malformed
	// supplied JSON for the whole batch.
	ExtraJSON string // prepared supplementary event attributes
}

// OpsSystemLogFilter supplies conjunctive log predicates and pagination values
// read without mutation.
type OpsSystemLogFilter struct {
	// StartTime and EndTime constrain a half-open range when nonnil and nonzero.
	// PostgreSQL converts endpoints to UTC and combines them with all other
	// effective dimensions.
	StartTime *time.Time // inclusive log observation start
	EndTime   *time.Time // exclusive log observation end
	Host      string     // exact logging host filter

	Level     string // case-folded severity text filter
	Component string // exact component text filter

	// RequestID resolves request_lookup_ids candidates with exact-local
	// precedence, while ClientRequestID adds its independent saved-column
	// equality. Supplying both requires both predicates to match the observation.
	RequestID       string // request lookup correlation filter
	ClientRequestID string // exact caller correlation filter
	// UserID, APIKeyID, and ProviderID constrain saved associations only when
	// positive. Nil and nonpositive values add no predicate and do not satisfy the
	// effective cleanup condition.
	UserID     *int64 // saved user association filter
	APIKeyID   *int64 // saved API-key association filter
	ProviderID *int64 // saved provider association filter
	Platform   string // exact saved platform filter
	Model      string // exact saved model filter
	// Query is trimmed and wrapped in percent markers for message, both
	// correlation columns, and metadata text. Caller percent/underscore wildcards
	// remain active in the search.
	Query string // wildcard message/correlation/metadata search

	// Page and PageSize select normalized PostgreSQL pagination: nonpositive
	// values become one and fifty, and PageSize above two hundred is capped. Count
	// and page read separately.
	Page     int // one-based selected log page
	PageSize int // selected log page size
}

// OpsSystemLogCleanupFilter supplies the effective conditions guarding a
// system-log deletion statement.
type OpsSystemLogCleanupFilter struct {
	// StartTime and EndTime constrain a half-open range when nonnil and nonzero.
	// PostgreSQL converts endpoints to UTC and combines them with all other
	// effective dimensions.
	StartTime *time.Time // inclusive log observation start
	EndTime   *time.Time // exclusive log observation end
	Host      string     // exact logging host filter

	Level     string // case-folded severity text filter
	Component string // exact component text filter

	// RequestID resolves request_lookup_ids candidates with exact-local
	// precedence, while ClientRequestID adds its independent saved-column
	// equality. Supplying both requires both predicates to match the observation.
	RequestID       string // request lookup correlation filter
	ClientRequestID string // exact caller correlation filter
	// UserID, APIKeyID, and ProviderID constrain saved associations only when
	// positive. Nil and nonpositive values add no predicate and do not satisfy the
	// effective cleanup condition.
	UserID     *int64 // saved user association filter
	APIKeyID   *int64 // saved API-key association filter
	ProviderID *int64 // saved provider association filter
	Platform   string // exact saved platform filter
	Model      string // exact saved model filter
	// Query retains caller LIKE wildcards when PostgreSQL wraps it as substring
	// search. Even a broad wildcard counts as an effective condition for the
	// deletion guard.
	Query string // wildcard message/correlation/metadata search
}

// OpsSystemLogList carries one normalized system-log page with its
// independently queried count.
type OpsSystemLogList struct {
	Logs     []*OpsSystemLog `json:"logs"`      // ordered matching system observations
	Total    int             `json:"total"`     // separately queried matching count
	Page     int             `json:"page"`      // normalized one-based returned page
	PageSize int             `json:"page_size"` // normalized returned page limit
}

// OpsSystemLogCleanupAudit records an administrator's requested conditions and
// completed deletion count.
type OpsSystemLogCleanupAudit struct {
	CreatedAt  time.Time // cleanup audit capture instant
	OperatorID int64     // administrator operation actor identity
	// Conditions describes requested filters serialized by OpsService. It can
	// include supplied nonpositive identities that did not become effective
	// PostgreSQL predicates, so it is diagnostic input evidence.
	Conditions  string // serialized requested cleanup conditions
	DeletedRows int64  // reported deleted observation count
}

// OpsSystemMetricsSnapshot carries persisted nullable system observations with
// optional current pool-limit context.
type OpsSystemMetricsSnapshot struct {
	ID            int64     `json:"id"`             // saved global sample identity
	CreatedAt     time.Time `json:"created_at"`     // saved sample capture instant
	WindowMinutes int       `json:"window_minutes"` // saved observation window minutes

	// CPUUsagePercent, MemoryUsedMB, MemoryTotalMB, MemoryUsagePercent,
	// DiskUsedMB, DiskTotalMB, and DiskUsagePercent retain saved host measurement
	// presence. Nil is unavailable, including zero disk sizes omitted by the write
	// encoder.
	CPUUsagePercent    *float64 `json:"cpu_usage_percent"`    // observed CPU usage percentage
	MemoryUsedMB       *int64   `json:"memory_used_mb"`       // observed used memory MiB
	MemoryTotalMB      *int64   `json:"memory_total_mb"`      // observed memory capacity MiB
	MemoryUsagePercent *float64 `json:"memory_usage_percent"` // observed memory usage percentage
	DiskUsedMB         *int64   `json:"disk_used_mb"`         // observed used disk MiB
	DiskTotalMB        *int64   `json:"disk_total_mb"`        // observed disk capacity MiB
	DiskUsagePercent   *float64 `json:"disk_usage_percent"`   // observed disk usage percentage

	// DBOK and RedisOK retain nullable dependency health observations from the
	// saved sample. A nonnil false value is an available failed-check result,
	// independently of pool-size or connection counts.
	DBOK    *bool `json:"db_ok"`    // database health observation boolean
	RedisOK *bool `json:"redis_ok"` // Redis health observation boolean

	// DBMaxOpenConns and RedisPoolSize are supplied by
	// OpsService.GetDashboardOverview from positive current options after reading
	// a sample. They are comparison limits, independently of that sample's saved
	// connection counts.
	DBMaxOpenConns *int `json:"db_max_open_conns"` // configured database connection limit
	RedisPoolSize  *int `json:"redis_pool_size"`   // configured Redis connection limit

	// RedisConnTotal and RedisConnIdle retain separate saved total and idle
	// connection counts. Nil means absent persisted evidence, while an available
	// zero count remains a present scalar.
	RedisConnTotal *int `json:"redis_conn_total"` // observed Redis connection count
	RedisConnIdle  *int `json:"redis_conn_idle"`  // observed idle Redis connections

	// DBConnActive and DBConnIdle retain separate saved active and idle database
	// counts. Nil means absent persisted evidence, while measured zero remains
	// available through the dedicated write encoder.
	DBConnActive *int `json:"db_conn_active"` // observed active database connections
	DBConnIdle   *int `json:"db_conn_idle"`   // observed idle database connections
	// DBConnWaiting is accepted by metric persistence and returned by
	// latest-sample reads. The current OpsMetricsCollector supplies no
	// waiting-count field, leaving its input nil and storage absent for those samples.
	DBConnWaiting *int `json:"db_conn_waiting"` // supplied database waiting-count observation

	// GoroutineCount, ConcurrencyQueueDepth, and ProviderSwitchCount retain
	// runtime counts from the saved sample. Nil and measured zero remain distinct
	// when the respective write encoder supplied a value.
	GoroutineCount        *int   `json:"goroutine_count"`         // observed runtime goroutine count
	ConcurrencyQueueDepth *int   `json:"concurrency_queue_depth"` // observed provider wait depth
	ProviderSwitchCount   *int64 `json:"provider_switch_count"`   // saved failover attempt count
}

// OpsUpsertJobHeartbeatInput carries omission-aware updates for one persisted
// background job.
type OpsUpsertJobHeartbeatInput struct {
	// JobName requires a nonempty string in PostgreSQL upsert but is not trimmed.
	// A whitespace-only value can therefore identify its own heartbeat row.
	JobName string // persisted background job identity

	// LastRunAt, LastSuccessAt, LastErrorAt, LastError, and LastDurationMs use
	// NULL for omitted values, including zero times, blank text, and zero
	// duration. On conflict, omission retains previous data and supplied success
	// clears stored error fields; timestamp ordering is not checked.
	LastRunAt      *time.Time // latest supplied attempt instant
	LastSuccessAt  *time.Time // latest supplied success instant
	LastErrorAt    *time.Time // latest supplied failure instant
	LastError      *string    // latest supplied error text
	LastDurationMs *int64     // latest supplied run milliseconds

	// LastResult updates on a conflict only when a nonzero LastSuccessAt is
	// supplied and result text is nonblank. Failed conflict updates retain the
	// prior success result; first insertion stores supplied values directly.
	LastResult *string // latest supplied success summary
}

// OpsJobHeartbeat carries saved nullable job observations and the database
// update timestamp.
type OpsJobHeartbeat struct {
	JobName string `json:"job_name"` // persisted background job identity

	// LastRunAt, LastSuccessAt, LastErrorAt, LastError, LastDurationMs, and
	// LastResult describe persisted observations and can be absent independently.
	// The metrics upsert's omission/success rules determine retention, so these
	// fields do not alone establish current running state.
	LastRunAt      *time.Time `json:"last_run_at"`      // latest supplied attempt instant
	LastSuccessAt  *time.Time `json:"last_success_at"`  // latest supplied success instant
	LastErrorAt    *time.Time `json:"last_error_at"`    // latest supplied failure instant
	LastError      *string    `json:"last_error"`       // latest supplied error text
	LastDurationMs *int64     `json:"last_duration_ms"` // latest supplied run milliseconds
	LastResult     *string    `json:"last_result"`      // latest supplied success summary

	UpdatedAt time.Time `json:"updated_at"` // last heartbeat database update
}

// OpsWindowStats carries raw counts for a validated short realtime observation
// range.
type OpsWindowStats struct {
	// StartTime and EndTime identify the half-open queried range, returned in UTC
	// by PostgreSQL. Equal endpoints are accepted, and positive spans are limited
	// to one day.
	StartTime time.Time `json:"start_time"` // inclusive raw observation start
	EndTime   time.Time `json:"end_time"`   // exclusive raw observation end

	// SuccessCount and TokenConsumed come from one usage read, while
	// ErrorCountTotal comes from a separate non-probe final-error read. Concurrent
	// writes can therefore make their observation snapshots differ.
	SuccessCount    int64 `json:"success_count"`     // saved usage observation count
	ErrorCountTotal int64 `json:"error_count_total"` // final client failure count
	TokenConsumed   int64 `json:"token_consumed"`    // input/output/cache token total
}
