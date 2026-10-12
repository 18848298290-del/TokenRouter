package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"golang.org/x/sync/singleflight"

	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

const (
	// Persistent keys for gateway policies.
	SettingKeyBetaPolicySettings       = "beta_policy_settings"
	SettingKeyOpenAIFastPolicySettings = "openai_fast_policy_settings"
	SettingKeyRectifierSettings        = "rectifier_settings"

	// Beta actions and scopes use the values accepted by the management API.
	BetaPolicyActionPass   = "pass"   // Pass through unchanged.
	BetaPolicyActionFilter = "filter" // Remove the token from the beta header.
	BetaPolicyActionBlock  = "block"  // Reject the request.

	BetaPolicyScopeAll     = "all"     // All provider types.
	BetaPolicyScopeOAuth   = "oauth"   // OAuth providers.
	BetaPolicyScopeAPIKey  = "apikey"  // API key providers.
	BetaPolicyScopeBedrock = "bedrock" // AWS Bedrock providers.
)

// OpenAIFastPolicySettings configures OpenAI service tier rules.
type OpenAIFastPolicySettings = tierpolicy.OpenAIFastPolicySettings

// RuntimeSettingsStore reads and writes gateway policy settings.
type RuntimeSettingsStore interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
	GetValue(context.Context, string) (string, error)
	Set(context.Context, string, string) error
}

// RuntimeSettings reads and caches gateway settings using caller-supplied platform defaults.
type RuntimeSettings struct {
	clientOptions               ClientSettingsOptions
	antigravityUAVersionCache   atomic.Value
	antigravityUAVersionSF      singleflight.Group
	openAICodexUACache          atomic.Value
	openAICodexUASF             singleflight.Group
	openAIAllowCodexPluginCache atomic.Value
	openAIAllowCodexPluginSF    singleflight.Group
	versionBoundsCache          atomic.Value
	versionBoundsSF             singleflight.Group
	gatewayForwardingCache      atomic.Value
	gatewayForwardingSF         singleflight.Group
	settingRepo                 RuntimeSettingsStore
	notFound                    error
	defaultBeta                 func() *BetaPolicySettings
}

// RectifierSettings controls request signature and thinking budget repairs.
type RectifierSettings struct {
	Enabled                  bool     `json:"enabled"`                    // Enables all configured repairs.
	ThinkingSignatureEnabled bool     `json:"thinking_signature_enabled"` // Thinking signature repair.
	ThinkingBudgetEnabled    bool     `json:"thinking_budget_enabled"`    // Thinking budget repair.
	APIKeySignatureEnabled   bool     `json:"apikey_signature_enabled"`   // Enables signature repair for API key providers.
	APIKeySignaturePatterns  []string `json:"apikey_signature_patterns"`  // Custom signature-error patterns for API key providers.
}

// BetaPolicyRule defines a beta token action and its provider and model scope.
type BetaPolicyRule struct {
	BetaToken            string   `json:"beta_token"`                       // Beta token value.
	Action               string   `json:"action"`                           // "pass" | "filter" | "block"
	Scope                string   `json:"scope"`                            // "all" | "oauth" | "apikey" | "bedrock"
	ErrorMessage         string   `json:"error_message,omitempty"`          // Custom error message when action is block.
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // Model patterns; empty matches all models.
	FallbackAction       string   `json:"fallback_action,omitempty"`        // Action for models outside the whitelist.
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // Custom error for unmatched models when fallback_action is block.
}

// BetaPolicySettings holds beta rules in matching order.
type BetaPolicySettings struct {
	Rules []BetaPolicyRule `json:"rules"`
}

// NewRuntimeSettings stores dependencies without reading settings. Assembly supplies betaDefaults.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error, betaDefaults func() *BetaPolicySettings, clientOptions ...ClientSettingsOptions) *RuntimeSettings {
	value := &RuntimeSettings{settingRepo: repo, notFound: notFound, defaultBeta: betaDefaults}
	if len(clientOptions) > 0 {
		value.clientOptions = clientOptions[0]
	}
	return value
}

// DefaultRectifierSettings enables thinking signature and budget repairs.
func DefaultRectifierSettings() *RectifierSettings {
	return &RectifierSettings{
		Enabled:                  true,
		ThinkingSignatureEnabled: true,
		ThinkingBudgetEnabled:    true,
	}
}

// GetRectifierSettings returns defaults for missing, empty, or invalid JSON settings.
func (s *RuntimeSettings) GetRectifierSettings(ctx context.Context) (*RectifierSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRectifierSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return DefaultRectifierSettings(), nil
		}
		return nil, fmt.Errorf("get rectifier settings: %w", err)
	}
	if value == "" {
		return DefaultRectifierSettings(), nil
	}

	var settings RectifierSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultRectifierSettings(), nil
	}

	return &settings, nil
}

// SetRectifierSettings saves settings as JSON and rejects nil settings.
func (s *RuntimeSettings) SetRectifierSettings(ctx context.Context, settings *RectifierSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal rectifier settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyRectifierSettings, string(data))
}

