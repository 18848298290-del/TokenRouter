package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPreparedModelRulesParity exercises four platforms with empty, wildcard,
// alias, and Unicode whitelists. It compares prepared admission, final membership,
// mapped targets and flags, forwarding, and configured names with per-call helpers.
func TestPreparedModelRulesParity(t *testing.T) {
	rules := ModelPlatformRules{
		NormalizeQoder:      func(model string) string { return strings.TrimSuffix(strings.ToLower(model), "-alias") },
		QoderCompatible:     func(model string) bool { return model != "blocked-site" },
		OpenAIOAuthServable: func(model string) bool { return !strings.HasPrefix(model, "foreign-") },
	}
	defaults := ModelMappingDefaults{Models: func(*Record) []string { return []string{"known", "foreign-model"} }, Antigravity: func() map[string]string { return map[string]string{"alias": "known"} }}
	for _, platform := range []string{PlatformOpenAI, PlatformQoder, PlatformAnthropic, PlatformAntigravity} {
		for _, whitelist := range [][]string{nil, {}, {"known", "gpt-*", "ſ", "Σ", "K", "İ"}, {"*"}, {"known-alias", "blocked-site"}} {
			record := &Record{Platform: platform, Type: "oauth", Credentials: map[string]any{
				"model_whitelist": whitelist,
				"model_mapping":   map[string]any{"alias": "known", "gpt-*": "known", "gpt-special*": "foreign-model", "empty-target": ""},
			}}
			snapshot := PrepareModelRules(record, defaults, rules)
			for _, model := range []string{"", "known", "KNOWN", " known ", "alias", " alias ", "gpt-new", "gpt-special-new", "empty-target", "unknown", "foreign-model", "blocked-site", "known-alias", "S", "s", "ſ", "σ", "ς", "Σ", "K", "k", "K", "İ", "i", "I", "ı"} {
				require.Equal(t, record.IsModelSupported(model, defaults, rules), snapshot.Supports(model), "platform=%s whitelist=%v model=%s", platform, whitelist, model)
				require.Equal(t, record.FinalModelWhitelisted(model, defaults, rules), snapshot.FinalModelWhitelisted(model), "platform=%s whitelist=%v model=%s", platform, whitelist, model)
				mapped, matched := ResolveMappedModel(ResolveModelMapping(record, defaults), model)
				got, gotMatch := snapshot.ResolveMappedModel(model)
				require.Equal(t, mapped, got)
				require.Equal(t, matched, gotMatch)
				require.Equal(t, ResolveForwardMappedModel(record, model, defaults), snapshot.ForwardMappedModel(model))
			}
			require.Equal(t, record.GetConfiguredRequestModels(defaults), snapshot.ConfiguredModels())
		}
	}
}

// TestPreparedModelRulesOwnConfiguration mutates the original mapping and
// whitelist plus the snapshot's returned collections. It checks that captured
// admission, the alias target, and configured names retain their prepared values.
func TestPreparedModelRulesOwnConfiguration(t *testing.T) {
	mapping := map[string]any{"alias": "known"}
	whitelist := []string{"known"}
	record := &Record{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": mapping, "model_whitelist": whitelist}}
	snapshot := PrepareModelRules(record, ModelMappingDefaults{}, ModelPlatformRules{})
	mapping["alias"] = "changed"
	whitelist[0] = "changed"
	copy := snapshot.Mapping()
	copy["alias"] = "copy-change"
	models := snapshot.ConfiguredModels()
	models[0] = "copy-change"
	require.True(t, snapshot.Supports("alias"))
	require.False(t, snapshot.Supports("changed"))
	require.Equal(t, "known", snapshot.ForwardMappedModel("alias"))
	require.ElementsMatch(t, []string{"known", "alias"}, snapshot.ConfiguredModels())
}

// TestPreparedModelRulesReadDefaultsOnce repeatedly validates an Antigravity
// alias and reads configured names after preparation. It checks that the model
// and mapping factories each ran once across those subsequent queries.
func TestPreparedModelRulesReadDefaultsOnce(t *testing.T) {
	modelsReads, mappingReads := 0, 0
	defaults := ModelMappingDefaults{
		Models:      func(*Record) []string { modelsReads++; return []string{"known"} },
		Antigravity: func() map[string]string { mappingReads++; return map[string]string{"alias": "known"} },
	}
	record := &Record{Platform: PlatformAntigravity}
	snapshot := PrepareModelRules(record, defaults, ModelPlatformRules{})
	for range 100 {
		require.True(t, snapshot.Supports("alias"))
		require.True(t, snapshot.FinalModelWhitelisted("known"))
		_ = snapshot.ConfiguredModels()
	}
	require.Equal(t, 1, modelsReads)
	require.Equal(t, 1, mappingReads)
}
