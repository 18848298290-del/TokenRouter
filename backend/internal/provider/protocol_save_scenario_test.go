package provider_test

// These scenarios cover protocol validation during provider creation, editing, and bulk updates.

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// cnProviderProtocolCases lists supported combinations independently of the validator under test.
var cnProviderProtocolCases = []struct {
	platform  string
	mode      string
	protocols []string
}{
	{capability.PlatformDeepseek, provider.ProviderModePayG, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic, provider.APIProtocolResponses}},
	{capability.PlatformKimi, provider.ProviderModePayG, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic, provider.APIProtocolResponses}},
	{capability.PlatformKimi, provider.ProviderModeCoding, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic, provider.APIProtocolResponses}},
	{capability.PlatformZhipu, provider.ProviderModePayG, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic}},
	{capability.PlatformZhipu, provider.ProviderModeCoding, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic}},
}

// TestCNProviderProviderProtocolPersistence covers create, edit, and bulk credential-update entrypoints.
func TestCNProviderProviderProtocolPersistence(t *testing.T) {
	t.Parallel()
	for _, tc := range cnProviderProtocolCases {
		for _, protocol := range tc.protocols {
			t.Run(tc.platform+"/"+tc.mode+"/"+protocol, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				repo := &providerServiceTestRepo{}
				svc := newProviderEditorForTest(repo)
				credentials := cnProviderTestCredentials(tc.platform, tc.mode, protocol)
				wantCredentials := cnProviderTestCredentials(tc.platform, tc.mode, protocol)
				delete(wantCredentials, "api_protocol")
				wantProtocols := []protocolcore.ProtocolID{protocolcore.ProtocolOpenAIChatCompletions}
				switch protocol {
				case provider.APIProtocolAnthropic:
					wantProtocols = []protocolcore.ProtocolID{protocolcore.ProtocolAnthropicMessages}
				case provider.APIProtocolResponses:
					wantProtocols = []protocolcore.ProtocolID{protocolcore.ProtocolOpenAIResponses}
				case provider.APIProtocolAdaptive:
					wantProtocols = []protocolcore.ProtocolID{protocolcore.ProtocolAnthropicMessages, protocolcore.ProtocolOpenAIResponses, protocolcore.ProtocolOpenAIChatCompletions}
					if tc.platform == capability.PlatformZhipu {
						wantProtocols = []protocolcore.ProtocolID{protocolcore.ProtocolAnthropicMessages, protocolcore.ProtocolOpenAIChatCompletions}
					}
				}
				wantCredentials[provider.UpstreamProtocolsKey] = wantProtocols
				if protocol != provider.APIProtocolAdaptive {
					wantCredentials["api_base_urls"] = map[string]any{protocol: "https://relay.example.test/v1"}
				}

				created, err := svc.CreateProvider(ctx, &provider.CreateProviderInput{
					Name: "国产平台提供商", Platform: tc.platform, Type: capability.ProviderTypeAPIKey,
					Credentials: credentials,
				})
				require.NoError(t, err)
				require.Equal(t, wantProtocols, created.UpstreamProtocols())
				require.Equal(t, wantCredentials, repo.providers[created.ID].Credentials)

				// Editing an ordinary field revalidates the provider, including an existing adaptive protocol selection.
				updated, err := svc.UpdateProvider(ctx, created.ID, &provider.UpdateProviderInput{Name: "已编辑"})
				require.NoError(t, err)
				require.Equal(t, "已编辑", repo.providers[created.ID].Name)
				require.Equal(t, wantCredentials, updated.Credentials)

				// Switch from legacy Chat configuration to exercise credential merging in edit and bulk-update paths.
				repo.providers[created.ID].Credentials = cnProviderTestCredentials(tc.platform, tc.mode, provider.APIProtocolChatCompletions)
				updated, err = svc.UpdateProvider(ctx, created.ID, &provider.UpdateProviderInput{Credentials: credentials})
				require.NoError(t, err)
				require.Equal(t, wantCredentials, updated.Credentials)
				repo.providers[created.ID].Credentials = cnProviderTestCredentials(tc.platform, tc.mode, provider.APIProtocolChatCompletions)
				result, err := svc.BulkUpdateProviders(ctx, &provider.BulkUpdateProvidersInput{
					ProviderIDs: []int64{created.ID}, Credentials: credentials,
				})
				require.NoError(t, err)
				require.Equal(t, 1, result.Success)
				require.Len(t, repo.bulkUpdates, 1)
				require.Equal(t, wantProtocols, repo.bulkUpdates[0].ProtocolUpdates[created.ID][provider.UpstreamProtocolsKey])
			})
		}
	}
}

