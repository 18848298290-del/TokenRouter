package ops

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Ops services receive deployment settings and read-only provider, user,
// scheduler, and authentication observations from app. Realtime capacity
// display uses configured concurrency while scheduler load reads use the copied
// effective load factor. Logging callbacks connect runtime configuration
// changes with the process logger and cleanup reporting.

// Repository error sentinels let Ops distinguish absent settings from absent
// observation rows when its injected storage adapters classify failures.
var (
	ErrSettingNotFound = settings.ErrSettingNotFound     // stored setting key is absent
	ErrRowNotFound     = errors.New("ops row not found") // requested observation row is absent
)

// Options supplies deployment limits, logging callbacks, and retention defaults
// to Ops services.
type Options struct {
	Ops      RuntimeOptions // deployment Ops task settings
	Database struct {
		// MaxOpenConns is the configured database connection ceiling.
		// GetDashboardOverview includes this comparison value when it is positive and
		// a latest system sample exists.
		MaxOpenConns int
	} // database pool display options
	Redis struct {
		// PoolSize is the configured Redis pool size. GetDashboardOverview includes
		// this comparison value when it is positive and a latest system sample
		// exists.
		PoolSize int
	} // Redis pool display options
	Log LogOptions // deployment logger baseline options
	// Timezone selects report construction and cleanup schedule rebuilding
	// locations. Those paths use time.Local when the configured location is blank
	// or cannot be loaded.
	Timezone string
	// IsNotFound classifies missing observation rows for Ops error mapping. When
	// absent, isNotFound uses errors.Is against ErrRowNotFound as its default
	// classifier.
	IsNotFound func(error) bool
	// Logf receives formatted process diagnostics. A missing callback discards
	// diagnostics emitted through report and reportOptions, while direct
	// standard-library log calls retain their own behavior.
	Logf func(string, ...any)
	// CleanupCompleted receives the formatted deletion summary after a scheduled
	// cleanup succeeds. A nil callback leaves that optional success report
	// unobserved by this hook.
	CleanupCompleted func(string)
}

// RuntimeOptions contains deployment availability and background-task settings
// passed from app.
type RuntimeOptions struct {
	// Enabled is the deployment availability switch checked by Ops services before
	// monitoring work. With nonnil Options, false disables their guarded execution
	// paths.
	Enabled     bool
	Cleanup     CleanupOptions     // deployment cleanup scheduling defaults
	Aggregation AggregationOptions // deployment aggregation availability settings
	// MetricsCollectorCache is copied from deployment configuration by
	// provideOpsOptions. Current sampling uses MetricsQueries and has no reader of
	// this accepted cache setting.
	MetricsCollectorCache MetricsCollectorCacheOptions
}

// CleanupOptions describes deployment cleanup scheduling, batch pacing, and
// observation retention days.
type CleanupOptions struct {
	Enabled  bool   // deployment cleanup scheduling preference
	Schedule string // five-field cleanup cron expression
	// BatchSize limits rows per delete, and BatchPauseMS sets the pause between
	// full batches in milliseconds. ErrorLogRetentionDays covers errors, ingress
	// counts, alerts, and cleanup audits; SystemLogRetentionDays covers system
	// logs; MinuteMetricsRetentionDays covers samples; HourlyMetricsRetentionDays
	// covers hour and day aggregates. Positive retention keeps that many calendar
	// days, zero selects truncate, and negative deployment retention skips its
	// target when it reaches opsCleanupPlan.
	BatchSize, BatchPauseMS, ErrorLogRetentionDays, SystemLogRetentionDays, MinuteMetricsRetentionDays, HourlyMetricsRetentionDays int
}
type (
	// AggregationOptions carries deployment availability for the Ops hour and day
	// aggregators.
	AggregationOptions struct {
		// Enabled permits Ops aggregation under deployment configuration. The shared
		// runtime switch and overall monitoring switch can further disable an
		// otherwise available job.
		Enabled bool
	}
	// MetricsCollectorCacheOptions carries accepted deployment cache settings
	// copied into Ops runtime options.
	MetricsCollectorCacheOptions struct {
		// Enabled and TTL retain the deployment values supplied by app.
		// MetricsQueries currently reads SQL directly, so these fields do not
		// determine its query reuse or expiration.
		Enabled bool          // accepted metrics-cache availability preference
		TTL     time.Duration // accepted metrics-cache reuse duration
	}
)

// LogOptions supplies the deployment baseline used to construct runtime logging
// settings.
type LogOptions struct {
	// Level names the minimum log level, and StacktraceLevel names the stacktrace
	// threshold. defaultOpsRuntimeLogConfig trims and lowercases both strings when
	// constructing the deployment-derived logging baseline.
	Level, StacktraceLevel string
	Caller                 bool            // include logger caller locations
	Sampling               SamplingOptions // repeated-message logger sampling controls
}

