package openai

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	// benchmarkToolContinuationBoolSink retains each policy result after its
	// timed invocation, leaving the latest boolean available after the benchmark loop.
	benchmarkToolContinuationBoolSink bool

	// benchmarkIntSink counts large-history validation iterations that reported
	// tool output and complete reference coverage before the loop continued.
	benchmarkIntSink int
)

// BenchmarkToolContinuationValidationLegacy measures the multi-pass benchmark
// policy on a mixed text/call/output/reference history. Fixture construction
// precedes timing, and allocation reporting includes each validation whose
// result reaches the shared sink.
func BenchmarkToolContinuationValidationLegacy(b *testing.B) {
	reqBody := benchmarkToolContinuationRequestBody()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkToolContinuationBoolSink = legacyValidateFunctionCallOutputContext(reqBody)
	}
}

// BenchmarkToolContinuationValidationOptimized measures the production-flag
// benchmark policy on the same mixed history shape. Fixture construction
// precedes timing, and allocation reporting includes each validation whose
// result reaches the shared sink.
func BenchmarkToolContinuationValidationOptimized(b *testing.B) {
	reqBody := benchmarkToolContinuationRequestBody()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkToolContinuationBoolSink = optimizedValidateFunctionCallOutputContext(reqBody)
	}
}

// TestNeedsToolContinuationSignals supplies independent previous-ID, tool,
// choice, and input-item signals alongside empty/unsupported forms. Each
// fixture must match its expected continuation-presence result.
func TestNeedsToolContinuationSignals(t *testing.T) {
	cases := []struct {
		name string         // continuation fixture or size label
		body map[string]any // decoded request input fixture
		want bool           // expected continuation signal flag
	}{
		{name: "nil", body: nil, want: false},
		{name: "previous_response_id", body: map[string]any{"previous_response_id": "resp_1"}, want: true},
		{name: "previous_response_id_blank", body: map[string]any{"previous_response_id": "  "}, want: false},
		{name: "function_call_output", body: map[string]any{"input": []any{map[string]any{"type": "function_call_output"}}}, want: true},
		{name: "tool_search_output", body: map[string]any{"input": []any{map[string]any{"type": "tool_search_output"}}}, want: true},
		{name: "custom_tool_call_output", body: map[string]any{"input": []any{map[string]any{"type": "custom_tool_call_output"}}}, want: true},
		{name: "mcp_tool_call_output", body: map[string]any{"input": []any{map[string]any{"type": "mcp_tool_call_output"}}}, want: true},
		{name: "item_reference", body: map[string]any{"input": []any{map[string]any{"type": "item_reference"}}}, want: true},
		{name: "tools", body: map[string]any{"tools": []any{map[string]any{"type": "function"}}}, want: true},
		{name: "tools_empty", body: map[string]any{"tools": []any{}}, want: false},
		{name: "tools_invalid", body: map[string]any{"tools": "bad"}, want: false},
		{name: "tool_choice", body: map[string]any{"tool_choice": "auto"}, want: true},
		{name: "tool_choice_object", body: map[string]any{"tool_choice": map[string]any{"type": "function"}}, want: true},
		{name: "tool_choice_empty_object", body: map[string]any{"tool_choice": map[string]any{}}, want: false},
		{name: "none", body: map[string]any{"input": []any{map[string]any{"type": "text", "text": "hi"}}}, want: false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NeedsToolContinuation(tt.body))
		})
	}
}

// TestHasFunctionCallOutput supplies all four recognized Codex output labels
// in decoded arrays. Their presence is true, while nil requests and a string
// input container produce false.
func TestHasFunctionCallOutput(t *testing.T) {
	require.False(t, HasFunctionCallOutput(nil))
	for _, typ := range []string{
		"function_call_output",
		"tool_search_output",
		"custom_tool_call_output",
		"mcp_tool_call_output",
	} {
		require.True(t, HasFunctionCallOutput(map[string]any{
			"input": []any{map[string]any{"type": typ}},
		}), typ)
	}
	require.False(t, HasFunctionCallOutput(map[string]any{
		"input": "text",
	}))
}

