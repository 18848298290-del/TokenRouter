package ops

import providercore "github.com/TokenFlux/TokenRouter/internal/provider"

// Ops stores mail, logging, alert runtime, and advanced preferences as separate
// JSON settings with different readers. Report recipients can fall back to an
// administrator, while alert mail requires configured addresses. Display
// thresholds affect dashboard colors, and capture exclusions and retention
// settings determine the observations retained for diagnosis.

// OpsEmailNotificationConfig holds persisted alert-mail and scheduled-report
// preferences in one setting.
type OpsEmailNotificationConfig struct {
	Alert  OpsEmailAlertConfig  `json:"alert"`  // alert notification delivery settings
	Report OpsEmailReportConfig `json:"report"` // scheduled report delivery settings
}

// OpsEmailAlertConfig supplies recipients and delivery policy to the alert
// evaluator.
type OpsEmailAlertConfig struct {
	Enabled bool `json:"enabled"` // enable alert email attempts
	// Recipients contains alert email addresses read by the evaluator. An empty
	// list skips alert mail, while a nil recipients slice in a supplied update
	// branch retains the stored list.
	Recipients []string `json:"recipients"`
	// MinSeverity names the critical, warning, or info mail floor. Empty or
	// unknown labels permit all classes in the evaluator; P0 maps to critical, P1
	// to warning, and other rule severities to info.
	MinSeverity string `json:"min_severity"`
	// RateLimitPerHour caps admitted recipient attempts per process within one
	// hour. Nonpositive values disable this evaluator limit; accepted attempts can
	// still fail delivery.
	RateLimitPerHour int `json:"rate_limit_per_hour"`
	// BatchingWindowSeconds is stored and returned as an accepted email setting.
	// The current evaluator has no reader that batches mail using this value.
	BatchingWindowSeconds int `json:"batching_window_seconds"`
	// IncludeResolvedAlerts is the saved preference for resolved-alert mail. The
	// current evaluator sends mail when creating firing events and has no reader
	// of this flag.
	IncludeResolvedAlerts bool `json:"include_resolved_alerts"`
}

// OpsEmailReportConfig controls report kinds, recipients, schedules, and
// content suppression preferences.
type OpsEmailReportConfig struct {
	Enabled bool `json:"enabled"` // enable scheduled report generation
	// Recipients supplies report email addresses, normalized by the scheduler
	// before delivery. An empty resolved list tries the first administrator email;
	// a nil update slice keeps the stored recipients.
	Recipients []string `json:"recipients"`
	// DailySummaryEnabled, WeeklySummaryEnabled, ErrorDigestEnabled, and
	// ProviderHealthEnabled select individual report kinds when the global Enabled
	// flag also permits reports. DailySummarySchedule, WeeklySummarySchedule,
	// ErrorDigestSchedule, and ProviderHealthSchedule carry their corresponding
	// minute-resolution five-field cron text. Normalization fills empty schedules,
	// and the scheduler skips invalid cron text.
	// enable daily summary schedule
	DailySummaryEnabled bool `json:"daily_summary_enabled"`
	// daily five-field cron text
	DailySummarySchedule string `json:"daily_summary_schedule"`
	// enable weekly summary schedule
	WeeklySummaryEnabled bool `json:"weekly_summary_enabled"`
	// weekly five-field cron text
	WeeklySummarySchedule string `json:"weekly_summary_schedule"`
	// enable error digest schedule
	ErrorDigestEnabled bool `json:"error_digest_enabled"`
	// digest five-field cron text
	ErrorDigestSchedule string `json:"error_digest_schedule"`
	// ErrorDigestMinCount suppresses digest content when the matched error total
	// is lower. Nonpositive values permit content regardless of the returned
	// total.
	ErrorDigestMinCount int `json:"error_digest_min_count"`
	// enable provider availability schedule
	ProviderHealthEnabled bool `json:"provider_health_enabled"`
	// provider five-field cron text
	ProviderHealthSchedule string `json:"provider_health_schedule"`
	// ProviderHealthErrorRateThreshold is stored and copied into scheduled report
	// definitions. Availability reports currently ignore this accepted value and
	// summarize current provider status.
	ProviderHealthErrorRateThreshold float64 `json:"provider_health_error_rate_threshold"`
}

// OpsEmailNotificationConfigUpdateRequest selects alert and report branches to
// update from administrator input.
type OpsEmailNotificationConfigUpdateRequest struct {
	// Alert and Report leave their stored branch unchanged when nil. A supplied
	// branch replaces its scalar preferences, while that branch retains its
	// current recipients when Recipients is nil and clears them for an allocated
	// empty slice.
	Alert  *OpsEmailAlertConfig  `json:"alert"`  // optional alert mail replacement
	Report *OpsEmailReportConfig `json:"report"` // optional report settings replacement
}

