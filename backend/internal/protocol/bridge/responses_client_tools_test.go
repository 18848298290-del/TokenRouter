package bridge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestAdaptResponsesClientTools_LowersDeclarationsHistoryChoiceAndNamespaces
// combines custom, discovery, and namespace declarations with calls/results
// and choice. It checks selected mappings, wrapper schema/arguments, names,
// types, and item-ID conversion/removal.
func TestAdaptResponsesClientTools_LowersDeclarationsHistoryChoiceAndNamespaces(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"type": "custom", "name": "exec", "format": map[string]any{"type": "grammar"}},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "namespace", "name": "team", "tools": []any{map[string]any{"type": "function", "name": "send"}}},
		},
		"tool_choice": map[string]any{"type": "custom", "name": "exec"},
		"input": []any{
			map[string]any{"type": "custom_tool_call", "id": "ctc_client", "call_id": "c1", "name": "exec", "input": "dir"},
			map[string]any{"type": "custom_tool_call_output", "id": "ctco_client", "call_id": "c1", "output": "ok"},
			map[string]any{"type": "tool_search_call", "id": "tsc_client", "call_id": "s1", "arguments": map[string]any{"query": "git"}},
			map[string]any{"type": "tool_search_output", "id": "tso_client", "call_id": "s1", "output": map[string]any{"groups": []string{"git"}}},
			map[string]any{"type": "function_call", "call_id": "n1", "namespace": "team", "name": "send", "arguments": "{}"},
		},
	}

	mapping, changed, err := AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, mapping.CustomTools["exec"])
	require.True(t, mapping.ToolSearch)
	require.Equal(t, ResponsesNamespaceName{Namespace: "team", Name: "send"}, mapping.NamespaceTools["team__send"])

	tools := requireResponsesClientToolValue[[]any](t, req["tools"])
	require.Len(t, tools, 3)
	exec := requireResponsesClientToolValue[map[string]any](t, tools[0])
	require.Equal(t, "function", exec["type"])
	parameters := requireResponsesClientToolValue[json.RawMessage](t, exec["parameters"])
	require.JSONEq(t, customToolInputSchema, string(parameters))
	search := requireResponsesClientToolValue[map[string]any](t, tools[1])
	require.Equal(t, toolSearchProxyName, search["name"])
	namespaceTool := requireResponsesClientToolValue[map[string]any](t, tools[2])
	require.Equal(t, "team__send", namespaceTool["name"])

	choice := requireResponsesClientToolValue[map[string]any](t, req["tool_choice"])
	require.Equal(t, "function", choice["type"])
	input := requireResponsesClientToolValue[[]any](t, req["input"])
	customCall := requireResponsesClientToolValue[map[string]any](t, input[0])
	require.Equal(t, "function_call", customCall["type"])
	require.Equal(t, "fc_client", customCall["id"])
	require.JSONEq(t, `{"input":"dir"}`, requireResponsesClientToolValue[string](t, customCall["arguments"]))
	customOutput := requireResponsesClientToolValue[map[string]any](t, input[1])
	require.Equal(t, "function_call_output", customOutput["type"])
	require.NotContains(t, customOutput, "id")
	searchCall := requireResponsesClientToolValue[map[string]any](t, input[2])
	require.Equal(t, "function_call", searchCall["type"])
	require.Equal(t, "fc_client", searchCall["id"])
	require.Equal(t, toolSearchProxyName, searchCall["name"])
	require.JSONEq(t, `{"query":"git"}`, requireResponsesClientToolValue[string](t, searchCall["arguments"]))
	searchOutput := requireResponsesClientToolValue[map[string]any](t, input[3])
	require.Equal(t, "function_call_output", searchOutput["type"])
	require.NotContains(t, searchOutput, "id")
	require.JSONEq(t, `{"groups":["git"]}`, requireResponsesClientToolValue[string](t, searchOutput["output"]))
	namespaceCall := requireResponsesClientToolValue[map[string]any](t, input[4])
	require.Equal(t, "team__send", namespaceCall["name"])
}

// TestAdaptResponsesClientTools_RemovesDeferredFlagsWhenToolSearchIsLowered
// lowers discovery beside a deferred function. The proxy name is retained and
// the asserted defer_loading key disappears from the function declaration.
func TestAdaptResponsesClientTools_RemovesDeferredFlagsWhenToolSearchIsLowered(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "function", "name": "shell", "defer_loading": true},
			map[string]any{"type": "function", "name": "apply_patch"},
		},
	}

	_, changed, err := AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)
	tools := requireResponsesClientToolValue[[]any](t, req["tools"])
	require.Equal(t, toolSearchProxyName, requireResponsesClientToolValue[map[string]any](t, tools[0])["name"])
	require.NotContains(t, requireResponsesClientToolValue[map[string]any](t, tools[1]), "defer_loading")
}

// TestStripResponsesDeferredToolFlags_PreservesFlagsWithBuiltInToolSearch
// keeps a built-in search declaration beside a deferred function. The helper
// reports no change and retains that function's true loading flag.
func TestStripResponsesDeferredToolFlags_PreservesFlagsWithBuiltInToolSearch(t *testing.T) {
	tools := []any{
		map[string]any{"type": "tool_search"},
		map[string]any{"type": "function", "name": "shell", "defer_loading": true},
	}

	require.False(t, stripResponsesDeferredToolFlags(tools))
	require.Equal(t, true, requireResponsesClientToolValue[map[string]any](t, tools[1])["defer_loading"])
}

