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

// HealthInput supplies dependencies for provider health tests.
type HealthInput struct {
	Store   gatewayadapter.ExecutionProviderStore
	Cache   provider.TempUnschedCache
	Options provider.HealthOptions
	Readers *gatewayadapter.RuntimeReaders
}

type healthStore struct {
	gatewayadapter.ExecutionProviderStore
}

func (s healthStore) GetByID(ctx context.Context, id int64) (*provider.Record, error) {
	value, err := s.ExecutionProviderStore.GetByID(ctx, id)
	return gatewayadapter.ExecutionRecord(value), err
}

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

// NewHealthObserver wires production health decisions to test dependencies.
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
	options.ClearWindowRateLimit = func(ctx context.Context, id int64) error { return recovery.ClearRateLimit(ctx, id) }
	health := provider.NewHealthService(store, input.Cache, options)
	recovery = provider.NewRecoveryService(healthStore{input.Store}, input.Cache, provider.RecoveryOptions{Now: time.Now, Warn: slog.Warn, ResetCounter: health.ResetForbiddenCounter, InvalidateToken: options.InvalidateUnauthorizedToken})
	return &provideradapter.UpstreamHealth{
		Core: health,
		Team: provider.NewTeamLinkedHealth(teamStore, provider.TeamLinkedOptions{Now: time.Now, Warn: slog.Warn, Block: options.Block}),
		Limits: &provideradapter.RateLimitObserver{Health: health, Plans: input.Store, NextGeminiDaily: func() *int64 {
			location, err := time.LoadLocation("America/Los_Angeles")
			if err != nil {
				location = time.FixedZone("PST", -8*3600)
			}
			reset := provider.GeminiDailyResetTime(time.Now(), location).Unix()
			return &reset
		}},
		Models: &provideradapter.ModelHealth{Health: health, IsImageModel: media.IsGPTImageGenerationModel},
	}
}
