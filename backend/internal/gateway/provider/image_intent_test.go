package provider

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// These result sinks retain benchmark outputs so repeated intent, capability,
// and billing extraction work remains observable to the benchmark harness.
var (
	imageGenerationIntentBenchmarkResult           bool
	passthroughImageIntentBenchmarkSink            bool
	openAIResponsesImageIntentRoutingBenchmarkSink provider.OpenAIEndpointCapability

	// benchmarkStringSink retains parsed model, size tier, and input size values.
	benchmarkStringSink string
)

// BenchmarkIsImageGenerationIntent measures large-body intent detection with
// leading/trailing signals, invalid JSON, and duplicate keys. Setup verifies
// each expected result before timed iterations record the output sink.
func BenchmarkIsImageGenerationIntent(b *testing.B) {
	largeInput := strings.Repeat("x", 1<<20)
	benchmarks := []struct {
		name string // image intent scenario label
		body []byte // serialized request intent fixture
		want bool   // expected image intent result
	}{
		{
			name: "1MiBInputNoImage",
			body: []byte(`{"model":"gpt-5.5","tools":[],"input":"` + largeInput + `","tool_choice":"auto"}`),
			want: false,
		},
		{
			name: "1MiBInputLeadingImageTool",
			body: []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation"}],"input":"` + largeInput + `"}`),
			want: true,
		},
		{
			name: "1MiBInputTrailingToolChoice",
			body: []byte(`{"model":"gpt-5.5","tools":[],"input":"` + largeInput + `","tool_choice":{"type":"image_generation"}}`),
			want: true,
		},
		{
			name: "Invalid1MiBJSON",
			body: []byte(`{"model":"gpt-5.5","input":"` + largeInput),
			want: false,
		},
		{
			name: "DuplicateKeysFirstWins",
			body: []byte(`{"model":"gpt-5.5","model":"gpt-image-2","tools":[],"tools":[{"type":"image_generation"}],"input":"` + largeInput + `","tool_choice":"auto","tool_choice":{"type":"image_generation"}}`),
			want: false,
		},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			if got := ImageIntent().IsImageGenerationIntent("/v1/responses", "gpt-5.5", benchmark.body); got != benchmark.want {
				b.Fatalf("ImageIntent().IsImageGenerationIntent() = %v, want %v", got, benchmark.want)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(benchmark.body)))
			b.ResetTimer()
			var result bool
			for range b.N {
				result = ImageIntent().IsImageGenerationIntent("/v1/responses", "gpt-5.5", benchmark.body)
			}
			imageGenerationIntentBenchmarkResult = result
		})
	}
}

// TestIsExplicitImageGenerationIntent_IgnoresPassiveNamespace declares
// image_gen passively with auto choice. Explicit intent is false while the
// broader general intent still recognizes the declaration.
func TestIsExplicitImageGenerationIntent_IgnoresPassiveNamespace(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"input": [{"type":"message","role":"user","content":"hello"}],
		"tools": [
			{"type":"function","name":"Read"},
			{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}
		],
		"tool_choice": "auto"
	}`)

	assert.False(t, ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"passive image_gen namespace should NOT be explicit image intent")

	assert.True(t, ImageIntent().IsImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"passive image_gen namespace SHOULD remain general image intent for forwarding and billing")
}

// TestIsExplicitImageGenerationIntent_DetectsNativeTool declares a hosted
// image-generation tool with auto choice on a text model. Explicit intent
// still returns true for that supported tool form.
func TestIsExplicitImageGenerationIntent_DetectsNativeTool(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"tools": [{"type":"image_generation","model":"gpt-image-2"}],
		"tool_choice": "auto"
	}`)

	assert.True(t, ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"native image_generation tool IS explicit intent")
}

// TestIsExplicitImageGenerationIntent_DetectsImageModel queries Responses
// intent with an image-only model and no body. The explicit predicate
// recognizes that model identity as an image-generation signal.
func TestIsExplicitImageGenerationIntent_DetectsImageModel(t *testing.T) {
	assert.False(t, ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-image-2", nil),
		"model names need final route attributes")
}

// TestIsExplicitImageGenerationIntent_DetectsImageEndpoint queries an
// image-generation endpoint using a text model and no body. The explicit
// predicate recognizes the endpoint as sufficient image-generation intent.
func TestIsExplicitImageGenerationIntent_DetectsImageEndpoint(t *testing.T) {
	assert.True(t, ImageIntent().IsExplicitImageGenerationIntent("/v1/images/generations", "gpt-5.5", nil),
		"image endpoint IS explicit intent")
}