// TestAdaptResponsesClientTools_LowersDiscoveredToolSearchOutput promotes
// namespace children from one discovery result. It checks declaration order,
// returned pairs, normalized call/output fields, repeatable output encoding,
// and restoration of the asserted namespace call.
func TestAdaptResponsesClientTools_LowersDiscoveredToolSearchOutput(t *testing.T) {
	requestJSON := `{
		"tools":[{"type":"tool_search"}],
		"input":[
			{"type":"tool_search_call","id":"tsc_client","call_id":"call_search","arguments":{"query":"codex app"},"execution":"client","status":"completed"},
			{"type":"tool_search_output","id":"tso_client","call_id":"call_search","execution":"client","status":"completed","tools":[
				{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"load_workspace_dependencies","description":"Load workspace dependencies","parameters":{"type":"object","properties":{},"additionalProperties":false}}]},
				{"type":"namespace","name":"multi_agent_v1","tools":[
					{"type":"function","name":"spawn_agent","description":"Spawn an agent","parameters":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}},
					{"type":"function","name":"wait_agent","description":"Wait for agents","parameters":{"type":"object","properties":{},"additionalProperties":false}}
				]}
			]}
		]
	}`

	// adaptedRequest pairs one converted fixture request with its response-restoration mapping.
	type adaptedRequest struct {
		// req and mapping belong to the same fresh adaptation. The paired values
		// let assertions compare encoded history across requests and restore a
		// response using identities from the selected conversion.
		req     map[string]any             // converted decoded fixture request
		mapping ResponsesClientToolMapping // corresponding client restoration mapping
	}
	adapt := func() adaptedRequest {
		var req map[string]any
		require.NoError(t, json.Unmarshal([]byte(requestJSON), &req))
		mapping, changed, err := AdaptResponsesClientTools(req)
		require.NoError(t, err)
		require.True(t, changed)
		return adaptedRequest{req: req, mapping: mapping}
	}

	first := adapt()
	second := adapt()
	firstInput := requireResponsesClientToolValue[[]any](t, first.req["input"])
	secondInput := requireResponsesClientToolValue[[]any](t, second.req["input"])

	tools := requireResponsesClientToolValue[[]any](t, first.req["tools"])
	require.Len(t, tools, 4)
	require.Equal(t, []string{
		"tool_search",
		"codex_app__load_workspace_dependencies",
		"multi_agent_v1__spawn_agent",
		"multi_agent_v1__wait_agent",
	}, responsesClientToolNames(t, tools))
	require.Equal(t, ResponsesNamespaceName{Namespace: "multi_agent_v1", Name: "spawn_agent"}, first.mapping.NamespaceTools["multi_agent_v1__spawn_agent"])
	require.Equal(t, ResponsesNamespaceName{Namespace: "multi_agent_v1", Name: "wait_agent"}, first.mapping.NamespaceTools["multi_agent_v1__wait_agent"])

	call := requireResponsesClientToolValue[map[string]any](t, firstInput[0])
	require.Equal(t, "function_call", call["type"])
	require.Equal(t, toolSearchProxyName, call["name"])
	require.JSONEq(t, `{"query":"codex app"}`, requireResponsesClientToolValue[string](t, call["arguments"]))
	require.NotContains(t, call, "execution")

	output := requireResponsesClientToolValue[map[string]any](t, firstInput[1])
	require.Equal(t, map[string]any{
		"type":    "function_call_output",
		"call_id": "call_search",
		"output":  output["output"],
	}, output)
	outputText := requireResponsesClientToolValue[string](t, output["output"])
	require.JSONEq(t, `[
		{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"load_workspace_dependencies","description":"Load workspace dependencies","parameters":{"type":"object","properties":{},"additionalProperties":false}}]},
		{"type":"namespace","name":"multi_agent_v1","tools":[
			{"type":"function","name":"spawn_agent","description":"Spawn an agent","parameters":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}},
			{"type":"function","name":"wait_agent","description":"Wait for agents","parameters":{"type":"object","properties":{},"additionalProperties":false}}
		]}
	]`, outputText)
	secondOutput := requireResponsesClientToolValue[map[string]any](t, secondInput[1])
	require.Equal(t, outputText, secondOutput["output"], "tool discovery output encoding must be deterministic")

	restored, changed, err := RestoreResponsesClientToolPayload(
		[]byte(`{"output":[{"type":"function_call","name":"multi_agent_v1__spawn_agent","call_id":"call_spawn","arguments":"{\"message\":\"work\"}"}]}`),
		first.mapping,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"output":[{"type":"function_call","name":"spawn_agent","namespace":"multi_agent_v1","call_id":"call_spawn","arguments":"{\"message\":\"work\"}"}]}`, string(restored))
}

// TestAdaptResponsesClientTools_DiscoveredFunctionPreservesIdentity lowers a
// plain discovered function with an explicit call namespace. Its request uses
// the bare upstream name and its response restores the discovered identity.
func TestAdaptResponsesClientTools_DiscoveredFunctionPreservesIdentity(t *testing.T) {
	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_output","call_id":"search_1","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]},{"type":"function_call","name":"js","namespace":"js","call_id":"call_1","arguments":"{}"}]}`), &request))
	mapping, changed, err := AdaptResponsesClientTools(request)
	require.NoError(t, err)
	require.True(t, changed)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	require.Equal(t, "js", gjson.GetBytes(encoded, "input.1.name").String())
	require.False(t, gjson.GetBytes(encoded, "input.1.namespace").Exists())
	restored, _, err := RestoreResponsesClientToolPayload([]byte(`{"output":[{"type":"function_call","name":"js","call_id":"call_2","arguments":"{}"}]}`), mapping)
	require.NoError(t, err)
	require.Equal(t, "js", gjson.GetBytes(restored, "output.0.namespace").String())
	require.Equal(t, "call_2", gjson.GetBytes(restored, "output.0.call_id").String())
}

// TestAdaptResponsesClientToolsWithInheritedMappingKeepsDiscoveredBareAlias
// continues a discovered plain function without resending its declaration.
// Both outgoing history and restored output retain its original tool identity.
func TestAdaptResponsesClientToolsWithInheritedMappingKeepsDiscoveredBareAlias(t *testing.T) {
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_output","call_id":"search_1","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}]}`), &first))
	inherited, _, err := AdaptResponsesClientTools(first)
	require.NoError(t, err)
	var followup map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"input":[{"type":"function_call","name":"js","namespace":"js","call_id":"call_1","arguments":"{}"}]}`), &followup))
	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(followup, inherited, requireResponsesClientToolValue[[]any](t, first["tools"]))
	require.NoError(t, err)
	require.True(t, changed)
	body, err := json.Marshal(followup)
	require.NoError(t, err)
	require.Equal(t, "js", gjson.GetBytes(body, "input.0.name").String())
	require.False(t, gjson.GetBytes(body, "input.0.namespace").Exists())
	require.Equal(t, ResponsesNamespaceName{Namespace: "js", Name: "js"}, mapping.NamespaceTools["js"])
}

