package session

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// benchmarkOpenAIContentSessionSeed retains the last derived benchmark value
// so the measured seed remains observable after each iteration finishes.
var benchmarkOpenAIContentSessionSeed string

// BenchmarkDeriveOpenAIContentSessionSeedLargeBody derives seeds from Chat and
// Responses requests with large later assistant history. The harness reports
// allocations and payload bytes while retaining each result, measuring
// selection and root scanning for those two prepared bodies.
func BenchmarkDeriveOpenAIContentSessionSeedLargeBody(b *testing.B) {
	// Grow later assistant history so the selected initial user remains fixed
	// while the benchmark still scans a large prepared request representation.
	largeHistory := strings.Repeat("payload", 1<<17)
	tests := []struct {
		name string // named wire-shape comparison fixture
		body []byte // encoded request bytes under comparison
	}{
		{name: "ChatCompletions", body: []byte(`{"model":"gpt-5.4","tools":[{"type":"function","function":{"name":"lookup"}}],"messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"Hello"},{"role":"assistant","content":"` + largeHistory + `"},{"role":"user","content":"Follow-up"}]}`)},
		{name: "Responses", body: []byte(`{"model":"gpt-5.4","instructions":"Be concise.","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System prompt"},{"role":"user","content":"Hello"},{"role":"assistant","content":"` + largeHistory + `"}]}`)},
	}

	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.body)))
			for range b.N {
				benchmarkOpenAIContentSessionSeed = OpenAIContentSeed(test.body)
			}
		})
	}
}

// TestDeriveOpenAIContentSessionSeed_EmptyInputs supplies nil bytes, an empty
// slice, and an empty JSON object. Every case returns an empty seed because
// none supplies a selected root field or content anchor.
func TestDeriveOpenAIContentSessionSeed_EmptyInputs(t *testing.T) {
	require.Empty(t, OpenAIContentSeed(nil))
	require.Empty(t, OpenAIContentSeed([]byte{}))
	require.Empty(t, OpenAIContentSeed([]byte(`{}`)))
}

