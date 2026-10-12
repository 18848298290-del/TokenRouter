package provider

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// stubChatGPTHeadersRepo implements GetByID for credential-header tests.
// Other storage methods are unused.
type stubChatGPTHeadersRepo struct {
	ExecutionProviderStore

	byID map[int64]*ExecutionProvider
}

func (r *stubChatGPTHeadersRepo) GetByID(_ context.Context, id int64) (*ExecutionProvider, error) {
	return r.byID[id], nil
}

func TestResolveAndSetOpenAIChatGPTAccountHeaders(t *testing.T) {
	ctx := context.Background()
	pid := int64(100)

	parentCreds := map[string]any{"chatgpt_account_id": "org-parent"}
	parent := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 100,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Credentials: parentCreds,
		},
	}
	repo := &stubChatGPTHeadersRepo{byID: map[int64]*ExecutionProvider{100: parent}}

	t.Run("shadow_resolves_to_parent_org", func(t *testing.T) {
		shadow := &ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 200,
				ParentProviderID: &pid,
				Platform:         capability.PlatformOpenAI,
				Type:             capability.ProviderTypeOAuth,
			},
		}
		headers := make(http.Header)
		err := CredentialChatGPTHeaders(ctx, repo, headers, shadow)
		require.NoError(t, err)
		require.Equal(t, "org-parent", headers.Get("chatgpt-account-id"),
			"影子提供商应透传母提供商的 chatgpt-account-id")
	})

	t.Run("normal_provider_passthrough", func(t *testing.T) {
		ownCreds := map[string]any{"chatgpt_account_id": "org-own"}
		normal := &ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 300,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeOAuth,
				Credentials: ownCreds,
			},
		}
		headers := make(http.Header)
		err := CredentialChatGPTHeaders(ctx, repo, headers, normal)
		require.NoError(t, err)
		require.Equal(t, "org-own", headers.Get("chatgpt-account-id"),
			"普通提供商应透传自身的 chatgpt-account-id")
	})
}

func TestResolveCredentialProvider(t *testing.T) {
	ctx := context.Background()
	pid := int64(100)

	// An ordinary provider resolves to itself.
	parent := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 100, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}}
	repo := newStubCredRepo(parent)
	got, err := CredentialProvider(ctx, repo, parent)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.Record.ID)

	// A shadow resolves to its OpenAI OAuth parent.
	shadow := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 200, ParentProviderID: &pid, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	got, err = CredentialProvider(ctx, repo, shadow)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.Record.ID)

	// An API Key parent is invalid for an OAuth shadow.
	badRepo := newStubCredRepo(&ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 100, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}})
	_, err = CredentialProvider(ctx, badRepo, shadow)
	require.Error(t, err)
}