// TestAdaptResponsesClientToolsWithInheritedMappingResetsFutureToolsOnly clears
// active declarations while replaying a previously mapped function call.
// History retains its executable identity and the returned future mapping clears.
func TestAdaptResponsesClientToolsWithInheritedMappingResetsFutureToolsOnly(t *testing.T) {
	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[],"input":[{"type":"function_call","name":"js","namespace":"browser","call_id":"call_1","arguments":"{}"}]}`), &request))
	inherited := ResponsesClientToolMapping{NamespaceTools: map[string]ResponsesNamespaceName{"browser__js": {Namespace: "browser", Name: "js"}}}
	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(request, inherited)
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, mapping)
	body, err := json.Marshal(request)
	require.NoError(t, err)
	require.Empty(t, gjson.GetBytes(body, "tools").Array())
	require.Equal(t, "browser__js", gjson.GetBytes(body, "input.0.name").String())
	require.False(t, gjson.GetBytes(body, "input.0.namespace").Exists())
}

// TestAdaptResponsesClientToolsWithInheritedMappingReplaysUnknownHistory supplies
// known and undeclared historical calls across omitted, reset, and replaced tools.
// The shared inherited mapping stays unchanged and missing history never becomes callable.
func TestAdaptResponsesClientToolsWithInheritedMappingReplaysUnknownHistory(t *testing.T) {
	inherited := ResponsesClientToolMapping{
		NamespaceTools: map[string]ResponsesNamespaceName{"known__run": {Namespace: "known", Name: "run"}},
	}
	for _, toolField := range []string{"", `,"tools":[]`, `,"tools":null`, `,"tools":[{"type":"function","name":"current"}]`} {
		var request map[string]any
		require.NoError(t, json.Unmarshal([]byte(`{"input":[{"type":"function_call","namespace":"known","name":"run","call_id":"k","arguments":"{}"},{"type":"function_call_output","call_id":"k","output":"known"},{"type":"function_call","namespace":"missing","name":"run","call_id":"m","arguments":"{}"},{"type":"function_call_output","call_id":"m","output":"missing"}]`+toolField+`}`), &request))
		mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(request, inherited)
		require.NoError(t, err)
		require.True(t, changed)
		encoded, err := json.Marshal(request)
		require.NoError(t, err)
		require.Equal(t, "known__run", gjson.GetBytes(encoded, "input.0.name").String())
		require.Equal(t, "missing__run", gjson.GetBytes(encoded, "input.2.name").String())
		require.NotContains(t, mapping.NamespaceTools, "missing__run")
		require.False(t, gjson.GetBytes(encoded, `tools.#(name=="missing__run")`).Exists())
		require.Equal(t, map[string]ResponsesNamespaceName{"known__run": {Namespace: "known", Name: "run"}}, inherited.NamespaceTools)
		require.Empty(t, inherited.CallNames)
	}
}

