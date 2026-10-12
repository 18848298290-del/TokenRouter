package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func newCompactBodySignalTestContext(t *testing.T, path string, body []byte) *gin.Context {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestNormalizeOpenAIResponsesCompactRequest_RemoteV2StaysOnResponses(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"store":true,
		"prompt_cache_key":"pck-signal-1",
		"reasoning":{"effort":"max","context":"all_turns"},
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"compaction_trigger"}
		]
	}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses", body)

	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)

	require.Equal(t, "/v1/responses", c.Request.URL.Path)
	require.False(t, IsOpenAIResponsesCompactPath(c))
	require.True(t, IsBareOpenAIResponsesPath(c))
	require.True(t, IsOpenAIRemoteCompactionV2Request(normalized))
	require.Equal(t, providercore.OpenAIEndpointCapabilityRemoteCompactionV2,
		textflow.RequiredResponsesCapability(false, true, false, capability.PlatformOpenAI))
	require.Equal(t, body, normalized)
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.True(t, gjson.GetBytes(normalized, "store").Bool())
	require.Equal(t, "pck-signal-1", gjson.GetBytes(normalized, "prompt_cache_key").String())
	require.Equal(t, "max", gjson.GetBytes(normalized, "reasoning.effort").String())
	require.Equal(t, "all_turns", gjson.GetBytes(normalized, "reasoning.context").String())

	reqStream, streamOK := ParseOpenAICompatibleStream(normalized)
	require.True(t, streamOK)
	require.True(t, reqStream)

	_, seedExists := c.Get(OpenAICompactSessionSeedKey)
	require.False(t, seedExists)
	_, streamMarkerExists := c.Get(openAICompactClientStreamKey)
	require.False(t, streamMarkerExists)
	// V2 keeps its path and negotiation marker so outbound headers include the beta feature.
	require.True(t, c.GetBool("openai_native_compaction_v2"))
}

func TestNormalizeOpenAIResponsesCompactRequest_RemoteV2DoesNotRequireBetaHeader(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"compaction_trigger"}]}`)

	for _, betaHeader := range []string{"", "responses_websockets_v2", "REMOTE_COMPACTION_V2", "remote_compaction_v2"} {
		t.Run(betaHeader, func(t *testing.T) {
			c := newCompactBodySignalTestContext(t, "/v1/responses", body)
			if betaHeader != "" {
				c.Request.Header.Set("x-codex-beta-features", betaHeader)
			}

			normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
			require.True(t, ok)
			require.Equal(t, "/v1/responses", c.Request.URL.Path)
			require.True(t, IsOpenAIRemoteCompactionV2Request(normalized))
		})
	}
}

func TestNormalizeOpenAIResponsesCompactRequest_RemoteV2PathAliasesStayOnResponses(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"compaction_trigger"}]}`)
	for _, path := range []string{"/v1/responses/", "/openai/v1/responses", "/responses", "/backend-api/codex/responses"} {
		t.Run(path, func(t *testing.T) {
			c := newCompactBodySignalTestContext(t, path, body)

			normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
			require.True(t, ok)
			require.Equal(t, path, c.Request.URL.Path)
			require.Equal(t, body, normalized)
			require.True(t, IsBareOpenAIResponsesPath(c))
			require.True(t, IsOpenAIRemoteCompactionV2Request(normalized))
		})
	}
}

func TestIsBareOpenAIResponsesPath(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{path: "/v1/responses", want: true},
		{path: "/openai/v1/responses", want: true},
		{path: "/responses", want: true},
		{path: "/backend-api/codex/responses", want: true},
		{path: "/v1/responses/resp_123/responses", want: false},
		{path: "/v1/responses/compact", want: false},
		{path: "/other/responses", want: false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			c := newCompactBodySignalTestContext(t, tt.path, nil)
			require.Equal(t, tt.want, IsBareOpenAIResponsesPath(c))
		})
	}
}

func TestNormalizeOpenAIResponsesCompactRequest_BodySignalTrailingSlashPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/", body)

	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/compact", c.Request.URL.Path)
}

