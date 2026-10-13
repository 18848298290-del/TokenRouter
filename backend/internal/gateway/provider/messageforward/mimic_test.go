package messageforward

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// TestApplyClaudeCodeOAuthMimicryToBody_HaikuRewritesSystem converts a Haiku
// OAuth request carrying a client system string. The billing and Claude Code
// system blocks appear, and the client instructions move into messages.
func TestApplyClaudeCodeOAuthMimicryToBody_HaikuRewritesSystem(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 405, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"model":"claude-haiku-4-5","system":"Pi project instructions","messages":[{"role":"user","content":"hello"}]}`)
	svc := NewRuntime(Dependencies{}, Options{Configured: true})

	out := svc.mimic(
		context.Background(), (*requestBoundaryFixture)(nil), &AttemptState{},

		provider, body, "Pi project instructions", "claude-haiku-4-5",
	)

	system := gjson.GetBytes(out, "system").Array()
	require.Len(t, system, 3)
	require.Contains(t, system[0].Get("text").String(), "x-anthropic-billing-header:")
	require.Equal(t, claude.ClaudeCodeSystemPrompt, system[1].Get("text").String())
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), "Pi project instructions")
	require.Equal(t, "claude-haiku-4-5", gjson.GetBytes(out, "model").String())
}

// TestApplyClaudeCodeOAuthMimicryToBody_FableOmitsRefusedExpansion converts a
// Fable OAuth request carrying a client system string. Two identity blocks
// remain, the expansion is absent, and inserted instruction history precedes
// the user message.
func TestApplyClaudeCodeOAuthMimicryToBody_FableOmitsRefusedExpansion(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 406, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"model":"claude-fable-5","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	svc := NewRuntime(Dependencies{}, Options{Configured: true})

	out := svc.mimic(
		context.Background(), (*requestBoundaryFixture)(nil), &AttemptState{},

		provider, body, "Project instructions", "claude-fable-5",
	)

	system := gjson.GetBytes(out, "system").Array()
	require.Len(t, system, 2)
	require.Contains(t, system[0].Get("text").String(), "x-anthropic-billing-header:")
	require.Equal(t, claude.ClaudeCodeSystemPrompt, system[1].Get("text").String())
	require.NotContains(t, string(out), claude.ClaudeCodeSystemPromptExpansion)
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), "Project instructions")
	require.Equal(t, "Understood. I will follow these instructions.", gjson.GetBytes(out, "messages.1.content.0.text").String())
	require.Equal(t, "hello", gjson.GetBytes(out, "messages.2.content").String())
}

// TestGatewayClientDatelineNormalization_Scope compares provider eligibility
// with absent, disabled, and reenabled settings. OAuth and Setup Token
// normalize only while the setting allows the eligible Anthropic path.
func TestGatewayClientDatelineNormalization_Scope(t *testing.T) {
	repo := &betaSettingsFixture{values: map[string]string{}}
	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(repo.values)}, Options{})
	ctx := context.Background()

	// An absent setting enables normalization by default.
	require.True(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}))
	require.True(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeSetupToken}}))
	require.False(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}))
	require.False(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}))

	// Disabling the setting excludes both eligible credential types.
	repo.values[gateway.SettingKeyEnableClientDatelineNormalization] = "false"
	// Invalidate the settings snapshot after changing the fixture value so
	// the next eligibility check reads the updated flag during the same
	// test.
	svc.dependencies.Settings.InvalidateForwarding()
	require.False(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}))
	require.False(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeSetupToken}}))

	// Re-enabling the setting takes effect after invalidation.
	repo.values[gateway.SettingKeyEnableClientDatelineNormalization] = "true"
	// Invalidate the settings snapshot after changing the fixture value so
	// the next eligibility check reads the updated flag during the same
	// test.
	svc.dependencies.Settings.InvalidateForwarding()
	require.True(t, svc.shouldNormalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}))
}

// TestGatewayClientDatelineNormalization_HelperNoRewrite combines disabled and
// ineligible paths with recognized and ordinary user text. The helper reports
// nil and false for unchanged bodies, and returns normalized dates for eligible
// requests.
func TestGatewayClientDatelineNormalization_HelperNoRewrite(t *testing.T) {
	repo := &betaSettingsFixture{values: map[string]string{
		gateway.SettingKeyEnableClientDatelineNormalization: "true",
	}}
	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(repo.values)}, Options{})
	ctx := context.Background()

	dirty := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>"}]}`)
	clean := []byte(`{"messages":[{"role":"user","content":"just hello"}]}`)

	// API key requests keep the fingerprint text unchanged.
	next, ok := svc.normalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}}, dirty)
	require.False(t, ok)
	require.Nil(t, next)

	// A missing provider skips normalization.
	next, ok = svc.normalizeDateline(ctx, nil, dirty)
	require.False(t, ok)
	require.Nil(t, next)

	// An eligible provider with no dateline reports no change.
	next, ok = svc.normalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}, clean)
	require.False(t, ok)
	require.Nil(t, next)

	// OAuth requests normalize the dateline.
	next, ok = svc.normalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}, dirty)
	require.True(t, ok)
	require.NotNil(t, next)
	require.Contains(t, string(next), "Today's date is 2026-07-01.")
	require.NotContains(t, string(next), "2026/07/01")
	require.NotContains(t, string(next), "Today’s date is")

	// Setup Token requests also normalize the dateline.
	next, ok = svc.normalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeSetupToken}}, dirty)
	require.True(t, ok)
	require.Contains(t, string(next), "Today's date is 2026-07-01.")

	// The disabled setting overrides provider eligibility.
	repo.values[gateway.SettingKeyEnableClientDatelineNormalization] = "false"
	// Invalidate the settings snapshot after changing the fixture value so
	// the next eligibility check reads the updated flag during the same
	// test.
	svc.dependencies.Settings.InvalidateForwarding()
	next, ok = svc.normalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}, dirty)
	require.False(t, ok)
	require.Nil(t, next)
}

// TestGatewayClientDatelineNormalization_LeavesUserProseUntouched uses
// fingerprint-like date prose outside a system-reminder block on an eligible
// provider. Both runtime and protocol normalizer report unchanged bytes and no
// detected datelines.
func TestGatewayClientDatelineNormalization_LeavesUserProseUntouched(t *testing.T) {
	repo := &betaSettingsFixture{values: map[string]string{
		gateway.SettingKeyEnableClientDatelineNormalization: "true",
	}}
	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(repo.values)}, Options{})
	ctx := context.Background()

	// User prose resembling a fingerprint remains unchanged outside
	// system-reminder blocks.
	body := []byte(`{"messages":[{"role":"user","content":"I wrote: Today’s date is 2026/07/01. What do you think?"}]}`)
	next, ok := svc.normalizeDateline(ctx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}}, body)
	require.False(t, ok, "must not rewrite user prose outside <system-reminder>")
	require.Nil(t, next)

	// The underlying normalizer also limits its scan to system-reminder
	// blocks.
	out, hits, changed := claude.NormalizeDateline(body)
	require.False(t, changed)
	require.Empty(t, hits)
	require.Equal(t, body, out)
}
