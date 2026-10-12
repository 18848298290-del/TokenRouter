package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Gateway administration stores forwarding switches, client version overrides,
// and prompt policies as string-valued settings. Preparation validates the
// constrained TTFT and prompt-block values before producing that representation,
// while a participant removes omitted fields so partial updates retain their
// persisted values.
//
// A decoded administration object gives omitted fields the same zero values
// as explicitly supplied false or empty values. The participant therefore
// retains the original input's field names, validates the complete candidate
// map, and removes updates whose fields were absent. An explicit false still
// persists as "false", while an omitted forwarding toggle keeps its stored value.

// Gateway administration keys identify persisted ingress and forwarding values
// shared by the management API and process-local runtime readers.
const (
	SettingKeyAntigravityUserAgentVersion      = "antigravity_user_agent_version"
	SettingKeyBackendModeEnabled               = "backend_mode_enabled"
	SettingKeyEnableIdentityPatch              = "enable_identity_patch"
	SettingKeyGrokDefaultBaseURLMode           = "grok_default_base_url_mode"
	SettingKeyGrokDefaultTextModel             = "grok_default_text_model"
	SettingKeyIdentityPatchPrompt              = "identity_patch_prompt"
	SettingKeyOpenAIAllowClaudeCodeCodexPlugin = "openai_allow_claude_code_codex_plugin"
	SettingKeyOpenAICodexUserAgent             = "openai_codex_user_agent"
	SettingKeyUserPromptReplacementConfig      = promptpolicy.SettingKeyUserPromptReplacementConfig
)

// AdminSettings contains administration inputs for gateway ingress and forwarding behavior.
type AdminSettings struct {
	// Antigravity client version override
	AntigravityUserAgentVersion string `json:"antigravity_user_agent_version"`
	// backend-mode admission toggle
	BackendModeEnabled bool `json:"backend_mode_enabled"`
	// configured Anthropic system text
	ClaudeOAuthSystemPrompt string `json:"claude_oauth_system_prompt"`
	// JSON system content blocks
	ClaudeOAuthSystemPromptBlocks string `json:"claude_oauth_system_prompt_blocks"`
	// one-hour Anthropic cache injection
	EnableAnthropicCacheTTL1hInjection bool `json:"enable_anthropic_cache_ttl_1h_injection"`
	// Claude billing attribution signing
	EnableCCHSigning bool `json:"enable_cch_signing"`
	// Anthropic system injection toggle
	EnableClaudeOAuthSystemPromptInjection bool `json:"enable_claude_oauth_system_prompt_injection"`
	// client dateline normalization toggle
	EnableClientDatelineNormalization bool `json:"enable_client_dateline_normalization"`
	// provider fingerprint unification toggle
	EnableFingerprintUnification bool `json:"enable_fingerprint_unification"`
	// identity prompt patching toggle
	EnableIdentityPatch bool `json:"enable_identity_patch"`
	// client metadata passthrough toggle
	EnableMetadataPassthrough bool `json:"enable_metadata_passthrough"`
	// default Grok endpoint mode
	GrokDefaultBaseURLMode string `json:"grok_default_base_url_mode"`
	// default Grok text model
	GrokDefaultTextModel string `json:"grok_default_text_model"`
	// configured identity patch text
	IdentityPatchPrompt string `json:"identity_patch_prompt"`
	// upper CLI version bound
	MaxClaudeCodeVersion string `json:"max_claude_code_version"`
	// lower CLI version bound
	MinClaudeCodeVersion string `json:"min_claude_code_version"`
	// Claude Codex plugin permission
	OpenAIAllowClaudeCodeCodexPlugin bool `json:"openai_allow_claude_code_codex_plugin"`
	// outbound Codex User-Agent override
	OpenAICodexUserAgent string `json:"openai_codex_user_agent"`
	// semantic or visible TTFT
	OpenAITTFTMode string `json:"openai_ttft_mode"`
	// message cache-control rewrite toggle
	RewriteMessageCacheControl bool `json:"rewrite_message_cache_control"`
	// configured user-prompt replacement rules
	UserPromptReplacementConfig *promptpolicy.UserPromptReplacementConfig `json:"user_prompt_replacement_config"`
}

