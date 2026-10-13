package openai

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// benchmarkIntSink retains positive image-precheck counts across benchmark iterations,
// keeping observed classification results available beyond each timed loop execution.
var benchmarkIntSink int

// TestSanitizeOpenAICrossModeFailoverReasoning_DropsWholeEncryptedItem switches
// mode with encrypted reasoning between messages. It checks removal of the
// complete reasoning item, its identifier and summary, while two messages
// remain.
func TestSanitizeOpenAICrossModeFailoverReasoning_DropsWholeEncryptedItem(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","input":[` +
		`{"type":"message","role":"user","content":"hi"},` +
		`{"type":"reasoning","id":"rs_kiro_1","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"t"}]},` +
		`{"type":"message","role":"assistant","content":"yo"}` +
		`]}`)

	sanitized, changed, err := SanitizeOpenAICrossModeFailoverReasoning(body)
	require.NoError(t, err)
	require.True(t, changed)
	// Remove the complete reasoning item that carries ciphertext.
	require.NotContains(t, string(sanitized), "reasoning")
	require.NotContains(t, string(sanitized), "rs_kiro_1")
	require.NotContains(t, string(sanitized), "summary_text")
	require.Equal(t, int64(2), gjson.GetBytes(sanitized, "input.#").Int())
}

// TestSanitizeOpenAICrossModeFailoverReasoning_NoEncryptedIsNoop supplies
// plaintext reasoning summary with no ciphertext. It checks unchanged bytes and
// false change status, protecting portable history during mode switching.
func TestSanitizeOpenAICrossModeFailoverReasoning_NoEncryptedIsNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"t"}]}]}`)
	sanitized, changed, err := SanitizeOpenAICrossModeFailoverReasoning(body)
	require.NoError(t, err)
	require.False(t, changed, "没有 encrypted_content 的 reasoning 必须保留")
	require.Equal(t, string(body), string(sanitized))
}

// TestSanitizeOpenAICrossModeFailoverReasoning_NoInputIsNoop supplies a
// model-only request without history. It checks unchanged bytes and false
// change status so absent replay input does not trigger reconstruction.
func TestSanitizeOpenAICrossModeFailoverReasoning_NoInputIsNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1"}`)
	sanitized, changed, err := SanitizeOpenAICrossModeFailoverReasoning(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(sanitized))
}

// TestSanitizeOpenAICrossModeFailoverReasoning_PreservesLargeIntegers removes
// encrypted reasoning beside a tool schema containing a large integer. It
// checks successful cleanup retains that exact numeric literal.
func TestSanitizeOpenAICrossModeFailoverReasoning_PreservesLargeIntegers(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","input":[` +
		`{"type":"reasoning","id":"rs_kiro_1","encrypted_content":"ENC"},` +
		`{"type":"message","role":"user","content":"hi"}` +
		`],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"id":{"const":9007199254740993}}}}}]}`)

	sanitized, changed, err := SanitizeOpenAICrossModeFailoverReasoning(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(sanitized), `"const":9007199254740993`,
		"清理请求体不能将 JSON 大整数转换为 float64")
}

// TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplay mixes encrypted and
// server-only reasoning with references under store=false. It checks stale
// reasoning/reference removal, portable cipher retention, stripped server IDs,
// empty summary repair, and large-integer preservation.
func TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplay(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","store":false,"input":[` +
		`{"type":"reasoning","id":"rs_encrypted","call_id":"remove","encrypted_content":"cipher","summary":null,"opaque":9007199254740993},` +
		`{"type":"reasoning","id":"rs_server_only","summary":[{"type":"summary_text","text":"drop"}]},` +
		`{"type":"item_reference","id":"rs_server_only"},` +
		`{"type":"item_reference","id":"msg_keep"},` +
		`{"type":"message","id":"msg_keep","role":"user","content":"continue"}` +
		`]}`)

	normalized, changed, err := NormalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(3), gjson.GetBytes(normalized, "input.#").Int())
	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "input.0.type").String())
	require.False(t, gjson.GetBytes(normalized, "input.0.id").Exists())
	require.False(t, gjson.GetBytes(normalized, "input.0.call_id").Exists())
	require.Equal(t, "cipher", gjson.GetBytes(normalized, "input.0.encrypted_content").String())
	require.True(t, gjson.GetBytes(normalized, "input.0.summary").IsArray())
	require.Equal(t, "9007199254740993", gjson.GetBytes(normalized, "input.0.opaque").Raw)
	require.Equal(t, "msg_keep", gjson.GetBytes(normalized, "input.1.id").String())
	require.Equal(t, "message", gjson.GetBytes(normalized, "input.2.type").String())
}

// TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplayRequiresExplicitStoreFalse
// supplies absent and true storage settings with server reasoning history. It
// checks both requests remain byte-identical and report no change.
func TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplayRequiresExplicitStoreFalse(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"reasoning","id":"rs_keep"}]}`,
		`{"store":true,"input":[{"type":"reasoning","id":"rs_keep"}]}`,
	} {
		normalized, changed, err := NormalizeOpenAIAPIKeyStoreFalseReasoningReplay([]byte(body), false)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, string(normalized))
	}
}

// TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplayKnownCompactMode supplies
// server reasoning without a storage flag but enables compact mode. It checks
// the server-only item is dropped and the user message survives.
func TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplayKnownCompactMode(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_drop","summary":[]},{"type":"message","content":"continue"}]}`)

	normalized, changed, err := NormalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, true)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(1), gjson.GetBytes(normalized, "input.#").Int())
	require.Equal(t, "message", gjson.GetBytes(normalized, "input.0.type").String())
}

// TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplayRejectsEmptyEncryptedContent
// varies null, empty, blank, and numeric ciphertext values under store=false.
// Each unusable reasoning item must be dropped while its neighboring message
// remains.
func TestNormalizeOpenAIAPIKeyStoreFalseReasoningReplayRejectsEmptyEncryptedContent(t *testing.T) {
	for _, encrypted := range []string{"null", `""`, `"   "`, "123"} {
		body := []byte(`{"store":false,"input":[{"type":"reasoning","id":"rs_drop","encrypted_content":` + encrypted + `},{"type":"message","content":"continue"}]}`)
		normalized, changed, err := NormalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, int64(1), gjson.GetBytes(normalized, "input.#").Int())
		require.Equal(t, "message", gjson.GetBytes(normalized, "input.0.type").String())
	}
}

// TestNormalizeOpenAIParallelToolCallsWithoutTools compares requests with and
// without a top-level tool declaration. It retains the flag and bytes with
// tools but removes the irrelevant flag from a tool-less ordinary request.
func TestNormalizeOpenAIParallelToolCallsWithoutTools(t *testing.T) {
	withTools := []byte(`{"tools":[{"type":"function","name":"lookup"}],"parallel_tool_calls":false}`)
	normalized, changed, err := NormalizeOpenAIParallelToolCallsWithoutTools(withTools, false)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(withTools), string(normalized))

	withoutTools := []byte(`{"input":"hi","parallel_tool_calls":true}`)
	normalized, changed, err = NormalizeOpenAIParallelToolCallsWithoutTools(withoutTools, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists())
}

// TestNormalizeOpenAIParallelToolCallsWithoutTools_KeepsResponsesLiteAdditionalTools
// compares populated input tools, empty input tools, and a tool-less Lite flag.
// It checks meaningful nested declarations and Lite mode retain false while an
// ordinary empty declaration permits removal.
func TestNormalizeOpenAIParallelToolCallsWithoutTools_KeepsResponsesLiteAdditionalTools(t *testing.T) {
	liteBody := []byte(`{"input":[{"type":"message","role":"user","content":"hi"},{"type":"additional_tools","tools":[{"type":"function","name":"spawn_agent"}]}],"parallel_tool_calls":false}`)
	normalized, changed, err := NormalizeOpenAIParallelToolCallsWithoutTools(liteBody, false)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, gjson.False, gjson.GetBytes(normalized, "parallel_tool_calls").Type)

	// Empty additional_tools in a non-Lite request does not count as a tool declaration.
	emptyLiteBody := []byte(`{"input":[{"type":"additional_tools","tools":[]}],"parallel_tool_calls":true}`)
	normalized, changed, err = NormalizeOpenAIParallelToolCallsWithoutTools(emptyLiteBody, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists())

	// Lite requests force parallel_tool_calls=false.
	toolLessLiteBody := []byte(`{"input":"hi","parallel_tool_calls":false}`)
	normalized, changed, err = NormalizeOpenAIParallelToolCallsWithoutTools(toolLessLiteBody, true)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, gjson.False, gjson.GetBytes(normalized, "parallel_tool_calls").Type)
}