// TestDeriveOpenAIContentSessionSeed_ModelOnly supplies a request containing a
// model and no user input. The general seed retains its content prefix and
// model label; this case does not call the meaningful-anchor wrapper.
func TestDeriveOpenAIContentSessionSeed_ModelOnly(t *testing.T) {
	seed := OpenAIContentSeed([]byte(`{"model":"gpt-5.4"}`))
	require.Contains(t, seed, contentSessionSeedPrefix)
	require.Contains(t, seed, "model=gpt-5.4")
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_StableAcrossTurns extends
// Chat history after the same leading system and first user message. Both
// nonempty seeds remain equal when later assistant and user messages are
// appended to the selected initial conversation.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_StableAcrossTurns(t *testing.T) {
	turn1 := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	turn2 := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		]
	}`)
	s1 := OpenAIContentSeed(turn1)
	s2 := OpenAIContentSeed(turn2)
	require.Equal(t, s1, s2, "seed should be stable across later turns")
	require.NotEmpty(t, s1)
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_IgnoresLaterSystemMessages
// inserts a system message after user and assistant history. Its seed equals
// the otherwise matching Chat request because the leading instruction prefix
// has already closed.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_IgnoresLaterSystemMessages(t *testing.T) {
	turn1 := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		]
	}`)
	turn2 := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "system", "content": "Return JSON for this turn."},
			{"role": "user", "content": "How are you?"}
		]
	}`)

	require.Equal(t, OpenAIContentSeed(turn1), OpenAIContentSeed(turn2))
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_UsesLeadingSystemDeveloperPrefix
// changes a leading developer message and adds a later system item. The
// leading developer change alters the seed, while the later system item leaves
// the original selected prefix unchanged.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_UsesLeadingSystemDeveloperPrefix(t *testing.T) {
	firstSystem := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "System A"},
			{"role": "developer", "content": "Developer B"},
			{"role": "user", "content": "Hello"}
		]
	}`)
	changedLaterSystem := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "System A"},
			{"role": "developer", "content": "Developer C"},
			{"role": "user", "content": "Hello"}
		]
	}`)

	seed := OpenAIContentSeed(firstSystem)
	require.Contains(t, seed, "System A")
	require.Contains(t, seed, "Developer B")
	require.NotEqual(t, seed, OpenAIContentSeed(changedLaterSystem))

	withLaterSystem := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "System A"},
			{"role": "developer", "content": "Developer B"},
			{"role": "user", "content": "Hello"},
			{"role": "system", "content": "Dynamic system"}
		]
	}`)
	require.Equal(t, seed, OpenAIContentSeed(withLaterSystem))
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentFirstUserDiffers
// changes only the initial user question under equal model. The two supplied
// question fixtures produce different seeds, exposing the selected first-user
// contribution in this comparison.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentFirstUserDiffers(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Question A"}]}`)
	req2 := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Question B"}]}`)
	s1 := OpenAIContentSeed(req1)
	s2 := OpenAIContentSeed(req2)
	require.NotEqual(t, s1, s2)
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentSystemDiffers
// changes only the leading system message under equal model and user text. The
// resulting seeds differ for those two instruction fixtures before the first
// user anchor.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentSystemDiffers(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"A"},{"role":"user","content":"Hi"}]}`)
	req2 := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"B"},{"role":"user","content":"Hi"}]}`)
	s1 := OpenAIContentSeed(req1)
	s2 := OpenAIContentSeed(req2)
	require.NotEqual(t, s1, s2)
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentModelDiffers
// changes the root model with an unchanged initial user message. The seeds
// differ for the two model labels, demonstrating that general content identity
// includes supplied model text.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentModelDiffers(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hi"}]}`)
	req2 := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`)
	s1 := OpenAIContentSeed(req1)
	s2 := OpenAIContentSeed(req2)
	require.NotEqual(t, s1, s2)
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_WithTools compares Chat
// requests with and without one tool definition. The tool-bearing seed
// contains its tools label and differs from the otherwise equal request
// lacking that definition.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_WithTools(t *testing.T) {
	withTools := []byte(`{
		"model": "gpt-5.4",
		"tools": [{"type":"function","function":{"name":"get_weather"}}],
		"messages": [{"role": "user", "content": "Hello"}]
	}`)
	withoutTools := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role": "user", "content": "Hello"}]
	}`)
	s1 := OpenAIContentSeed(withTools)
	s2 := OpenAIContentSeed(withoutTools)
	require.NotEqual(t, s1, s2, "tools should affect the seed")
	require.Contains(t, s1, "|tools=")
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_WithFunctions supplies a
// legacy functions array in a Chat-shaped request. The resulting seed contains
// the functions label, covering that compatibility field in the selected root
// data.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_WithFunctions(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"functions": [{"name":"get_weather","parameters":{}}],
		"messages": [{"role": "user", "content": "Hello"}]
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|functions=")
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_DeveloperRole places a
// developer message before the first Chat user item. The seed includes system
// and first-user labels for this fixture, showing the developer item enters
// the leading instruction representation.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DeveloperRole(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "developer", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|system=")
	require.Contains(t, seed, "|first_user=")
}

// TestDeriveOpenAIContentSessionSeed_ChatCompletions_StructuredContent
// supplies the first Chat user message as a text-block array. The resulting
// seed is nonempty and contains the first-user label for that structured
// content representation.
func TestDeriveOpenAIContentSessionSeed_ChatCompletions_StructuredContent(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "user", "content": [{"type":"text","text":"Hello"}]}
		]
	}`)
	seed := OpenAIContentSeed(body)
	require.NotEmpty(t, seed)
	require.Contains(t, seed, "|first_user=")
}

// TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputString supplies a model
// and a Responses string input. The resulting seed retains the asserted input
// label and decoded text, covering the direct string-input representation.
func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputString(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"Hello, how are you?"}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|input=Hello, how are you?")
}

// TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputArray supplies system
// and user roles in a Responses input array. The seed contains system and
// first-user labels for those selected items, covering array-shaped initial
// conversation data.
func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputArray(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|system=")
	require.Contains(t, seed, "|first_user=")
}

// TestDeriveOpenAIContentSessionSeed_ResponsesAPI_WithInstructions combines
// root instructions and a Responses input string. The result contains both
// asserted labels and supplied text, so those separate root fields each
// contribute to the seed.
func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_WithInstructions(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"instructions": "You are a coding assistant.",
		"input": "Write a hello world"
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|instructions=You are a coding assistant.")
	require.Contains(t, seed, "|input=Write a hello world")
}

// TestDeriveOpenAIContentSessionSeed_Deterministic derives content identity
// twice from the same Chat request bytes. The two seed values remain equal for
// this supplied model, leading system message, and initial user text.
func TestDeriveOpenAIContentSessionSeed_Deterministic(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	s1 := OpenAIContentSeed(body)
	s2 := OpenAIContentSeed(body)
	require.Equal(t, s1, s2, "seed must be deterministic")
}

// TestDeriveOpenAIContentSessionSeed_PrefixPresent derives a seed from one
// Chat user message and model. The seed contains data beyond its prefix and
// begins with the exact content-session label checked by the fixture.
func TestDeriveOpenAIContentSessionSeed_PrefixPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hi"}]}`)
	seed := OpenAIContentSeed(body)
	require.True(t, len(seed) > len(contentSessionSeedPrefix))
	require.Equal(t, contentSessionSeedPrefix, seed[:len(contentSessionSeedPrefix)])
}