// TestIsExplicitImageGenerationIntent_DetectsExplicitToolChoice selects
// image_generation by string choice beside an ordinary function declaration.
// The explicit predicate reports image intent for the selected choice.
func TestIsExplicitImageGenerationIntent_DetectsExplicitToolChoice(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"Read"}],"tool_choice":"image_generation"}`)
	assert.True(t, ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"explicit tool_choice selecting image_generation IS explicit intent")
}

// TestIsExplicitImageGenerationIntent_PlainTextRequest supplies ordinary hello
// input and a Read function declaration on a GPT text model. The explicit
// image-generation predicate remains false for that request.
func TestIsExplicitImageGenerationIntent_PlainTextRequest(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"input": "hello",
		"tools": [{"type":"function","name":"Read"}]
	}`)

	assert.False(t, ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.5", body),
		"plain text request should NOT be explicit image intent")
}

// TestIsExplicitImageGenerationIntentMap_AfterRequestMutation checks decoded
// passive namespace, hosted tool, and selected namespace shapes. Explicit map
// intent distinguishes the passive declaration from the two active image
// signals.
func TestIsExplicitImageGenerationIntentMap_AfterRequestMutation(t *testing.T) {
	passiveNamespace := map[string]any{
		"model": "gpt-5.5",
		"tools": []any{
			map[string]any{"type": "namespace", "name": "image_gen"},
		},
		"tool_choice": "auto",
	}
	assert.False(t, ImageIntent().IsExplicitImageGenerationIntentMap("/v1/responses", "gpt-5.5", passiveNamespace))

	nativeTool := map[string]any{
		"model": "gpt-5.5",
		"tools": []any{
			map[string]any{"type": "image_generation"},
		},
	}
	assert.True(t, ImageIntent().IsExplicitImageGenerationIntentMap("/v1/responses", "gpt-5.5", nativeTool))

	explicitNamespaceChoice := map[string]any{
		"model":       "gpt-5.5",
		"tool_choice": map[string]any{"type": "namespace", "name": "image_gen"},
	}
	assert.True(t, ImageIntent().IsExplicitImageGenerationIntentMap("/v1/responses", "gpt-5.5", explicitNamespaceChoice))
}

// BenchmarkOpenAIPassthroughImageIntentReuse_LargeBody compares one versus two
// intent scans over the same large request body. Both timed paths retain their
// results in the fixture sink for measurement.
func BenchmarkOpenAIPassthroughImageIntentReuse_LargeBody(b *testing.B) {
	body := buildLargeOpenAIResponsesImageToolBody(32 << 20)

	b.Run("Once", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for range b.N {
			passthroughImageIntentBenchmarkSink = ImageIntent().IsImageGenerationIntent(media.OpenAIResponsesEndpoint, "gpt-5.4", body)
		}
	})

	b.Run("Twice", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for range b.N {
			permissionIntent := ImageIntent().IsImageGenerationIntent(media.OpenAIResponsesEndpoint, "gpt-5.4", body)
			billingIntent := ImageIntent().IsImageGenerationIntent(media.OpenAIResponsesEndpoint, "gpt-5.4", body)
			passthroughImageIntentBenchmarkSink = permissionIntent && billingIntent
		}
	})
}