func TestNormalizeOpenAIResponsesCompactRequest_CodexDirectAliasPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext(t, "/backend-api/codex/responses", body)

	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/backend-api/codex/responses/compact", c.Request.URL.Path)
}

func TestNormalizeOpenAIResponsesCompactRequest_NonRemoteV2BodySignalPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	tests := []struct {
		name       string
		body       []byte
		betaHeader string
		wantMarked bool
	}{
		{
			name: "stream_false_headerless",
			body: []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"compaction_trigger"}]}`),
		},
		{
			name: "stream_absent_headerless",
			body: []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`),
		},
		{
			name:       "stream_false_declared_header",
			body:       []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"compaction_trigger"}]}`),
			betaHeader: "remote_compaction_v2",
		},
		{
			name:       "stream_absent_wrong_case_header",
			body:       []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`),
			betaHeader: "REMOTE_COMPACTION_V2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCompactBodySignalTestContext(t, "/v1/responses", tt.body)
			if tt.betaHeader != "" {
				c.Request.Header.Set("x-codex-beta-features", tt.betaHeader)
			}

			normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), tt.body)
			require.True(t, ok)
			require.Equal(t, "/v1/responses/compact", c.Request.URL.Path)
			require.False(t, gjson.GetBytes(normalized, "stream").Exists())

			marked, exists := c.Get(openAICompactClientStreamKey)
			require.Equal(t, tt.wantMarked, exists)
			if tt.wantMarked {
				require.Equal(t, true, marked)
			}
		})
	}
}

func TestNormalizeOpenAIResponsesCompactRequest_NoTriggerUntouched(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses", body)

	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses", c.Request.URL.Path)
	require.False(t, IsOpenAIResponsesCompactPath(c))
	require.Equal(t, body, normalized)
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
}

func TestNormalizeOpenAIResponsesCompactRequest_PathBasedNoDoubleSuffix(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","stream":true,"store":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/compact", body)

	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/compact", c.Request.URL.Path)
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
}

func TestNormalizeOpenAIResponsesCompactRequest_SubpathNotPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/resp_123/cancel", body)

	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/resp_123/cancel", c.Request.URL.Path)
	require.Equal(t, body, normalized)
}

// TestNormalizeOpenAIResponsesCompactRequest_PathBasedStreamTrueNotMarked checks that unary path-based compact
// keeps JSON output even when the body contains stream:true.
func TestNormalizeOpenAIResponsesCompactRequest_PathBasedStreamTrueNotMarked(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/compact", body)

	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	_, exists := c.Get(openAICompactClientStreamKey)
	require.False(t, exists)
}

