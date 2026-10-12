package requeststate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// benchmarkStringSink retains extracted model and cache-key text so benchmark
// field reads remain observable after each timed iteration.
var benchmarkStringSink string

// TestOpenAIRequestView_ExtractsRawScalars supplies padded strings and
// streaming evidence in one Responses request. The view must trim each selected
// scalar and retain service-tier presence for later validation.
func TestOpenAIRequestView_ExtractsRawScalars(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":" gpt-5 ","stream":true,"prompt_cache_key":" ses-1 ","previous_response_id":" resp-1 ","service_tier":" fast ","reasoning":{"effort":" medium "}}`))

	require.Equal(t, "gpt-5", view.Model)
	require.True(t, view.Stream)
	require.Equal(t, "ses-1", view.PromptCacheKey)
	require.Equal(t, "resp-1", view.PreviousResponseID)
	require.Equal(t, "fast", view.ServiceTier)
	require.True(t, view.HasServiceTier)
	require.Equal(t, "medium", view.ReasoningEffort)
}

// TestOpenAIRequestView_ExtractsFieldsAfterLargeInput places optional scalars
// after a large input array. The root field walk must still find and trim those
// later values rather than stopping after initial model evidence.
func TestOpenAIRequestView_ExtractsFieldsAfterLargeInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"content":"` + strings.Repeat("payload", 1024) + `"}],"stream":true,"prompt_cache_key":"session-1","previous_response_id":"resp-1","service_tier":"flex","reasoning":{"effort":"high"}}`)

	view := NewOpenAIRequestView(body)

	require.Equal(t, "gpt-5", view.Model)
	require.True(t, view.Stream)
	require.Equal(t, "session-1", view.PromptCacheKey)
	require.Equal(t, "resp-1", view.PreviousResponseID)
	require.Equal(t, "flex", view.ServiceTier)
	require.Equal(t, "high", view.ReasoningEffort)
}

// TestOpenAIRequestView_KeepsFirstDuplicateField supplies duplicate model and
// reasoning root members with conflicting values. Scalar extraction must retain
// the first values to match the byte-view duplicate selection rule.
func TestOpenAIRequestView_KeepsFirstDuplicateField(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5","model":"gpt-5.1","reasoning":{"effort":"low"},"reasoning":{"effort":"high"}}`))

	require.Equal(t, "gpt-5", view.Model)
	require.Equal(t, "low", view.ReasoningEffort)
}

// TestOpenAIRequestView_KeepsLenientPrefixExtraction ends an otherwise useful
// request prefix at an unfinished input array. The view must retain earlier
// model/stream evidence before full decoding validates the complete document.
func TestOpenAIRequestView_KeepsLenientPrefixExtraction(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5","stream":true,"input":[`))

	require.Equal(t, "gpt-5", view.Model)
	require.True(t, view.Stream)
}

// TestOpenAIRequestView_DecodeKeepsFullMapBehavior decodes a view whose input
// contains a message array. The full-object path must retain model and []any
// input representation for later map-based transformations.
func TestOpenAIRequestView_DecodeKeepsFullMapBehavior(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5","stream":true,"input":[{"type":"message","content":"hi"}]}`))

	reqBody, err := view.Decode()
	require.NoError(t, err)
	require.Equal(t, "gpt-5", reqBody["model"])
	require.IsType(t, []any{}, reqBody["input"])
}

// TestOpenAIRequestView_ApplyPatches queues a model replacement, continuation
// deletion, and nested effort replacement. Applying the queue must produce
// their combined JSON result while retaining the unrelated input message.
func TestOpenAIRequestView_ApplyPatches(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5","previous_response_id":"resp_1","reasoning":{"effort":"minimal"},"input":[{"type":"message","content":"hi"}]}`))
	view.MarkPatchSet("model", "gpt-5.1")
	view.MarkPatchDelete("previous_response_id")
	view.MarkPatchSet("reasoning.effort", "none")

	patched, err := view.ApplyPatches()
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"gpt-5.1","reasoning":{"effort":"none"},"input":[{"type":"message","content":"hi"}]}`, string(patched))
}

// TestOpenAIRequestView_RejectsEscapedPatchPath queues a path containing an
// escaped member separator. The view must discard byte patching and return an
// application error so preparation uses decoded rewriting.
func TestOpenAIRequestView_RejectsEscapedPatchPath(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"metadata":{"user.id":"old"}}`))
	view.MarkPatchSet(`metadata.user\.id`, "new")

	require.False(t, view.HasPatches())
	_, err := view.ApplyPatches()
	require.Error(t, err)
}

// TestOpenAIRequestView_ApplyPatchesDisabled disables a view after queueing a
// model change. Applying it must fail because discarded queue state cannot
// supply a valid byte rewrite.
func TestOpenAIRequestView_ApplyPatchesDisabled(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5"}`))
	view.MarkPatchSet("model", "gpt-5.1")
	view.DisablePatches()

	_, err := view.ApplyPatches()
	require.Error(t, err)
}

// TestOpenAIRequestView_HasPatches reads queue presence before an edit, after
// enqueueing, and after disablement. The reported flag must track enabled
// queued work for the caller's rewrite decision.
func TestOpenAIRequestView_HasPatches(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5"}`))
	require.False(t, view.HasPatches())

	view.MarkPatchSet("model", "gpt-5.1")
	require.True(t, view.HasPatches())

	view.DisablePatches()
	require.False(t, view.HasPatches())
}

