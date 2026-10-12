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

var (
	ErrSettingNotFound = settings.ErrSettingNotFound
	ErrRowNotFound     = errors.New("ops row not found")
)

type Options struct {
	Ops              RuntimeOptions
	Database         struct{ MaxOpenConns int }
	Redis            struct{ PoolSize int }
	Log              LogOptions
	Timezone         string
	IsNotFound       func(error) bool
	Logf             func(string, ...any)
	CleanupCompleted func(string)
}
type RuntimeOptions struct {
	Enabled               bool
	Cleanup               CleanupOptions
	Aggregation           AggregationOptions
	MetricsCollectorCache MetricsCollectorCacheOptions
}
type CleanupOptions struct {
	Enabled                                                                                                                        bool
	Schedule                                                                                                                       string
	BatchSize, BatchPauseMS, ErrorLogRetentionDays, SystemLogRetentionDays, MinuteMetricsRetentionDays, HourlyMetricsRetentionDays int
}
type (
	AggregationOptions           struct{ Enabled bool }
	MetricsCollectorCacheOptions struct {
		Enabled bool
		TTL     time.Duration
	}
)

type LogOptions struct {
	Level, StacktraceLevel string
	Caller                 bool
	Sampling               SamplingOptions
}
type SamplingOptions struct {
	Enabled             bool
	Initial, Thereafter int
}
type Settings interface {
	GetValue(context.Context, string) (string, error)
	GetMultiple(context.Context, []string) (map[string]string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}
type PreAggregationReader interface{ OpsEnabled(context.Context) bool }

type GroupObservation struct {
	ID   int64
	Name string
}
type ProviderObservation struct {
	ID                                                      int64
	Name, Platform, Status, ErrorMessage                    string
	Schedulable                                             bool
	Concurrency, LoadFactor                                 int
	Groups                                                  []*GroupObservation
	TempUnschedulableUntil, RateLimitResetAt, OverloadUntil *time.Time
}

type UserObservation struct {
	ID              int64
	Email, Username string
	Concurrency     int
}
type ProviderReader interface {
	ListPage(context.Context, pagination.PaginationParams, string, int64) ([]ProviderObservation, *pagination.PaginationResult, error)
}

type UserReader interface {
	ListActivePage(context.Context, pagination.PaginationParams) ([]UserObservation, *pagination.PaginationResult, error)
}
type ConcurrencyReader interface {
	GetProvidersLoadBatch(context.Context, []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error)
	GetUsersLoadBatch(context.Context, []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error)
}
type (
	ProviderWithConcurrency = scheduler.ProviderWithConcurrency
	ProviderLoadInfo        = scheduler.ProviderLoadInfo
	UserWithConcurrency     = scheduler.UserWithConcurrency
	UserLoadInfo            = scheduler.UserLoadInfo
	AuthHealthReader        interface {
		Health(context.Context) apikey.AuthCacheInvalidationHealth
	}
)

type KeyHealthReader interface {
	AuthCacheInvalidationSubscriberHealth() apikey.AuthCacheInvalidationSubscriberHealth
	AuthLookupMetrics() apikey.APIKeyAuthLookupMetrics
	InvalidAuthAbuseHealth() apikey.InvalidAuthAbuseHealth
}
type (
	OpsAuthCacheInvalidationHealth = apikey.OpsAuthCacheInvalidationHealth
	LogControl                     interface {
		Apply(*OpsRuntimeLogConfig) error
		Changed(int64, *OpsRuntimeLogConfig, *OpsRuntimeLogConfig, string)
		Failed(int64, *OpsRuntimeLogConfig, *OpsRuntimeLogConfig, string)
	}
)

func (a ProviderObservation) EffectiveLoadFactor() int { return a.LoadFactor }

func (s *OpsService) isNotFound(e error) bool {
	if s != nil && s.cfg != nil && s.cfg.IsNotFound != nil {
		return s.cfg.IsNotFound(e)
	}
	return errors.Is(e, ErrRowNotFound)
}

func (s *OpsService) SetAuthObservers(worker AuthHealthReader, key KeyHealthReader) {
	s.authCacheInvalidationWorker = worker
	s.apiKeyService = key
}

// report writes through the injected process logger.
func (s *OpsService) report(format string, args ...any) {
	if s != nil && s.cfg != nil && s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}