// IsSignatureRectifierEnabled checks both repair switches and returns true on read errors.
func (s *RuntimeSettings) IsSignatureRectifierEnabled(ctx context.Context) bool {
	settings, err := s.GetRectifierSettings(ctx)
	if err != nil {
		return true // Enable repairs on read failure.
	}
	return settings.Enabled && settings.ThinkingSignatureEnabled
}

// IsBudgetRectifierEnabled checks both repair switches and returns true on read errors.
func (s *RuntimeSettings) IsBudgetRectifierEnabled(ctx context.Context) bool {
	settings, err := s.GetRectifierSettings(ctx)
	if err != nil {
		return true // Enable repairs on read failure.
	}
	return settings.Enabled && settings.ThinkingBudgetEnabled
}

// GetBetaPolicySettings uses caller-supplied defaults for missing, empty, or invalid JSON settings.
func (s *RuntimeSettings) GetBetaPolicySettings(ctx context.Context) (*BetaPolicySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyBetaPolicySettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return s.defaultBeta(), nil
		}
		return nil, fmt.Errorf("get beta policy settings: %w", err)
	}
	if value == "" {
		return s.defaultBeta(), nil
	}

	var settings BetaPolicySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return s.defaultBeta(), nil
	}

	return &settings, nil
}

// SetBetaPolicySettings validates and saves rules, trimming model patterns in place.
func (s *RuntimeSettings) SetBetaPolicySettings(ctx context.Context, settings *BetaPolicySettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}

	validActions := map[string]bool{
		BetaPolicyActionPass: true, BetaPolicyActionFilter: true, BetaPolicyActionBlock: true,
	}
	validScopes := map[string]bool{
		BetaPolicyScopeAll: true, BetaPolicyScopeOAuth: true, BetaPolicyScopeAPIKey: true, BetaPolicyScopeBedrock: true,
	}

	for i, rule := range settings.Rules {
		if rule.BetaToken == "" {
			return fmt.Errorf("rule[%d]: beta_token cannot be empty", i)
		}
		if !validActions[rule.Action] {
			return fmt.Errorf("rule[%d]: invalid action %q", i, rule.Action)
		}
		if !validScopes[rule.Scope] {
			return fmt.Errorf("rule[%d]: invalid scope %q", i, rule.Scope)
		}
		for j, pattern := range rule.ModelWhitelist {
			trimmed := strings.TrimSpace(pattern)
			if trimmed == "" {
				return fmt.Errorf("rule[%d]: model_whitelist[%d] cannot be empty", i, j)
			}
			settings.Rules[i].ModelWhitelist[j] = trimmed
		}
		if rule.FallbackAction != "" && !validActions[rule.FallbackAction] {
			return fmt.Errorf("rule[%d]: invalid fallback_action %q", i, rule.FallbackAction)
		}
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal beta policy settings: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyBetaPolicySettings, string(data))
}

// GetOpenAIFastPolicySettings reads OpenAI service tier rules, using defaults when absent.
func (s *RuntimeSettings) GetOpenAIFastPolicySettings(ctx context.Context) (*OpenAIFastPolicySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIFastPolicySettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			return tierpolicy.Default(), nil
		}
		return nil, fmt.Errorf("get openai fast policy settings: %w", err)
	}
	if value == "" {
		return tierpolicy.Default(), nil
	}

	var settings OpenAIFastPolicySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		// Invalid JSON replaces configured block/filter rules with defaults.
		// Log the failure so operators can trace unexpected policy behavior
		// to the stored settings.
		slog.Warn("failed to unmarshal openai fast policy settings, falling back to defaults",
			"error", err,
			"key", SettingKeyOpenAIFastPolicySettings)
		return tierpolicy.Default(), nil
	}

	return &settings, nil
}

// SetOpenAIFastPolicySettings validates and saves OpenAI service tier rules.
func (s *RuntimeSettings) SetOpenAIFastPolicySettings(ctx context.Context, settings *OpenAIFastPolicySettings) error {
	value, err := tierpolicy.Prepare(settings)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyOpenAIFastPolicySettings, value)
}

// FastSettingsParticipant validates Fast tier settings; absent or null fields produce no change.
func FastSettingsParticipant() settings.Participant {
	return settings.Participant{Module: "gateway", Fields: []string{"openai_fast_policy_settings"}, Keys: []string{SettingKeyOpenAIFastPolicySettings}, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		raw, ok := input["openai_fast_policy_settings"]
		if !ok || string(raw) == "null" {
			return settings.PreparedChange{}, nil
		}
		var value tierpolicy.OpenAIFastPolicySettings
		if err := json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, apperror.BadRequest("", err.Error())
		}
		prepared, err := tierpolicy.Prepare(&value)
		if err != nil {
			return settings.PreparedChange{}, apperror.BadRequest("", err.Error())
		}
		return settings.PreparedChange{Values: map[string]string{SettingKeyOpenAIFastPolicySettings: prepared}}, nil
	}}
}
