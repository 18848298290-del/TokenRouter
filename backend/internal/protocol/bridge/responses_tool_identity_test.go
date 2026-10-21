package bridge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResponsesToolIdentityMappingSeparatesNamespaceAndSearchAliases builds
// explicit, default, and dynamically loaded function identities together. The
// same child name resolves independently while discovered functions stay bare.
func TestResponsesToolIdentityMappingSeparatesNamespaceAndSearchAliases(t *testing.T) {
	tools := []ResponsesTool{
		{Type: "namespace", Name: "functions", Tools: []ResponsesTool{{Type: "function", Name: "wait"}}},
		{Type: "namespace", Name: "browser", Tools: []ResponsesTool{{Type: "function", Name: "js"}}},
		{Type: "namespace", Name: "console", Tools: []ResponsesTool{{Type: "function", Name: "js"}}},
	}
	input := json.RawMessage(`[{"type":"tool_search_output","status":"completed","tools":[{"type":"function","name":"inspect"}]}]`)
	mapping, err := responsesToolIdentityMapping(tools, input)
	require.NoError(t, err)
	for _, identity := range []NamespacedToolName{
		{Name: "wait"},
		{Namespace: "functions", Name: "wait"},
		{Namespace: "browser", Name: "js"},
		{Namespace: "console", Name: "js"},
		{Namespace: "inspect", Name: "inspect"},
	} {
		name, err := mapping.resolveCallName(identity.Namespace, identity.Name)
		require.NoError(t, err)
		switch identity.Namespace {
		case "browser", "console":
			require.Equal(t, identity.Namespace+"__js", name)
		default:
			require.Equal(t, identity.Name, name)
		}
	}
	require.Equal(t, NamespacedToolName{Name: "wait"}, mapping.NamespaceTools["wait"])
	require.Equal(t, NamespacedToolName{Namespace: "inspect", Name: "inspect"}, mapping.NamespaceTools["inspect"])
}

// TestResponsesToolIdentityMappingRejectsAmbiguousNames supplies independent
// tools whose flattened names or client aliases overlap. Conversion rejects
// each request instead of choosing one tool through declaration order.
func TestResponsesToolIdentityMappingRejectsAmbiguousNames(t *testing.T) {
	for _, tools := range [][]ResponsesTool{
		{{Type: "function", Name: "browser__js"}, {Type: "namespace", Name: "browser", Tools: []ResponsesTool{{Type: "function", Name: "js"}}}},
		{{Type: "namespace", Name: "inspect", Tools: []ResponsesTool{{Type: "function", Name: "inspect"}}}},
	} {
		_, err := responsesToolIdentityMapping(tools, json.RawMessage(`[{"type":"tool_search_output","tools":[{"type":"function","name":"inspect"}]}]`))
		require.Error(t, err)
	}
}

// TestResponsesToolIdentityMappingRequiresNamespaceEvidence supplies an
// ordinary function beside unknown qualified identities. Declaration lookup
// cannot infer a discovery alias or authorize an undeclared namespace selector.
func TestResponsesToolIdentityMappingRequiresNamespaceEvidence(t *testing.T) {
	mapping, err := responsesToolIdentityMapping([]ResponsesTool{{Type: "function", Name: "js"}}, nil)
	require.NoError(t, err)
	_, err = mapping.resolveCallName("js", "js")
	require.ErrorContains(t, err, "without its declaration or inherited mapping")
	_, err = mapping.resolveCallName("browser", "js")
	require.Error(t, err)
}

// TestResponsesMappedToolChoiceUsesCallIdentity supplies selectors for explicit
// and dynamically loaded functions. Choice conversion follows the same names
// as call history and rejects a selector without declaration evidence.
func TestResponsesMappedToolChoiceUsesCallIdentity(t *testing.T) {
	mapping, err := responsesToolIdentityMapping([]ResponsesTool{
		{Type: "namespace", Name: "browser", Tools: []ResponsesTool{{Type: "function", Name: "js"}}},
	}, json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"function","name":"inspect"}]}]`))
	require.NoError(t, err)
	for raw, expected := range map[string]string{
		`{"type":"function","name":"js","namespace":"browser"}`:      `{"type":"function","name":"browser__js"}`,
		`{"type":"function","name":"inspect","namespace":"inspect"}`: `{"type":"function","name":"inspect"}`,
		`"none"`: `"none"`,
	} {
		mapped, err := responsesMappedToolChoice(json.RawMessage(raw), &mapping)
		require.NoError(t, err)
		require.JSONEq(t, expected, string(mapped))
	}
	_, err = responsesMappedToolChoice(json.RawMessage(`{"type":"function","name":"js","namespace":"unknown"}`), &mapping)
	require.Error(t, err)
}