// TestAdaptResponsesClientToolsWithInheritedMappingRejectsAliasReplacement
// discovers an explicit namespace colliding with a previously loaded plain
// function. The continuation fails before the inherited alias can replace
// the newly declared tool's executable identity.
func TestAdaptResponsesClientToolsWithInheritedMappingRejectsAliasReplacement(t *testing.T) {
	inherited := ResponsesClientToolMapping{
		FunctionTools:  map[string]bool{"js": true},
		NamespaceTools: map[string]ResponsesNamespaceName{"js": {Namespace: "js", Name: "js"}},
		ToolSearch:     true,
	}
	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"input":[{"type":"tool_search_output","call_id":"search_2","tools":[{"type":"namespace","name":"js","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}]}]}`), &request))
	_, _, err := AdaptResponsesClientToolsWithInheritedMapping(request, inherited, []any{
		map[string]any{"type": "function", "name": "tool_search"},
		map[string]any{"type": "function", "name": "js", "parameters": map[string]any{"type": "object"}},
	})
	require.ErrorContains(t, err, "conflicts")
}

// TestAdaptResponsesClientToolsPromotesAdditionalNamespaceTools converts an
// additional_tools declaration and matching history for function-only execution.
// The declared and called names agree and the declaration item is consumed.
func TestAdaptResponsesClientToolsPromotesAdditionalNamespaceTools(t *testing.T) {
	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"browser","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}]},{"type":"function_call","name":"js","namespace":"browser","call_id":"call_1","arguments":"{}"}]}`), &request))
	mapping, changed, err := AdaptResponsesClientTools(request)
	require.NoError(t, err)
	require.True(t, changed)
	body, err := json.Marshal(request)
	require.NoError(t, err)
	require.Equal(t, "browser__js", gjson.GetBytes(body, "tools.0.name").String())
	require.Equal(t, "browser__js", gjson.GetBytes(body, "input.0.name").String())
	require.False(t, gjson.GetBytes(body, "input.0.namespace").Exists())
	require.Equal(t, ResponsesNamespaceName{Namespace: "browser", Name: "js"}, mapping.NamespaceTools["browser__js"])
}

// TestAdaptResponsesClientToolsRejectsUndeclaredNamespaceChoice selects a group
// absent from otherwise valid namespace declarations. Lowering rejects that
// selector before flattening can silently replace it with automatic selection.
func TestAdaptResponsesClientToolsRejectsUndeclaredNamespaceChoice(t *testing.T) {
	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"namespace","name":"browser","tools":[{"type":"function","name":"js"}]}],"tool_choice":{"type":"namespace","name":"unknown"}}`), &request))
	_, _, err := AdaptResponsesClientTools(request)
	require.ErrorContains(t, err, "without a declared group")
}

// TestAdaptResponsesClientTools_LowersToolSearchDiscoveryPayloadWithoutOutput
// supplies discovery tools with no output value. The lowered result retains
// its call ID, serializes the tool list, and removes tools/status/execution.
func TestAdaptResponsesClientTools_LowersToolSearchDiscoveryPayloadWithoutOutput(t *testing.T) {
	req := map[string]any{
		"tools": []any{map[string]any{"type": "tool_search"}},
		"input": []any{map[string]any{
			"type":      "tool_search_output",
			"call_id":   "search_1",
			"tools":     []any{map[string]any{"name": "github", "type": "namespace"}},
			"status":    "completed",
			"execution": "client",
		}},
	}

	mapping, changed, err := AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, mapping.ToolSearch)

	item := requireResponsesClientToolValue[map[string]any](t, requireResponsesClientToolValue[[]any](t, req["input"])[0])
	require.Equal(t, "function_call_output", item["type"])
	require.Equal(t, "search_1", item["call_id"])
	require.JSONEq(t, `[{"name":"github","type":"namespace"}]`, requireResponsesClientToolValue[string](t, item["output"]))
	require.NotContains(t, item, "tools")
	require.NotContains(t, item, "status")
	require.NotContains(t, item, "execution")
}

// TestAdaptResponsesClientTools_ToolSearchExplicitOutputTakesPriority supplies
// both output and a discovery tools list. Lowering keeps the authoritative
// output text and removes the tools key from the inspected result.
func TestAdaptResponsesClientTools_ToolSearchExplicitOutputTakesPriority(t *testing.T) {
	req := map[string]any{
		"tools": []any{map[string]any{"type": "tool_search"}},
		"input": []any{map[string]any{
			"type": "tool_search_output", "call_id": "search_1",
			"output": "authoritative", "tools": []any{map[string]any{"name": "ignored"}},
		}},
	}

	_, changed, err := AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)
	item := requireResponsesClientToolValue[map[string]any](t, requireResponsesClientToolValue[[]any](t, req["input"])[0])
	require.Equal(t, "authoritative", item["output"])
	require.NotContains(t, item, "tools")
}

// TestAdaptResponsesClientTools_DoesNotInventMissingToolSearchOutput supplies
// neither output nor tools on an incomplete discovery result. Its type lowers,
// while output stays absent and asserted status/execution values remain.
func TestAdaptResponsesClientTools_DoesNotInventMissingToolSearchOutput(t *testing.T) {
	req := map[string]any{
		"tools": []any{map[string]any{"type": "tool_search"}},
		"input": []any{map[string]any{
			"type": "tool_search_output", "call_id": "search_1",
			"status": "incomplete", "execution": "client",
		}},
	}

	_, changed, err := AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)
	item := requireResponsesClientToolValue[map[string]any](t, requireResponsesClientToolValue[[]any](t, req["input"])[0])
	require.Equal(t, "function_call_output", item["type"])
	require.NotContains(t, item, "output")
	require.Equal(t, "incomplete", item["status"])
	require.Equal(t, "client", item["execution"])
}

// TestAdaptResponsesClientTools_RejectsAmbiguousNames supplies
// custom/function, reserved discovery/function, and flat namespace/function
// name conflicts. Every supplied declaration set must return an error from
// adaptation.
func TestAdaptResponsesClientTools_RejectsAmbiguousNames(t *testing.T) {
	cases := []map[string]any{
		{"tools": []any{map[string]any{"type": "custom", "name": "same"}, map[string]any{"type": "function", "name": "same"}}},
		{"tools": []any{map[string]any{"type": "tool_search"}, map[string]any{"type": "function", "name": "tool_search"}}},
		{"tools": []any{map[string]any{"type": "function", "name": "team__send"}, map[string]any{"type": "namespace", "name": "team", "tools": []any{map[string]any{"type": "function", "name": "send"}}}}},
	}
	for _, req := range cases {
		_, _, err := AdaptResponsesClientTools(req)
		require.Error(t, err)
	}
}

// TestAdaptResponsesClientToolsWithInheritedMapping_LowersFollowupHistoryWithoutTools
// omits declarations while inheriting a custom identity. It checks the
// retained mapping, wrapped call input, lowered result/ID removal, and
// unchanged supported content array.
func TestAdaptResponsesClientToolsWithInheritedMapping_LowersFollowupHistoryWithoutTools(t *testing.T) {
	req := map[string]any{
		"input": []any{
			map[string]any{
				"type": "custom_tool_call", "name": "exec",
				"call_id": "call_1", "input": "pwd",
			},
			map[string]any{
				"type": "custom_tool_call_output", "call_id": "call_1",
				"id": "ctco_client_output_1", "output": []any{
					map[string]any{"type": "input_text", "text": "ok"},
				},
			},
		},
	}
	inherited := ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}}

	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(req, inherited)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, inherited, mapping)
	items, ok := req["input"].([]any)
	require.True(t, ok)
	call, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call", call["type"])
	arguments, ok := call["arguments"].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"input":"pwd"}`, arguments)
	require.NotContains(t, call, "input")
	output, ok := items[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call_output", output["type"])
	require.NotContains(t, output, "id")
	require.Equal(t, []any{map[string]any{"type": "input_text", "text": "ok"}}, output["output"])
}

// TestAdaptResponsesClientTools_NormalizesCustomToolOutput compares recognized
// content arrays with ordinary, empty, mixed, and unsupported typed outputs.
// Every result type lowers and its value matches the fixture's retained array
// or encoded string.
func TestAdaptResponsesClientTools_NormalizesCustomToolOutput(t *testing.T) {
	tests := []struct {
		name string // client-tool fixture case label
		// output and wantOutput pair source shape with its expected representation.
		// Recognized block arrays remain arrays, while unsupported fixture shapes
		// are compared with their exact encoded string form.
		output     any // supplied client output value
		wantOutput any // expected normalized output value
	}{
		{
			name: "supported content parts remain an array",
			output: []any{
				map[string]any{"type": "input_text", "text": "ok"},
				map[string]any{"type": "input_image", "image_url": "https://example.com/image.png"},
				map[string]any{"type": "input_file", "file_id": "file_123"},
			},
			wantOutput: []any{
				map[string]any{"type": "input_text", "text": "ok"},
				map[string]any{"type": "input_image", "image_url": "https://example.com/image.png"},
				map[string]any{"type": "input_file", "file_id": "file_123"},
			},
		},
		{name: "ordinary object is stringified", output: map[string]any{"ok": true}, wantOutput: `{"ok":true}`},
		{name: "arbitrary array is stringified", output: []any{"ok"}, wantOutput: `["ok"]`},
		{name: "empty array is stringified", output: []any{}, wantOutput: `[]`},
		{name: "mixed array is stringified", output: []any{map[string]any{"type": "input_text", "text": "ok"}, "bad"}, wantOutput: `[{"text":"ok","type":"input_text"},"bad"]`},
		{name: "unknown content type is stringified", output: []any{map[string]any{"type": "output_text", "text": "bad"}}, wantOutput: `[{"text":"bad","type":"output_text"}]`},
		{name: "whitespace-padded content type is stringified", output: []any{map[string]any{"type": " input_text ", "text": "bad"}}, wantOutput: `[{"text":"bad","type":" input_text "}]`},
		{name: "missing content type is stringified", output: []any{map[string]any{"text": "bad"}}, wantOutput: `[{"text":"bad"}]`},
		{name: "non-string content type is stringified", output: []any{map[string]any{"type": 1}}, wantOutput: `[{"type":1}]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := map[string]any{
				"tools": []any{map[string]any{"type": "custom", "name": "exec"}},
				"input": []any{map[string]any{
					"type": "custom_tool_call_output", "call_id": "call_1", "output": tc.output,
				}},
			}

			_, changed, err := AdaptResponsesClientTools(req)
			require.NoError(t, err)
			require.True(t, changed)
			item := requireResponsesClientToolValue[map[string]any](t, requireResponsesClientToolValue[[]any](t, req["input"])[0])
			require.Equal(t, "function_call_output", item["type"])
			require.Equal(t, tc.wantOutput, item["output"])
		})
	}
}

// TestAdaptResponsesClientToolsWithInheritedMapping_PromotesOmittedToolsDiscoveryIntoEffectiveDeclarations
// restores omitted tools from inherited lowered declarations before promotion.
// It checks static-first names, old/new namespace mappings, search enablement,
// and lowered discovery output fields.
func TestAdaptResponsesClientToolsWithInheritedMapping_PromotesOmittedToolsDiscoveryIntoEffectiveDeclarations(t *testing.T) {
	req := map[string]any{
		"input": []any{map[string]any{
			"type": "tool_search_output", "call_id": "call_search", "status": "completed", "execution": "client",
			"tools": []any{map[string]any{
				"type": "namespace", "name": "multi_agent_v1", "tools": []any{map[string]any{
					"type": "function", "name": "spawn_agent", "parameters": map[string]any{"type": "object"},
				}},
			}},
		}},
	}
	inherited := ResponsesClientToolMapping{
		ToolSearch: true,
		NamespaceTools: map[string]ResponsesNamespaceName{
			"codex_app__read_resource": {Namespace: "codex_app", Name: "read_resource"},
		},
	}
	lowered := []any{
		map[string]any{"type": "function", "name": "static_first", "parameters": map[string]any{"type": "object"}},
		map[string]any{"type": "function", "name": "tool_search", "parameters": json.RawMessage(toolSearchProxySchema)},
		map[string]any{"type": "function", "name": "codex_app__read_resource", "parameters": map[string]any{"type": "object"}},
	}

	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(req, inherited, lowered)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, mapping.ToolSearch)
	require.Equal(t, ResponsesNamespaceName{Namespace: "codex_app", Name: "read_resource"}, mapping.NamespaceTools["codex_app__read_resource"])
	require.Equal(t, ResponsesNamespaceName{Namespace: "multi_agent_v1", Name: "spawn_agent"}, mapping.NamespaceTools["multi_agent_v1__spawn_agent"])
	tools := requireResponsesClientToolValue[[]any](t, req["tools"])
	require.Equal(t, []string{
		"static_first", "tool_search", "codex_app__read_resource", "multi_agent_v1__spawn_agent",
	}, responsesClientToolNames(t, tools))
	output := requireResponsesClientToolValue[map[string]any](t, requireResponsesClientToolValue[[]any](t, req["input"])[0])
	require.Equal(t, "function_call_output", output["type"])
	require.IsType(t, "", output["output"])
	require.NotContains(t, output, "tools")
	require.NotContains(t, output, "status")
	require.NotContains(t, output, "execution")
}

// TestAdaptResponsesClientToolsWithInheritedMapping_ExplicitToolsReplaceInheritedMapping
// supplies an explicit empty declarations array beside inherited custom
// identity. The mapping clears, the change flag is false, and the inspected
// call keeps its client type.
func TestAdaptResponsesClientToolsWithInheritedMapping_ExplicitToolsReplaceInheritedMapping(t *testing.T) {
	req := map[string]any{
		"tools": []any{},
		"input": []any{map[string]any{
			"type": "custom_tool_call", "name": "exec", "input": "pwd",
		}},
	}

	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(
		req,
		ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}},
	)

	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, mapping)
	input, ok := req["input"].([]any)
	require.True(t, ok)
	firstInput, ok := input[0].(map[string]any)
	require.True(t, ok)
	inputType, ok := firstInput["type"].(string)
	require.True(t, ok)
	require.Equal(t, "custom_tool_call", inputType)
}

// TestAdaptResponsesClientToolsWithInheritedMapping_ExplicitToolResetDoesNotPromoteDiscovery
// supplies explicit null or empty tools beside inherited search and discovery
// history. Both resets return no change, clear mappings, and keep the
// inspected discovery type.
func TestAdaptResponsesClientToolsWithInheritedMapping_ExplicitToolResetDoesNotPromoteDiscovery(t *testing.T) {
	for _, reset := range []any{nil, []any{}} {
		req := map[string]any{
			"tools": reset,
			"input": []any{map[string]any{
				"type": "tool_search_output", "call_id": "call_reset", "status": "completed",
				"tools": []any{map[string]any{"type": "function", "name": "must_not_promote"}},
			}},
		}
		mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(
			req,
			ResponsesClientToolMapping{ToolSearch: true},
			[]any{map[string]any{"type": "function", "name": "tool_search"}},
		)
		require.NoError(t, err)
		require.False(t, changed)
		require.Empty(t, mapping)
		item := requireResponsesClientToolValue[map[string]any](t, requireResponsesClientToolValue[[]any](t, req["input"])[0])
		require.Equal(t, "tool_search_output", item["type"])
	}
}

// TestRestoreResponsesClientToolPayload_RestoresClientAndNamespaceCalls
// combines three function proxies with custom/search/namespace mapping.
// Complete JSON equality checks client kinds, freeform/object arguments,
// identity fields, and restored namespace/name pairs.
func TestRestoreResponsesClientToolPayload_RestoresClientAndNamespaceCalls(t *testing.T) {
	mapping := ResponsesClientToolMapping{
		CustomTools: map[string]bool{"exec": true}, ToolSearch: true,
		NamespaceTools: map[string]ResponsesNamespaceName{"team__send": {Namespace: "team", Name: "send"}},
	}
	payload := []byte(`{"id":"resp","output":[{"type":"function_call","id":"i1","call_id":"c1","name":"exec","arguments":"{\"input\":\"dir\"}","namespace":"ignore"},{"type":"function_call","id":"i2","call_id":"s1","name":"tool_search","arguments":"{\"query\":\"git\"}"},{"type":"function_call","id":"i3","call_id":"n1","name":"team__send","arguments":"{}"}]}`)

	restored, changed, err := RestoreResponsesClientToolPayload(payload, mapping)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"id":"resp","output":[{"type":"custom_tool_call","id":"i1","call_id":"c1","name":"exec","input":"dir"},{"type":"tool_search_call","id":"i2","call_id":"s1","execution":"client","arguments":{"query":"git"}},{"type":"function_call","id":"i3","call_id":"n1","name":"send","namespace":"team","arguments":"{}"}]}`, string(restored))
}

// TestResponsesClientToolStreamRestorer_CustomToolBuffersWrapperAndSequences
// splits custom wrapper arguments across delta/done events. It checks
// suppressed delta output, expanded input events, their sequence numbers, and
// the closing item's restored kind/input.
func TestResponsesClientToolStreamRestorer_CustomToolBuffersWrapperAndSequences(t *testing.T) {
	restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})
	added := restorer.Restore(ResponsesStreamEvent{Type: "response.output_item.added", SequenceNumber: 7, OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "i1", CallID: "c1", Name: "exec", Status: "in_progress"}})
	require.Len(t, added, 1)
	require.Equal(t, 7, added[0].SequenceNumber)
	require.Equal(t, "custom_tool_call", added[0].Item.Type)
	require.Empty(t, restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.delta", SequenceNumber: 8, ItemID: "i1", Delta: `{"input":"di`}))
	done := restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.done", SequenceNumber: 9, ItemID: "i1", CallID: "c1", Name: "exec", Arguments: `{"input":"dir"}`})
	require.Len(t, done, 2)
	require.Equal(t, 8, done[0].SequenceNumber)
	require.Equal(t, "response.custom_tool_call_input.delta", done[0].Type)
	require.Equal(t, "dir", done[0].Delta)
	require.Equal(t, 9, done[1].SequenceNumber)
	require.Equal(t, "response.custom_tool_call_input.done", done[1].Type)
	require.Equal(t, "dir", done[1].Input)
	closed := restorer.Restore(ResponsesStreamEvent{Type: "response.output_item.done", SequenceNumber: 10, OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "i1", CallID: "c1", Name: "exec", Arguments: `{"input":"dir"}`, Status: "completed"}})
	require.Equal(t, 10, closed[0].SequenceNumber)
	require.Equal(t, "custom_tool_call", closed[0].Item.Type)
	require.Equal(t, "dir", closed[0].Item.Input)
}

// TestResponsesClientToolStreamRestorer_ToolSearchAndFunction buffers
// discovery arguments until item completion, then sends an unrelated ordinary
// argument event. Assertions check suppression, restored search arguments, and
// consecutive emitted sequence values.
func TestResponsesClientToolStreamRestorer_ToolSearchAndFunction(t *testing.T) {
	restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{ToolSearch: true})
	search := restorer.Restore(ResponsesStreamEvent{Type: "response.output_item.added", SequenceNumber: 0, OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "s1", CallID: "c1", Name: "tool_search", Status: "in_progress"}})
	require.Equal(t, "tool_search_call", search[0].Item.Type)
	require.Empty(t, restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.delta", SequenceNumber: 1, ItemID: "s1", Delta: `{"query":"git"}`}))
	require.Empty(t, restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.done", SequenceNumber: 2, ItemID: "s1", Arguments: `{"query":"git"}`}))
	closed := restorer.Restore(ResponsesStreamEvent{Type: "response.output_item.done", SequenceNumber: 3, OutputIndex: 0, Item: &ResponsesOutput{Type: "function_call", ID: "s1", CallID: "c1", Name: "tool_search", Status: "completed"}})
	require.Equal(t, 1, closed[0].SequenceNumber)
	require.Equal(t, "tool_search_call", closed[0].Item.Type)
	require.JSONEq(t, `{"query":"git"}`, string(toolSearchCallArgumentsJSON(closed[0].Item.Arguments)))

	function := restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.done", SequenceNumber: 4, ItemID: "plain", Name: "plain", Arguments: "{}"})
	require.Len(t, function, 1)
	require.Equal(t, "response.function_call_arguments.done", function[0].Type)
	require.Equal(t, 2, function[0].SequenceNumber)
}

// TestResponsesClientToolStreamRestorer_RestoresNamespaceLifecycle supplies
// added, delta, and done JSON payloads with one mapped flat name. It checks
// one changed payload per event and the asserted child name/namespace fields.
func TestResponsesClientToolStreamRestorer_RestoresNamespaceLifecycle(t *testing.T) {
	restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{
		NamespaceTools: map[string]ResponsesNamespaceName{
			"browser__open": {Namespace: "browser", Name: "open"},
		},
	})

	added, changed, err := restorer.RestoreEvent([]byte(`{"type":"response.output_item.added","sequence_number":4,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"browser__open","arguments":"","status":"in_progress"}}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, added, 1)
	require.Equal(t, "open", gjson.GetBytes(added[0], "item.name").String())
	require.Equal(t, "browser", gjson.GetBytes(added[0], "item.namespace").String())

	delta, changed, err := restorer.RestoreEvent([]byte(`{"type":"response.function_call_arguments.delta","sequence_number":5,"output_index":0,"item_id":"i1","name":"browser__open","delta":"{\"url\":"}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, delta, 1)
	require.Equal(t, "open", gjson.GetBytes(delta[0], "name").String())

	done, changed, err := restorer.RestoreEvent([]byte(`{"type":"response.function_call_arguments.done","sequence_number":6,"output_index":0,"item_id":"i1","name":"browser__open","arguments":"{}"}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, done, 1)
	require.Equal(t, "open", gjson.GetBytes(done[0], "name").String())
}