// TestHasToolCallContext supplies six recognized call-context labels with
// nonblank IDs. All qualify, while nil requests and a call lacking an ID
// produce false.
func TestHasToolCallContext(t *testing.T) {
	require.False(t, AnalyzeToolContinuationSignals(nil).HasToolCallContext)
	for _, typ := range []string{
		"tool_call",
		"function_call",
		"local_shell_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call",
	} {
		require.True(t, AnalyzeToolContinuationSignals(map[string]any{
			"input": []any{map[string]any{"type": typ, "call_id": "call_1"}},
		}).HasToolCallContext, typ)
	}
	require.False(t, AnalyzeToolContinuationSignals(map[string]any{
		"input": []any{map[string]any{"type": "tool_call"}},
	}).HasToolCallContext)
}

// TestFunctionCallOutputCallIDs mixes all four output kinds with an empty ID
// and a duplicate. The returned set contains the four expected distinct IDs,
// independent of order.
func TestFunctionCallOutputCallIDs(t *testing.T) {
	require.Empty(t, AnalyzeToolContinuationSignals(nil).FunctionCallOutputCallIDs)
	callIDs := AnalyzeToolContinuationSignals(map[string]any{
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
			map[string]any{"type": "tool_search_output", "call_id": "call_search"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_custom"},
			map[string]any{"type": "mcp_tool_call_output", "call_id": "call_mcp"},
			map[string]any{"type": "function_call_output", "call_id": ""},
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
		},
	}).FunctionCallOutputCallIDs
	require.ElementsMatch(t, []string{"call_1", "call_search", "call_custom", "call_mcp"}, callIDs)
}

// TestHasFunctionCallOutputMissingCallID supplies missing-ID
// function/discovery outputs and a discovery output with a valid ID. The
// missing fixtures qualify, while nil and the valid-ID case remain false.
func TestHasFunctionCallOutputMissingCallID(t *testing.T) {
	require.False(t, AnalyzeToolContinuationSignals(nil).HasFunctionCallOutputMissingCallID)
	require.True(t, AnalyzeToolContinuationSignals(map[string]any{
		"input": []any{map[string]any{"type": "function_call_output"}},
	}).HasFunctionCallOutputMissingCallID)
	require.True(t, AnalyzeToolContinuationSignals(map[string]any{
		"input": []any{map[string]any{"type": "tool_search_output"}},
	}).HasFunctionCallOutputMissingCallID)
	require.False(t, AnalyzeToolContinuationSignals(map[string]any{
		"input": []any{map[string]any{"type": "tool_search_output", "call_id": "call_1"}},
	}).HasFunctionCallOutputMissingCallID)
}

// TestHasItemReferenceForCallIDs compares supplied call-ID lists with two
// available references. Single/full subsets pass, while absent input and a
// list containing an unmatched ID fail.
func TestHasItemReferenceForCallIDs(t *testing.T) {
	require.False(t, HasItemReferenceForCallIDs(nil, []string{"call_1"}))
	require.False(t, HasItemReferenceForCallIDs(map[string]any{}, []string{"call_1"}))
	req := map[string]any{
		"input": []any{
			map[string]any{"type": "item_reference", "id": "call_1"},
			map[string]any{"type": "item_reference", "id": "call_2"},
		},
	}
	require.True(t, HasItemReferenceForCallIDs(req, []string{"call_1"}))
	require.True(t, HasItemReferenceForCallIDs(req, []string{"call_1", "call_2"}))
	require.False(t, HasItemReferenceForCallIDs(req, []string{"call_1", "call_3"}))
}

