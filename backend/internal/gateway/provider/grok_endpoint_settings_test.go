package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grokBaseURLSettingRepoStub supplies saved endpoint settings for runtime
// policy tests.
type grokBaseURLSettingRepoStub struct {
	values map[string]string /* saved endpoint setting values */
}

// TestGrokBaseURLForMode covers the API, regional, CLI, and invalid endpoint
// modes. Recognized modes choose their named constants, while invalid input
// selects the CLI endpoint.
func TestGrokBaseURLForMode(t *testing.T) {
	for _, tc := range []struct {
		mode string // configured default endpoint mode
		want string // expected resolved endpoint URL
	}{
		{"api", xai.DefaultBaseURL},
		{"us-east-1", xai.DefaultUSEast1BaseURL},
		{"us-west-2", xai.DefaultUSWest2BaseURL},
		{"eu-west-1", xai.DefaultEUWest1BaseURL},
		{"cli", xai.DefaultCLIBaseURL},
		{"invalid", xai.DefaultCLIBaseURL},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			require.Equal(t, tc.want, GrokBaseURLForMode(tc.mode))
		})
	}
}

// GetValue reads the fixture's saved string for key and returns an error when
// absent. Runtime settings use this operation to resolve the configured
// default endpoint mode.
func (r *grokBaseURLSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", fmt.Errorf("setting %s not found", key)
}

// Get returns an unused-operation error because this fixture supplies endpoint
// settings through GetValue. Runtime endpoint tests use that string reader;
// calls requesting a full Setting object are unsupported by this fixture.
func (r *grokBaseURLSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	return nil, fmt.Errorf("unused")
}

// Set accepts fixture writes and returns success while leaving saved values
// unchanged.
func (r *grokBaseURLSettingRepoStub) Set(context.Context, string, string) error { return nil }

// GetMultiple returns all saved fixture values, ignoring the requested key
// list. The returned map is shared with the fixture, so consumers keep it
// unchanged during assertions.
func (r *grokBaseURLSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return r.values, nil
}

// SetMultiple accepts a fixture batch write and leaves the saved map
// unchanged.
func (r *grokBaseURLSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

// GetAll returns the fixture's complete saved map by reference for settings
// reads. Consumers keep its entries stable while runtime settings resolve
// their configuration from this shared object.
func (r *grokBaseURLSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

// Delete accepts a fixture deletion and leaves the configured entries
// unchanged.
func (r *grokBaseURLSettingRepoStub) Delete(context.Context, string) error { return nil }

// TestSettingServiceResolveGrokBaseURLHonorsModeAndExplicitPins configures a
// regional runtime default, then supplies official and regional provider URLs.
// An unpinned OAuth target uses the default, while each configured endpoint
// stays selected.
func TestSettingServiceResolveGrokBaseURLHonorsModeAndExplicitPins(t *testing.T) {
	repo := &grokBaseURLSettingRepoStub{values: map[string]string{gateway.SettingKeyGrokDefaultBaseURLMode: "us-west-2"}}
	svc := gateway.NewRuntimeSettings(repo, settings.ErrSettingNotFound, nil)
	provider := &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{}}
	require.Equal(t, xai.DefaultUSWest2BaseURL, provideradapter.GrokProviderBaseURLOr(provider, GrokBaseURLForMode(svc.GetGrokDefaultBaseURLMode(context.Background()))))

	// A configured official endpoint remains pinned.
	provider.Credentials["base_url"] = xai.DefaultBaseURL
	require.Equal(t, xai.DefaultBaseURL, provideradapter.GrokProviderBaseURLOr(provider, GrokBaseURLForMode(svc.GetGrokDefaultBaseURLMode(context.Background()))))

	// A configured regional endpoint overrides the runtime default.
	provider.Credentials["base_url"] = xai.DefaultEUWest1BaseURL
	require.Equal(t, xai.DefaultEUWest1BaseURL, provideradapter.GrokProviderBaseURLOr(provider, GrokBaseURLForMode(svc.GetGrokDefaultBaseURLMode(context.Background()))))
}

// TestProviderGetGrokBaseURLOrPreservesCustomOAuthURLForPolicyValidation
// supplies a custom OAuth base URL and an official default. Resolution returns
// the custom URL verbatim, leaving its acceptance to the later URL policy
// check.
func TestProviderGetGrokBaseURLOrPreservesCustomOAuthURLForPolicyValidation(t *testing.T) {
	provider := &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{
		"base_url": "https://attacker.invalid/v1",
	}}
	require.Equal(t, "https://attacker.invalid/v1", provideradapter.GrokProviderBaseURLOr(provider, xai.DefaultCLIBaseURL))
}
