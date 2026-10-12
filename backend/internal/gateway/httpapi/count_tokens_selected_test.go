package httpapi

import (
	"context"
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
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestSelectedCountDispatchPreservesLocalAndUnsupportedPaths checks local estimates and unsupported providers without
// network execution.
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
