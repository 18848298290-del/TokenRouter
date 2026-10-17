package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// Selected count dispatch exposes a Messages count shape across local estimates
// and native Gemini counting, while incompatible auxiliary providers produce
// unsupported output. Recorder fixtures check the chosen HTTP request, returned
// count fields, and absence of network work in the exercised local paths.

// TestSelectedCountDispatchPreservesLocalAndUnsupportedPaths exercises Grok,
// Kimi, Qoder, and Antigravity with a recording transport. The asserted result
// must be a positive local count or unsupported status, and the transport must
// retain no request for those fixtures.
func TestSelectedCountDispatchPreservesLocalAndUnsupportedPaths(t *testing.T) {
	for _, platform := range []string{"grok", "qoder", "antigravity", "kimi"} {
		t.Run(platform, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
			body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello world"}]}`)
			parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), "anthropic")
			require.NoError(t, err)
			target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: platform, Type: "apikey"})
			transport := &auxiliaryHTTPRecorder{}
			auxiliary := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: transport})
			require.NoError(t, ForwardSelectedCountTokens(context.Background(), c, target, parsed, nil, auxiliary, nil))
			if platform == "qoder" || platform == "antigravity" {
				require.Equal(t, http.StatusNotFound, rec.Code)
			} else {
				require.Equal(t, http.StatusOK, rec.Code)
				require.Positive(t, gjson.Get(rec.Body.String(), "input_tokens").Int())
			}
			require.Nil(t, transport.lastReq)
		})
	}
}

