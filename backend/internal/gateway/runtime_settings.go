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

// Gateway policies are stored as JSON values and interpreted at their point of
// use. Missing, empty, or malformed values select the relevant policy default,
// while other storage failures are returned for the execution caller to handle.
// Valid JSON is decoded as supplied, so omitted fields retain their zero values
// rather than being merged with defaults.
//
// Beta updates validate actions and provider scopes before persistence and
// trim model patterns in the supplied settings object. Fast policy preparation
// is delegated to tierpolicy so direct saves and coordinated administration use
// the same validation. The runtime also owns the typed caches and shared refresh
// groups whose client and forwarding read rules are implemented alongside these
// JSON policy readers.

// Gateway policy keys identify the stored JSON values used by beta, Fast-tier,
// and request-rectifier readers and administration participants.
const (
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

// OpenAIFastPolicySettings configures the OpenAI service-tier rules used by execution.
type OpenAIFastPolicySettings = tierpolicy.OpenAIFastPolicySettings

// RuntimeSettingsStore reads and writes persisted settings used by gateway policies.
type RuntimeSettingsStore interface {
	// GetMultiple reads string values for the requested keys into a map.
	// Missing entries are omitted so each gateway reader can apply its setting's
	// own defaults; storage failures are returned to that reader.
	GetMultiple(context.Context, []string) (map[string]string, error)
	// GetValue reads one persisted setting using the caller's context. Missing
	// values return an error matching the constructor's notFound through errors.Is,
	// letting readers distinguish policy defaults from other storage failures.
	GetValue(context.Context, string) (string, error)
	// Set persists a prepared string value under its gateway setting key.
	// The caller performs policy validation and encoding before this write, and
	// receives the storage error when persistence fails.
	Set(context.Context, string, string) error
}

// RuntimeSettings reads and caches gateway settings using caller-supplied platform defaults.
type RuntimeSettings struct {
	// clientOptions contains the platform callbacks supplied at construction.
	// Antigravity identity reads and publication require both callbacks; omitted
	// options are left unset by the constructor.
	clientOptions ClientSettingsOptions
	// antigravityUAVersionCache and antigravityUAVersionSF store a typed version
	// snapshot and share expired-entry reads. Publication replaces the atomic
	// value; already-running shared callbacks may still replace it afterward.
	antigravityUAVersionCache atomic.Value       // cached client version snapshot
	antigravityUAVersionSF    singleflight.Group // shared client version refresh
	// openAICodexUACache and openAICodexUASF hold the chosen Codex header and
	// coalesce database refreshes. Getter fallbacks and administrative publication
	// store the same cachedOpenAICodexUserAgent pointer type in the atomic value.
	openAICodexUACache atomic.Value       // cached Codex header snapshot
	openAICodexUASF    singleflight.Group // shared Codex header refresh
	// openAIAllowCodexPluginCache and openAIAllowCodexPluginSF hold interpreted
	// plugin permission and shared refresh work. Missing settings and read failures
	// both disable permission but use different snapshot deadlines.
	openAIAllowCodexPluginCache atomic.Value       // cached plugin permission snapshot
	openAIAllowCodexPluginSF    singleflight.Group // shared plugin permission refresh
	// versionBoundsCache and versionBoundsSF retain a paired client range and
	// share its refresh. Error snapshots have empty bounds, with a shorter
	// deadline than successfully read settings.
	versionBoundsCache atomic.Value       // cached client range snapshot
	versionBoundsSF    singleflight.Group // shared client range refresh
	// gatewayForwardingCache and gatewayForwardingSF retain an interpreted
	// forwarding tuple and share batch reads. Snapshot fields come from that
	// returned settings map or the reader's complete error-default tuple.
	gatewayForwardingCache atomic.Value       // cached forwarding settings snapshot
	gatewayForwardingSF    singleflight.Group // shared forwarding settings refresh
	// settingRepo is the persisted settings source retained by the runtime.
	// Policy and forwarding reads require it; only individually guarded accessors
	// can use a nil repository.
	settingRepo RuntimeSettingsStore
	// notFound identifies absent settings through errors.Is. A matching error
	// selects defaults, while other policy read errors are returned to the caller.
	notFound error
	// defaultBeta supplies missing, empty, and invalid beta-policy values.
	// GetBetaPolicySettings requires this callback whenever one of those cases occurs.
	defaultBeta func() *BetaPolicySettings
}

// RectifierSettings controls request signature and thinking budget repairs.
type RectifierSettings struct {
	// Enabled gates ThinkingSignatureEnabled and ThinkingBudgetEnabled in their
	// runtime accessors. Each accessor requires both its specific toggle and
	// Enabled after a successful settings read.
	Enabled                  bool `json:"enabled"`                    // global rectifier toggle
	ThinkingSignatureEnabled bool `json:"thinking_signature_enabled"` // thinking signature repair
	ThinkingBudgetEnabled    bool `json:"thinking_budget_enabled"`    // thinking budget repair
	// APIKeySignatureEnabled permits signature recovery for API Key providers.
	// Message forwarding additionally requires Enabled and a successful settings read for this path.
	APIKeySignatureEnabled bool `json:"apikey_signature_enabled"`
	// APIKeySignaturePatterns supplies custom error matching for API Key
	// signature recovery. Message forwarding reads these patterns when classifying
	// the rejected upstream response.
	APIKeySignaturePatterns []string `json:"apikey_signature_patterns"`
}

// BetaPolicyRule defines a beta token action and its provider and model scope.
type BetaPolicyRule struct {
	BetaToken string `json:"beta_token"` // scoped beta header token
	Action    string `json:"action"`     // pass, filter, or block
	Scope     string `json:"scope"`      // all, oauth, apikey, or bedrock
	// ErrorMessage supplies the primary block response. An empty value lets the
	// Anthropic evaluator generate a message naming the blocked beta token.
	ErrorMessage string `json:"error_message,omitempty"`
	// ModelWhitelist, FallbackAction, and FallbackErrorMessage determine the
	// model-specific rule outcome. Empty or matching whitelists use the primary
	// action; unmatched models use the configured fallback, or pass when it is absent.
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // primary-action model patterns
	FallbackAction       string   `json:"fallback_action,omitempty"`        // unmatched-model beta action
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // unmatched-model block message
}

// BetaPolicySettings holds beta rules in their configured evaluation order.
type BetaPolicySettings struct {
	// Rules retain evaluation order. The Anthropic evaluator keeps the first
	// matching block message while collecting tokens from all matching filter rules.
	Rules []BetaPolicyRule `json:"rules"`
}

// NewRuntimeSettings binds the settings store, missing-setting error, and beta
// default callback without performing reads. It uses the first supplied client
// options value and leaves omitted callbacks unset; callers provide the dependencies
// required by whichever policy, forwarding, or identity accessors they use.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error, betaDefaults func() *BetaPolicySettings, clientOptions ...ClientSettingsOptions) *RuntimeSettings {
	value := &RuntimeSettings{settingRepo: repo, notFound: notFound, defaultBeta: betaDefaults}
	if len(clientOptions) > 0 {
		value.clientOptions = clientOptions[0]
	}
	return value
}