// TestIsImageGenerationIntentForPlatform_GrokCodexDeclarations compares
// passive Codex declarations, active image choices, vision input, and call
// history on Grok. Platform intent follows active generation signals rather
// than passive or historical tool availability.
func TestIsImageGenerationIntentForPlatform_GrokCodexDeclarations(t *testing.T) {
	tests := []struct {
		name string // image intent scenario label
		body string // request declaration JSON fixture
		want bool   // expected image intent result
	}{
		{
			name: "top-level image_gen namespace is passive",
			body: `{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto","input":"write code"}`,
		},
		{
			name: "responses lite additional_tools image_gen is passive",
			body: `{"model":"grok-4.5","tool_choice":"auto","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]},{"type":"message","role":"user","content":"write code"}]}`,
		},
		{
			name: "legacy adapter flattened image_gen function declaration is passive",
			body: `{"model":"grok-4.5","tools":[{"type":"function","name":"image_gen.imagegen"}],"input":"write code"}`,
		},
		{
			name: "native image generation declaration remains explicit",
			body: `{"model":"grok-4.5","tools":[{"type":"image_generation"}],"input":"draw a cat"}`,
			want: true,
		},
		{
			name: "native image generation tool choice is explicit",
			body: `{"model":"grok-4.5","tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"},"input":"draw a cat"}`,
			want: true,
		},
		{
			name: "image_gen namespace tool choice is explicit",
			body: `{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":{"type":"namespace","name":"image_gen"},"input":"draw a cat"}`,
			want: true,
		},
		{
			name: "flattened image_gen function tool choice is explicit",
			body: `{"model":"grok-4.5","tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":{"type":"function","name":"image_gen.imagegen"},"input":"draw a cat"}`,
			want: true,
		},
		{
			name: "wrapped image_gen function tool choice is explicit",
			body: `{"model":"grok-4.5","tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":{"tool":{"type":"function","name":"image_gen.imagegen"}},"input":"draw a cat"}`,
			want: true,
		},
		{
			name: "vision input is not image generation",
			body: `{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`,
		},
		{
			name: "ordinary function declaration is not image generation",
			body: `{"model":"grok-4.5","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto","input":"write code"}`,
		},
		{
			name: "image_gen function call history is not current intent",
			body: `{"model":"grok-4.5","input":[{"type":"function_call","namespace":"image_gen","name":"imagegen","arguments":"{}"}]}`,
		},
		{
			name: "image generation call history is not current intent",
			body: `{"model":"grok-4.5","input":[{"type":"image_generation_call","id":"ig_1"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ImageIntentForPlatform(
				media.OpenAIResponsesEndpoint,
				"grok-4.5",
				[]byte(tt.body),
				capability.PlatformGrok,
			))
		})
	}
}

// TestIsImageGenerationIntentForPlatform_GrokPreservesHardSignals supplies an
// image endpoint, image-model argument, or body image-model identity under
// Grok. Each independent signal still selects image generation.
func TestIsImageGenerationIntentForPlatform_GrokPreservesHardSignals(t *testing.T) {
	require.True(t, ImageIntentForPlatform(
		"/v1/images/generations",
		"grok-4.5",
		[]byte(`{"input":"draw"}`),
		capability.PlatformGrok,
	))
	require.False(t, ImageIntentForPlatform(
		media.OpenAIResponsesEndpoint,
		"gpt-image-2",
		[]byte(`{"input":"draw"}`),
		capability.PlatformGrok,
	))
	require.False(t, ImageIntentForPlatform(
		media.OpenAIResponsesEndpoint,
		"grok-4.5",
		[]byte(`{"model":"gpt-image-2","input":"draw"}`),
		capability.PlatformGrok,
	))
}

// TestIsImageGenerationIntentForPlatform_OtherPlatformsKeepDeclarationSemantics
// declares the image_gen namespace passively on an ordinary GPT text request.
// Both OpenAI and Anthropic general platform predicates retain
// declaration-based image recognition.
func TestIsImageGenerationIntentForPlatform_OtherPlatformsKeepDeclarationSemantics(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"input":"write code"}`)

	require.True(t, ImageIntentForPlatform(media.OpenAIResponsesEndpoint, "gpt-5.5", body, capability.PlatformOpenAI))
	require.True(t, ImageIntentForPlatform(media.OpenAIResponsesEndpoint, "gpt-5.5", body, capability.PlatformAnthropic))
}

// TestIsImageGenerationIntent varies endpoint/model signals, hosted and
// namespace tools, tool choices, and ordinary/custom declarations. General
// intent accepts the supported image forms and excludes unrelated names or
// text-only shapes.
func TestIsImageGenerationIntent(t *testing.T) {
	tests := []struct {
		name     string // image intent scenario label
		endpoint string // requested client endpoint path
		model    string // requested complete model identity
		body     []byte // serialized intent request fixture
		want     bool   // expected image intent result
	}{
		{
			name:     "images endpoint",
			endpoint: "/v1/images/generations",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     true,
		},
		{
			name:     "image model",
			endpoint: "/v1/responses",
			model:    "gpt-image-2",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     false,
		},
		{
			name:     "image tool",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`),
			want:     true,
		},
		{
			name:     "image tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tool_choice":{"type":"image_generation"}}`),
			want:     true,
		},
		{
			name:     "namespace image_gen tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tool_choice":{"type":"namespace","name":"image_gen"}}`),
			want:     true,
		},
		{
			name:     "custom imagegen function tool choice is not image intent",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tool_choice":{"function":{"name":"imagegen"}}}`),
			want:     false,
		},
		{
			name:     "required tool choice alone is text",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tool_choice":"required"}`),
			want:     false,
		},
		{
			name:     "text only gpt 5.4",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","input":"write code"}`),
			want:     false,
		},
		{
			name:     "namespace image_gen tool in top-level tools",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]}`),
			want:     true,
		},
		{
			name:     "custom namespace with nested imagegen function is not image intent",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"media_tools","tools":[{"type":"function","name":"imagegen"}]}]}`),
			want:     false,
		},
		{
			name:     "namespace image_gen in input additional_tools (Responses Lite)",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]}]}`),
			want:     true,
		},
		{
			name:     "non-image namespace tool is not flagged",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"code_tools","tools":[{"type":"function","name":"run"}]}]}`),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ImageIntent().IsImageGenerationIntent(tt.endpoint, tt.model, tt.body))
		})
	}
}