// TestResponsesClientToolStreamRestorer_RawEventsPreserveUnknownFieldsAndOutputFallback
// first passes an in-sequence raw event containing an extension, then uses
// index-only argument fragments. The extension remains, deltas suppress, and
// completed custom input matches buffered text.
func TestResponsesClientToolStreamRestorer_RawEventsPreserveUnknownFieldsAndOutputFallback(t *testing.T) {
	restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})
	passthrough, changed, err := restorer.RestoreEvent([]byte(`{"type":"response.created","sequence_number":4,"response":{"id":"r"},"upstream_extension":{"keep":true}}`))
	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, passthrough, 1)
	require.Contains(t, string(passthrough[0]), `"upstream_extension":{"keep":true}`)

	restorer.Restore(ResponsesStreamEvent{Type: "response.output_item.added", SequenceNumber: 5, OutputIndex: 9, Item: &ResponsesOutput{Type: "function_call", ID: "item", CallID: "call", Name: "exec"}})
	// Omit item/call/name fields in these fragments to exercise output-index
	// lookup against the registered item, while its buffered wrapper supplies the final input.
	require.Empty(t, restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.delta", SequenceNumber: 6, OutputIndex: 9, Delta: `{"input":"pwd"}`}))
	done := restorer.Restore(ResponsesStreamEvent{Type: "response.function_call_arguments.done", SequenceNumber: 7, OutputIndex: 9})
	require.Len(t, done, 2)
	require.Equal(t, "pwd", done[1].Input)
}

