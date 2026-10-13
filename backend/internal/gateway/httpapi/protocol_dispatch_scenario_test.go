package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Fixed provider protocols can conflict with retained route/probe metadata, and
// configured dispatch must still choose the matching upstream URL and payload.
// Fixture transports stop after capture or return known output, allowing
// protocol conversion, client model identity and returned billing attribution
// to be checked across inbound forms. Token adapters additionally expose their
// funding, planning, failover and release order through serial targets.
//
// Dispatch scenarios cross openai_responses_execution.go,
// openai_protocol_execution_chat.go and openai_protocol_execution_messages.go.
// Token ordering uses openai_count_tokens.go and input_tokens.go; error-language
// cases connect anthropic_failover_error.go with text_errors.go. These paths
// share the selected provider while retaining their distinct inbound preparation
// and returned result fields.

// cnProtocolIngressCase pairs one inbound representation with its matching
// fixture forward call.
type cnProtocolIngressCase struct {
	// name, path, body and forward describe one inbound protocol together. Tests
	// pass that same body to its matching entry callback, then inspect the
	// configured provider destination independently of inbound shape.
	name string // named inbound protocol case
	path string // inbound fixture request path
	body []byte // encoded inbound request bytes
	// matching inbound forward callback
	forward func(*OpenAIResponsesExecutor, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) error
}

// tokenExecutionContract records selection and HTTP exchange order through the
// real token adapters.
type tokenExecutionContract struct {
	OpenAITokenExecution            // unconfigured fixture token operations
	t                    *testing.T // owning fixture assertion context
	events               []string   // ordered token execution phases
	selections           int        // numbered provider selection count
}

// tokenContractTarget identifies one numbered provider for serial
// token-execution fixture exchanges.
type tokenContractTarget struct {
	f  *tokenExecutionContract // shared token phase recorder
	id int64                   // selected fixture provider number
}

// cnProtocolIngressCases constructs Chat, Messages and Responses inputs for
// configured-protocol assertions. Each case pairs its path/body with the
// matching forward call, letting tests compare the same provider policy across
// distinct inbound representations.
func cnProtocolIngressCases() []cnProtocolIngressCase {
	return []cnProtocolIngressCase{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			forward: func(svc *OpenAIResponsesExecutor, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) error {
				_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
				return err
			},
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: []byte(`{"model":"deepseek-chat","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`),
			forward: func(svc *OpenAIResponsesExecutor, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) error {
				_, err := svc.Text.Messages(context.Background(), c, provider, body, "", "")
				return err
			},
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: []byte(`{"model":"deepseek-chat","input":"hello","stream":false}`),
			forward: func(svc *OpenAIResponsesExecutor, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) error {
				_, err := svc.Forward(context.Background(), c, provider, body)
				return err
			},
		},
	}
}

// TestFixedCNChatProtocolOverridesStaleResponsesMode supplies a fixed Deepseek
// Chat protocol with stale force-Responses metadata. Every inbound form must
// dispatch to the configured Chat endpoint before the injected transport
// failure returns.
func TestFixedCNChatProtocolOverridesStaleResponsesMode(t *testing.T) {
	for _, tc := range cnProtocolIngressCases() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
			svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
			provider := adaptiveProtocolTestProvider(capability.PlatformDeepseek, nil)
			provider.Record.Credentials["api_protocol"] = providercore.APIProtocolChatCompletions
			provider.Record.Credentials["base_url"] = "http://chat.example"
			provider.Record.Extra = map[string]any{
				providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceResponses),
			}

			err := tc.forward(svc, adaptiveProtocolTestContext(tc.path, tc.body), provider, tc.body)

			require.Error(t, err)
			require.Equal(t, "http://chat.example/v1/chat/completions", upstream.lastReq.URL.String())
		})
	}
}