// TestValidateFunctionCallOutputContextBytesMatchesMapValidation serializes
// seven decoded fixtures covering missing input/IDs, reference coverage,
// context, and all output kinds. Raw and map preliminary result structs must
// agree for these supplied cases.
func TestValidateFunctionCallOutputContextBytesMatchesMapValidation(t *testing.T) {
	// Encode these decoded fixtures before comparing their result structs.
	// This table covers handler-style cases with string IDs and no early-scan missing-ID divergence.
	cases := []struct {
		name string         // continuation fixture or size label
		body map[string]any // decoded request input fixture
	}{
		{
			name: "no_input",
			body: map[string]any{"model": "gpt-5.4"},
		},
		{
			name: "missing_call_id",
			body: map[string]any{"input": []any{map[string]any{"type": "function_call_output"}}},
		},
		{
			name: "call_id_without_reference",
			body: map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "call_1"}}},
		},
		{
			name: "matching_reference",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call_output", "call_id": "call_1"},
				map[string]any{"type": "item_reference", "id": "call_1"},
			}},
		},
		{
			name: "partial_reference",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call_output", "call_id": "call_1"},
				map[string]any{"type": "tool_search_output", "call_id": "call_2"},
				map[string]any{"type": "item_reference", "id": "call_1"},
			}},
		},
		{
			name: "tool_context",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call_output", "call_id": "call_1"},
				map[string]any{"type": "function_call", "call_id": "call_1"},
			}},
		},
		{
			name: "all_codex_tool_outputs",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call_output", "call_id": "call_function"},
				map[string]any{"type": "tool_search_output", "call_id": "call_search"},
				map[string]any{"type": "custom_tool_call_output", "call_id": "call_custom"},
				map[string]any{"type": "mcp_tool_call_output", "call_id": "call_mcp"},
				map[string]any{"type": "item_reference", "id": "call_function"},
				map[string]any{"type": "item_reference", "id": "call_search"},
				map[string]any{"type": "item_reference", "id": "call_custom"},
				map[string]any{"type": "item_reference", "id": "call_mcp"},
			}},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes, err := json.Marshal(tt.body)
			require.NoError(t, err)

			require.Equal(t, ValidateFunctionCallOutputContext(tt.body), ValidateFunctionCallOutputContextBytes(bodyBytes))
		})
	}
}

// TestAnalyzeToolCallOutputContextCoverageBytes supplies absent output,
// single-object output, complete/partial/unrelated context, missing IDs, and
// mixed references. Both output-presence and full-coverage flags must match
// each fixture's expected observations.
func TestAnalyzeToolCallOutputContextCoverageBytes(t *testing.T) {
	cases := []struct {
		name string         // continuation fixture or size label
		body map[string]any // decoded request input fixture
		// hasOutput and coversAllIDs describe separate observations from body.
		// Full coverage requires output presence plus a matching ID for every
		// result, so absent output keeps both expected flags false.
		hasOutput    bool // expected recognized output presence
		coversAllIDs bool // expected complete context coverage
	}{
		{
			name:         "no_input",
			body:         map[string]any{"model": "gpt-5.1"},
			hasOutput:    false,
			coversAllIDs: false,
		},
		{
			name: "no_tool_output",
			body: map[string]any{"input": []any{
				map[string]any{"type": "message", "content": "hi"},
			}},
			hasOutput:    false,
			coversAllIDs: false,
		},
		{
			name: "object_tool_output_requires_context_replay",
			body: map[string]any{"input": map[string]any{
				"type": "custom_tool_call_output", "call_id": "call_a",
			}},
			hasOutput:    true,
			coversAllIDs: false,
		},
		{
			name: "all_outputs_covered_by_context",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call", "call_id": "call_a"},
				map[string]any{"type": "function_call_output", "call_id": "call_a"},
			}},
			hasOutput:    true,
			coversAllIDs: true,
		},
		{
			name: "all_outputs_covered_by_item_reference",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call_output", "call_id": "call_a"},
				map[string]any{"type": "item_reference", "id": "call_a"},
			}},
			hasOutput:    true,
			coversAllIDs: true,
		},
		{
			// One output matches supplied context, while the other remains uncovered.
			// This fixture distinguishes any-context presence from the full coverage flag.
			name: "partial_coverage_not_movable",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call", "call_id": "call_a"},
				map[string]any{"type": "function_call_output", "call_id": "call_a"},
				map[string]any{"type": "function_call_output", "call_id": "call_b"},
			}},
			hasOutput:    true,
			coversAllIDs: false,
		},
		{
			name: "unrelated_context_does_not_cover",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call", "call_id": "call_x"},
				map[string]any{"type": "function_call_output", "call_id": "call_b"},
			}},
			hasOutput:    true,
			coversAllIDs: false,
		},
		{
			name: "output_missing_call_id_not_movable",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call", "call_id": "call_a"},
				map[string]any{"type": "function_call_output"},
				map[string]any{"type": "function_call_output", "call_id": "call_a"},
			}},
			hasOutput:    true,
			coversAllIDs: false,
		},
		{
			name: "mixed_context_and_reference_cover_all",
			body: map[string]any{"input": []any{
				map[string]any{"type": "function_call", "call_id": "call_a"},
				map[string]any{"type": "function_call_output", "call_id": "call_a"},
				map[string]any{"type": "function_call_output", "call_id": "call_b"},
				map[string]any{"type": "item_reference", "id": "call_b"},
			}},
			hasOutput:    true,
			coversAllIDs: true,
		},
		{
			name: "all_codex_output_types_covered",
			body: map[string]any{"input": []any{
				map[string]any{"type": "tool_search_output", "call_id": "call_s"},
				map[string]any{"type": "tool_search_call", "call_id": "call_s"},
				map[string]any{"type": "mcp_tool_call_output", "call_id": "call_m"},
				map[string]any{"type": "mcp_tool_call", "call_id": "call_m"},
			}},
			hasOutput:    true,
			coversAllIDs: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes, err := json.Marshal(tt.body)
			require.NoError(t, err)

			coverage := AnalyzeToolCallOutputContextCoverageBytes(bodyBytes)
			require.Equal(t, tt.hasOutput, coverage.HasFunctionCallOutput, "HasFunctionCallOutput")
			require.Equal(t, tt.coversAllIDs, coverage.ContextCoversAllCallIDs, "ContextCoversAllCallIDs")
		})
	}
}