// OpsDistributedLockSettings controls Redis leader acquisition for alert
// evaluation cycles.
type OpsDistributedLockSettings struct {
	// Enabled selects Redis leader acquisition for an evaluation cycle. False
	// permits unlocked evaluation, and a missing Redis dependency also permits
	// execution under the evaluator policy.
	Enabled bool `json:"enabled"`
	// Key names the evaluator lease in Redis. Settings readers and updates trim it
	// and supply the evaluator default when the stored string is empty.
	Key string `json:"key"`
	// TTLSeconds is the lease lifetime in seconds. Normalization supplies a
	// default for nonpositive values, and evaluator cycles do not renew the lease
	// during execution.
	TTLSeconds int `json:"ttl_seconds"`
}

// OpsAlertSilenceEntry matches rule or severity selections for suppressing
// alert email attempts.
type OpsAlertSilenceEntry struct {
	// RuleID narrows this silence to a matching evaluated rule when supplied. A
	// nil pointer leaves the rule identity unconstrained in isOpsAlertSilenced.
	RuleID *int64 `json:"rule_id,omitempty"`
	// Severities matches either the event or current rule severity after trimming
	// and case folding. An empty slice leaves the severity unconstrained in
	// isOpsAlertSilenced.
	Severities []string `json:"severities,omitempty"`

	// UntilRFC3339 gives the silence end in RFC3339 text. The evaluator skips
	// malformed or empty times and keeps an entry active when now equals its end.
	UntilRFC3339 string `json:"until_rfc3339"`
	Reason       string `json:"reason"` // stored silence explanation text
}

// OpsAlertSilencingSettings supplies global and selected-rule mail suppression
// to the evaluator.
type OpsAlertSilencingSettings struct {
	Enabled bool `json:"enabled"` // enable runtime mail silencing

	// GlobalUntilRFC3339 supplies a global mail-silence deadline in RFC3339 text.
	// The evaluator accepts valid text and silences while now is strictly before
	// the parsed end.
	GlobalUntilRFC3339 string `json:"global_until_rfc3339"`
	GlobalReason       string `json:"global_reason"` // stored global silence explanation

	// Entries contains rule and severity mail silences checked after the global
	// deadline. The evaluator uses the first applicable entry and leaves raw
	// metric collection unchanged.
	Entries []OpsAlertSilenceEntry `json:"entries,omitempty"`
}

// OpsMetricThresholds supplies optional administrator dashboard warning and
// critical display cutoffs.
type OpsMetricThresholds struct {
	// SLAPercentMin, TTFTp99MsMax, RequestErrorRatePercentMax, and
	// UpstreamErrorRatePercentMax use nil to omit a cutoff. OpsDashboardHeader
	// returns a normal display classification when either the cutoff or its
	// measurement is absent.
	// SLAPercentMin is a percentage cutoff used by OpsDashboardHeader. Values
	// below it are critical, and remaining values below the cutoff plus 0.1
	// percentage points are warning.
	SLAPercentMin *float64 `json:"sla_percent_min,omitempty"`
	// TTFTp99MsMax is the millisecond cutoff for first-token p99 used by
	// OpsDashboardHeader. Values at or above the cutoff are critical; lower values
	// reaching eighty percent of that cutoff are warning.
	TTFTp99MsMax *float64 `json:"ttft_p99_ms_max,omitempty"`
	// RequestErrorRatePercentMax is the percentage cutoff for request errors used
	// by OpsDashboardHeader. Values at or above the cutoff are critical; lower
	// values reaching eighty percent of that cutoff are warning.
	RequestErrorRatePercentMax *float64 `json:"request_error_rate_percent_max,omitempty"`
	// UpstreamErrorRatePercentMax is the percentage cutoff for upstream errors
	// used by OpsDashboardHeader. Values at or above the cutoff are critical;
	// lower values reaching eighty percent of that cutoff are warning.
	UpstreamErrorRatePercentMax *float64 `json:"upstream_error_rate_percent_max,omitempty"`
}

// OpsRuntimeLogConfig carries persisted logger controls, retention, and
// administrator update metadata.
type OpsRuntimeLogConfig struct {
	Level          string `json:"level"`           // minimum emitted log level
	EnableSampling bool   `json:"enable_sampling"` // enable repeated-message log sampling
	// SamplingInitial is the initial allowance per level/message pair in each
	// one-second logger sampling window. With EnableSampling true, SamplingNext
	// selects every nth later occurrence in that same window.
	SamplingInitial int `json:"sampling_initial"`
	// subsequent repeated-message sampling interval
	SamplingNext    int    `json:"sampling_thereafter"`
	Caller          bool   `json:"caller"`           // include log caller location
	StacktraceLevel string `json:"stacktrace_level"` // stacktrace emission level threshold
	RetentionDays   int    `json:"retention_days"`   // system log retention days
	// Source, UpdatedAt, and UpdatedByUserID identify a runtime logging update or
	// reset returned to administrators. Updates store these values with the
	// setting; a reset returns its metadata after deleting the override.
	Source    string `json:"source,omitempty"`     // configuration source display label
	UpdatedAt string `json:"updated_at,omitempty"` // RFC3339
	// last logging configuration operator
	UpdatedByUserID int64 `json:"updated_by_user_id,omitempty"`
	// Extra carries opaque JSON attributes through storage and change-audit
	// encoding. UpdateRuntimeLogConfig shallow-copies this map with its request,
	// so callers keep entries stable during encoding; JSON-compatible values avoid
	// serialization errors after logger application.
	Extra map[string]any `json:"extra,omitempty"`
}