// TestDeriveOpenAIContentSessionSeed_EmptyToolsIgnored supplies the compact
// empty tools array alongside a user message. The generated seed has no tools
// label for this exact [] representation; other whitespace-shaped arrays are
// outside this assertion.
func TestDeriveOpenAIContentSessionSeed_EmptyToolsIgnored(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[],"messages":[{"role":"user","content":"Hi"}]}`)
	seed := OpenAIContentSeed(body)
	require.NotContains(t, seed, "|tools=")
}

// TestDeriveOpenAIContentSessionSeed_MessagesPreferredOverInput supplies both
// a messages array and a Responses input string. The seed contains a
// first-user label from messages and omits the direct input label, exposing
// representation priority.
func TestDeriveOpenAIContentSessionSeed_MessagesPreferredOverInput(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role": "user", "content": "from messages"}],
		"input": "from input"
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|first_user=")
	require.NotContains(t, seed, "|input=")
}

// TestDeriveOpenAIContentSessionSeed_JSONCanonicalisation formats the same
// tool definition and user content with different spacing and property order.
// Both selected seeds remain equal after their structured fields use the
// shared JSON normalizer.
func TestDeriveOpenAIContentSessionSeed_JSONCanonicalisation(t *testing.T) {
	compact := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather"}}],"messages":[{"role":"user","content":"Hi"}]}`)
	spaced := []byte(`{
		"model": "gpt-5.4",
		"tools": [
			{ "type" : "function", "function": { "description": "Get weather", "name": "get_weather" } }
		],
		"messages": [ { "role": "user", "content": "Hi" } ]
	}`)
	s1 := OpenAIContentSeed(compact)
	s2 := OpenAIContentSeed(spaced)
	require.Equal(t, s1, s2, "different formatting of identical JSON should produce the same seed")
}