// TestSelectedGeminiCountUsesNativeCountProtocol checks model mapping, system and tool input, and Anthropic-shaped
// counts.
// The request URL and generateContentRequest model must use the provider target;
// converted system and tool fields must exist, and totalTokens must return as
// the asserted input_tokens value.
func TestSelectedGeminiCountUsesNativeCountProtocol(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	body := []byte(`{"model":"draw-alias","system":"Be concise","messages":[{"role":"user","content":"hello world"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), "anthropic")
	require.NoError(t, err)
	target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: "gemini", Type: "apikey", Credentials: map[string]any{"api_key": "test-key", "model_mapping": map[string]any{"draw-alias": "gemini-2.5-flash"}}})
	transport := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"totalTokens":37}`))}}
	executor := &GeminiExecutor{Runtime: &googleforward.Gemini{Transport: transport, Options: googleforward.Options{Configured: true, ResponseReadLimit: 1 << 20}}}
	require.NoError(t, ForwardSelectedCountTokens(context.Background(), c, target, parsed, nil, nil, executor))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"input_tokens":37}`, rec.Body.String())
	require.Contains(t, transport.lastReq.URL.Path, "gemini-2.5-flash:countTokens")
	require.Equal(t, "models/gemini-2.5-flash", gjson.GetBytes(transport.lastBody, "generateContentRequest.model").String())
	require.True(t, gjson.GetBytes(transport.lastBody, "generateContentRequest.systemInstruction").Exists())
	require.True(t, gjson.GetBytes(transport.lastBody, "generateContentRequest.tools").Exists())
}

// TestResponsesInputTokensRejectsUnsupportedActualProvider chooses four incompatible
// provider platforms for Responses preflight. Each must receive unsupported
// HTTP output with no recorded transport request and no propagated execution error.
func TestResponsesInputTokensRejectsUnsupportedActualProvider(t *testing.T) {
	for _, platform := range []string{"anthropic", "gemini", "antigravity", "qoder"} {
		t.Run(platform, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
			transport := &auxiliaryHTTPRecorder{}
			executor := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: transport})
			target := gatewayadapter.NewExecutionProvider(&provider.Record{ID: 1, Platform: platform, Type: "apikey"})
			require.NoError(t, executor.ForwardResponsesInputTokens(context.Background(), c, target, []byte(`{"model":"public-model","input":"hello"}`)))
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.Nil(t, transport.lastReq)
		})
	}
}

// TestSelectedCountFinalizesObservedExchange routes counted responses through the
// actual selected-provider adapters and HTTP pool. Success, body failures, and
// handled HTTP errors must retain their selected identifier and terminal outcome.
func TestSelectedCountFinalizesObservedExchange(t *testing.T) {
	for _, platform := range []string{"openai", "anthropic", "anthropic_standard", "gemini"} {
		for _, mode := range []string{"success", "read_failure", "canceled", "invalid_count", "unsupported"} {
			if strings.HasPrefix(platform, "anthropic") && mode == "invalid_count" {
				continue
			}
			t.Run(platform+"/"+mode, func(t *testing.T) {
				var saved telemetry.RequestRecord
				ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "count"}, func(r telemetry.RequestRecord) { saved = r })
				payload := `{"input_tokens":42}`
				model := "claude-sonnet-4-5"
				if platform == "gemini" {
					payload = `{"totalTokens":42}`
					model = "gemini-2.5-flash"
				}
				status := 200
				if mode == "invalid_count" {
					payload = `{}`
				}
				if mode == "unsupported" {
					status = 404
					payload = `{"error":{"type":"not_found_error","message":"count_tokens input_tokens not found"}}`
				}
				response := &http.Response{
					StatusCode: status,
					Header: http.Header{
						"Content-Type":        {"application/json"},
						"X-Request-Id":        {"unselected"},
						"X-Client-Request-Id": {"selected"},
					},
					Body: io.NopCloser(strings.NewReader(payload)),
				}
				if mode == "read_failure" {
					response.Body = &openAIImagesReadErrorBody{err: io.ErrUnexpectedEOF}
				}
				if mode == "canceled" {
					response.Body = &openAIImagesReadErrorBody{err: context.Canceled}
				}
				transport := &diagnosticHTTPTransport{pool: httpclient.NewUpstreamPool(), responses: []*http.Response{response}}
				auxiliary := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: transport})
				messages := NewMessagesExecutor(messageforward.NewRuntime(messageforward.Dependencies{
					Credentials: &provider.MessageCredentialSource{},
					Transport:   transport,
					Search:      gatewayadapter.NewSearchTools(nil, nil),
				}, messageforward.Options{Configured: true, ResponseReadLimit: 1 << 20}), nil)
				gemini := &GeminiExecutor{Runtime: &googleforward.Gemini{
					Transport: transport,
					Errors:    &provideradapter.GeminiErrorObserver{},
					Options:   googleforward.Options{Configured: true, ResponseReadLimit: 1 << 20},
				}}
				target := gatewayadapter.NewExecutionProvider(&provider.Record{
					ID:          7,
					Platform:    strings.TrimSuffix(platform, "_standard"),
					Type:        "apikey",
					Concurrency: 1,
					Credentials: map[string]any{"api_key": "test-key"},
					Extra: map[string]any{
						provider.ProviderExtraUpstreamRequestIDHeader: "X-Client-Request-ID",
						"anthropic_passthrough":                       platform == "anthropic",
					},
				})
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil).WithContext(ctx)
				SetOpsSelectedProvider(c, target.Record.RoutingSnapshot())
				body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))
				parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), "anthropic")
				require.NoError(t, err)
				err = ForwardSelectedCountTokens(c.Request.Context(), c, target, parsed, messages, auxiliary, gemini)
				expected := "failed"
				if mode == "success" {
					expected = "completed"
					require.NoError(t, err)
					require.Equal(t, 200, c.Writer.Status())
				}
				require.Len(t, saved.Attempts, 1)
				require.Equal(t, expected, saved.Attempts[0].Outcome)
				require.Equal(t, "selected", saved.Attempts[0].RequestID)
				require.Equal(t, "X-Client-Request-Id", saved.Attempts[0].RequestIDHeader)
				require.Empty(t, saved.UsageWriteState)
				for _, alias := range saved.Aliases {
					require.NotEqual(t, "unselected", alias.Value)
				}
			})
		}
	}
}