// TestIsImageGenerationIntentJSONSemantics combines large input,
// duplicate/null keys, invalid JSON, and escaped field names. Detection
// follows first-value JSON view behavior while retaining supported trailing or
// escaped signals.
func TestIsImageGenerationIntentJSONSemantics(t *testing.T) {
	largeInput := strings.Repeat("x", 1<<20)
	tests := []struct {
		name     string // image intent scenario label
		endpoint string // requested client endpoint path
		body     []byte // serialized intent request fixture
		want     bool   // expected image intent result
	}{
		{
			name:     "chat body image model",
			endpoint: "/v1/chat/completions",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     false,
		},
		{
			name:     "large responses input with trailing namespace tool choice",
			endpoint: "/v1/responses",
			body:     []byte(`{"model":"gpt-5.5","input":"` + largeInput + `","tool_choice":{"type":"namespace","name":"image_gen"}}`),
			want:     true,
		},
		{
			name:     "invalid json with image tool",
			endpoint: "/v1/responses",
			body:     []byte(`{"tools":[{"type":"image_generation"}]`),
			want:     false,
		},
		{
			name:     "duplicate model uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"model":"gpt-5.5","model":"gpt-image-2"}`),
			want:     false,
		},
		{
			name:     "duplicate null model still uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"model":null,"model":"gpt-image-2"}`),
			want:     false,
		},
		{
			name:     "duplicate tools uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"tools":[],"tools":[{"type":"image_generation"}]}`),
			want:     false,
		},
		{
			name:     "duplicate input uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"input":[],"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}]}`),
			want:     false,
		},
		{
			name:     "duplicate tool choice uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"tool_choice":"required","tool_choice":{"type":"image_generation"}}`),
			want:     false,
		},
		{
			name:     "escaped top level key",
			endpoint: "/v1/responses",
			body:     []byte(`{"tool_\u0063hoice":{"type":"image_generation"}}`),
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ImageIntent().IsImageGenerationIntent(tt.endpoint, "gpt-5.5", tt.body))
		})
	}
}