// TestFixedCNResponsesProtocolOverridesStaleChatMode supplies a fixed Deepseek
// Responses protocol with stale force-Chat metadata. Every inbound form must
// select the configured bare /responses endpoint despite the conflicting old
// route mode.
func TestFixedCNResponsesProtocolOverridesStaleChatMode(t *testing.T) {
	for _, tc := range cnProtocolIngressCases() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
			svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
			provider := adaptiveProtocolTestProvider(capability.PlatformDeepseek, nil)
			provider.Record.Credentials["api_protocol"] = providercore.APIProtocolResponses
			provider.Record.Credentials["base_url"] = "http://responses.example"
			provider.Record.Extra = map[string]any{
				providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
			}

			err := tc.forward(svc, adaptiveProtocolTestContext(tc.path, tc.body), provider, tc.body)

			require.Error(t, err)
			require.Equal(t, "http://responses.example/responses", upstream.lastReq.URL.String())
		})
	}
}

// TestGatewayErrorsUseEnglish supplies English and Chinese request language
// with both stream-intent flags. Built-in overload type/message and the
// five-hour billing-limit message must remain their English gateway forms.
func TestGatewayErrorsUseEnglish(t *testing.T) {
	for _, language := range []string{"en", "zh-Hans"} {
		for _, stream := range []bool{false, true} {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(locale.WithLanguage(context.Background(), language))
			c.Request.Header.Set("Accept-Language", language)
			WriteAnthropicFailover(c, &forwardcore.UpstreamFailoverError{StatusCode: 529}, "anthropic", stream, nil, func([]byte) bool { return false }, "")
			require.Contains(t, recorder.Body.String(), `"type":"overloaded_error"`)
			require.Contains(t, recorder.Body.String(), "Upstream service overloaded")
		}
	}
	status, _, message, _ := BillingErrorDetails(billing.ErrAPIKeyRateLimit5hExceeded)
	require.Equal(t, 429, status)
	require.Equal(t, "The API key five-hour limit has been reached", message)
}

// TestOpenAIAdministratorProtocolOverridesAllLegacyProbeState combines
// administrator routing modes with false, true and invalid legacy probe values.
// Selected upstream URLs and recorded actual endpoints must follow the
// configured mode across three inbound protocols.
func TestOpenAIAdministratorProtocolOverridesAllLegacyProbeState(t *testing.T) {
	for _, mode := range []string{"preserve_client_protocol", "force_responses", "force_chat_completions"} {
		for _, legacy := range []any{false, true, "invalid"} {
			for _, inbound := range []string{"responses", "chat/completions", "messages"} {
				t.Run(fmt.Sprintf("%s/%v/%s", mode, legacy, inbound), func(t *testing.T) {
					body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":false}`)
					if inbound == "responses" {
						body = []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+inbound, bytes.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")
					// Keep upstream output deterministic after request capture, so administrator
					// route selection can be asserted independently of successful response parsing or stream conversion.
					upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
						StatusCode: http.StatusBadRequest,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"test endpoint reached"}}`)),
					}}
					svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
					provider := rawChatCompletionsTestProvider()
					provider.Record.Extra = map[string]any{"openai_text_route_mode": mode, "openai_responses_supported": legacy, "openai_responses_probe_status": "unsupported"}
					var err error
					switch inbound {
					case "responses":
						_, err = svc.Forward(context.Background(), c, provider, body)
					case "chat/completions":
						_, err = svc.Text.Chat(context.Background(), c, provider, body, "", "")
					case "messages":
						_, err = svc.Text.Messages(context.Background(), c, provider, body, "", "")
					}
					require.Error(t, err)
					require.NotNil(t, upstream.lastReq)
					want := "/v1/responses"
					if mode == "force_chat_completions" || (mode == "preserve_client_protocol" && inbound == "chat/completions") {
						want = "/v1/chat/completions"
					}
					require.Equal(t, want, upstream.lastReq.URL.Path)
					require.Equal(t, want, GetActualOpenAIUpstreamEndpoint(c))
				})
			}
		}
	}
}

// TestOpenAIAgentIdentityCompatRoutesRecoverInvalidTaskOnce returns
// invalid_task_id before and after registration for Chat and Messages.
// Forwarding must perform one registration, retain the new task credential and
// stop after the second upstream request.
func TestOpenAIAgentIdentityCompatRoutesRecoverInvalidTaskOnce(t *testing.T) {
	tests := []struct {
		name string // named inbound recovery case
		path string // inbound fixture request path
		body []byte // encoded inbound request bytes
		// call invokes the matching Chat or Messages route. Each scenario keeps name,
		// path and body paired with that callback while registration and the repeated
		// upstream denial run.
		call func(*OpenAIResponsesExecutor, context.Context, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) (*forwardcore.OpenAIResult, error)
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"hi"}]}`),
			call: func(s *OpenAIResponsesExecutor, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.OpenAIResult, error) {
				return s.Text.Chat(ctx, c, provider, body, "", "gpt-5.4")
			},
		},
		{
			name: "anthropic messages",
			path: "/v1/messages",
			body: []byte(`{"model":"gpt-5.4","stream":false,"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`),
			call: func(s *OpenAIResponsesExecutor, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.OpenAIResult, error) {
				return s.Text.Messages(ctx, c, provider, body, "", "gpt-5.4")
			},
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: int64(40 + index),
					Name:        "agent-identity-compat",
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeOAuth,
					Status:      billing.StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Credentials: map[string]any{
						"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
						"agent_runtime_id":   key.RuntimeID,
						"agent_private_key":  privateKey,
						"task_id":            "task-compat-old",
						"chatgpt_account_id": "provider-compat-recovery",
					},
				},
			}
			repo := &agentIdentityForwardRepo{provider: provider}
			registerCalls := 0
			registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				registerCalls++
				_, _ = io.WriteString(w, `{"task_id":"task-compat-new"}`)
			}))
			// Keep registration available through this forward attempt, then close the
			// fixture listener when the serial scenario finishes its retry and
			// credential assertions.
			defer registerServer.Close()

			// Repeat invalid_task_id after successful registration so the executor must
			// expose the second denial rather than starting an unbounded
			// registration/retry cycle.
			upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
				{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
				{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
			}}
			svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, providers: repo, registerTaskURL: registerServer.URL, transport: upstream})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body))

			_, err := tt.call(svc, context.Background(), c, provider, tt.body)
			require.Error(t, err)
			require.Equal(t, 1, registerCalls)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "task-compat-new", provider.View().GetCredential("task_id"))
		})
	}
}

