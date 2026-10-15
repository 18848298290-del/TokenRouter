package testkit

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// health.go constructs gateway health observers backed by execution-provider
// test stores. The adapter converts store results to provider records, while
// the factory replaces clock and logging options and can bind runtime settings
// readers so test inputs follow the same policy decisions as execution.

// HealthInput supplies store, cache, options, and optional readers for health
// fixtures.
type HealthInput struct {
	Store gatewayadapter.ExecutionProviderStore // execution-provider fixture storage source
	Cache provider.TempUnschedCache             // temporary scheduling block cache
	// Options seeds the health service. NewHealthObserver replaces its clock,
	// loggers, session-window source and recovery callback before constructing
	// the service.
	Options provider.HealthOptions // initial production health configuration
	// Readers optionally supplies production runtime policy callbacks; when present,
	// the factory replaces matching policy readers from Options with these methods.
	Readers *gatewayadapter.RuntimeReaders
}

// healthStore converts execution fixture records for production health and
// recovery reads.
type healthStore struct {
	gatewayadapter.ExecutionProviderStore // inherited execution-provider fixture operations
}

// GetByID returns the source provider through production ExecutionRecord
// conversion with its read error. A nil provider remains nil, allowing health
// callers to interpret absent data themselves.
func (s healthStore) GetByID(ctx context.Context, id int64) (*provider.Record, error) {
	value, err := s.ExecutionProviderStore.GetByID(ctx, id)
	return gatewayadapter.ExecutionRecord(value), err
}

// ListByPlatform converts each source execution provider into a production
// record and retains its read error. A nil collection remains nil; the result
// slice is newly allocated for nonnil input.
func (s healthStore) ListByPlatform(ctx context.Context, platform string) ([]provider.Record, error) {
	values, err := s.ExecutionProviderStore.ListByPlatform(ctx, platform)
	if values == nil {
		return nil, err
	}
	out := make([]provider.Record, len(values))
	for i := range values {
		out[i] = *gatewayadapter.ExecutionRecord(&values[i])
	}
	return out, err
}

// NewHealthObserver constructs production health, recovery, rate-limit, model,
// and team observers from fixture inputs. It supplies wall-clock time and
// production logging, and optional Readers replaces the corresponding
// health-setting callbacks.
//
// The recovery callback captures a service assigned after health construction
// and is used by later observations. Input.Store supplies session-window and
// quota-plan operations as well as converted record reads. Gemini daily reset
// uses Los Angeles time when loadable; a fixed UTC-8 location is the recovery
// choice when zone loading fails.
func NewHealthObserver(input HealthInput) *provideradapter.UpstreamHealth {
	options := input.Options
	options.Now = time.Now
	options.Warn, options.Info = slog.Warn, slog.Info
	options.APIKeyHealthWarn = provideradapter.LogAPIKeyHealthWarning
	options.SessionWindows = input.Store
	if input.Readers != nil {
		source := input.Readers.Provider
		options.APIKeyHealthSettings = source.GetOpenAIAPIKeyHealthBreakerSettings
		options.RateLimit429Settings = source.GetRateLimit429CooldownSettings
		options.ForbiddenSettings = source.GetOpenAI403CooldownSettings
		options.OverloadSettings = source.GetOverloadCooldownSettings
		options.HasThresholdSettings = func() bool { return true }
		options.Thresholds = source.GetProviderSchedulingThresholds
		options.StreamSettings = func(ctx context.Context) (*provider.StreamTimeoutSettings, error, bool) {
			v, err := source.GetStreamTimeoutSettings(ctx)
			return v, err, true
		}
	}
	var store provider.HealthStore
	var teamStore provider.TeamLinkedStore
	if input.Store != nil {
		store = healthStore{input.Store}
		teamStore = healthStore{input.Store}
	}
	var recovery *provider.RecoveryService
	// Bind the health callback before constructing recovery, then populate its
	// captured service before the returned observer can process any window event.
	options.ClearWindowRateLimit = func(ctx context.Context, id int64) error { return recovery.ClearRateLimit(ctx, id) }
	health := provider.NewHealthService(store, input.Cache, options)
	recovery = provider.NewRecoveryService(healthStore{input.Store}, input.Cache, provider.RecoveryOptions{Now: time.Now, Warn: slog.Warn, ResetCounter: health.ResetForbiddenCounter, InvalidateToken: options.InvalidateUnauthorizedToken})
	return &provideradapter.UpstreamHealth{
		Core: health,
		Team: provider.NewTeamLinkedHealth(teamStore, provider.TeamLinkedOptions{Now: time.Now, Warn: slog.Warn, Block: options.Block}),
		Limits: &provideradapter.RateLimitObserver{Health: health, Plans: input.Store, NextGeminiDaily: func() *int64 {
			location, err := time.LoadLocation("America/Los_Angeles")
			// Use a fixed UTC-8 reset location when zone data cannot load, keeping the
			// fixture's daily-reset callback available to production rate-limit observations.
			if err != nil {
				location = time.FixedZone("PST", -8*3600)
			}
			reset := provider.GeminiDailyResetTime(time.Now(), location).Unix()
			return &reset
		}},
		Models: &provideradapter.ModelHealth{Health: health, IsImageModel: media.IsGPTImageGenerationModel},
	}
}
