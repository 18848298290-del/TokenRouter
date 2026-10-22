package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_buffered_failure_scenario_test.go covers buffered failure callbacks in
// openai_chat_response_output.go, openai_messages_response_output.go, and
// openai_response_buffered_failure.go through their Responses readers.
// Each reader consumes the same failed SSE terminal
// so assertions compare protocol presentation with request state and health writes.

// TestOpenAIBufferedFailureProtocols reads failed terminals through both
// protocol callers. It protects response envelopes, commitment, health write
// counts, cyber token evidence, failover effects, and presentation-rule priority.
func TestOpenAIBufferedFailureProtocols(t *testing.T) {
	tests := []struct {
		name           string         // descriptive failure scenario name
		errorJSON      string         // upstream terminal error object
		credentials    map[string]any // provider policy configuration values
		matchStatus    int            // rule source status code
		matchKeyword   string         // rule source message keyword
		wantStatus     int            // downstream HTTP status code
		wantType       string         // shared expected error type
		wantMessage    string         // downstream error message text
		wantError      string         // returned error string text
		wantOpsKind    string         // recorded upstream event kind
		wantOpsStatus  int            // recorded upstream failure status
		wantHealth     int            // persistent health write count
		wantFailover   bool           // typed provider switch result
		wantRuleCommit bool           // rule explicitly commits response
		wantCyber      bool           // captured cyber policy evidence
	}{
		{
			name:          "default_permission_error",
			errorJSON:     `{"type":"permission_error","code":"forbidden","message":"access denied for this request"}`,
			credentials:   map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{}},
			wantStatus:    http.StatusBadGateway,
			wantMessage:   "access denied for this request",
			wantError:     "upstream response failed: access denied for this request",
			wantOpsKind:   "http_error",
			wantOpsStatus: http.StatusForbidden,
		},
		{
			name:           "presentation_rule",
			errorJSON:      `{"type":"permission_error","code":"forbidden","message":"access denied for this request"}`,
			credentials:    map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{}},
			matchStatus:    http.StatusForbidden,
			matchKeyword:   "access denied",
			wantStatus:     http.StatusTeapot,
			wantType:       "upstream_error",
			wantMessage:    "Configured rejection",
			wantError:      "upstream response failed (passthrough): Configured rejection",
			wantOpsKind:    "http_error",
			wantOpsStatus:  http.StatusForbidden,
			wantRuleCommit: true,
		},
		{
			name:      "generic_rejection_precedes_rule",
			errorJSON: `{"type":"server_error","code":"server_error","message":"upstream failed"}`,
			credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
			},
			matchStatus:  http.StatusBadGateway,
			matchKeyword: "upstream failed",
			wantStatus:   http.StatusInternalServerError,
			wantType:     "api_error",
			wantMessage:  "Upstream gateway error",
			wantError:    "upstream response failed: status=502 (not in custom error codes)",
		},
		{
			name:      "failover_consumes_applied_health",
			errorJSON: `{"type":"authentication_error","code":"invalid_api_key","message":"credential rejected"}`,
			credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
			},
			matchStatus:   http.StatusUnauthorized,
			matchKeyword:  "credential rejected",
			wantOpsKind:   "failover",
			wantOpsStatus: http.StatusUnauthorized,
			wantHealth:    1,
			wantFailover:  true,
		},
		{
			name:      "cyber_precedes_health_and_rule",
			errorJSON: `{"type":"invalid_request_error","code":"cyber_policy","message":"Cyber request blocked"}`,
			credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusBadRequest)},
			},
			matchStatus:  http.StatusBadRequest,
			matchKeyword: "Cyber request blocked",
			wantStatus:   http.StatusBadRequest,
			wantType:     "invalid_request_error",
			wantMessage:  "Cyber request blocked",
			wantError:    "openai cyber_policy: Cyber request blocked",
			wantCyber:    true,
		},
		{
			name:        "cyber_default_message",
			errorJSON:   `{"type":"invalid_request_error","code":"cyber_policy","message":""}`,
			wantStatus:  http.StatusBadRequest,
			wantType:    "invalid_request_error",
			wantMessage: "Request blocked by upstream cyber-security policy",
			wantError:   "openai cyber_policy: ",
			wantCyber:   true,
		},
	}
	for _, protocol := range []string{"chat", "messages"} {
		for _, tt := range tests {
			t.Run(protocol+"/"+tt.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, nil)
				store := &testkit.ErrorPolicyStore{}
				output := &OpenAIResponseOutput{
					Health: &provideradapter.OpenAIResponseHealth{
						Health:  newHTTPHealthFixture(store, nil, nil, providercore.HealthOptions{}, nil),
						Runtime: providercore.NewRuntimeBlockState(time.Now),
					},
				}
				provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           920,
					Name:         "buffered-failure-provider",
					Platform:     capability.PlatformOpenAI,
					Type:         capability.ProviderTypeAPIKey,
					Credentials:  tt.credentials,
				}}
				if tt.matchStatus != 0 {
					status, message := http.StatusTeapot, "Configured rejection"
					BindErrorPassthroughService(c, testkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{{
						ID:            1,
						Enabled:       true,
						Platforms:     []string{capability.PlatformOpenAI},
						MatchMode:     errorpolicy.MatchModeAll,
						ErrorCodes:    []int{tt.matchStatus},
						Keywords:      []string{tt.matchKeyword},
						ResponseCode:  &status,
						CustomMessage: &message,
					}}))
				}
				payload := `{"type":"response.failed","response":{"id":"resp_buffered_failure",` +
					`"object":"response","status":"failed","model":"gpt-5.4","error":` + tt.errorJSON +
					`,"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}}}`
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"X-Request-Id": []string{"req-buffered-failure"}},
					Body:       io.NopCloser(strings.NewReader("data: " + payload + "\n\n")),
				}
				sink := upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer})
				var result *openai.CompatResponseResult
				var err error
				if protocol == "chat" {
					result, err = openai.ReadChatBuffered(
						resp, sink,
						output.ChatOptions(c, provider, resp, "client-model", "gpt-5.4", "gpt-5.4"),
						"client-model", "gpt-5.4", time.Now(),
					)
				} else {
					result, err = openai.ReadMessagesBuffered(
						resp, sink,
						output.MessagesOptions(c, provider, resp, "client-model", "gpt-5.4", "gpt-5.4"),
						"client-model", "gpt-5.4", time.Now(),
					)
				}
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, tt.wantHealth, store.SetErrCalls)
				require.Zero(t, store.TempCalls)
				require.Empty(t, store.ModelRateLimitCalls)
				var failover *forward.UpstreamFailoverError
				if tt.wantFailover {
					require.ErrorAs(t, err, &failover)
					require.Equal(t, http.StatusUnauthorized, failover.StatusCode)
					require.False(t, failover.IsCredentialFailure())
					require.False(t, failover.RetryableOnSameProvider)
					require.Equal(t, "req-buffered-failure", http.Header(failover.ResponseHeaders).Get("x-request-id"))
					require.Empty(t, recorder.Body.String())
					require.False(t, c.Writer.Written())
					require.False(t, IsResponseCommitted(c))
					_, _, pending := consumeOpenAIResponseFailureEffects(c)
					require.False(t, pending)
				} else {
					require.NotErrorAs(t, err, &failover)
					require.EqualError(t, err, tt.wantError)
					require.Equal(t, tt.wantStatus, recorder.Code)
					require.True(t, c.Writer.Written())
					require.Equal(t, protocol == "chat" || tt.wantRuleCommit, IsResponseCommitted(c))
					kind := tt.wantType
					if kind == "" {
						kind = "upstream_error"
						if protocol == "messages" {
							kind = "api_error"
						}
					}
					body := `{"error":{"type":"` + kind + `","message":"` + tt.wantMessage + `"}`
					if protocol == "messages" {
						body += `,"type":"error"`
					}
					require.JSONEq(t, body+"}", recorder.Body.String())
					require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
				}
				value, _ := c.Get(OpsUpstreamErrorsKey)
				events, _ := value.([]*ops.OpsUpstreamErrorEvent)
				if tt.wantOpsKind == "" {
					require.Empty(t, events)
				} else {
					require.Len(t, events, 1)
					require.Equal(t, tt.wantOpsKind, events[0].Kind)
					require.Equal(t, tt.wantOpsStatus, events[0].UpstreamStatusCode)
					require.Equal(t, provider.Record.ID, events[0].ProviderID)
					require.Equal(t, "req-buffered-failure", events[0].UpstreamRequestID)
					require.False(t, events[0].Passthrough)
				}
				mark := GetOpsCyberPolicy(c)
				if tt.wantCyber {
					require.NotNil(t, mark)
					require.Equal(t, "cyber_policy", mark.Code)
					require.Equal(t, http.StatusOK, mark.UpstreamStatus)
					require.EqualValues(t, 17, mark.UpstreamInTok)
					require.EqualValues(t, 9, mark.UpstreamOutTok)
					require.Contains(t, mark.Body, `"type":"response.failed"`)
				} else {
					require.Nil(t, mark)
				}
			})
		}
	}
}