// DefaultRectifierSettings returns a fresh configuration enabling the global
// rectifier and thinking signature and budget repairs. API Key signature repair
// remains disabled with no custom patterns in this default object.
func DefaultRectifierSettings() *RectifierSettings {
	return &RectifierSettings{
		Enabled:                  true,
		ThinkingSignatureEnabled: true,
		ThinkingBudgetEnabled:    true,
	}
}

// GetRectifierSettings returns decoded rectifier settings or a fresh default
// for recognized missing keys, empty values, and invalid JSON. Other storage
// errors are returned; valid JSON keeps supplied zero-valued fields rather than
// merging with the defaults.
func (s *RuntimeSettings) GetRectifierSettings(ctx context.Context) (*RectifierSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyRectifierSettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			// Deleting the policy key restores its enabled repair defaults. Other
			// storage errors remain visible because they cannot establish an unconfigured policy.
			return DefaultRectifierSettings(), nil
		}
		return nil, fmt.Errorf("get rectifier settings: %w", err)
	}
	if value == "" {
		return DefaultRectifierSettings(), nil
	}

	var settings RectifierSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		// Malformed stored JSON cannot supply usable repair switches. Return the
		// enabled default configuration so the caller can continue with the documented repairs.
		return DefaultRectifierSettings(), nil
	}

	return &settings, nil
}

