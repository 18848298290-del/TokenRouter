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

// Remote compaction chooses its HTTP route from the bare Responses path,
// stream flag, and body trigger. These cases distinguish native streaming from
// unary promotion, keep session and negotiation markers at the intended stage,
// and verify the Compact codec's retained payload fields.

// newCompactBodySignalTestContext creates a JSON request for compaction path detection.
func newCompactBodySignalTestContext(t *testing.T, path string, body []byte) *gin.Context {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

// TestNormalizeOpenAIResponsesCompactRequest_RemoteV2StaysOnResponses declares a
// streaming body trigger on ordinary Responses. Normalization must retain its
// route and payload, install the native marker, and avoid unary session and output markers.
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
	// Check the saved recognition marker in addition to the body and path.
	// This fixture protects negotiation state publication but does not construct outbound headers.
	require.True(t, c.GetBool("openai_native_compaction_v2"))
}

// TestNormalizeOpenAIResponsesCompactRequest_RemoteV2DoesNotRequireBetaHeader varies
// the negotiation header around a streaming body trigger. Native recognition
// and the Responses route must remain the same regardless of those header values.
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

// TestNormalizeOpenAIResponsesCompactRequest_RemoteV2PathAliasesStayOnResponses
// supplies supported Responses aliases and a trailing slash. Native recognition
// must retain each supplied route and the unchanged body across those endpoint spellings.
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

// TestIsBareOpenAIResponsesPath compares supported roots with response-resource
// subpaths, Compact, and unrelated prefixes. Recognition must accept the roots
// while leaving those other paths outside body-triggered promotion.
func TestIsBareOpenAIResponsesPath(t *testing.T) {
	for _, tt := range []struct {
		path string // endpoint path candidate
		want bool   // expected bare-path recognition
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

// TestNormalizeOpenAIResponsesCompactRequest_BodySignalTrailingSlashPromoted supplies
// a unary body trigger on a Responses root ending in a slash. Promotion
// must select the single Compact suffix on the normalized root.
func TestNormalizeOpenAIResponsesCompactRequest_BodySignalTrailingSlashPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/", body)

	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/compact", c.Request.URL.Path)
}

// TestNormalizeOpenAIResponsesCompactRequest_CodexDirectAliasPromoted supplies a
// unary body trigger on the Codex Responses alias. Promotion must append
// Compact beneath that alias so its subsequent request construction uses the correct endpoint.
func TestNormalizeOpenAIResponsesCompactRequest_CodexDirectAliasPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext(t, "/backend-api/codex/responses", body)

	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/backend-api/codex/responses/compact", c.Request.URL.Path)
}