// benchmarkToolContinuationRequestBody creates a fresh decoded history with
// twenty-four text entries and ten call/output/reference triples. Each triple
// shares a generated string ID for the two benchmark policy helpers.
func benchmarkToolContinuationRequestBody() map[string]any {
	input := make([]any, 0, 64)
	for range 24 {
		input = append(input, map[string]any{
			"type": "text",
			"text": "benchmark text",
		})
	}
	for i := range 10 {
		callID := "call_" + strconv.Itoa(i)
		input = append(input, map[string]any{
			"type":    "tool_call",
			"call_id": callID,
		})
		input = append(input, map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
		})
		input = append(input, map[string]any{
			"type": "item_reference",
			"id":   callID,
		})
	}
	return map[string]any{
		"model": "gpt-5.3-codex",
		"input": input,
	}
}

// legacyValidateFunctionCallOutputContext applies the multi-pass fixture
// policy to function_call_output items. It accepts no-output requests, a
// nonblank string previous ID, or any recognized nonblank call context before
// considering missing output IDs. Nil requests therefore qualify through the
// no-output branch.
//
// Reference-only acceptance compares trimmed reference IDs with the helper's
// untrimmed distinct output IDs, so whitespace can affect matching. These
// predicates recognize the benchmark's function-output and two call-context
// forms; callers use production helpers for the expanded Codex label set and
// full continuation coverage.
func legacyValidateFunctionCallOutputContext(reqBody map[string]any) bool {
	if !legacyHasFunctionCallOutput(reqBody) {
		return true
	}
	previousResponseID, _ := reqBody["previous_response_id"].(string)
	if strings.TrimSpace(previousResponseID) != "" {
		return true
	}
	if legacyHasToolCallContext(reqBody) {
		return true
	}
	if legacyHasFunctionCallOutputMissingCallID(reqBody) {
		return false
	}
	callIDs := legacyFunctionCallOutputCallIDs(reqBody)
	return legacyHasItemReferenceForCallIDs(reqBody, callIDs)
}

