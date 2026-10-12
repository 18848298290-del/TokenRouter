package messageforward

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

func TestResolveBedrockBetaTokensForRequest_BlocksOnOriginalAnthropicToken(t *testing.T) {
	settings := &anthropic.BetaPolicySettings{
		Rules: []anthropic.BetaPolicyRule{
			{
				BetaToken:    "advanced-tool-use-2025-11-20",
				Action:       anthropic.BetaPolicyActionBlock,
				Scope:        anthropic.BetaPolicyScopeAll,
				ErrorMessage: "advanced tool use is blocked",
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(map[string]string{gateway.SettingKeyBetaPolicySettings: string(raw)})}, Options{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock}}

	_, err = svc.bedrockBetaTokens(
		context.Background(),
		provider,
		"advanced-tool-use-2025-11-20",
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		"us.anthropic.claude-opus-4-6-v1",
	)
	if err == nil {
		t.Fatal("expected raw advanced-tool-use token to be blocked before Bedrock transform")
	}
	if err.Error() != "advanced tool use is blocked" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveBedrockBetaTokensForRequest_FiltersAfterBedrockTransform(t *testing.T) {
	settings := &anthropic.BetaPolicySettings{
		Rules: []anthropic.BetaPolicyRule{
			{
				BetaToken: "tool-search-tool-2025-10-19",
				Action:    anthropic.BetaPolicyActionFilter,
				Scope:     anthropic.BetaPolicyScopeAll,
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(map[string]string{gateway.SettingKeyBetaPolicySettings: string(raw)})}, Options{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock}}

	betaTokens, err := svc.bedrockBetaTokens(
		context.Background(),
		provider,
		"advanced-tool-use-2025-11-20",
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		"us.anthropic.claude-opus-4-6-v1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, token := range betaTokens {
		if token == "tool-search-tool-2025-10-19" {
			t.Fatalf("expected transformed Bedrock token to be filtered")
		}
	}
}

// TestResolveBedrockBetaTokensForRequest_BlocksBodyAutoInjectedComputerUse checks
// that the block rule also applies when the header omits computer-use
// and the tool in the body causes the beta token to be injected.
func TestResolveBedrockBetaTokensForRequest_BlocksBodyAutoInjectedComputerUse(t *testing.T) {
	settings := &anthropic.BetaPolicySettings{
		Rules: []anthropic.BetaPolicyRule{
			{
				BetaToken:    "computer-use-2025-11-24",
				Action:       anthropic.BetaPolicyActionBlock,
				Scope:        anthropic.BetaPolicyScopeAll,
				ErrorMessage: "computer use is blocked",
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(map[string]string{gateway.SettingKeyBetaPolicySettings: string(raw)})}, Options{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock}}

	// The body supplies the tool while the header omits its beta.
	_, err = svc.bedrockBetaTokens(
		context.Background(),
		provider,
		"", // Empty beta header.
		[]byte(`{"tools":[{"type":"computer_20250124","name":"computer"}],"messages":[{"role":"user","content":"hi"}]}`),
		"us.anthropic.claude-opus-4-6-v1",
	)
	if err == nil {
		t.Fatal("expected body-injected computer-use to be blocked")
	}
	if err.Error() != "computer use is blocked" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestResolveBedrockBetaTokensForRequest_BlocksBodyAutoInjectedToolSearch checks
// that a blocked tool-search beta is rejected even when it is inferred
// from the request body rather than supplied in the header.
func TestResolveBedrockBetaTokensForRequest_BlocksBodyAutoInjectedToolSearch(t *testing.T) {
	settings := &anthropic.BetaPolicySettings{
		Rules: []anthropic.BetaPolicyRule{
			{
				BetaToken:    "tool-search-tool-2025-10-19",
				Action:       anthropic.BetaPolicyActionBlock,
				Scope:        anthropic.BetaPolicyScopeAll,
				ErrorMessage: "tool search is blocked",
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(map[string]string{gateway.SettingKeyBetaPolicySettings: string(raw)})}, Options{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock}}

	// The body supplies tool search while the header omits its beta.
	_, err = svc.bedrockBetaTokens(
		context.Background(),
		provider,
		"",
		[]byte(`{"tools":[{"type":"tool_search_tool_regex_20251119","name":"search"}],"messages":[{"role":"user","content":"hi"}]}`),
		"us.anthropic.claude-sonnet-4-6",
	)
	if err == nil {
		t.Fatal("expected body-injected tool-search-tool to be blocked")
	}
	if err.Error() != "tool search is blocked" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestResolveBedrockBetaTokensForRequest_PassesWhenNoBlockRuleMatches checks
// that an injected token passes when its beta has no matching block rule.
func TestResolveBedrockBetaTokensForRequest_PassesWhenNoBlockRuleMatches(t *testing.T) {
	settings := &anthropic.BetaPolicySettings{
		Rules: []anthropic.BetaPolicyRule{
			{
				BetaToken:    "context-1m-2025-08-07",
				Action:       anthropic.BetaPolicyActionBlock,
				Scope:        anthropic.BetaPolicyScopeAll,
				ErrorMessage: "context is blocked",
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	svc := NewRuntime(Dependencies{Settings: newBetaRuntime(map[string]string{gateway.SettingKeyBetaPolicySettings: string(raw)})}, Options{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock}}

	// The body injects computer-use, but the block rule targets context-1m.
	tokens, err := svc.bedrockBetaTokens(
		context.Background(),
		provider,
		"",
		[]byte(`{"tools":[{"type":"computer_20250124","name":"computer"}],"messages":[{"role":"user","content":"hi"}]}`),
		"us.anthropic.claude-opus-4-6-v1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, token := range tokens {
		if token == "computer-use-2025-11-24" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected computer-use token to be present")
	}
}
