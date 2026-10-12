package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestModelMappingDefaultsAreLazyAndIsolated checks that defaults are read on demand and copied.
func TestModelMappingDefaultsAreLazyAndIsolated(t *testing.T) {
	calls := 0
	defaults := map[string]string{"alias": "default-model"}
	options := ModelMappingDefaults{Antigravity: func() map[string]string { calls++; return defaults }}
	r := &Record{Platform: PlatformAntigravity, Credentials: map[string]any{"model_mapping": map[string]any{"explicit": "target"}}}
	value := ResolveModelMapping(r, options)
	require.Zero(t, calls)
	require.Equal(t, "target", value["explicit"])
	r.Credentials = nil
	value = ResolveModelMapping(r, options)
	require.Equal(t, 1, calls)
	value["alias"] = "changed"
	require.Equal(t, "default-model", defaults["alias"])
	require.Equal(t, "default-model", ResolveModelMapping(r, options)["alias"])
}

// TestResolveMappedModelPreservesMatchOrder checks raw exact, raw wildcard, then trimmed matching.
func TestResolveMappedModelPreservesMatchOrder(t *testing.T) {
	cases := []struct {
		name      string
		mapping   map[string]string
		requested string
		want      string
		matched   bool
	}{
		{name: "原始精确值优先", mapping: map[string]string{" model ": "raw", "model": "trimmed", "*": "wildcard"}, requested: " model ", want: "raw", matched: true},
		{name: "原始通配符优先", mapping: map[string]string{"model": "trimmed", "*": "wildcard"}, requested: " model ", want: "wildcard", matched: true},
		{name: "去空白后匹配", mapping: map[string]string{"model": "trimmed"}, requested: " model ", want: "trimmed", matched: true},
		{name: "未命中保留输入", mapping: map[string]string{"other": "target"}, requested: " model ", want: " model "},
		{name: "映射执行一次", mapping: map[string]string{"first": "second", "second": "third"}, requested: "first", want: "second", matched: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, matched := ResolveMappedModel(tc.mapping, tc.requested)
			require.Equal(t, tc.want, model)
			require.Equal(t, tc.matched, matched)
		})
	}
}

// TestExplicitAntigravityMappingIsNotAugmented checks that explicit mappings stay separate from defaults.
func TestExplicitAntigravityMappingIsNotAugmented(t *testing.T) {
	raw := map[string]any{"gemini-pro-agent": "custom", "gemini-3.1-pro-high": "gemini-3.1-pro-high"}
	value := &Record{Platform: PlatformAntigravity, Credentials: map[string]any{"model_mapping": raw}}
	defaults := ModelMappingDefaults{}
	got := ResolveModelMapping(value, defaults)
	require.Equal(t, map[string]string{"gemini-pro-agent": "custom", "gemini-3.1-pro-high": "gemini-3.1-pro-high"}, got)
	got["gemini-pro-agent"] = "changed"
	require.Equal(t, "custom", raw["gemini-pro-agent"])
	model, matched := ResolveMappedModel(nil, "gemini-3.1-pro-preview-customtools")
	require.False(t, matched)
	require.Equal(t, "gemini-3.1-pro-preview-customtools", model)
}