// TestOpenAIGatewayService_Forward_TextResponsesBillingModelMatchesChatCompletions
// maps gpt-5.4 to gpt-5.5 for native Responses and Chat conversion. Both
// returned results must expose the same mapped BillingModel for later
// accounting.
func TestOpenAIGatewayService_Forward_TextResponsesBillingModelMatchesChatCompletions(t *testing.T) {
	cfg := &responsesFixtureOptions{}
	cfg.Request.URLPolicy.Enabled = false
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5,
			Name:        "openai-apikey",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":       "sk-test",
				"base_url":      "https://example.com",
				"model_mapping": map[string]any{"gpt-5.4": "gpt-5.5"},
			},
			Extra: map[string]any{"use_responses_api": true},
		},
	}

	responsesUpstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_responses_mapped_billing"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"resp_native","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}`,
			)),
		},
	}
	responsesSvc := newResponsesFixture(responsesFixtureInputs{options: cfg, transport: responsesUpstream})
	responsesRecorder := httptest.NewRecorder()
	responsesCtx, _ := gin.CreateTestContext(responsesRecorder)
	responsesCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	SetOpenAIClientTransport(responsesCtx, OpenAIClientTransportHTTP)
	responsesResult, err := responsesSvc.Forward(context.Background(), responsesCtx, provider, []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, responsesResult)

	chatUpstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_mapped_billing"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"id":"resp_chat","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}}` + "\n\n",
			)),
		},
	}
	chatSvc := newResponsesFixture(responsesFixtureInputs{options: cfg, transport: chatUpstream})
	chatRecorder := httptest.NewRecorder()
	chatCtx, _ := gin.CreateTestContext(chatRecorder)
	chatCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	chatResult, err := chatSvc.Text.Chat(context.Background(), chatCtx, provider, []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"hello"}]}`), "", "")
	require.NoError(t, err)
	require.NotNil(t, chatResult)

	require.Equal(t, chatResult.BillingModel, responsesResult.BillingModel)
	require.Equal(t, "gpt-5.5", responsesResult.BillingModel)
	require.Equal(t, "gpt-5.5", chatResult.BillingModel)
}

// TestOpenAIGatewayEntrypointsRejectUltraBeforeUpstream supplies unsupported
// ultra effort through three inbound representations. Each entry must return a
// nil result and local 400 error before requiring a configured upstream
// transport.
func TestOpenAIGatewayEntrypointsRejectUltraBeforeUpstream(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:  capability.ProviderTypeAPIKey,
			Extra: map[string]any{"openai_text_route_mode": "force_chat_completions"},
		},
	}

	t.Run("Responses", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.6-sol","input":"hi","reasoning":{"effort":"ultra"}}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))

		result, err := svc.Forward(context.Background(), c, provider, body)
		require.ErrorContains(t, err, "not supported")
		require.Nil(t, result)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Chat Completions 原始透传", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.6-sol","messages":[],"reasoning_effort":"ultra"}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))

		result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
		require.ErrorContains(t, err, "not supported")
		require.Nil(t, result)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Anthropic Messages", func(t *testing.T) {
		body := []byte(`{"model":"gpt-5.6-sol","max_tokens":1024,"messages":[],"output_config":{"effort":"ultra"}}`)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))

		result, err := svc.Text.Messages(context.Background(), c, provider, body, "", "")
		require.ErrorContains(t, err, "not supported")
		require.Nil(t, result)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// TestRemovedGPT56AliasAcrossGatewayProtocols compares explicit mapping with an
// unmapped gpt-5.6 name for OAuth and API Key providers. Three HTTP paths must
// retain the expected outbound model, and the WS session-frame extraction plus
// model-policy helper must produce the same name.
func TestRemovedGPT56AliasAcrossGatewayProtocols(t *testing.T) {
	for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeAPIKey} {
		for _, explicit := range []bool{false, true} {
			for _, protocol := range []string{"responses", "chat", "messages"} {
				name := providerType + "/" + protocol
				if explicit {
					name += "/explicit_mapping"
				}
				t.Run(name, func(t *testing.T) {
					// Return a fixed captured-request error after model mapping, isolating the
					// outbound identity assertion from successful output conversion and billing completion.
					upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
						StatusCode: http.StatusBadRequest,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"request captured"}}`)),
					}}
					cfg := &responsesFixtureOptions{}
					cfg.Request.URLPolicy.Enabled = false
					svc := newResponsesFixture(responsesFixtureInputs{options: cfg, transport: upstream})
					provider := &gatewayprovider.ExecutionProvider{
						Record: providercore.Record{
							LoadLocation: time.LoadLocation, ID: 991, Name: "alias-regression", Platform: capability.PlatformOpenAI, Type: providerType, Concurrency: 1,
							Credentials: map[string]any{"api_key": "sk-test", "access_token": "oauth-test", "base_url": "https://example.com", "chatgpt_account_id": "test-provider"},
							Extra:       map[string]any{"use_responses_api": true},
						},
					}
					want := "gpt-5.6"
					if explicit {
						provider.Record.Credentials["model_mapping"] = map[string]any{"gpt-5.6": "gpt-5.6-sol"}
						want = "gpt-5.6-sol"
					}
					body := map[string]any{"model": "gpt-5.6", "stream": false, "max_tokens": 16}
					if protocol == "responses" {
						body["input"] = "hello"
					} else {
						body["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
					}
					payload, err := json.Marshal(body)
					require.NoError(t, err)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					switch protocol {
					case "responses":
						_, err = svc.Forward(context.Background(), c, provider, payload)
					case "chat":
						_, err = svc.Text.Chat(context.Background(), c, provider, payload, "", "")
					case "messages":
						_, err = svc.Text.Messages(context.Background(), c, provider, payload, "", "")
					}
					require.Error(t, err)
					require.NotEmpty(t, upstream.lastBody)
					require.Equal(t, want, gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, want, gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(gatewayprovider.ExecutionModelPolicy(provider).Mapped(gjson.GetBytes(payload, "model").String())))
					// Check the same mapped identity through WS session-frame parsing and
					// model policy; this assertion exercises prepared frame attribution
					// without opening a WebSocket exchange.
					session, err := json.Marshal(map[string]any{"type": "session.update", "session": body})
					require.NoError(t, err)
					require.Equal(t, want, gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(gatewayprovider.ExecutionModelPolicy(provider).Mapped(gatewayws.RequestModelFromSessionFrame(session))))
				})
			}
		}
	}
}