// TestCNProviderLegacyCredentialDefaults checks read defaults and ordinary edits that leave missing legacy
// fields absent.
func TestCNProviderLegacyCredentialDefaults(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{capability.PlatformDeepseek, capability.PlatformKimi, capability.PlatformZhipu} {
		t.Run(platform, func(t *testing.T) {
			ctx := context.Background()
			repo := &providerServiceTestRepo{providers: map[int64]*provider.Record{
				1: {ID: 1, Platform: platform, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}},
			}}
			svc := newProviderEditorForTest(repo)
			updated, err := svc.UpdateProvider(ctx, 1, &provider.UpdateProviderInput{Name: "历史提供商"})
			require.NoError(t, err)
			require.Equal(t, provider.ProviderModePayG, updated.GetProviderMode())
			require.Equal(t, []protocolcore.ProtocolID{protocolcore.ProtocolOpenAIChatCompletions}, updated.UpstreamProtocols())
			require.NotContains(t, updated.Credentials, "provider_mode")
			require.NotContains(t, updated.Credentials, "api_protocol")
			created, err := svc.CreateProvider(ctx, &provider.CreateProviderInput{
				Name: "缺省提供商", Platform: platform, Type: capability.ProviderTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test"},
			})
			require.NoError(t, err)
			require.Equal(t, provider.ProviderModePayG, created.Credentials["provider_mode"])
			require.Equal(t, []protocolcore.ProtocolID{protocolcore.ProtocolOpenAIChatCompletions}, created.UpstreamProtocols())
		})
	}
}

func TestProtocolSaveEntrypointsAndBulkRejectBeforeWrite(t *testing.T) {
	repo := &providerServiceTestRepo{providers: map[int64]*provider.Record{}}
	svc := newProviderEditorForTest(repo)
	created, err := svc.CreateProvider(context.Background(), &provider.CreateProviderInput{Name: "native", Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test", provider.UpstreamProtocolsKey: []string{"anthropic_messages", "openai_responses", "openai_chat_completions"}, "api_base_urls": map[string]any{"responses": "https://relay.example/v1"}}})
	require.NoError(t, err)
	require.NotContains(t, created.Credentials, "api_protocol")
	require.Len(t, created.UpstreamProtocols(), 3)
	updated, err := svc.UpdateProvider(context.Background(), created.ID, &provider.UpdateProviderInput{Name: "renamed"})
	require.NoError(t, err)
	require.Len(t, updated.UpstreamProtocols(), 3)
	require.Equal(t, map[string]any{"responses": "https://relay.example/v1"}, updated.Credentials["api_base_urls"])
	rotated, err := svc.UpdateProvider(context.Background(), created.ID, &provider.UpdateProviderInput{Credentials: map[string]any{"api_key": "rotated"}})
	require.NoError(t, err)
	require.Len(t, rotated.UpstreamProtocols(), 3)
	require.Equal(t, map[string]any{"responses": "https://relay.example/v1"}, rotated.Credentials["api_base_urls"])
	repo.providers[99] = &provider.Record{ID: 99, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{provider.UpstreamProtocolsKey: []string{"openai_chat_completions"}}}
	_, err = svc.BulkUpdateProviders(context.Background(), &provider.BulkUpdateProvidersInput{ProviderIDs: []int64{created.ID, 99}, Credentials: map[string]any{provider.UpstreamProtocolsKey: []string{"openai_responses"}}})
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	require.Empty(t, repo.bulkUpdates)
}
