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

type grokBaseURLSettingRepoStub struct{ values map[string]string }

func TestGrokBaseURLForMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
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

func (r *grokBaseURLSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", fmt.Errorf("setting %s not found", key)
}

func (r *grokBaseURLSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	return nil, fmt.Errorf("unused")
}

func (r *grokBaseURLSettingRepoStub) Set(context.Context, string, string) error { return nil }

func (r *grokBaseURLSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return r.values, nil
}

func (r *grokBaseURLSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (r *grokBaseURLSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

func (r *grokBaseURLSettingRepoStub) Delete(context.Context, string) error { return nil }

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

func TestProviderGetGrokBaseURLOrPreservesCustomOAuthURLForPolicyValidation(t *testing.T) {
	provider := &providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{
		"base_url": "https://attacker.invalid/v1",
	}}
	require.Equal(t, "https://attacker.invalid/v1", provideradapter.GrokProviderBaseURLOr(provider, xai.DefaultCLIBaseURL))
}