// OpsAlertRuntimeSettings supplies evaluation cadence, leader settings, and
// runtime mail silencing.
type OpsAlertRuntimeSettings struct {
	// seconds between evaluator cycles
	EvaluationIntervalSeconds int `json:"evaluation_interval_seconds"`

	// evaluator Redis lease settings
	DistributedLock OpsDistributedLockSettings `json:"distributed_lock"`
	Silencing       OpsAlertSilencingSettings  `json:"silencing"` // runtime alert mail silencing
	// Thresholds retains nested metric-display values in alert runtime JSON. The
	// dashboard reads its display thresholds from the separate metric-threshold
	// settings endpoint.
	Thresholds OpsMetricThresholds `json:"thresholds"`
}

// OpsAdvancedSettings holds capture policy, dashboard preferences, quota
// thresholds, and cleanup settings.
type OpsAdvancedSettings struct {
	// operational data cleanup settings
	DataRetention OpsDataRetentionSettings `json:"data_retention"`
	// provider quota threshold defaults
	OpenAIProviderQuotaAutoPause OpsOpenAIProviderQuotaAutoPauseSettings `json:"openai_provider_quota_auto_pause"`
	// IgnoreCountTokensErrors suppresses capture when the request path contains
	// /count_tokens. The gateway capture helper applies this flag to every
	// matching error path, including errors beyond the expected probe responses.
	IgnoreCountTokensErrors bool `json:"ignore_count_tokens_errors"`
	// IgnoreContextCanceled suppresses capture when the lowercased message or body
	// contains the context-canceled marker. Matched words select suppression
	// independently of the current request context state.
	IgnoreContextCanceled bool `json:"ignore_context_canceled"`
	// IgnoreNoAvailableProviders suppresses capture when the lowercased message or
	// body contains the no-available-providers marker. The flag affects this
	// diagnostic filter after request classification has been observed.
	IgnoreNoAvailableProviders bool `json:"ignore_no_available_providers"`
	// IgnoreInvalidApiKeyErrors is retained for older settings clients and
	// normalized to true. Admission rejections have a dedicated aggregate path,
	// while the gateway legacy body filter still checks invalid or missing API-key
	// markers.
	IgnoreInvalidApiKeyErrors bool `json:"ignore_invalid_api_key_errors"`
	// IgnoreInsufficientBalanceErrors suppresses matching balance or
	// provider-balance messages and bodies, including the body quota marker. This
	// changes diagnostic capture and leaves the completed request outcome
	// available to its other recorders.
	IgnoreInsufficientBalanceErrors bool `json:"ignore_insufficient_balance_errors"`
	// IgnoredStatusCodes supplies client-status exclusions for SLA categories. Nil
	// selects default codes and an allocated empty slice selects no exclusions;
	// upstream context can keep a matching status countable.
	IgnoredStatusCodes []int `json:"ignored_status_codes"`
	// show model token statistics
	DisplayOpenAITokenStats bool `json:"display_openai_token_stats"`
	DisplayAlertEvents      bool `json:"display_alert_events"` // show dashboard alert events
	// enable dashboard automatic refresh
	AutoRefreshEnabled bool `json:"auto_refresh_enabled"`
	// seconds between dashboard refreshes
	AutoRefreshIntervalSec int `json:"auto_refresh_interval_seconds"`
}

// OpsOpenAIProviderQuotaAutoPauseSettings aliases the provider-owned default
// thresholds for quota auto-pause.
type OpsOpenAIProviderQuotaAutoPauseSettings = providercore.QuotaAutoPauseSettings

// OpsDataRetentionSettings supplies stored cleanup scheduling, pacing, and
// retention overrides.
type OpsDataRetentionSettings struct {
	CleanupEnabled bool `json:"cleanup_enabled"` // enable stored cleanup schedule
	// CleanupSchedule carries five-field cron text. Normalization supplies the
	// default when blank; a reload reports invalid syntax after retiring the
	// previous schedule.
	CleanupSchedule string `json:"cleanup_schedule"`
	// CleanupBatchSize and CleanupPauseMS supply delete-batch size and pauses
	// between full batches. Normalization replaces nonpositive tuning values with
	// cleanup defaults; updates validate supplied positive values against their
	// accepted ranges.
	CleanupBatchSize int `json:"cleanup_batch_size"` // maximum delete batch rows
	CleanupPauseMS   int `json:"cleanup_pause_ms"`   // milliseconds
	// ErrorLogRetentionDays covers errors, ingress rejections, alert events, and
	// cleanup audits; MinuteMetricsRetentionDays covers system samples;
	// HourlyMetricsRetentionDays covers hour and day buckets. Zero selects full
	// clearing, positive values select retained calendar days, and settings
	// updates reject negative retention.
	ErrorLogRetentionDays      int `json:"error_log_retention_days"`      // days
	MinuteMetricsRetentionDays int `json:"minute_metrics_retention_days"` // days
	HourlyMetricsRetentionDays int `json:"hourly_metrics_retention_days"` // days
}