func TestIsOpenAILegacyCompactPath(t *testing.T) {
	require.False(t, IsOpenAIResponsesCompactPath(nil))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	require.True(t, IsOpenAIResponsesCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact/", nil)
	require.True(t, IsOpenAIResponsesCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact/detail", nil)
	require.True(t, IsOpenAIResponsesCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.False(t, IsOpenAIResponsesCompactPath(c))
}

func TestLogOpenAIRemoteCompactOutcome_Succeeded(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.125.0")
	c.Set(OpsModelKey, "gpt-5.3-codex")
	c.Set(OpsProviderIDKey, int64(123))
	c.Header("x-request-id", "rid-compact-ok")
	c.Status(http.StatusOK)

	h := &OpenAITextHandler{}
	h.LogRemoteCompactOutcome(c, time.Now().Add(-8*time.Millisecond))

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "info"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "succeeded"))
	require.True(t, logSink.ContainsFieldValue("status_code", "200"))
	require.True(t, logSink.ContainsFieldValue("path", "/v1/responses/compact"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.3-codex"))
	require.True(t, logSink.ContainsFieldValue("provider_id", "123"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-compact-ok"))
}

func TestLogOpenAIRemoteCompactOutcome_Failed(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.125.0")
	c.Status(http.StatusBadGateway)

	h := &OpenAITextHandler{}
	h.LogRemoteCompactOutcome(c, time.Now())

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "failed"))
	require.True(t, logSink.ContainsFieldValue("status_code", "502"))
	require.True(t, logSink.ContainsFieldValue("path", "/responses/compact"))
}

func TestLogOpenAIRemoteCompactOutcome_NonCompactSkips(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Status(http.StatusOK)

	h := &OpenAITextHandler{}
	h.LogRemoteCompactOutcome(c, time.Now())

	require.False(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
}

// TestSupplementCompactionItemFromSSE_Gating checks compact-only supplementation and avoids duplicate compaction items.
func TestSupplementCompactionItemFromSSE_Gating(t *testing.T) {
	bodyText := `data: {"type":"response.output_item.done","item":{"id":"cmp_g","type":"compaction","encrypted_content":"g"}}` + "\n"

	// Non-compact paths bypass supplementation.

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	finalResponse := []byte(`{"id":"r1","output":[{"type":"message"}]}`)
	require.Equal(t, string(finalResponse), string(protocolopenai.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c), finalResponse, bodyText)))

	// An existing terminal compaction item is not duplicated.
	c2, _ := newCompactBridgeTestContext(t, false)
	already := []byte(`{"id":"r2","output":[{"type":"compaction","encrypted_content":"x"}]}`)
	require.Equal(t, string(already), string(protocolopenai.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c2), already, bodyText)))

	// Append the missing compaction item to a nonempty terminal output.
	missing := []byte(`{"id":"r3","output":[{"type":"message"}]}`)
	patched := protocolopenai.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c2), missing, bodyText)
	items := gjson.GetBytes(patched, "output").Array()
	require.Len(t, items, 2)
	require.Equal(t, "compaction", items[1].Get("type").String())
	require.Equal(t, "g", items[1].Get("encrypted_content").String())
}

func TestNormalizeOpenAICompactRequestBodyPreservesServiceTier(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"hello"}],
		"service_tier":"priority",
		"prompt_cache_key":"compact-cache-key",
		"store":false,
		"stream":true
	}`)

	normalized, changed, err := openai.NormalizeOpenAICompactRequestBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(normalized, "model").String())
	require.Equal(t, "priority", gjson.GetBytes(normalized, "service_tier").String())
	require.False(t, gjson.GetBytes(normalized, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
}

func TestNormalizeOpenAICompactRequestBodyPreservesCurrentCodexPayloadFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"compact me"}],"instructions":"compact-test","tools":[{"type":"function","name":"shell"}],"parallel_tool_calls":true,"reasoning":{"effort":"high"},"text":{"verbosity":"low"},"previous_response_id":"resp_123","store":true,"stream":true,"prompt_cache_key":"cache_123"}`)

	normalized, changed, err := openai.NormalizeOpenAICompactRequestBody(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "gpt-5.5", gjson.GetBytes(normalized, "model").String())
	require.True(t, gjson.GetBytes(normalized, "tools").Exists())
	require.True(t, gjson.GetBytes(normalized, "parallel_tool_calls").Bool())
	require.Equal(t, "high", gjson.GetBytes(normalized, "reasoning.effort").String())
	require.Equal(t, "low", gjson.GetBytes(normalized, "text.verbosity").String())
	require.Equal(t, "resp_123", gjson.GetBytes(normalized, "previous_response_id").String())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "prompt_cache_key").Exists())
}

func TestNormalizeOpenAICompactRequestBodyDropsParallelToolCallsWithoutUsableTools(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5.5","input":[],"parallel_tool_calls":true}`,
		`{"model":"gpt-5.5","input":[],"tools":[],"parallel_tool_calls":false}`,
		`{"model":"gpt-5.5","input":[],"tools":null,"parallel_tool_calls":true}`,
		`{"model":"gpt-5.5","input":[],"tools":{},"parallel_tool_calls":true}`,
	} {
		normalized, changed, err := openai.NormalizeOpenAICompactRequestBody([]byte(body))
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists(), string(normalized))
	}

	withTools := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":false}`)
	normalized, _, err := openai.NormalizeOpenAICompactRequestBody(withTools)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists())
	require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Bool())
}