// TestExtractOpenAIRequestMetaFromBody reads model, stream, and prompt-cache
// metadata from complete, partial-field, and empty fixtures. Extraction must
// trim the key and return the table's zero values for absent fields.
func TestExtractOpenAIRequestMetaFromBody(t *testing.T) {
	tests := []struct {
		name          string // case label for diagnostics
		body          []byte // JSON request fixture bytes
		wantModel     string // expected extracted model spelling
		wantStream    bool   // expected extracted streaming flag
		wantPromptKey string // expected trimmed prompt-cache key
	}{
		{
			name:          "完整字段",
			body:          []byte(`{"model":"gpt-5","stream":true,"prompt_cache_key":" ses-1 "}`),
			wantModel:     "gpt-5",
			wantStream:    true,
			wantPromptKey: "ses-1",
		},
		{
			name:          "缺失可选字段",
			body:          []byte(`{"model":"gpt-4"}`),
			wantModel:     "gpt-4",
			wantStream:    false,
			wantPromptKey: "",
		},
		{
			name:          "空请求体",
			body:          nil,
			wantModel:     "",
			wantStream:    false,
			wantPromptKey: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, stream, promptKey := OpenAIRequestMetaFromBody(tt.body)
			require.Equal(t, tt.wantModel, model)
			require.Equal(t, tt.wantStream, stream)
			require.Equal(t, tt.wantPromptKey, promptKey)
		})
	}
}

// TestGetOpenAIRequestBodyMap_ParseError submits malformed JSON through the
// complete object decoder. The returned error must retain its parse-request
// prefix for preparation diagnostics and caller error classification.
func TestGetOpenAIRequestBodyMap_ParseError(t *testing.T) {
	_, err := DecodeOpenAIRequestBody([]byte(`{invalid-json`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse request")
}

// TestOpenAIViewCompatibility combines first-duplicate scalars, shared byte
// storage, a large integer, and queued edits. Patching must preserve the
// unrelated numeric spelling and source body while applying nested replacement
// and deletion.
func TestOpenAIViewCompatibility(t *testing.T) {
	body := []byte(`{"model":" first ","model":"second","stream":true,"input":[{"future":{"value":123456789012345678}}],"reasoning":{"effort":" high "},"service_tier":" priority "}`)
	view := NewOpenAIRequestView(body)
	require.Equal(t, "first", view.Model)
	require.Equal(t, "high", view.ReasoningEffort)
	require.True(t, view.Stream)
	require.True(t, view.HasServiceTier)
	require.Equal(t, "priority", view.ServiceTier)
	require.Same(t, &body[0], &view.Bytes()[0])
	view.MarkPatchSet("reasoning.effort", "low")
	view.MarkPatchDelete("service_tier")
	patched, err := view.ApplyPatches()
	require.NoError(t, err)
	require.Equal(t, "123456789012345678", gjson.GetBytes(patched, "input.0.future.value").Raw)
	require.Equal(t, "low", gjson.GetBytes(patched, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(patched, "service_tier").Exists())
	require.Equal(t, " high ", gjson.GetBytes(body, "reasoning.effort").String())
}

// TestOpenAIViewDisabledPatches supplies an unsupported escaped path to a fresh
// view. Disablement must be visible through PatchesDisabled and HasPatches, and
// later application must return an error.
func TestOpenAIViewDisabledPatches(t *testing.T) {
	view := NewOpenAIRequestView([]byte(`{"model":"gpt-5"}`))
	view.MarkPatchSet(`unsupported\.field`, true)
	require.True(t, view.PatchesDisabled())
	require.False(t, view.HasPatches())
	_, err := view.ApplyPatches()
	require.Error(t, err)
}

// BenchmarkOpenAIResponses_LargeInputMeta extracts model/stream/cache-key
// fields from successively larger Responses payloads. Construction stays
// outside timing, while retained sink values and actual-byte reporting measure
// lenient field reads.
func BenchmarkOpenAIResponses_LargeInputMeta(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeOpenAIResponsesBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				model, stream, promptCacheKey := OpenAIRequestMetaFromBody(body)
				benchmarkStringSink = model + promptCacheKey
				if stream {
					benchmarkIntSink++
				}
			}
		})
	}
}

// BenchmarkOpenAIResponses_LargeInputDecodeMap decodes successively larger
// Responses payloads to full objects. Construction stays outside timing, and
// retained map size plus allocation reporting measure complete-object decoding.
func BenchmarkOpenAIResponses_LargeInputDecodeMap(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeOpenAIResponsesBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				reqBody, err := DecodeOpenAIRequestBody(body)
				if err != nil {
					b.Fatalf("解析 OpenAI 请求失败: %v", err)
				}
				benchmarkIntSink = len(reqBody)
			}
		})
	}
}

// BenchmarkOpenAIResponses_LargeInputRawPatch queues instruction and effort
// replacements on larger Responses payloads. The timed workload retains patched
// lengths and reports actual bytes/allocations for sequential byte changes.
func BenchmarkOpenAIResponses_LargeInputRawPatch(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeOpenAIResponsesBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				view := NewOpenAIRequestView(body)
				view.MarkPatchSet("instructions", "You are a helpful coding assistant.")
				view.MarkPatchSet("reasoning.effort", "none")
				patched, err := view.ApplyPatches()
				if err != nil {
					b.Fatalf("应用 OpenAI raw patch 失败: %v", err)
				}
				benchmarkIntSink = len(patched)
			}
		})
	}
}

// buildLargeOpenAIResponsesBody appends user text messages until the partial
// builder reaches targetBytes, then adds a function tool declaration. Final
// output exceeds that partial-body threshold, so benchmarks report the actual
// returned length.
func buildLargeOpenAIResponsesBody(targetBytes int) []byte {
	var builder strings.Builder
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