// TestResponsesClientToolStreamRestorer_RestoresAllTerminalEvents supplies six
// terminal JSON kinds carrying a custom proxy call. Each output retains
// type/sequence and exposes custom kind/input while omitting the inspected
// argument field.
func TestResponsesClientToolStreamRestorer_RestoresAllTerminalEvents(t *testing.T) {
	for _, eventType := range []string{
		"response.completed",
		"response.done",
		"response.incomplete",
		"response.failed",
		"response.cancelled",
		"response.canceled",
	} {
		t.Run(eventType, func(t *testing.T) {
			restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})
			payload := []byte(`{"type":"` + eventType + `","sequence_number":7,"response":{"id":"resp_tools","output":[{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"}]}}`)

			restored, changed, err := restorer.RestoreEvent(payload)

			require.NoError(t, err)
			require.True(t, changed)
			require.Len(t, restored, 1)
			require.Equal(t, eventType, gjson.GetBytes(restored[0], "type").String())
			require.Equal(t, int64(7), gjson.GetBytes(restored[0], "sequence_number").Int())
			require.Equal(t, "custom_tool_call", gjson.GetBytes(restored[0], "response.output.0.type").String())
			require.Equal(t, "pwd", gjson.GetBytes(restored[0], "response.output.0.input").String())
			require.False(t, gjson.GetBytes(restored[0], "response.output.0.arguments").Exists())
		})
	}
}