// PlanTokenRoute records planning order and constructs the fixture group-model
// route. It requires a Key and keeps its group identity available to the shared
// planner before the token adapter selects an execution target.
func (f *tokenExecutionContract) PlanTokenRoute(_ context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	f.events = append(f.events, "plan")
	return routing.Plan(routing.PlanInput{GroupID: key.GroupID, RequestedModel: model, GroupMapping: routing.GroupMappingResult{Mapped: true, MappedModel: "group-model"}})
}

// TokenSessionHash records session lookup and returns the fixed fixture
// token-session identity.
func (f *tokenExecutionContract) TokenSessionHash(*gin.Context, []byte) string {
	f.events = append(f.events, "session")
	return "token-session"
}

// CheckKey records funding order and requires empty platform with afterWait
// false. It returns successful fixture admission, allowing the test to inspect
// whether route planning occurs before or after that initial check.
func (f *tokenExecutionContract) CheckKey(_ context.Context, _ *apikey.APIKey, _ *billing.UserSubscription, platform string, afterWait bool) error {
	f.events = append(f.events, "funding")
	require.Empty(f.t, platform)
	require.False(f.t, afterWait)
	return nil
}

// ApplyUserPromptReplacementToBody returns supplied token fixture bytes without
// prompt rewriting.
func (*tokenExecutionContract) ApplyUserPromptReplacementToBody(_ context.Context, body []byte, _ string) []byte {
	return body
}