// TestIsImageGenerationIntentMap_NamespaceImageGen checks decoded namespace
// tools in top-level/Lite lists and selected choices. Exact image_gen is
// recognized while unrelated namespaces or custom imagegen choices are
// excluded.
func TestIsImageGenerationIntentMap_NamespaceImageGen(t *testing.T) {
	tests := []struct {
		name    string         // decoded namespace scenario label
		reqBody map[string]any // decoded mutable request fixture
		want    bool           // expected image declaration intent
	}{
		{
			name: "top-level namespace image_gen",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{"type": "namespace", "name": "image_gen", "tools": []any{
						map[string]any{"type": "function", "name": "imagegen"},
					}},
				},
			},
			want: true,
		},
		{
			name: "additional_tools in input",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"input": []any{
					map[string]any{
						"type": "additional_tools",
						"tools": []any{
							map[string]any{"type": "namespace", "name": "image_gen"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "custom namespace with nested imagegen function is not image intent",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{
						"type": "namespace",
						"name": "media_tools",
						"tools": []any{
							map[string]any{"type": "function", "name": "imagegen"},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "namespace image_gen tool choice",
			reqBody: map[string]any{
				"model":       "gpt-5.5",
				"tool_choice": map[string]any{"type": "namespace", "name": "image_gen"},
			},
			want: true,
		},
		{
			name: "custom imagegen function tool choice is not image intent",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tool_choice": map[string]any{
					"function": map[string]any{"name": "imagegen"},
				},
			},
			want: false,
		},
		{
			name: "non-image namespace not flagged",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{"type": "namespace", "name": "code_tools"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ImageIntent().IsImageGenerationIntentMap("/v1/responses", "gpt-5.5", tt.reqBody))
		})
	}
}

// TestResolveOpenAIResponsesImageBillingConfigUsesConfirmedModel supplies a
// mapped body model and image tool without its own model. Billing config uses
// the confirmed image identity and derives the 1K size tier.
func TestResolveOpenAIResponsesImageBillingConfigUsesConfirmedModel(t *testing.T) {
	imageModel, imageSize, err := ImageIntent().ResolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"mapped-image-model","tools":[{"type":"image_generation","size":"1024x1024"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "requested-model", imageModel)
	require.Equal(t, "1K", imageSize)
}

// TestResolveOpenAIResponsesImageBillingConfigToolModelWins supplies different
// driver, requested, and image-tool model names. Billing config selects the
// tool model and its tested 2K size.
func TestResolveOpenAIResponsesImageBillingConfigToolModelWins(t *testing.T) {
	imageModel, imageSize, err := ImageIntent().ResolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"mapped-text-model","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1536x1024"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", imageModel)
	require.Equal(t, "2K", imageSize)
}

// TestResolveOpenAIResponsesImageBillingConfigFromBodyIgnoresUnrelatedLargeInput
// includes an extreme numeric nonce in unrelated input beside image model/size
// fields. Billing extraction succeeds from those relevant fields without
// interpreting the unrelated number.
func TestResolveOpenAIResponsesImageBillingConfigFromBodyIgnoresUnrelatedLargeInput(t *testing.T) {
	cfg, err := ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(
		[]byte(`{"model":"mapped-text-model","tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}],"input":[{"type":"message","content":[{"type":"input_text","text":"hi","nonce":1e1000000}]}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", cfg.Model)
	require.Equal(t, "2K", cfg.SizeTier)
	require.Equal(t, "2048x1152", cfg.InputSize)
}

// TestResolveOpenAIResponsesImageBillingConfigSupportsOfficialAndCustomSizes
// compares official, custom, default-tool, and top-level image sizes. Billing
// extraction returns a nonempty model and each expected size tier.
func TestResolveOpenAIResponsesImageBillingConfigSupportsOfficialAndCustomSizes(t *testing.T) {
	tests := []struct {
		name     string // image intent scenario label
		body     []byte // serialized intent request fixture
		wantTier string // expected image billing tier
	}{
		{
			name:     "official 2k landscape",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}]}`),
			wantTier: "2K",
		},
		{
			name:     "official 4k landscape",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-2","size":"3840x2160"}]}`),
			wantTier: "4K",
		},
		{
			name:     "custom valid 2k",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1280x768"}]}`),
			wantTier: "2K",
		},
		{
			name:     "default image tool model supports flexible size",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","size":"2048x1152"}]}`),
			wantTier: "2K",
		},
		{
			name:     "top level image size is moved into billing",
			body:     []byte(`{"model":"gpt-image-2","size":"2048x2048","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`),
			wantTier: "2K",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageModel, imageSize, err := ImageIntent().ResolveOpenAIResponsesImageBillingConfigFromBody(tt.body, "requested-model")
			require.NoError(t, err)
			require.NotEmpty(t, imageModel)
			require.Equal(t, tt.wantTier, imageSize)
		})
	}
}

// TestResolveOpenAIResponsesImageBillingConfigDoesNotRejectUnknownSizes
// supplies a wide size for the tested image model. Billing extraction retains
// the model and classifies 2K without returning an error.
func TestResolveOpenAIResponsesImageBillingConfigDoesNotRejectUnknownSizes(t *testing.T) {
	imageModel, imageSize, err := ImageIntent().ResolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-1.5","size":"2048x1152"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-1.5", imageModel)
	require.Equal(t, "2K", imageSize)
}