// SetRectifierSettings encodes and persists rectifier switches and custom
// signature patterns. A nil input is rejected before encoding; the method
// performs no additional rule validation and returns encoding or storage errors.
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

// IsSignatureRectifierEnabled reports whether the global and thinking-signature
// switches are enabled after a successful read. A storage error returns true,
// selecting the repair-enabled behavior for callers using this accessor.
func (s *RuntimeSettings) IsSignatureRectifierEnabled(ctx context.Context) bool {
	settings, err := s.GetRectifierSettings(ctx)
	if err != nil {
		// Signature recovery remains enabled when the accessor cannot read its
		// switches. Provider-specific API Key recovery applies its own read-error rule.
		return true
	}
	return settings.Enabled && settings.ThinkingSignatureEnabled
}

// IsBudgetRectifierEnabled reports whether the global and thinking-budget
// switches are enabled after a successful read. A storage error returns true,
// selecting the repair-enabled behavior for callers using this accessor.
func (s *RuntimeSettings) IsBudgetRectifierEnabled(ctx context.Context) bool {
	settings, err := s.GetRectifierSettings(ctx)
	if err != nil {
		// A storage failure leaves budget repair enabled for this accessor's
		// callers, so a temporary settings outage does not disable that recovery choice.
		return true
	}
	return settings.Enabled && settings.ThinkingBudgetEnabled
}

// GetBetaPolicySettings returns decoded beta policy or the constructor's
// default callback result for recognized missing keys, empty values, and invalid
// JSON. Supply that callback for these cases; other storage errors are returned,
// and valid JSON retains its supplied rule list.
func (s *RuntimeSettings) GetBetaPolicySettings(ctx context.Context) (*BetaPolicySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyBetaPolicySettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			// An absent policy uses the application's rule set. This includes key
			// deletion, which restores defaults without persisting a replacement JSON value.
			return s.defaultBeta(), nil
		}
		return nil, fmt.Errorf("get beta policy settings: %w", err)
	}
	if value == "" {
		return s.defaultBeta(), nil
	}

	var settings BetaPolicySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		// Invalid persisted JSON cannot provide an ordered rule list. Delegate
		// the replacement policy to the application-owned default callback used by
		// missing and empty settings.
		return s.defaultBeta(), nil
	}

	return &settings, nil
}

// SetBetaPolicySettings validates ordered beta rules and saves their JSON form.
// Nil settings, empty tokens, unsupported actions or scopes, and empty model
// patterns are rejected before persistence. Nonempty patterns are trimmed in
// the supplied slices as validation proceeds, so earlier patterns can remain
// modified when a later rule fails; callers retaining input should account for
// those partial in-memory edits after an error.
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

	// The range value copies a rule while its whitelist still refers to shared
	// slice storage. Assign normalized patterns through settings.Rules so the
	// persisted object and caller-visible values use the same spelling.
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

// GetOpenAIFastPolicySettings returns decoded service-tier rules or tierpolicy
// defaults for recognized missing keys, empty values, and malformed JSON.
// Malformed JSON is logged before using defaults; other storage failures return
// an error, and valid JSON retains its supplied fields.
func (s *RuntimeSettings) GetOpenAIFastPolicySettings(ctx context.Context) (*OpenAIFastPolicySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIFastPolicySettings)
	if err != nil {
		if errors.Is(err, s.notFound) {
			// An absent Fast policy selects tierpolicy's defaults, including after
			// key deletion. Keep other storage failures distinct from that unconfigured state.
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

// SetOpenAIFastPolicySettings validates and encodes service-tier rules through
// tierpolicy.Prepare, then persists the prepared value. Preparation errors return
// before storage is called; persistence failures are returned for the administration
// caller to handle.
func (s *RuntimeSettings) SetOpenAIFastPolicySettings(ctx context.Context, settings *OpenAIFastPolicySettings) error {
	value, err := tierpolicy.Prepare(settings)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyOpenAIFastPolicySettings, value)
}

// FastSettingsParticipant binds Fast service-tier administration to the same
// tierpolicy preparer used by direct saves. Absent or null input produces no
// change; invalid JSON or invalid rules returns a BadRequest error before a
// prepared storage value is supplied.
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