// TestRetypedResponsesToolCallItemID compares known-family conversions with
// correct, unknown, unprefixed, empty, and unsupported-type values. Each
// result must match its expected suffix-preserving or unchanged ID spelling.
func TestRetypedResponsesToolCallItemID(t *testing.T) {
	for _, tc := range []struct {
		name string // client-tool fixture case label
		// id, itemType, and want select one prefix conversion and its expected
		// spelling. The table compares suffix retention and passthrough conditions
		// without imposing another call/result correlation rule.
		id       string // supplied item ID spelling
		itemType string // target output kind label
		want     string // expected item ID spelling
	}{
		{name: "function to custom", id: "fc_abc", itemType: "custom_tool_call", want: "ctc_abc"},
		{name: "function to search", id: "fc_abc", itemType: "tool_search_call", want: "tsc_abc"},
		{name: "correct prefix unchanged", id: "ctc_abc", itemType: "custom_tool_call", want: "ctc_abc"},
		{name: "custom to function", id: "ctc_abc", itemType: "function_call", want: "fc_abc"},
		{name: "unknown prefix unchanged", id: "item_abc", itemType: "custom_tool_call", want: "item_abc"},
		{name: "unprefixed unchanged", id: "abc", itemType: "custom_tool_call", want: "abc"},
		{name: "empty unchanged", id: "", itemType: "custom_tool_call", want: ""},
		{name: "unconstrained type unchanged", id: "fc_abc", itemType: "message", want: "fc_abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, retypedResponsesToolCallItemID(tc.id, tc.itemType))
		})
	}
}