// TestNormalizeOpenAIResponsesCompactRequest_NonRemoteV2BodySignalPromoted varies
// missing or false stream flags and declared beta features. All fixtures
// must promote to unary Compact, remove stream, and leave the client-stream marker absent.
func TestNormalizeOpenAIResponsesCompactRequest_NonRemoteV2BodySignalPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	tests := []struct {
		name string // unary promotion scenario
		body []byte // triggered request JSON
		// betaHeader is installed when nonempty. Native detection still follows
		// body stream intent, so even a V2 feature header cannot make these fixtures streaming.
		betaHeader string
		wantMarked bool // expected stream-marker presence
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

// TestNormalizeOpenAIResponsesCompactRequest_NoTriggerUntouched supplies an ordinary
// streaming message with no compaction trigger. Normalization must keep the
// Responses route, original bytes, and stream intent without identifying a Compact request.
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

// TestNormalizeOpenAIResponsesCompactRequest_PathBasedNoDoubleSuffix starts on the
// explicit Compact endpoint with stream and store fields. Normalization must
// retain one Compact suffix and remove those fields from its unary payload.
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

// TestNormalizeOpenAIResponsesCompactRequest_SubpathNotPromoted places a trigger on
// an existing response cancellation path. Normalization must leave both path
// and body unchanged because resource subpaths are outside bare-root promotion.
func TestNormalizeOpenAIResponsesCompactRequest_SubpathNotPromoted(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/resp_123/cancel", body)

	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/resp_123/cancel", c.Request.URL.Path)
	require.Equal(t, body, normalized)
}

// TestNormalizeOpenAIResponsesCompactRequest_PathBasedStreamTrueNotMarked normalizes
// an explicit Compact path containing stream:true. Its client-stream marker
// must remain absent, distinguishing path-based compaction from body promotion
// that saves streaming intent for the response bridge.
func TestNormalizeOpenAIResponsesCompactRequest_PathBasedStreamTrueNotMarked(t *testing.T) {
	h := &OpenAITextHandler{}
	body := []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	c := newCompactBodySignalTestContext(t, "/v1/responses/compact", body)

	_, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	_, exists := c.Get(openAICompactClientStreamKey)
	require.False(t, exists)
}

// TestIsOpenAILegacyCompactPath compares ordinary Responses with Compact roots
// and descendants under supported aliases. Recognition must include the legacy
// paths and trailing slash while tolerating nil context and excluding the ordinary root.
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

// TestLogOpenAIRemoteCompactOutcome_Succeeded supplies HTTP 200 with model,
// provider, path, and upstream request observations. Logging must emit the
// info success event and retain each asserted field for Compact diagnostics.
func TestLogOpenAIRemoteCompactOutcome_Succeeded(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	// Restore removes the shared sink and releases its capture mutex, allowing
	// subsequent logging fixtures to observe their own events after this assertion scope ends.
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
	// Put the start before logging to supply a positive elapsed-time fixture.
	h.LogRemoteCompactOutcome(c, time.Now().Add(-8*time.Millisecond))

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "info"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "succeeded"))
	require.True(t, logSink.ContainsFieldValue("status_code", "200"))
	require.True(t, logSink.ContainsFieldValue("path", "/v1/responses/compact"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.3-codex"))
	require.True(t, logSink.ContainsFieldValue("provider_id", "123"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-compact-ok"))
}

// TestLogOpenAIRemoteCompactOutcome_Failed supplies a bad-gateway Compact response.
// Logging must emit the warn failure event with failed outcome, the actual
// HTTP status, and the selected Compact path.
func TestLogOpenAIRemoteCompactOutcome_Failed(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	// Keep the capture lock through all failure assertions, then remove this
	// sink so another fixture cannot inherit the recorded bad-gateway outcome.
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

// TestLogOpenAIRemoteCompactOutcome_NonCompactSkips supplies a successful ordinary
// Responses request. The Compact outcome logger must emit neither success nor
// failure messages because that request did not use the legacy Compact path.
func TestLogOpenAIRemoteCompactOutcome_NonCompactSkips(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	// Hold the shared capture through these absence checks. Restore clears the
	// sink and releases the mutex before another fixture installs its own logger.
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
// Ordinary paths and already complete output must remain unchanged, while a
// Compact terminal result missing its captured item gains that item's type and encrypted content.
func TestSupplementCompactionItemFromSSE_Gating(t *testing.T) {
	bodyText := `data: {"type":"response.output_item.done","item":{"id":"cmp_g","type":"compaction","encrypted_content":"g"}}` + "\n"

	// Ordinary Responses output bypasses Compact recovery, so a separately seen
	// compaction item must not alter this terminal body on an unmarked route.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	finalResponse := []byte(`{"id":"r1","output":[{"type":"message"}]}`)
	require.Equal(t, string(finalResponse), string(protocolopenai.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c), finalResponse, bodyText)))

	// A terminal body already carrying compaction supplies its own result.
	// The captured SSE item must not add a second compaction payload to that body.
	c2, _ := newCompactBridgeTestContext(t, false)
	already := []byte(`{"id":"r2","output":[{"type":"compaction","encrypted_content":"x"}]}`)
	require.Equal(t, string(already), string(protocolopenai.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c2), already, bodyText)))

	// Supply terminal output that omits the captured compaction item. Supplementation
	// must append it after the message so the saved encrypted result becomes available.
	missing := []byte(`{"id":"r3","output":[{"type":"message"}]}`)
	patched := protocolopenai.SupplementCompactionItemFromSSE(IsOpenAIResponsesCompactPath(c2), missing, bodyText)
	items := gjson.GetBytes(patched, "output").Array()
	require.Len(t, items, 2)
	require.Equal(t, "compaction", items[1].Get("type").String())
	require.Equal(t, "g", items[1].Get("encrypted_content").String())
}

// TestNormalizeOpenAICompactRequestBodyPreservesServiceTier supplies priority tier
// with request-scoped fields excluded from Compact. The codec must retain
// model and service_tier while removing prompt_cache_key, store, and stream.
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

// TestNormalizeOpenAICompactRequestBodyPreservesCurrentCodexPayloadFields exercises
// tools, parallel-call intent, reasoning, text, and previous-response input.
// The codec must keep those asserted fields and model while dropping the
// request-scoped store, stream, and prompt-cache key.
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

// TestNormalizeOpenAICompactRequestBodyDropsParallelToolCallsWithoutUsableTools varies
// absent, empty, and non-array tools with parallel-call flags. Normalization
// must remove unsupported flags in those cases while retaining false alongside a usable tool array.
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