// AdminSettingsRules supplies application-owned platform defaults and validators for preparation.
type AdminSettingsRules struct {
	GrokDefaultTextModel string // model used for blank input
	// NormalizeUserAgentVersion supplies Antigravity's accepted version form.
	// PrepareAdminSettings and ReadAdminSettings require this callback to interpret overrides.
	NormalizeUserAgentVersion func(string) string
	// ValidateClaudePromptBlocks checks configured Anthropic system-block JSON.
	// Preparation requires this callback and returns its error before producing updates.
	ValidateClaudePromptBlocks func(string) error
}

// PrepareAdminSettings validates and encodes gateway administration values for
// persistence. Supply nonnil settings and both validation callbacks; invalid
// nonblank TTFT modes, rejected prompt blocks, or invalid replacement rules return
// an error before a value map is returned.
//
// Blank Grok model input uses the supplied default, Grok URL mode is normalized,
// and client overrides use their platform normalizer or whitespace trimming.
// All fields produce candidate updates, including zero-valued fields. Partial
// update callers therefore filter the resulting map by fields actually present
// in their input before handing it to the settings transaction.
func PrepareAdminSettings(settings *AdminSettings, rules AdminSettingsRules) (map[string]string, error) {
	updates := map[string]string{}
	if model := strings.TrimSpace(settings.GrokDefaultTextModel); model != "" {
		updates[SettingKeyGrokDefaultTextModel] = model
	} else {
		updates[SettingKeyGrokDefaultTextModel] = rules.GrokDefaultTextModel
	}
	updates[SettingKeyGrokDefaultBaseURLMode] = NormalizeGrokDefaultBaseURLMode(settings.GrokDefaultBaseURLMode)
	updates[SettingKeyEnableIdentityPatch] = strconv.FormatBool(settings.EnableIdentityPatch)
	updates[SettingKeyIdentityPatchPrompt] = settings.IdentityPatchPrompt
	updates[SettingKeyMinClaudeCodeVersion] = settings.MinClaudeCodeVersion
	updates[SettingKeyMaxClaudeCodeVersion] = settings.MaxClaudeCodeVersion
	updates[SettingKeyBackendModeEnabled] = strconv.FormatBool(settings.BackendModeEnabled)
	mode := NormalizeOpenAITTFTMode(settings.OpenAITTFTMode)
	if raw := strings.TrimSpace(settings.OpenAITTFTMode); raw != "" && !strings.EqualFold(raw, OpenAITTFTModeSemantic) && !strings.EqualFold(raw, OpenAITTFTModeVisible) {
		return nil, fmt.Errorf("%s must be one of: %s/%s", SettingKeyOpenAITTFTMode, OpenAITTFTModeSemantic, OpenAITTFTModeVisible)
	}
	updates[SettingKeyOpenAITTFTMode] = mode
	updates[SettingKeyEnableFingerprintUnification] = strconv.FormatBool(settings.EnableFingerprintUnification)
	updates[SettingKeyEnableMetadataPassthrough] = strconv.FormatBool(settings.EnableMetadataPassthrough)
	updates[SettingKeyEnableCCHSigning] = strconv.FormatBool(settings.EnableCCHSigning)
	if err := rules.ValidateClaudePromptBlocks(settings.ClaudeOAuthSystemPromptBlocks); err != nil {
		return nil, err
	}
	updates[SettingKeyEnableClaudeOAuthSystemPromptInjection] = strconv.FormatBool(settings.EnableClaudeOAuthSystemPromptInjection)
	updates[SettingKeyClaudeOAuthSystemPrompt] = settings.ClaudeOAuthSystemPrompt
	updates[SettingKeyClaudeOAuthSystemPromptBlocks] = settings.ClaudeOAuthSystemPromptBlocks
	updates[SettingKeyEnableAnthropicCacheTTL1hInjection] = strconv.FormatBool(settings.EnableAnthropicCacheTTL1hInjection)
	updates[SettingKeyRewriteMessageCacheControl] = strconv.FormatBool(settings.RewriteMessageCacheControl)
	updates[SettingKeyEnableClientDatelineNormalization] = strconv.FormatBool(settings.EnableClientDatelineNormalization)
	updates[SettingKeyAntigravityUserAgentVersion] = rules.NormalizeUserAgentVersion(settings.AntigravityUserAgentVersion)
	updates[SettingKeyOpenAICodexUserAgent] = strings.TrimSpace(settings.OpenAICodexUserAgent)
	updates[SettingKeyOpenAIAllowClaudeCodeCodexPlugin] = strconv.FormatBool(settings.OpenAIAllowClaudeCodeCodexPlugin)
	userPromptReplacementConfigJSON, err := promptpolicy.ConfigToRaw(settings.UserPromptReplacementConfig)
	if err != nil {
		return nil, err
	}
	updates[SettingKeyUserPromptReplacementConfig] = userPromptReplacementConfigJSON
	return updates, nil
}