// SamplingOptions carries repetition-sampling controls copied into the runtime
// logger configuration.
type SamplingOptions struct {
	Enabled bool // enable repeated-message sampling policy
	// Initial supplies the initial allowance for each level/message pair in a
	// one-second sampling window, and Thereafter keeps every nth later occurrence.
	// Logging applies this rule when Enabled is true; runtime settings can replace
	// both baseline counts.
	Initial, Thereafter int
}

// Settings supplies persisted Ops configuration strings used by query and
// background services.
type Settings interface {
	// GetValue reads one Ops setting string using the supplied context. Missing
	// keys are represented by ErrSettingNotFound so default-bearing readers can
	// distinguish first access from other storage failures.
	GetValue(context.Context, string) (string, error)
	// GetMultiple reads the requested setting strings for one snapshot refresh.
	// Missing keys are omitted from the returned map, allowing each service to
	// apply its defaults while a read error retains the previous snapshot.
	GetMultiple(context.Context, []string) (map[string]string, error)
	// Set replaces one stored setting string and reports storage failures to its
	// caller. Settings services publish their local effects after a successful
	// write, except runtime logging which applies and then attempts persistence.
	Set(context.Context, string, string) error
	// Delete removes one stored setting key using the supplied context. Runtime
	// logging reset tolerates ErrSettingNotFound and attempts to restore the
	// previous logger when other deletion errors occur.
	Delete(context.Context, string) error
}

// PreAggregationReader supplies the shared aggregation decision used by
// dashboard query routing.
type PreAggregationReader interface {
	// OpsEnabled resolves whether deployment availability and runtime settings
	// permit Ops aggregation. Dashboard query routing uses false to select raw
	// observations, while the shared reader supplies its own cache and
	// read-failure policy.
	OpsEnabled(context.Context) bool
}

// GroupObservation carries a provider membership identity and name for realtime
// grouping.
type GroupObservation struct {
	ID   int64  // persisted provider membership identity
	Name string // current group display name
}

// ProviderObservation carries provider scheduling and availability data used
// for Ops observations.
type ProviderObservation struct {
	ID int64 // persisted observed provider identity
	// Name is the provider display name, Platform identifies its execution
	// platform, Status carries the saved provider status, and ErrorMessage carries
	// its saved diagnostic text. App copies these values from provider records before
	// realtime queries use them.
	Name, Platform, Status, ErrorMessage string
	Schedulable                          bool // provider scheduling preference flag
	// Concurrency is the configured capacity displayed in realtime totals, and
	// LoadFactor is the effective scheduler load limit used in batch observations.
	// EffectiveLoadFactor returns the copied limit directly; provider policy has
	// already been applied by the observation adapter.
	Concurrency, LoadFactor int
	// Groups supplies the observed provider membership entries. Statistics
	// aggregate each matching membership and use the first entry for provider
	// display when no group filter is selected.
	Groups []*GroupObservation
	// TempUnschedulableUntil marks temporary scheduling exclusion,
	// RateLimitResetAt marks rate-limit recovery, and OverloadUntil marks overload
	// recovery. Nil pointers omit their condition; availability compares supplied
	// times with its collection time and gives an error status precedence over
	// rate-limit and overload badges.
	TempUnschedulableUntil, RateLimitResetAt, OverloadUntil *time.Time
}

// UserObservation carries user identity and concurrency settings for Ops
// diagnostics and report recipient selection.
type UserObservation struct {
	ID int64 // persisted observed user identity
	// Email is the observed user email address, and Username is the observed user
	// display name. Realtime user views expose these copied identity values
	// alongside the configured concurrency capacity.
	Email, Username string
	Concurrency     int // configured user concurrency capacity
}

// ProviderReader supplies paginated provider observations for realtime
// statistics when needed.
type ProviderReader interface {
	// ListPage reads a provider page with platform and group filters for realtime
	// Ops statistics. A zero group represents an absent group filter, and page
	// metadata lets the caller stop collecting pages.
	ListPage(context.Context, pagination.PaginationParams, string, int64) ([]ProviderObservation, *pagination.PaginationResult, error)
}

// UserReader supplies active-user observations for realtime concurrency and
// queue views.
type UserReader interface {
	// ListActivePage reads active-user identities and concurrency settings for a
	// realtime Ops page. Page metadata reports the total, while an empty or short
	// page ends the caller traversal.
	ListActivePage(context.Context, pagination.PaginationParams) ([]UserObservation, *pagination.PaginationResult, error)
}