// TestNormalizeOpenAIParallelToolCallsWithoutTools_KeepsResponsesLiteAdditionalToolsUpstreamRegression
// repeats nested, empty, and tool-less Lite fixtures. It protects retained
// false flags for meaningful or Lite declarations and removal for an ordinary
// empty declaration.
func TestNormalizeOpenAIParallelToolCallsWithoutTools_KeepsResponsesLiteAdditionalToolsUpstreamRegression(t *testing.T) {
	liteBody := []byte(`{"input":[{"type":"message","role":"user","content":"hi"},{"type":"additional_tools","tools":[{"type":"function","name":"spawn_agent"}]}],"parallel_tool_calls":false}`)
	normalized, changed, err := NormalizeOpenAIParallelToolCallsWithoutTools(liteBody, false)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, gjson.False, gjson.GetBytes(normalized, "parallel_tool_calls").Type)

	// Empty additional_tools in a non-Lite request does not count as a tool declaration.
	emptyLiteBody := []byte(`{"input":[{"type":"additional_tools","tools":[]}],"parallel_tool_calls":true}`)
	normalized, changed, err = NormalizeOpenAIParallelToolCallsWithoutTools(emptyLiteBody, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists())

	// Lite requests force parallel_tool_calls=false.
	toolLessLiteBody := []byte(`{"input":"hi","parallel_tool_calls":false}`)
	normalized, changed, err = NormalizeOpenAIParallelToolCallsWithoutTools(toolLessLiteBody, true)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, gjson.False, gjson.GetBytes(normalized, "parallel_tool_calls").Type)
}

// TestNormalizeOpenAIResponsesReasoningContentReplayStripsCrossProviderArray
// replays visible reasoning content after messages and tool history. It checks
// content removal while summary, large integer metadata, item position, and
// later assistant text survive.
func TestNormalizeOpenAIResponsesReasoningContentReplayStripsCrossProviderArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[` +
		`{"type":"message","role":"user","content":"one"},` +
		`{"type":"message","role":"assistant","content":"two"},` +
		`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"ok"},` +
		`{"type":"message","role":"user","content":"five"},` +
		`{"type":"reasoning","id":"rs_provider","summary":[{"type":"summary_text","text":"portable"}],"content":[{"type":"reasoning_text","text":"visible reasoning"}],"opaque":9007199254740993},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}` +
		`]}`)

	normalized, changed, err := NormalizeOpenAIResponsesReasoningContentReplay(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "input.5.type").String())
	require.False(t, gjson.GetBytes(normalized, "input.5.content").Exists())
	require.Equal(t, "portable", gjson.GetBytes(normalized, "input.5.summary.0.text").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(normalized, "input.5.opaque").Raw)
	require.Equal(t, "answer", gjson.GetBytes(normalized, "input.6.content.0.text").String())
}

// TestNormalizeOpenAIResponsesReasoningContentReplayKeepsPortableShapes
// supplies absent/empty reasoning content and ordinary message content. It
// checks no change and JSON equivalence, protecting those portable replay
// forms.
func TestNormalizeOpenAIResponsesReasoningContentReplayKeepsPortableShapes(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"reasoning","summary":[]}]}`,
		`{"input":[{"type":"reasoning","content":[],"summary":[]}]}`,
		`{"input":[{"type":"message","content":[{"type":"input_text","text":"keep"}]}]}`,
	} {
		normalized, changed, err := NormalizeOpenAIResponsesReasoningContentReplay([]byte(body))
		require.NoError(t, err)
		require.False(t, changed)
		require.JSONEq(t, body, string(normalized))
	}
}

// BenchmarkOpenAIResponses_LargeInputEmptyBase64Guard measures image prechecks
// over text-only bodies from four to thirty-two MiB targets. Fixture
// construction is outside timing, and iterations report bytes/allocations while
// retaining positive guard counts.
func BenchmarkOpenAIResponses_LargeInputEmptyBase64Guard(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeOpenAIResponsesBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if OpenAIRequestBodyMayContainEmptyBase64InputImage(body) {
					benchmarkIntSink++
				}
			}
		})
	}
}

// benchmarkBodySizes returns the four large-input target sizes and their
// display labels. Values are byte counts used by the fixture builder; the
// completed JSON can exceed its target by framing and tool metadata.
func benchmarkBodySizes() []struct {
	name  string // benchmark size display label
	bytes int    // bytes
} {
	return []struct {
		name  string // benchmark size display label
		bytes int    // bytes
	}{
		{name: "4MB", bytes: 4 << 20},
		{name: "8MB", bytes: 8 << 20},
		{name: "16MB", bytes: 16 << 20},
		{name: "32MB", bytes: 32 << 20},
	}
}

// buildLargeOpenAIResponsesBody creates text history until the positive target
// byte count is reached, then appends tool JSON. The generated body can exceed
// targetBytes; callers allocate it before timing the image-field precheck.
func buildLargeOpenAIResponsesBody(targetBytes int) []byte {
	var builder strings.Builder
	// Reserve estimated space for history plus trailing request metadata.
	builder.Grow(targetBytes + 1024)
	_, _ = builder.WriteString(`{"model":"gpt-5.4","stream":true,"prompt_cache_key":"session-benchmark","input":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"`)
		_, _ = builder.WriteString(strings.Repeat("openai responses payload ", 48))
		_, _ = builder.WriteString(strconv.Itoa(i))
		_, _ = builder.WriteString(`"}]}`)
	}
	_, _ = builder.WriteString(`],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
	return []byte(builder.String())
}
