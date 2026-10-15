package testkit

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// runtime_settings.go supplies an in-memory settings repository and production
// runtime readers for gateway tests. The map repository implements value and
// batch reads plus individual writes; other repository operations panic so a
// test notices when its execution path asks for unsupported storage behavior.

// FastPolicySettingsRepo supplies raw setting values and captures individual
// test writes.
type FastPolicySettingsRepo struct {
	// Values retains mutable fixture storage and Set initializes it when nil.
	// Tests serialize reads and writes because this repository has no map lock.
	Values map[string]string
}

// Get panics because this fixture exposes raw-value reads rather than full
// Setting records. Callers use GetValue or GetMultiple for supported reads,
// making an unexpected repository path visible in tests.
func (s *FastPolicySettingsRepo) Get(ctx context.Context, key string) (*settings.Setting, error) {
	panic("unexpected Get call")
}

// GetValue returns the fixture's raw string for key or ErrSettingNotFound when
// absent. It reads retained map storage directly; missing defaults are chosen
// later by the production runtime reader.
func (s *FastPolicySettingsRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.Values[key]; ok {
		return v, nil
	}
	return "", settings.ErrSettingNotFound
}

// Set stores a raw setting value in the fixture map and initializes nil
// storage. Tests serialize this mutation with reads; the value remains
// available to subsequent production reader calls.
func (s *FastPolicySettingsRepo) Set(ctx context.Context, key, value string) error {
	if s.Values == nil {
		s.Values = map[string]string{}
	}
	s.Values[key] = value
	return nil
}

// GetMultiple returns newly allocated map storage containing the requested
// fixture keys that exist. Missing keys are omitted so production readers can
// supply their own defaults; stored strings are copied by value.
func (s *FastPolicySettingsRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	Values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.Values[key]; ok {
			Values[key] = value
		}
	}
	return Values, nil
}

// SetMultiple panics because the fixture supports individual writes for its
// current tests. Callers needing bulk persistence provide a repository
// implementation for that path before invoking the production settings
// operation.
func (s *FastPolicySettingsRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll panics because complete settings enumeration is outside this
// fixture's implemented reads. Tests use GetValue or GetMultiple, or supply
// another repository when their production path needs enumeration.
func (s *FastPolicySettingsRepo) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics when a production operation attempts unsupported fixture
// deletion. Tests configure the retained value map directly or supply a
// repository implementing deletion when they exercise that storage path.
func (s *FastPolicySettingsRepo) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

// RuntimeReaders constructs production gateway, provider, quota, routing,
// moderation, search, and scheduler readers over the supplied repository.
// Gateway defaults use production Beta and User-Agent rules; the fixture
// returns shared readers for later request tests.
func RuntimeReaders(repo settings.Repository) *gatewayprovider.RuntimeReaders {
	runtime := gateway.NewRuntimeSettings(repo, settings.ErrSettingNotFound, func() *gateway.BetaPolicySettings {
		return gatewayprovider.GatewayBetaPolicy(anthropic.DefaultBetaPolicySettings())
	}, gateway.ClientSettingsOptions{NormalizeUserAgentVersion: antigravity.NormalizeUserAgentVersion, DefaultUserAgentVersion: antigravity.GetDefaultUserAgentVersion})
	return &gatewayprovider.RuntimeReaders{Gateway: runtime, Provider: provider.NewRuntimeSettings(repo, settings.ErrSettingNotFound), Quota: provider.NewQuotaSettingsCache(repo, settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings), Routing: routing.NewRuntimeSettings(repo), Moderation: moderation.NewRuntimeSettings(repo, settings.ErrSettingNotFound), Search: search.NewConfigService(repo, nil, nil, search.NewRegistry()), Scheduler: repo}
}