// TestDeriveOpenAIContentSessionSeed_SingleScanMatchesLegacyBytes compares
// valid, duplicate-key, malformed, and large-body fixtures with the
// repeated-lookup reference. The single root scan produces exactly the
// reference seed for each listed byte representation, including trailing and
// prefixed fragments.
func TestDeriveOpenAIContentSessionSeed_SingleScanMatchesLegacyBytes(t *testing.T) {
	// Place a large unrelated metadata value before selected fields to exercise
	// root scanning while the expected seed still follows the small chosen content.
	largeValue := strings.Repeat("payload", 1<<17)
	tests := []struct {
		name string // named wire-shape comparison fixture
		body []byte // encoded request bytes under comparison
	}{
		{
			name: "large chat completions",
			body: []byte(`{"metadata":"` + largeValue + `","model":"gpt-5.4","tools":[{"type":"function","function":{"name":"lookup"}}],"functions":[{"name":"legacy_lookup"}],"messages":[{"role":"system","content":"System prompt"},{"role":"developer","content":[{"type":"text","text":"Developer prompt"}]},{"role":"user","content":"Hello"}]}`),
		},
		{
			name: "large responses",
			body: []byte(`{"metadata":"` + largeValue + `","model":"gpt-5.4","instructions":"Be concise.","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System prompt"},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`),
		},
		{
			name: "fields in reverse order",
			body: []byte(`{"input":"fallback input","messages":[{"role":"user","content":"chat wins"}],"instructions":"Be concise.","functions":[{"name":"lookup"}],"tools":[{"type":"function","name":"lookup"}],"model":"gpt-5.4"}`),
		},
		{
			name: "missing and wrong type fields",
			body: []byte(`{"tools":[],"functions":null,"instructions":0,"messages":{},"input":[{"type":"input_text","text":"fallback"}]}`),
		},
		{
			name: "duplicate fields keep first value",
			body: []byte(`{"model":"first","model":"second","tools":[{"name":"first"}],"tools":[{"name":"second"}],"functions":[],"functions":[{"name":"second"}],"instructions":"first","instructions":"second","messages":null,"messages":[{"role":"user","content":"second"}],"input":"first input","input":"second input"}`),
		},
		{
			name: "escaped field names",
			body: []byte(`{"mo\u0064el":"gpt-5.4","mess\u0061ges":[{"role":"user","content":"Hello"}]}`),
		},
		{
			name: "trailing object fields are outside the root",
			body: []byte(`{"foo":1}{"model":"trailing","input":"trailing input"}`),
		},
		{
			name: "trailing quoted fields are outside the root",
			body: []byte(`{"model":"root"}"input":"trailing input"`),
		},
		{
			name: "leading garbage before the root",
			body: []byte(`garbage{"model":"gpt-5.4","input":"Hello"}`),
		},
		{
			name: "escaped braces remain inside string values",
			body: []byte(`{"metadata":"escaped } and [ and \" quote","model":"root","input":"Hello"}{"model":"trailing"}`),
		},
		{
			name: "nested braces do not end the root",
			body: []byte(`{"metadata":{"nested":"} ]"},"model":"root","input":"Hello"}{"model":"trailing"}`),
		},
		{
			name: "root array does not expose nested or trailing object fields",
			body: []byte(`[{"model":"nested"}]{"model":"trailing","input":"trailing input"}`),
		},
		{
			name: "trailing messages do not override root input",
			body: []byte(`{"input":"root input"}{"messages":[{"role":"user","content":"trailing"}]}`),
		},
		{
			name: "truncated string containing a closing brace",
			body: []byte(`{"model":"root","metadata":"still } inside`),
		},
		{
			name: "lenient truncated body",
			body: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hello"}]`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, referenceDeriveOpenAIContentSessionSeed(test.body), OpenAIContentSeed(test.body))
		})
	}
}

// TestDeriveOpenAIContentSessionSeed_AllTruncationOffsetsMatchLegacyBytes
// compares every nonempty proper byte prefix of two request fixtures. Each
// truncated seed equals the repeated-lookup reference, covering all truncation
// positions within those two supplied bodies.
func TestDeriveOpenAIContentSessionSeed_AllTruncationOffsetsMatchLegacyBytes(t *testing.T) {
	bodies := []string{
		`{"model":"gpt-5.4","tools":[{"type":"function","function":{"name":"lookup"}}],"functions":[{"name":"legacy"}],"instructions":"escaped \" } text","messages":[{"role":"system","content":"System"},{"role":"user","content":[{"type":"text","text":"Hello"}]}],"input":"fallback"}`,
		`{"model":"gpt-5.4","instructions":"Be concise.","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System"},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`,
	}
	for bodyIndex, body := range bodies {
		for end := 1; end < len(body); end++ {
			truncated := []byte(body[:end])
			require.Equalf(t, referenceDeriveOpenAIContentSessionSeed(truncated), OpenAIContentSeed(truncated), "body %d truncated at byte %d", bodyIndex, end)
		}
	}
}

// TestDeriveOpenAIContentSessionSeed_DeterministicMalformedCorpusMatchesLegacy
// generates five thousand short byte fixtures from a fixed pseudo-random seed
// and alphabet. Each extracted seed equals the repeated-lookup reference; this
// finite corpus checks tolerant parsing rather than complete JSON validation.
func TestDeriveOpenAIContentSessionSeed_DeterministicMalformedCorpusMatchesLegacy(t *testing.T) {
	// A fixed random seed makes malformed-byte comparisons reproducible when
	// the single-scan extraction differs from the tolerant repeated-lookup reference.
	rng := rand.New(rand.NewSource(4274))
	alphabet := []byte(`{}[]":,modeltfsuinpcr0123456789 \\`)
	for caseIndex := range 5000 {
		body := make([]byte, rng.Intn(256))
		for i := range body {
			body[i] = alphabet[rng.Intn(len(alphabet))]
		}
		require.Equalf(
			t,
			referenceDeriveOpenAIContentSessionSeed(body), OpenAIContentSeed(body), "malformed case %d: %q",
			caseIndex,
			body,
		)
	}
}

// TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputTextTypedItem supplies
// a standalone input_text item in Responses history. The seed contains the
// first-user label and supplied text, covering type-based anchoring when a
// role is absent.
func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputTextTypedItem(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [{"type": "input_text", "text": "Hello world"}]
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|first_user=")
	require.Contains(t, seed, "Hello world")
}

// TestDeriveOpenAIContentSessionSeed_ResponsesAPI_TypedMessageItem supplies a
// type=message item with user role and string content. The seed contains the
// first-user label and asserted text from this compatible Responses message
// representation.
func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_TypedMessageItem(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [{"type": "message", "role": "user", "content": "Hello from typed message"}]
	}`)
	seed := OpenAIContentSeed(body)
	require.Contains(t, seed, "|first_user=")
	require.Contains(t, seed, "Hello from typed message")
}

// TestDeriveOpenAIStablePrefixSessionSeed_IgnoresUserContent changes a user
// question while retaining instructions and the normalized tool definition.
// The nonempty stable-prefix seeds remain equal and exclude the tested
// question text and first-user label.
func TestDeriveOpenAIStablePrefixSessionSeed_IgnoresUserContent(t *testing.T) {
	first := []byte(`{
		"model": "grok",
		"instructions": "Be concise.",
		"tools": [{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"input": [{"role":"user","content":"Question A"}]
	}`)
	second := []byte(`{
		"model": "grok",
		"instructions": "Be concise.",
		"tools": [{"parameters":{"type":"object"},"name":"lookup","type":"function"}],
		"input": [{"role":"user","content":"Question B"}]
	}`)

	firstSeed := OpenAIStablePrefixSeed(first)
	secondSeed := OpenAIStablePrefixSeed(second)

	require.NotEmpty(t, firstSeed)
	require.Equal(t, firstSeed, secondSeed)
	require.NotContains(t, firstSeed, "Question A")
	require.NotContains(t, firstSeed, "first_user")
}

// TestDeriveOpenAIStablePrefixSessionSeed_IsolatesStablePrefixFields changes
// instructions, tools, or system text independently. Each changed fixture
// differs from the base seed, showing all three selected reusable-field
// contributions in those comparisons.
func TestDeriveOpenAIStablePrefixSessionSeed_IsolatesStablePrefixFields(t *testing.T) {
	base := []byte(`{
		"instructions":"Be concise.",
		"tools":[{"type":"function","name":"lookup"}],
		"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question"}]
	}`)
	differentInstructions := []byte(`{
		"instructions":"Be detailed.",
		"tools":[{"type":"function","name":"lookup"}],
		"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question"}]
	}`)
	differentTools := []byte(`{
		"instructions":"Be concise.",
		"tools":[{"type":"function","name":"search"}],
		"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question"}]
	}`)
	differentSystem := []byte(`{
		"instructions":"Be concise.",
		"tools":[{"type":"function","name":"lookup"}],
		"input":[{"role":"system","content":"System B"},{"role":"user","content":"Question"}]
	}`)

	baseSeed := OpenAIStablePrefixSeed(base)
	require.NotEqual(t, baseSeed, OpenAIStablePrefixSeed(differentInstructions))
	require.NotEqual(t, baseSeed, OpenAIStablePrefixSeed(differentTools))
	require.NotEqual(t, baseSeed, OpenAIStablePrefixSeed(differentSystem))
}

// TestDeriveOpenAIStablePrefixSessionSeed_ChatSystemAndDeveloper compares
// matching Chat system/developer data with different user questions and
// property order. The seeds remain equal and contain both instruction texts,
// covering reusable roles while the user question changes.
func TestDeriveOpenAIStablePrefixSessionSeed_ChatSystemAndDeveloper(t *testing.T) {
	first := []byte(`{
		"messages":[
			{"role":"system","content":"System prompt"},
			{"role":"developer","content":[{"type":"text","text":"Developer prompt"}]},
			{"role":"user","content":"Question A"}
		]
	}`)
	second := []byte(`{
		"messages":[
			{"role":"system","content":"System prompt"},
			{"role":"developer","content":[{"text":"Developer prompt","type":"text"}]},
			{"role":"user","content":"Question B"}
		]
	}`)

	firstSeed := OpenAIStablePrefixSeed(first)
	require.Equal(t, firstSeed, OpenAIStablePrefixSeed(second))
	require.Contains(t, firstSeed, "System prompt")
	require.Contains(t, firstSeed, "Developer prompt")
}

// TestDeriveOpenAIStablePrefixSessionSeed_EncodesSystemAndDeveloperRoles swaps
// system and developer labels around equal positional text. The seeds differ
// and retain their respective role labels, keeping those instruction
// identities distinct in the selected prefix.
func TestDeriveOpenAIStablePrefixSessionSeed_EncodesSystemAndDeveloperRoles(t *testing.T) {
	systemThenDeveloper := []byte(`{
		"messages":[
			{"role":"system","content":"Prompt A"},
			{"role":"developer","content":"Prompt B"}
		]
	}`)
	developerThenSystem := []byte(`{
		"messages":[
			{"role":"developer","content":"Prompt A"},
			{"role":"system","content":"Prompt B"}
		]
	}`)

	firstSeed := OpenAIStablePrefixSeed(systemThenDeveloper)
	secondSeed := OpenAIStablePrefixSeed(developerThenSystem)

	require.NotEqual(t, firstSeed, secondSeed)
	require.Contains(t, firstSeed, "|system=")
	require.Contains(t, firstSeed, "|developer=")
}

// TestDeriveOpenAIStablePrefixSessionSeed_EncodesInstructionDelimiters
// compares delimiter-looking instruction text with separate instructions and
// system content. Both seeds are nonempty and differ because JSON encoding
// keeps the embedded label text inside its instruction value.
func TestDeriveOpenAIStablePrefixSessionSeed_EncodesInstructionDelimiters(t *testing.T) {
	instructionOnly := []byte(`{
		"instructions":"foo|system=\"bar\""
	}`)
	instructionAndSystem := []byte(`{
		"instructions":"foo",
		"input":[{"role":"system","content":"bar"}]
	}`)

	firstSeed := OpenAIStablePrefixSeed(instructionOnly)
	secondSeed := OpenAIStablePrefixSeed(instructionAndSystem)

	require.NotEmpty(t, firstSeed)
	require.NotEmpty(t, secondSeed)
	require.NotEqual(t, firstSeed, secondSeed)
}

// TestDeriveOpenAIAnchoredContentSessionSeed_RequiresMeaningfulAnchor compares
// model-only, assistant-only, blank, and readable first-user inputs. Empty
// anchor fixtures return empty and the supplied nonblank string or block
// fixtures return nonempty seeds.
func TestDeriveOpenAIAnchoredContentSessionSeed_RequiresMeaningfulAnchor(t *testing.T) {
	emptyAnchors := [][]byte{
		nil,
		[]byte(`{"model":"grok"}`),
		[]byte(`{"model":"grok","messages":[{"role":"assistant","content":"answer"}]}`),
		[]byte(`{"model":"grok","messages":[{"role":"user","content":"  "}]}`),
		[]byte(`{"model":"grok","messages":[{"role":"user","content":[{"type":"text","text":""}]}]}`),
		[]byte(`{"model":"grok","input":"  "}`),
		[]byte(`{"model":"grok","input":[{"type":"input_text","text":""}]}`),
	}
	for _, body := range emptyAnchors {
		require.Empty(t, OpenAIAnchoredContentSeed(body))
	}

	meaningfulAnchors := [][]byte{
		[]byte(`{"model":"grok","messages":[{"role":"user","content":"question"}]}`),
		[]byte(`{"model":"grok","messages":[{"role":"user","content":[{"type":"text","text":"question"}]}]}`),
		[]byte(`{"model":"grok","input":"question"}`),
		[]byte(`{"model":"grok","input":[{"type":"input_text","text":"question"}]}`),
	}
	for _, body := range meaningfulAnchors {
		require.NotEmpty(t, OpenAIAnchoredContentSeed(body))
	}
}

// TestDeriveOpenAIStablePrefixSessionSeed_RequiresMeaningfulPrefix supplies
// absent fields, user-only content, and empty reusable values. Every fixture
// returns an empty stable-prefix seed, leaving the consumer to select more
// specific affinity evidence.
func TestDeriveOpenAIStablePrefixSessionSeed_RequiresMeaningfulPrefix(t *testing.T) {
	tests := [][]byte{
		nil,
		[]byte(`{}`),
		[]byte(`{"model":"grok","input":"Question A"}`),
		[]byte(`{"model":"grok","tools":[],"input":"Question A"}`),
		[]byte(`{"model":"grok","functions":[],"instructions":"  ","messages":[{"role":"system","content":""},{"role":"user","content":"Question A"}]}`),
	}

	for _, body := range tests {
		require.Empty(t, OpenAIStablePrefixSeed(body))
	}
}

// referenceDeriveOpenAIContentSessionSeed derives the fixture oracle by
// separate gjson lookups for each root field. Empty bytes or no selected data
// returns empty; model-only requests can produce a labelled seed. It follows
// the same Chat leading-instruction and Responses first-user rules expected
// from the single scan.
//
// Structured tools, functions, and content use NormalizeCompatSeedJSON.
// Duplicate and malformed field extraction follows gjson's tolerant individual
// lookups, letting comparisons detect a root scan that accepts trailing fields
// or chooses a later duplicate differently. The helper produces expected seed
// bytes rather than validating the request, and callers keep fixture bytes
// stable during lookup.
func referenceDeriveOpenAIContentSessionSeed(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var b strings.Builder

	if model := gjson.GetBytes(body, "model").String(); model != "" {
		_, _ = b.WriteString("model=")
		_, _ = b.WriteString(model)
	}

	// Use the shared normalizer for structured tools and legacy functions so
	// expected bytes follow the production seed's property-order and whitespace rules.
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() && tools.Raw != "[]" {
		_, _ = b.WriteString("|tools=")
		_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(tools.Raw)))
	}

	if funcs := gjson.GetBytes(body, "functions"); funcs.Exists() && funcs.IsArray() && funcs.Raw != "[]" {
		_, _ = b.WriteString("|functions=")
		_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(funcs.Raw)))
	}

	if instr := gjson.GetBytes(body, "instructions").String(); instr != "" {
		_, _ = b.WriteString("|instructions=")
		_, _ = b.WriteString(instr)
	}

	firstUserCaptured := false

	// A messages array wins even when empty. Keep the oracle's Chat scan
	// separate from input so combined representations retain the production priority.
	msgs := gjson.GetBytes(body, "messages")
	if msgs.Exists() && msgs.IsArray() {
		systemPrefixOpen := true
		// Include normalized leading system/developer content and the first user.
		// Any other role closes that prefix, excluding later Chat instructions from expected identity.
		msgs.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			switch role {
			case "system", "developer":
				if systemPrefixOpen {
					_, _ = b.WriteString("|system=")
					if c := msg.Get("content"); c.Exists() {
						_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
					}
				}
			case "user":
				systemPrefixOpen = false
				if !firstUserCaptured {
					_, _ = b.WriteString("|first_user=")
					if c := msg.Get("content"); c.Exists() {
						_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
					}
					firstUserCaptured = true
				}
			default:
				systemPrefixOpen = false
			}
			return true
		})
	} else if inp := gjson.GetBytes(body, "input"); inp.Exists() {
		if inp.Type == gjson.String {
			_, _ = b.WriteString("|input=")
			_, _ = b.WriteString(inp.String())
		} else if inp.IsArray() {
			// Retain Responses system/developer items throughout input while capturing
			// its first user or input_text item, matching the seed's distinct Responses anchoring rule.
			inp.ForEach(func(_, item gjson.Result) bool {
				role := item.Get("role").String()
				switch role {
				case "system", "developer":
					_, _ = b.WriteString("|system=")
					if c := item.Get("content"); c.Exists() {
						_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
					}
				case "user":
					if !firstUserCaptured {
						_, _ = b.WriteString("|first_user=")
						if c := item.Get("content"); c.Exists() {
							_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
						}
						firstUserCaptured = true
					}
				}
				if !firstUserCaptured && item.Get("type").String() == "input_text" {
					_, _ = b.WriteString("|first_user=")
					if text := item.Get("text").String(); text != "" {
						_, _ = b.WriteString(text)
					}
					firstUserCaptured = true
				}
				return true
			})
		}
	}

	if b.Len() == 0 {
		return ""
	}
	return contentSessionSeedPrefix + b.String()
}