// SelectCount records selection and requires the fixed session, mapped model
// and unspecified platform. It returns provider one for direct Messages count
// forwarding, with the serial counter available for call-order assertions.
func (f *tokenExecutionContract) SelectCount(_ context.Context, _ *int64, hash, model, platform string) (OpenAICountTarget, error) {
	f.events = append(f.events, "select")
	f.selections++
	require.Equal(f.t, "token-session", hash)
	require.Equal(f.t, "group-model", model)
	require.Empty(f.t, platform)
	return tokenContractTarget{f: f, id: 1}, nil
}

// SelectInputTokens records numbered provider selection while checking client
// and mapped model identity separately. Second selection requires provider one
// excluded; each returned release callback records invocation, so tests can
// assert cleanup for both attempts.
func (f *tokenExecutionContract) SelectInputTokens(_ context.Context, _ *int64, hash, model, routingModel string, excluded map[int64]struct{}, platform string) (InputTokensSelection, error) {
	f.events = append(f.events, "select")
	f.selections++
	require.Equal(f.t, "token-session", hash)
	require.Equal(f.t, "client-model", model)
	require.Equal(f.t, "group-model", routingModel)
	require.Empty(f.t, platform)
	if f.selections == 2 {
		require.Contains(f.t, excluded, int64(1))
	}
	return InputTokensSelection{
		Target:  tokenContractTarget{f: f, id: int64(f.selections)},
		Release: func() { f.events = append(f.events, "release") },
	}, nil
}

// Snapshot returns this numbered token fixture target on the OpenAI platform.
func (t tokenContractTarget) Snapshot() providercore.ProviderSnapshot {
	return providercore.ProviderSnapshot{ID: t.id, Platform: "openai"}
}

// RetryLimit disables retry on the same token fixture provider.
func (tokenContractTarget) RetryLimit() int { return 0 }

// ForwardCount records exchange order after requiring auth latency observation
// and mapped body/model identity. It writes the fixed input_tokens 17 response,
// allowing the test to compare adapter preparation with native count output.
func (t tokenContractTarget) ForwardCount(_ context.Context, c *gin.Context, body []byte, model string) error {
	_, observed := c.Get(OpsAuthLatencyMsKey)
	require.True(t.f.t, observed)
	t.f.events = append(t.f.events, "forward")
	require.Equal(t.f.t, "group-model", model)
	require.Equal(t.f.t, "group-model", gjson.GetBytes(body, "model").String())
	c.JSON(http.StatusOK, gin.H{"input_tokens": 17})
	return nil
}