// optimizedValidateFunctionCallOutputContext applies the fixture policy using
// production preliminary flags and a string previous ID. No output, a nonblank
// previous ID, or any observed call context accepts the request before
// missing-ID checks. Otherwise, a missing ID rejects it and the
// reference-coverage flag supplies the result.
//
// The preliminary context shortcut can qualify unrelated context because this
// helper measures the coarse handler-style decision. It receives neither a
// full-coverage scan nor an upstream existence check. Benchmarks retain its
// boolean result after each call and compare performance using a fixture that
// both policy helpers accept.
func optimizedValidateFunctionCallOutputContext(reqBody map[string]any) bool {
	validation := ValidateFunctionCallOutputContext(reqBody)
	if !validation.HasFunctionCallOutput {
		return true
	}
	previousResponseID, _ := reqBody["previous_response_id"].(string)
	if strings.TrimSpace(previousResponseID) != "" {
		return true
	}
	if validation.HasToolCallContext {
		return true
	}
	if validation.HasFunctionCallOutputMissingCallID {
		return false
	}
	return validation.HasItemReferenceForAllCallIDs
}

// legacyHasFunctionCallOutput recognizes exact function_call_output labels in
// decoded []any input. Nil requests and unsupported item/container shapes
// return false; this benchmark predicate excludes the expanded
// discovery/custom/MCP output labels.
func legacyHasFunctionCallOutput(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType == "function_call_output" {
			return true
		}
	}
	return false
}

// legacyHasToolCallContext recognizes exact tool_call or function_call labels
// with nonblank string call IDs. Nil requests and unsupported decoded input
// shapes return false; matching context need not correspond to any output.
func legacyHasToolCallContext(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType != "tool_call" && itemType != "function_call" {
			continue
		}
		if callID, ok := itemMap["call_id"].(string); ok && strings.TrimSpace(callID) != "" {
			return true
		}
	}
	return false
}

// legacyFunctionCallOutputCallIDs collects distinct original-spelling IDs from
// exact function_call_output items in a decoded array. IDs must be strings
// with nonblank trimmed text, but the stored key retains whitespace. Nil,
// unsupported, or ID-free inputs return nil; result order is unspecified.
//
// The reference helper trims reference IDs and compares supplied IDs
// literally. Keeping output spelling therefore makes whitespace part of this
// benchmark policy's identity match, unlike the production trimmed-ID helper.
// Other output kinds and invalid item shapes contribute no ID to this
// collected set.
func legacyFunctionCallOutputCallIDs(reqBody map[string]any) []string {
	if reqBody == nil {
		return nil
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return nil
	}
	ids := make(map[string]struct{})
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType != "function_call_output" {
			continue
		}
		if callID, ok := itemMap["call_id"].(string); ok && strings.TrimSpace(callID) != "" {
			// Keep the qualifying original ID as its key. The legacy reference
			// helper compares supplied IDs literally, so retained whitespace affects later matching.
			ids[callID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	callIDs := make([]string, 0, len(ids))
	for id := range ids {
		callIDs = append(callIDs, id)
	}
	return callIDs
}

// legacyHasFunctionCallOutputMissingCallID recognizes a missing or blank
// string ID on exact function_call_output items. Nil or unsupported input
// returns false; other output labels are outside this benchmark predicate.
func legacyHasFunctionCallOutputMissingCallID(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType != "function_call_output" {
			continue
		}
		callID, _ := itemMap["call_id"].(string)
		if strings.TrimSpace(callID) == "" {
			return true
		}
	}
	return false
}

// legacyHasItemReferenceForCallIDs compares every supplied ID literally
// against trimmed string IDs on exact item_reference items. Nil requests, an
// empty ID list, unsupported input, or no nonblank references return false.
// The supplied ID list can contain repeats.
//
// The helper gathers reference evidence independently of output items and does
// not trim requested IDs. A collected output containing surrounding whitespace
// can therefore fail even when its trimmed spelling matches a reference. The
// result describes this benchmark's identity comparison, leaving upstream
// validity and complete continuation policy to its caller.
func legacyHasItemReferenceForCallIDs(reqBody map[string]any, callIDs []string) bool {
	if reqBody == nil || len(callIDs) == 0 {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	referenceIDs := make(map[string]struct{})
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType != "item_reference" {
			continue
		}
		idValue, _ := itemMap["id"].(string)
		idValue = strings.TrimSpace(idValue)
		if idValue == "" {
			continue
		}
		referenceIDs[idValue] = struct{}{}
	}
	if len(referenceIDs) == 0 {
		return false
	}
	for _, callID := range callIDs {
		if _, ok := referenceIDs[callID]; !ok {
			return false
		}
	}
	return true
}

// BenchmarkOpenAIResponses_LargeInputFunctionCallValidation measures raw
// preliminary scans at four target request sizes. Each timed iteration must
// observe tool output and full reference coverage before increasing the shared
// iteration sink.
func BenchmarkOpenAIResponses_LargeInputFunctionCallValidation(b *testing.B) {
	for _, size := range benchmarkBodySizes() {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeOpenAIResponsesToolContinuationBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				validation := ValidateFunctionCallOutputContextBytes(body)
				if !validation.HasFunctionCallOutput || !validation.HasItemReferenceForAllCallIDs {
					b.Fatalf("工具续链校验结果异常: %+v", validation)
				}
				benchmarkIntSink++
			}
		})
	}
}

