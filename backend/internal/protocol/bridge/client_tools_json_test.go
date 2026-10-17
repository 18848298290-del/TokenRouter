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

// TestAdaptResponsesClientToolsJSONRequiresHistoricalNamespaceEvidence sends
// qualified history without declarations or inherited state. Conversion reports
// the missing identity instead of forwarding an unresolved namespace downstream.
func TestAdaptResponsesClientToolsJSONRequiresHistoricalNamespaceEvidence(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","name":"js","namespace":"browser","call_id":"call_1","arguments":"{}"}]}`)
	_, _, err := AdaptResponsesClientToolsJSON(body, "OpenAI")
	require.ErrorContains(t, err, "without its declaration or inherited mapping")
}