// ForwardInputTokens records exchange order and requires mapped body model
// identity. Provider one returns retryable 503; another target writes
// input_tokens 17, exposing provider exclusion and release order through the
// real input-token attempt loop.
func (t tokenContractTarget) ForwardInputTokens(_ context.Context, c *gin.Context, body []byte) error {
	t.f.events = append(t.f.events, "forward")
	require.Equal(t.f.t, "group-model", gjson.GetBytes(body, "model").String())
	// Fail the first provider before token output so the real adapter must exclude
	// it and run its release callback before another target succeeds.
	if t.id == 1 {
		return &forwardcore.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": 17})
	return nil
}

// TestOpenAITokensNativeHTTPContracts invokes Messages count and Responses
// input-token adapters with grouped access. It requires their distinct
// funding/planning order, two released attempts for Responses failover, and the
// same successful token-count JSON.
func TestOpenAITokensNativeHTTPContracts(t *testing.T) {
	for _, inputTokens := range []bool{false, true} {
		name := "messages-count"
		if inputTokens {
			name = "responses-input"
		}
		t.Run(name, func(t *testing.T) {
			fixture := &tokenExecutionContract{t: t}
			handler := NewOpenAITokensHandler(OpenAITokenOptions{MaxSwitches: 2}, OpenAITokenPorts{Execution: fixture, Funding: fixture, Diagnoser: routing.ModelAvailabilityDiagnoserFunc(unexpectedCountModelDiagnosis), ResolvedDiagnoser: routing.ModelAvailabilityDiagnoserFunc(unexpectedCountModelDiagnosis)}, fixture)
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"client-model","input":"hello","messages":[{"role":"user","content":"hello"}]}`))
			group := int64(7)
			c.Set(string(keyhttp.ContextKeyAPIKey), &apikey.APIKey{ID: 9, UserID: 7, GroupID: &group, Group: &routing.Group{
				ID: group, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolAnthropicMessages, protocol.ProtocolOpenAIResponses},
			}})
			c.Set(authctx.ContextKeyUser, authctx.AuthSubject{UserID: 7})
			if inputTokens {
				handler.ResponsesInputTokens(c)
				require.Equal(t, []string{"funding", "plan", "session", "select", "forward", "release", "select", "forward", "release"}, fixture.events)
			} else {
				handler.CountTokens(c)
				require.Equal(t, []string{"plan", "funding", "session", "select", "forward"}, fixture.events)
			}
			require.Equal(t, http.StatusOK, writer.Code)
			require.JSONEq(t, `{"input_tokens":17}`, writer.Body.String())
		})
	}
}

// TestProtocolForwardUsesConfiguredTarget pairs four provider platforms with
// supported inbound and configured target protocols. Outbound URL and payload
// shape must match the chosen target, while the selected provider Route stays
// unset after forwarding.
func TestProtocolForwardUsesConfiguredTarget(t *testing.T) {
	for _, platform := range []string{capability.PlatformDeepseek, capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformGrok} {
		for _, ingress := range cnProtocolIngressCases() {
			if platform == capability.PlatformGrok {
				ingress.body = bytes.ReplaceAll(ingress.body, []byte("deepseek-chat"), []byte("grok-4.5"))
			}
			source := protocol.ProtocolOpenAIResponses
			if ingress.name == "messages" {
				source = protocol.ProtocolAnthropicMessages
			}
			if ingress.name == "chat completions" {
				source = protocol.ProtocolOpenAIChatCompletions
			}
			a := adaptiveProtocolTestProvider(platform, map[string]any{providercore.APIProtocolChatCompletions: "http://chat.example", providercore.APIProtocolAnthropic: "http://anthropic.example", providercore.APIProtocolResponses: "http://responses.example"})
			for _, target := range a.View().NativeProtocolOptions() {
				if target != protocol.ProtocolAnthropicMessages && target != protocol.ProtocolOpenAIResponses && target != protocol.ProtocolOpenAIChatCompletions {
					continue
				}
				t.Run(platform+"/"+string(source)+"/"+string(target), func(t *testing.T) {
					provider := *a
					// Give the copied provider one configured target protocol while retaining
					// its base URL map, so fallback selection and body conversion are observed
					// for that specific choice.
					provider.Record.Credentials = map[string]any{"api_key": "test", "base_url": "http://grok.example/v1", providercore.UpstreamProtocolsKey: []protocol.ProtocolID{target}, "api_base_urls": a.Record.Credentials["api_base_urls"]}
					group := &routing.Group{ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{source: {target}}}
					ctx := requeststate.WithClientProtocol(requeststate.WithGroup(context.Background(), group), source)
					c := adaptiveProtocolTestContext(ingress.path, ingress.body)
					c.Request = c.Request.WithContext(ctx)
					upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
					svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
					var err error
					switch source {
					case protocol.ProtocolAnthropicMessages:
						_, err = svc.Text.Messages(ctx, c, &provider, ingress.body, "", "")
					case protocol.ProtocolOpenAIChatCompletions:
						_, err = svc.Text.Chat(ctx, c, &provider, ingress.body, "", "")
					default:
						_, err = svc.Forward(ctx, c, &provider, ingress.body)
					}
					require.Error(t, err)
					require.NotNil(t, upstream.lastReq, err)
					switch target {
					case protocol.ProtocolAnthropicMessages:
						require.Equal(t, "http://anthropic.example/v1/messages", upstream.lastReq.URL.String())
						require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
						require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
					case protocol.ProtocolOpenAIChatCompletions:
						endpoint := "http://chat.example/v1/chat/completions"
						if platform == capability.PlatformGrok {
							endpoint = "http://grok.example/v1/chat/completions"
						}
						require.Equal(t, endpoint, upstream.lastReq.URL.String())
						require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
						require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
					case protocol.ProtocolOpenAIResponses:
						endpoint := "http://responses.example/v1/responses"
						if platform == capability.PlatformGrok {
							endpoint = "http://grok.example/v1/responses"
						}
						if platform == capability.PlatformDeepseek {
							endpoint = "http://responses.example/responses"
						}
						require.Equal(t, endpoint, upstream.lastReq.URL.String())
						require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
						require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
					}
					require.Empty(t, provider.Route.Protocol())
				})
			}
		}
	}
}

// TestProtocolForwardConvertedResponsesRetainsWireContract converts Chat JSON,
// SSE and tool output into client Responses. It requires input/output token
// counts and stream intent. Each case checks converted text, function_call and
// lookup substrings, or terminal/DONE framing as applicable.
func TestProtocolForwardConvertedResponsesRetainsWireContract(t *testing.T) {
	for _, tc := range []struct {
		// name, body and response pair one input representation with its expected
		// upstream output, while stream selects response framing. Each scenario
		// checks returned input/output counts and the converted downstream shape.
		// name labels the case; body contains inbound JSON; response supplies
		// configured upstream output.
		name, body, response string
		stream               bool // requested client stream intent
	}{
		{"json", `{"model":"gpt-test","input":"hello","stream":false}`, `{"id":"chat-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":1}}}`, false},
		{"sse", `{"model":"gpt-test","input":"hello","stream":true}`, "data: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", true},
		{"tool", `{"model":"gpt-test","input":"hello","stream":false,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`, `{"id":"chat-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "http://upstream.example", providercore.UpstreamProtocolsKey: []string{"openai_chat_completions"}}}}
			group := &routing.Group{ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolOpenAIResponses: {protocol.ProtocolOpenAIChatCompletions}}}
			// Attach Responses client identity and an explicit Chat fallback before
			// forwarding, allowing the test to inspect conversion of configured upstream
			// output back into client framing.
			ctx := requeststate.WithClientProtocol(requeststate.WithGroup(context.Background(), group), protocol.ProtocolOpenAIResponses)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body)).WithContext(ctx)
			contentType := "application/json"
			if tc.stream {
				contentType = "text/event-stream"
			}
			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(tc.response))}}
			svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
			result, err := svc.Forward(ctx, c, provider, []byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Equal(t, tc.stream, result.Stream)
			if tc.stream {
				require.Contains(t, recorder.Body.String(), "response.completed")
				require.Contains(t, recorder.Body.String(), "data: [DONE]")
			} else if tc.name == "tool" {
				require.Contains(t, recorder.Body.String(), "function_call")
				require.Contains(t, recorder.Body.String(), "lookup")
			} else {
				require.Equal(t, "ok", gjson.Get(recorder.Body.String(), "output.0.content.0.text").String())
			}
		})
	}
}