// ConcurrencyReader reads scheduler load observations for provider and user
// identities.
type ConcurrencyReader interface {
	// GetProvidersLoadBatch returns scheduler load values keyed by provider ID for
	// the supplied identities and limits. Realtime statistics omit failed batches
	// from their map and display missing loads as zero.
	GetProvidersLoadBatch(context.Context, []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error)
	// GetUsersLoadBatch returns scheduler load values keyed by user ID for the
	// supplied identities and limits. Realtime user statistics omit failed batches
	// and include users whose resulting load shows activity.
	GetUsersLoadBatch(context.Context, []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error)
}
type (
	// ProviderWithConcurrency pairs a scheduler provider identity with its load
	// limit.
	ProviderWithConcurrency = scheduler.ProviderWithConcurrency
	// ProviderLoadInfo is the scheduler provider-load observation consumed by Ops
	// concurrency views.
	ProviderLoadInfo = scheduler.ProviderLoadInfo
	// UserWithConcurrency pairs a scheduler user identity with its concurrency
	// limit.
	UserWithConcurrency = scheduler.UserWithConcurrency
	// UserLoadInfo is the scheduler user-load observation consumed by Ops
	// concurrency views.
	UserLoadInfo = scheduler.UserLoadInfo
	// AuthHealthReader supplies authentication invalidation outbox observations
	// for administrator diagnostics.
	AuthHealthReader interface {
		// Health returns authentication invalidation outbox observations using the
		// supplied context. GetAuthCacheInvalidationHealth places this result
		// alongside independently sampled subscriber, lookup, and abuse diagnostics
		// from the API-key service.
		Health(context.Context) apikey.AuthCacheInvalidationHealth
	}
)

// KeyHealthReader supplies API-key lookup, subscriber, and invalid-credential
// pressure observations.
type KeyHealthReader interface {
	// AuthCacheInvalidationSubscriberHealth returns local authentication
	// subscriber diagnostics for the Ops health response.
	AuthCacheInvalidationSubscriberHealth() apikey.AuthCacheInvalidationSubscriberHealth
	// AuthLookupMetrics returns API-key authentication lookup counters for the Ops
	// health response.
	AuthLookupMetrics() apikey.APIKeyAuthLookupMetrics
	// InvalidAuthAbuseHealth returns invalid-credential pressure observations for
	// the Ops health response.
	InvalidAuthAbuseHealth() apikey.InvalidAuthAbuseHealth
}
type (
	// OpsAuthCacheInvalidationHealth combines authentication publication and local
	// lookup diagnostics for Ops responses.
	OpsAuthCacheInvalidationHealth = apikey.OpsAuthCacheInvalidationHealth
	// LogControl applies process logging settings and reports administrator change
	// or failure audits.
	LogControl interface {
		// Apply installs the process logging configuration requested by Ops settings
		// operations. A successful call changes the running logger before the
		// settings service attempts persistence; failures are returned for its audit
		// and response.
		Apply(*OpsRuntimeLogConfig) error
		// Changed reports the operator, previous configuration, saved configuration,
		// and successful action for a runtime logging update or reset. The settings
		// service invokes it after persistence and then attempts cleanup schedule
		// refresh.
		Changed(int64, *OpsRuntimeLogConfig, *OpsRuntimeLogConfig, string)
		// Failed reports the operator, prior and candidate configurations, and
		// failure reason for a logging update or reset. The settings service retains
		// error reporting and attempts any logger restoration before a
		// persistence-failure audit.
		Failed(int64, *OpsRuntimeLogConfig, *OpsRuntimeLogConfig, string)
	}
)

// EffectiveLoadFactor returns the scheduler load limit copied into this
// observation.
func (a ProviderObservation) EffectiveLoadFactor() int { return a.LoadFactor }

// isNotFound applies the injected observation-row classifier when one is
// configured. Otherwise it checks ErrRowNotFound with errors.Is, including for
// nil service options; settings-key absence is handled separately by settings
// readers.
func (s *OpsService) isNotFound(e error) bool {
	if s != nil && s.cfg != nil && s.cfg.IsNotFound != nil {
		return s.cfg.IsNotFound(e)
	}
	// The core default recognizes its own row sentinel. SQL-backed error queries
	// need the app classifier to map sql.ErrNoRows to the same missing-row
	// response.
	return errors.Is(e, ErrRowNotFound)
}

// SetAuthObservers binds outbox and API-key health readers during app
// construction. Bind them before concurrent health queries start, because
// assignments replace the service references directly and use no
// synchronization.
func (s *OpsService) SetAuthObservers(worker AuthHealthReader, key KeyHealthReader) {
	s.authCacheInvalidationWorker = worker
	s.apiKeyService = key
}

// report forwards a formatted Ops diagnostic to the injected process logger
// when available. A nil service, nil options, or absent callback discards that
// diagnostic; direct log.Printf calls have their own path.
func (s *OpsService) report(format string, args ...any) {
	if s != nil && s.cfg != nil && s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}