// TestRestoreResponsesClientToolPayloadRetypesItemIDs restores custom,
// discovery, and namespace proxies with function-prefixed IDs. Complete JSON
// equality checks retyped custom/search prefixes, retained suffixes/call IDs,
// and the namespace function's unchanged ID.
func TestRestoreResponsesClientToolPayloadRetypesItemIDs(t *testing.T) {
	mapping := ResponsesClientToolMapping{
		CustomTools: map[string]bool{"exec": true}, ToolSearch: true,
		NamespaceTools: map[string]ResponsesNamespaceName{"team__send": {Namespace: "team", Name: "send"}},
	}
	payload := []byte(`{"id":"resp","output":[` +
		`{"type":"function_call","id":"fc_abc123","call_id":"call_1","name":"exec","arguments":"{\"input\":\"dir\"}"},` +
		`{"type":"function_call","id":"fc_def456","call_id":"call_2","name":"tool_search","arguments":"{\"query\":\"git\"}"},` +
		`{"type":"function_call","id":"fc_ghi789","call_id":"call_3","name":"team__send","arguments":"{}"}]}`)

	restored, changed, err := RestoreResponsesClientToolPayload(payload, mapping)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"id":"resp","output":[`+
		`{"type":"custom_tool_call","id":"ctc_abc123","call_id":"call_1","name":"exec","input":"dir"},`+
		`{"type":"tool_search_call","id":"tsc_def456","call_id":"call_2","execution":"client","arguments":{"query":"git"}},`+
		`{"type":"function_call","id":"fc_ghi789","call_id":"call_3","name":"send","namespace":"team","arguments":"{}"}]}`,
		string(restored))
}

// TestRestoreResponsesOutputClientToolsRetypesItemIDs converts typed
// custom/search outputs in place. Assertions check both client kinds, their
// changed known prefixes, and the two unchanged call IDs after restoration.
func TestRestoreResponsesOutputClientToolsRetypesItemIDs(t *testing.T) {
	mapping := ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}, ToolSearch: true}
	outputs := []ResponsesOutput{
		{Type: "function_call", ID: "fc_abc123", CallID: "call_1", Name: "exec", Arguments: `{"input":"dir"}`},
		{Type: "function_call", ID: "fc_def456", CallID: "call_2", Name: toolSearchProxyName, Arguments: `{"query":"git"}`},
	}

	restoreResponsesOutputClientTools(outputs, &mapping)

	require.Equal(t, "custom_tool_call", outputs[0].Type)
	require.Equal(t, "ctc_abc123", outputs[0].ID)
	require.Equal(t, "call_1", outputs[0].CallID)
	require.Equal(t, "tool_search_call", outputs[1].Type)
	require.Equal(t, "tsc_def456", outputs[1].ID)
	require.Equal(t, "call_2", outputs[1].CallID)
}

// TestResponsesClientToolStreamRestorerRetypesCustomItemID tracks one function
// ID through custom add, argument completion, and item completion. Assertions
// keep upstream lookup working while emitted item IDs use the custom prefix.
func TestResponsesClientToolStreamRestorerRetypesCustomItemID(t *testing.T) {
	const upstreamID = "fc_09f77ac43cf7db36016a8920e7934487"
	const clientID = "ctc_09f77ac43cf7db36016a8920e7934487"

	restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})

	added := restorer.Restore(ResponsesStreamEvent{
		Type: "response.output_item.added", SequenceNumber: 0, OutputIndex: 0,
		Item: &ResponsesOutput{Type: "function_call", ID: upstreamID, CallID: "call_1", Name: "exec", Status: "in_progress"},
	})
	require.Len(t, added, 1)
	require.Equal(t, "custom_tool_call", added[0].Item.Type)
	require.Equal(t, clientID, added[0].Item.ID)

	// Argument events keep the upstream lookup ID after the emitted item ID
	// changes prefix. This fixture catches using the client ID for subsequent record lookup.
	require.Empty(t, restorer.Restore(ResponsesStreamEvent{
		Type: "response.function_call_arguments.delta", SequenceNumber: 1, ItemID: upstreamID, Delta: `{"input":"di`,
	}))
	done := restorer.Restore(ResponsesStreamEvent{
		Type: "response.function_call_arguments.done", SequenceNumber: 2, ItemID: upstreamID,
		CallID: "call_1", Name: "exec", Arguments: `{"input":"dir"}`,
	})
	require.Len(t, done, 2)
	require.Equal(t, "response.custom_tool_call_input.delta", done[0].Type)
	require.Equal(t, clientID, done[0].ItemID)
	require.Equal(t, "response.custom_tool_call_input.done", done[1].Type)
	require.Equal(t, clientID, done[1].ItemID)
	require.Equal(t, "call_1", done[1].CallID)

	closed := restorer.Restore(ResponsesStreamEvent{
		Type: "response.output_item.done", SequenceNumber: 3, OutputIndex: 0,
		Item: &ResponsesOutput{Type: "function_call", ID: upstreamID, CallID: "call_1", Name: "exec", Arguments: `{"input":"dir"}`, Status: "completed"},
	})
	require.Len(t, closed, 1)
	require.Equal(t, "custom_tool_call", closed[0].Item.Type)
	require.Equal(t, clientID, closed[0].Item.ID)
	require.Equal(t, "dir", closed[0].Item.Input)
}

// TestResponsesClientToolStreamRestorerRetypesToolSearchItemID opens a mapped
// discovery proxy with a function-prefixed ID. The single emitted added event
// must have discovery kind and the corresponding discovery-prefixed ID.
func TestResponsesClientToolStreamRestorerRetypesToolSearchItemID(t *testing.T) {
	restorer := NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{ToolSearch: true})

	added := restorer.Restore(ResponsesStreamEvent{
		Type: "response.output_item.added", SequenceNumber: 0, OutputIndex: 0,
		Item: &ResponsesOutput{Type: "function_call", ID: "fc_search1", CallID: "call_1", Name: toolSearchProxyName, Status: "in_progress"},
	})
	require.Len(t, added, 1)
	require.Equal(t, "tool_search_call", added[0].Item.Type)
	require.Equal(t, "tsc_search1", added[0].Item.ID)
}

// TestAdaptResponsesClientToolsRecoversRetypedItemID replays known
// custom/search IDs and an incompatible result ID. It checks call type/prefix
// recovery, retained input length, and removal of the inspected output ID.
func TestAdaptResponsesClientToolsRecoversRetypedItemID(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
		},
		"input": []any{
			map[string]any{"type": "custom_tool_call", "id": "ctc_upstream1", "call_id": "call_1", "name": "exec", "input": "dir"},
			map[string]any{"type": "tool_search_call", "id": "tsc_upstream2", "call_id": "call_2", "arguments": map[string]any{"query": "git"}},
			map[string]any{"type": "custom_tool_call_output", "id": "ctco_client", "call_id": "call_1", "output": "ok"},
		},
	}

	_, changed, err := AdaptResponsesClientTools(req)
	require.NoError(t, err)
	require.True(t, changed)

	input := requireResponsesClientToolValue[[]any](t, req["input"])
	require.Len(t, input, 3)
	customCall := requireResponsesClientToolValue[map[string]any](t, input[0])
	require.Equal(t, "function_call", customCall["type"])
	require.Equal(t, "fc_upstream1", customCall["id"])
	searchCall := requireResponsesClientToolValue[map[string]any](t, input[1])
	require.Equal(t, "function_call", searchCall["type"])
	require.Equal(t, "fc_upstream2", searchCall["id"])
	customOutput := requireResponsesClientToolValue[map[string]any](t, input[2])
	require.Equal(t, "function_call_output", customOutput["type"])
	require.NotContains(t, customOutput, "id")
}

// requireResponsesClientToolValue requires the fixture value's runtime type to
// match T and returns it. A mismatch fails the test through require.True, so
// callers can use the returned typed value after the assertion.
func requireResponsesClientToolValue[T any](t *testing.T, value any) T {
	t.Helper()
	typed, ok := value.(T)
	require.True(t, ok, "unexpected value type %T", value)
	return typed
}

// responsesClientToolNames returns names from required map declarations in
// input order. A nonmap fails the test; a missing or nonstring name
// contributes an empty label, and string values are retained without trimming.
func responsesClientToolNames(t *testing.T, tools []any) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool := requireResponsesClientToolValue[map[string]any](t, raw)
		// Record an empty label for a missing or nonstring name so the later
		// ordered-list assertion can expose the fixture's mismatch at that position.
		name, _ := tool["name"].(string)
		names = append(names, name)
	}
	return names
}
