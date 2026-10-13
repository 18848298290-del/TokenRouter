package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Fingerprinting starts disabled unless an eligible provider has a recognized
// administrator mode. The mode matrix covers nil and unrelated providers,
// OpenAI Setup Tokens, and each accepted OAuth mode, using a fixed seed fixture
// when a selected mode needs one.

// testCodexFingerprintSeed gives enabled fixtures a stable canonical UUID so
// mode assertions do not depend on randomness or a persistence-owned generator.
const testCodexFingerprintSeed = "11111111-1111-4111-8111-111111111111"

// newTestOAuthProvider builds an OpenAI OAuth fixture and inserts the fixed
// seed when its mode requires one. A supplied Extra map is mutated for a
// missing seed and then retained by the returned record.
func newTestOAuthProvider(id int64, extra map[string]any) *Record {
	if CodexFingerprintModeRequiresSeed(CodexFingerprintModeFromExtra(extra)) {
		if extra == nil {
			extra = make(map[string]any)
		}
		if _, exists := extra[CodexFingerprintSeedExtraKey]; !exists {
			extra[CodexFingerprintSeedExtraKey] = testCodexFingerprintSeed
		}
	}
	return &Record{
		ID:       id,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Extra:    extra,
	}
}

// TestGetCodexFingerprintMode compares fingerprint mode readings across
// provider kinds and saved settings. Missing, empty, and invalid settings stay
// off, while eligible records return each recognized configured mode.
func TestGetCodexFingerprintMode(t *testing.T) {
	tests := []struct {
		name     string               // test case display name
		provider *Record              // provider configuration under test
		expected CodexFingerprintMode // expected accessor result
	}{
		{"nil 提供商", nil, CodexFingerprintOff},
		{"非 OAuth 提供商", &Record{Platform: capability.PlatformOpenAI, Type: "api_key"}, CodexFingerprintOff},
		{"OpenAI setup token", &Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken, Extra: map[string]any{CodexFingerprintModeExtraKey: "session"}}, CodexFingerprintSession},
		{"Anthropic setup token", &Record{Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeSetupToken, Extra: map[string]any{CodexFingerprintModeExtraKey: "session"}}, CodexFingerprintOff},
		{"无 extra 默认 off", newTestOAuthProvider(1, nil), CodexFingerprintOff},
		{"空值默认 off", newTestOAuthProvider(1, map[string]any{CodexFingerprintModeExtraKey: ""}), CodexFingerprintOff},
		{"非法值默认 off", newTestOAuthProvider(1, map[string]any{CodexFingerprintModeExtraKey: "invalid"}), CodexFingerprintOff},
		{"显式 off", newTestOAuthProvider(1, map[string]any{CodexFingerprintModeExtraKey: "off"}), CodexFingerprintOff},
		{"device", newTestOAuthProvider(1, map[string]any{CodexFingerprintModeExtraKey: "device"}), CodexFingerprintDevice},
		{"session", newTestOAuthProvider(1, map[string]any{CodexFingerprintModeExtraKey: "session"}), CodexFingerprintSession},
		{"full", newTestOAuthProvider(1, map[string]any{CodexFingerprintModeExtraKey: "full"}), CodexFingerprintFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.provider.GetCodexFingerprintMode())
		})
	}
}
