package bridge

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewResponsesToolHistoryNamesSeparatesCollisions supplies colliding joins,
// active and bare names, and long or non-ASCII identities. Every replay name
// stays distinct and bounded, and reversing history leaves the allocation unchanged.
func TestNewResponsesToolHistoryNamesSeparatesCollisions(t *testing.T) {
	identities := []NamespacedToolName{
		{Namespace: "a", Name: "b__c"},
		{Namespace: "a__b", Name: "c"},
		{Namespace: "browser", Name: "js"},
		{Name: "browser__js"},
		{Namespace: "terminal", Name: "exec"},
		{Namespace: strings.Repeat("long", 30), Name: "run"},
		{Namespace: strings.Repeat("long", 30), Name: "stop"},
		{Namespace: "空间.with dots", Name: "run"},
	}
	mapping := ResponsesClientToolMapping{
		FunctionTools: map[string]bool{"terminal__exec": true},
		NamespaceTools: map[string]ResponsesNamespaceName{
			"known__run": {Namespace: "known", Name: "run"},
		},
	}
	items := make([]map[string]any, 0, len(identities))
	for _, identity := range identities {
		items = append(items, map[string]any{
			"type": "function_call", "namespace": identity.Namespace, "name": identity.Name,
		})
	}
	encoded, err := json.Marshal(items)
	require.NoError(t, err)
	names, err := newResponsesToolHistoryNames(encoded, mapping)
	require.NoError(t, err)
	seen := make(map[string]bool)
	for _, identity := range identities {
		name, err := names.resolve(identity.Namespace, identity.Name)
		require.NoError(t, err)
		require.Regexp(t, `^[A-Za-z0-9_-]{1,64}$`, name)
		require.False(t, seen[name], "identities must retain distinct replay names: %v", identity)
		require.NotEqual(t, "terminal__exec", name)
		seen[name] = true
	}
	slices.Reverse(items)
	reversed, err := json.Marshal(items)
	require.NoError(t, err)
	again, err := newResponsesToolHistoryNames(reversed, mapping)
	require.NoError(t, err)
	require.Equal(t, names, again)
	require.Equal(t, map[string]bool{"terminal__exec": true}, mapping.FunctionTools)
	require.Equal(t, map[string]ResponsesNamespaceName{"known__run": {Namespace: "known", Name: "run"}}, mapping.NamespaceTools)
}

// TestNewResponsesToolHistoryNamesPreservesKnownAliases supplies inherited
// discovery and namespace identities beside an undeclared call. Known names
// remain unchanged, and allocating history never authorizes the unknown selector.
func TestNewResponsesToolHistoryNamesPreservesKnownAliases(t *testing.T) {
	mapping := ResponsesClientToolMapping{
		NamespaceTools: map[string]ResponsesNamespaceName{
			"js":          {Namespace: "js", Name: "js"},
			"browser__js": {Namespace: "browser", Name: "js"},
		},
	}
	input := json.RawMessage(`[
		{"type":"function_call","namespace":"js","name":"js"},
		{"type":"function_call","namespace":"browser","name":"js"},
		{"type":"function_call","namespace":"old","name":"js"},
		{"type":"function_call_output","output":{"type":"function_call","namespace":"nested","name":"data"}}
	]`)
	names, err := newResponsesToolHistoryNames(input, mapping)
	require.NoError(t, err)
	require.Equal(t, "js", names[NamespacedToolName{Namespace: "js", Name: "js"}])
	require.Equal(t, "browser__js", names[NamespacedToolName{Namespace: "browser", Name: "js"}])
	require.Equal(t, "old__js", names[NamespacedToolName{Namespace: "old", Name: "js"}])
	require.Len(t, names, 3)
	require.Len(t, mapping.NamespaceTools, 2)
	require.Empty(t, mapping.CallNames)
	_, err = responsesMappedToolChoice(json.RawMessage(`{"type":"function","namespace":"old","name":"js"}`), &mapping)
	require.ErrorContains(t, err, "without its declaration or inherited mapping")
}

// TestNewResponsesToolHistoryNamesRejectsEmptyQualifiedNames supplies an empty
// child name in a nondefault namespace. Preparing replay state does not turn
// that malformed call into an invented executable function name.
func TestNewResponsesToolHistoryNamesRejectsEmptyQualifiedNames(t *testing.T) {
	names, err := newResponsesToolHistoryNames(json.RawMessage(`[{"type":"function_call","namespace":"browser","name":""}]`), ResponsesClientToolMapping{})
	require.NoError(t, err)
	_, err = names.resolve("browser", "")
	require.Error(t, err)
}
