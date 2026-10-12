package provider_test

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

// CN legacy input becomes a unified native selection during saving. These
// scenarios exercise creation, ordinary editing, credential replacement, and
// bulk validation through the administrative service, with stored fields and
// recorded bulk updates checked for the supplied platform combinations. The
// save paths exercise create_record.go, admin_edit.go, protocol_legacy.go, and
// protocols.go together.

// cnProviderProtocolCases lists explicit platform/mode/legacy-variant combinations
// for save scenarios, independently of the validator's own capability lookup.
var cnProviderProtocolCases = []struct {
	platform  string   // CN provider platform under test
	mode      string   // configured provider billing mode
	protocols []string // accepted legacy routing variants
}{
	{capability.PlatformDeepseek, provider.ProviderModePayG, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic, provider.APIProtocolResponses}},
	{capability.PlatformKimi, provider.ProviderModePayG, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic, provider.APIProtocolResponses}},
	{capability.PlatformKimi, provider.ProviderModeCoding, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic, provider.APIProtocolResponses}},
	{capability.PlatformZhipu, provider.ProviderModePayG, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic}},
	{capability.PlatformZhipu, provider.ProviderModeCoding, []string{provider.APIProtocolAdaptive, provider.APIProtocolChatCompletions, provider.APIProtocolAnthropic}},
}

// TestCNProviderProviderProtocolPersistence runs supported legacy variants through
// creation, editing, and bulk credential updates. It checks stored canonical
// protocols, fixed endpoint migration, ordinary-field retention, and the recorded
// bulk protocol patch for each supplied platform/mode combination.
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

				// Editing an ordinary field revalidates the existing protocol selection and
				// keeps its endpoint slots available for the subsequent credential updates.
				updated, err := svc.UpdateProvider(ctx, created.ID, &provider.UpdateProviderInput{Name: "已编辑"})
				require.NoError(t, err)
				require.Equal(t, "已编辑", repo.providers[created.ID].Name)
				require.Equal(t, wantCredentials, updated.Credentials)

				// Switch to a legacy Chat record before applying new credentials so edit and
				// bulk-update normalization derive selections from incoming protocol fields.
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

// TestCNProviderLegacyCredentialDefaults compares editing a record with omitted
// legacy fields against creating a new record. It checks payg/Chat defaults while
// ordinary edits keep legacy keys absent and creation stores the provider mode.
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

// TestProtocolSaveEntrypointsAndBulkRejectBeforeWrite creates and edits a unified
// Kimi selection, then rotates credentials and attempts a cross-platform bulk
// Responses patch. It checks the three-protocol count and endpoint retention,
// followed by a 400 error code before any bulk update is recorded.
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