// BenchmarkOpenAIResponses_LargeInputImageBillingRaw measures billing field
// extraction for increasing generated body sizes. Each iteration checks errors
// and retains extracted model/tier/input-size values in its sink.
func BenchmarkOpenAIResponses_LargeInputImageBillingRaw(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeOpenAIResponsesImageToolBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				cfg, err := ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, "gpt-5.4")
				if err != nil {
					b.Fatalf("解析 OpenAI 图片计费配置失败: %v", err)
				}
				benchmarkStringSink = cfg.Model + cfg.SizeTier + cfg.InputSize
			}
		})
	}
}

// buildLargeOpenAIResponsesImageToolBody creates an image-tool request with
// repeated user text until the builder reaches targetBytes. Callers supply
// nonnegative fixture sizes. Closing JSON adds further bytes, so the result
// approximates the requested fixture size and can exceed it.
func buildLargeOpenAIResponsesImageToolBody(targetBytes int) []byte {
	// Builder writes assemble fixture JSON in memory; WriteString and WriteByte
	// return nil errors, so discarded errors do not mask external I/O failure here.
	var builder strings.Builder
	builder.Grow(targetBytes + 1024)
	_, _ = builder.WriteString(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}],"input":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"`)
		_, _ = builder.WriteString(strings.Repeat("openai image billing payload ", 48))
		_, _ = builder.WriteString(strconv.Itoa(i))
		_, _ = builder.WriteString(`"}]}`)
	}
	_, _ = builder.WriteString(`]}`)
	return []byte(builder.String())
}

// BenchmarkOpenAIResponsesImageIntentRouting_LargeToolsBody compares reused
// explicit-intent routing with a second scan of unchanged large tools. Setup
// rejects unexpected image intent, and timed results retain their endpoint
// capability.
func BenchmarkOpenAIResponsesImageIntentRouting_LargeToolsBody(b *testing.B) {
	body := buildLargeOpenAIResponsesToolsBody(32 << 20)
	if ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.4", body) {
		b.Fatal("large tools body must not have explicit image intent")
	}
	platform := capability.PlatformOpenAI

	b.Run("reuse_once", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for range b.N {
			imageIntent := ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.4", body)
			openAIResponsesImageIntentRoutingBenchmarkSink = textflow.ResponsesCapability(imageIntent, platform)
		}
	})

	b.Run("rescan_twice", func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for range b.N {
			imageIntent := ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.4", body)
			// The baseline scans the same unchanged body again during route selection.
			requiredCapability := provider.OpenAIEndpointCapabilityTextGeneration
			if ImageIntent().IsExplicitImageGenerationIntent("/v1/responses", "gpt-5.4", body) && platform == capability.PlatformOpenAI {
				requiredCapability = provider.OpenAIEndpointCapabilityResponses
			}
			if imageIntent && requiredCapability != provider.OpenAIEndpointCapabilityResponses {
				b.Fatal("explicit image intent must require Responses")
			}
			openAIResponsesImageIntentRoutingBenchmarkSink = requiredCapability
		}
	})
}

// buildLargeOpenAIResponsesToolsBody builds a function-tool request with
// targetBytes of description text. Fixed JSON structure adds further bytes;
// callers pass nonnegative sizes and receive a request with no explicit image
// tool.
func buildLargeOpenAIResponsesToolsBody(targetBytes int) []byte {
	// Builder writes assemble fixture JSON in memory; WriteString and WriteByte
	// return nil errors, so discarded errors do not mask external I/O failure here.
	var builder strings.Builder
	builder.Grow(targetBytes + 256)
	_, _ = builder.WriteString(`{"model":"gpt-5.4","tools":[{"type":"function","name":"search","description":"`)
	_, _ = builder.WriteString(strings.Repeat("x", targetBytes))
	_, _ = builder.WriteString(`"}],"tool_choice":"auto","input":"write code"}`)
	return []byte(builder.String())
}

// benchmarkBodySizes returns the labelled four-, eight-, sixteen-, and
// thirty-two-MiB fixture targets.
func benchmarkBodySizes() []struct {
	name  string // body size benchmark label
	bytes int    // requested fixture size bytes
} {
	return []struct {
		name  string // body size benchmark label
		bytes int    // requested fixture size bytes
	}{
		{name: "4MB", bytes: 4 << 20},
		{name: "8MB", bytes: 8 << 20},
		{name: "16MB", bytes: 16 << 20},
		{name: "32MB", bytes: 32 << 20},
	}
}