// benchmarkBodySizes returns fresh target-size entries for four, eight,
// sixteen, and thirty-two MiB requests. Their MB display labels identify
// nominal targets; benchmarks report the actual generated byte length for
// throughput.
func benchmarkBodySizes() []struct {
	// name and bytes identify the same nominal benchmark request size. The
	// generator can overshoot bytes by a complete pair, so throughput uses
	// the result length instead of this target value.
	name  string // continuation fixture or size label
	bytes int    // target request byte count
} {
	return []struct {
		name  string // continuation fixture or size label
		bytes int    // target request byte count
	}{
		{name: "4MB", bytes: 4 << 20},
		{name: "8MB", bytes: 8 << 20},
		{name: "16MB", bytes: 16 << 20},
		{name: "32MB", bytes: 32 << 20},
	}
}

// buildLargeOpenAIResponsesToolContinuationBody builds JSON with repeated
// reference/output pairs until its current length reaches targetBytes. Callers
// supply a nonnegative target, and the result can exceed that size by a
// complete pair and closing bytes. No incomplete pair is emitted.
//
// Each generated call ID is written into both items, allowing the raw
// validator to match every output without call-context items. Constant payload
// text and decimal indexes need no JSON escaping here. Builder writes return
// nil errors, and the returned byte slice copies the finished string for
// repeated benchmark scans.
func buildLargeOpenAIResponsesToolContinuationBody(targetBytes int) []byte {
	var builder strings.Builder
	// Reserve extra capacity for complete pairs near the target size.
	builder.Grow(targetBytes + 1024)
	// strings.Builder writes return nil errors. These constant JSON fragments
	// and decimal-generated IDs contain no text needing another escaping step.
	_, _ = builder.WriteString(`{"model":"gpt-5.4","stream":true,"previous_response_id":"resp_benchmark","input":[`)
	// Finish a reference/output pair before rechecking the size target, so a
	// nominal benchmark limit cannot leave an output without its matching reference.
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		callID := "call_" + strconv.Itoa(i)
		_, _ = builder.WriteString(`{"type":"item_reference","id":"`)
		_, _ = builder.WriteString(callID)
		_, _ = builder.WriteString(`"},{"type":"function_call_output","call_id":"`)
		_, _ = builder.WriteString(callID)
		_, _ = builder.WriteString(`","output":"`)
		// Repeat payload text so most fixture bytes represent tool output.
		_, _ = builder.WriteString(strings.Repeat("tool output payload ", 48))
		_, _ = builder.WriteString(strconv.Itoa(i))
		_, _ = builder.WriteString(`"}`)
	}
	_, _ = builder.WriteString(`]}`)
	return []byte(builder.String())
}
