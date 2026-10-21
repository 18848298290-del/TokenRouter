package bridge

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestAdaptResponsesClientToolsJSONPromotesInitialAdditionalTools supplies
// additional declarations through the byte adapter without prior conversation
// state. The request and restored response use the same namespace mapping.
func TestAdaptResponsesClientToolsJSONPromotesInitialAdditionalTools(t *testing.T) {
	body := []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"browser","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}]},{"type":"function_call","name":"js","namespace":"browser","call_id":"call_1","arguments":"{}"}]}`)
	adapted, mapping, err := AdaptResponsesClientToolsJSON(body, "OpenAI")
	require.NoError(t, err)
	require.Equal(t, "browser__js", gjson.GetBytes(adapted, "tools.0.name").String())
	require.Equal(t, "browser__js", gjson.GetBytes(adapted, "input.0.name").String())
	require.False(t, gjson.GetBytes(adapted, "input.0.namespace").Exists())
	restored, _, err := RestoreResponsesClientToolPayload([]byte(`{"output":[{"type":"function_call","name":"browser__js","call_id":"call_2","arguments":"{}"}]}`), mapping)
	require.NoError(t, err)
	require.Equal(t, "browser", gjson.GetBytes(restored, "output.0.namespace").String())
	require.Equal(t, "js", gjson.GetBytes(restored, "output.0.name").String())
}

// TestAdaptResponsesClientToolsJSONReplaysUndeclaredHistory sends qualified
// history with absent, reset, and replaced tools. Conversion retains the call
// and result while synthesized identities stay out of future mappings and tools.
func TestAdaptResponsesClientToolsJSONReplaysUndeclaredHistory(t *testing.T) {
	for name, tools := range map[string]string{
		"omitted": "", "empty": `,"tools":[]`, "null": `,"tools":null`,
		"search":      `,"tools":[{"type":"tool_search"}]`,
		"replacement": `,"tools":[{"type":"function","name":"current"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"function_call","name":"js","namespace":"browser","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":{"value":9007199254740993,"nested":{"type":"function_call","namespace":"data","name":"js"}}}]` + tools + `}`)
			adapted, mapping, err := AdaptResponsesClientToolsJSON(body, "OpenAI")
			require.NoError(t, err)
			require.Equal(t, "browser__js", gjson.GetBytes(adapted, "input.0.name").String())
			require.False(t, gjson.GetBytes(adapted, "input.0.namespace").Exists())
			require.Equal(t, "call_1", gjson.GetBytes(adapted, "input.0.call_id").String())
			require.Equal(t, "call_1", gjson.GetBytes(adapted, "input.1.call_id").String())
			require.Equal(t, "9007199254740993", gjson.GetBytes(adapted, "input.1.output.value").Raw)
			require.Equal(t, "data", gjson.GetBytes(adapted, "input.1.output.nested.namespace").String())
			require.NotContains(t, mapping.NamespaceTools, "browser__js")
			require.NotContains(t, mapping.CallNames, NamespacedToolName{Namespace: "browser", Name: "js"})
			require.False(t, gjson.GetBytes(adapted, `tools.#(name=="browser__js")`).Exists())
			again, _, err := AdaptResponsesClientToolsJSON(adapted, "OpenAI")
			require.NoError(t, err)
			require.JSONEq(t, string(adapted), string(again))
		})
	}
}