// NormalizeGrokDefaultBaseURLMode returns a supported Grok base URL mode after
// trimming and case folding. Empty or unknown input selects cli; api and the
// three configured region names retain their normalized values.
func NormalizeGrokDefaultBaseURLMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "api":
		return "api"
	case "us-east-1":
		return "us-east-1"
	case "us-west-2":
		return "us-west-2"
	case "eu-west-1":
		return "eu-west-1"
	case "cli":
		return "cli"
	default:
		return "cli"
	}
}

// AdminSettingsParticipant binds gateway administration fields to their storage
// keys and validation rules. An empty input produces no change; otherwise the
// decoded values are prepared and only explicitly supplied fields remain in
// the returned updates.
//
// The decoded struct alone cannot distinguish an omitted boolean from an
// explicit false value. Preparation uses the original input's field names to
// remove absent candidates after validation, preserving the distinction in the
// string-valued storage update. For example, a supplied backend-mode false
// persists as "false" while another omitted toggle produces no update.
func AdminSettingsParticipant(rules AdminSettingsRules) settings.Participant {
	keys := []string{SettingKeyAntigravityUserAgentVersion, SettingKeyBackendModeEnabled, SettingKeyClaudeOAuthSystemPrompt, SettingKeyClaudeOAuthSystemPromptBlocks, SettingKeyEnableAnthropicCacheTTL1hInjection, SettingKeyEnableCCHSigning, SettingKeyEnableClaudeOAuthSystemPromptInjection, SettingKeyEnableClientDatelineNormalization, SettingKeyEnableFingerprintUnification, SettingKeyEnableIdentityPatch, SettingKeyEnableMetadataPassthrough, SettingKeyGrokDefaultBaseURLMode, SettingKeyGrokDefaultTextModel, SettingKeyIdentityPatchPrompt, SettingKeyMaxClaudeCodeVersion, SettingKeyMinClaudeCodeVersion, SettingKeyOpenAIAllowClaudeCodeCodexPlugin, SettingKeyOpenAICodexUserAgent, SettingKeyOpenAITTFTMode, SettingKeyRewriteMessageCacheControl, SettingKeyUserPromptReplacementConfig}
	return settings.Participant{Module: "gateway-forwarding", Fields: keys, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value AdminSettings
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		values, err := PrepareAdminSettings(&value, rules)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		// Decoding a partial object leaves omitted fields at their zero values.
		// Remove their prepared defaults so this participant updates the supplied
		// fields without overwriting other persisted administration values.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
