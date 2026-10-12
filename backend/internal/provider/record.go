package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/routing/modelmap"
)

const (
	OpenAIWorkloadCapabilitiesCredentialKey = "openai_workload_capabilities"

	GeminiProviderTypeCredentialKey = "provider_type"
	GeminiProviderTypeThirdParty    = "third_party"
	GeminiOfficialAPIHost           = "generativelanguage.googleapis.com"

	// GrokMediaEligibleExtraKey overrides media eligibility in providers.extra: true enables
	// media scheduling, false disables it, and missing or null uses upstream observations.
	GrokMediaEligibleExtraKey = "grok_media_eligible"

	OpenAIAuthModePersonalAccessToken = "personalAccessToken"
	OpenAIAuthModeCredentialKey       = "auth_mode"
	OpenAIAuthModeLegacyCredentialKey = "openai_auth_mode"

	// OpenAICompactModeForceOn enables the corresponding compaction capability.
	OpenAICompactModeForceOn = "force_on"
	// OpenAICompactModeForceOff disables the corresponding compaction capability.
	OpenAICompactModeForceOff            = "force_off"
	OpenAINativeCompactionV2ModeExtraKey = "openai_native_compaction_v2_mode"

	DefaultPoolModeRetryCount = 3
	MaxPoolModeRetryCount     = 10

	// OpenAIOAuthClientPolicyAny allows any client to use the OpenAI OAuth provider.
	OpenAIOAuthClientPolicyAny = "any"
	// OpenAIOAuthClientPolicyCodexOnly restricts access to official Codex clients.
	OpenAIOAuthClientPolicyCodexOnly = "codex_only"
	// OpenAIOAuthClientPolicyTLSRouterMatchedOnly requires a UA matched by the TLS router.
	OpenAIOAuthClientPolicyTLSRouterMatchedOnly = "tls_router_matched_only"

	// Status constants.
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusError    = "error"

	// Platform constants.
	PlatformAnthropic   = capability.PlatformAnthropic
	PlatformOpenAI      = capability.PlatformOpenAI
	PlatformGemini      = capability.PlatformGemini
	PlatformAntigravity = capability.PlatformAntigravity
	PlatformQoder       = capability.PlatformQoder
	PlatformGrok        = capability.PlatformGrok
	PlatformKimi        = capability.PlatformKimi
	PlatformZhipu       = capability.PlatformZhipu
	PlatformDeepseek    = capability.PlatformDeepseek
	PlatformJev         = capability.PlatformJev

	// Access modes for Chinese providers: pay-as-you-go and Coding Plan.
	ProviderModePayG   = "payg"
	ProviderModeCoding = "coding"

	// API protocol names used by compatibility credentials and administrator tests.
	APIProtocolChatCompletions = "chat_completions"
	APIProtocolAnthropic       = "anthropic"
	APIProtocolResponses       = "responses"
	APIProtocolAdaptive        = "adaptive"
	APIProtocolSystemOne       = string(capability.ProtocolSystemOne)

	// Default OpenAI-compatible base URLs for each Chinese provider mode.
	// Keep these values consistent with the frontend credentialsBuilder.ts presets.
	DefaultKimiPayGBaseURL    = "https://api.moonshot.cn/v1"
	DefaultKimiCodingBaseURL  = "https://api.kimi.com/coding/v1"
	DefaultZhipuPayGBaseURL   = "https://open.bigmodel.cn/api/paas/v4"
	DefaultZhipuCodingBaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"
	DefaultDeepseekBaseURL    = "https://api.deepseek.com"
	DefaultJevBaseURL         = "https://api.typesafe.ai"

	// Default Anthropic-compatible base URLs; requests append /v1/messages.
	// Keep these values consistent with the frontend credentialsBuilder.ts presets.
	DefaultKimiPayGAnthropicBaseURL   = "https://api.moonshot.cn/anthropic"
	DefaultKimiCodingAnthropicBaseURL = "https://api.kimi.com/coding"
	DefaultZhipuAnthropicBaseURL      = "https://open.bigmodel.cn/api/anthropic"
	DefaultDeepseekAnthropicBaseURL   = "https://api.deepseek.com/anthropic"

	// Provider type constants.
	ProviderTypeOAuth          = capability.ProviderTypeOAuth          // OAuth with profile and inference scopes.
	ProviderTypeSetupToken     = capability.ProviderTypeSetupToken     // Setup Token with inference scope.
	ProviderTypeAPIKey         = capability.ProviderTypeAPIKey         // API Key authentication.
	ProviderTypeUpstream       = capability.ProviderTypeUpstream       // Upstream passthrough using a base URL and API key.
	ProviderTypeBedrock        = capability.ProviderTypeBedrock        // AWS Bedrock using SigV4 or an API key, selected by credentials.auth_mode.
	ProviderTypeServiceAccount = capability.ProviderTypeServiceAccount // Google Service Account for Vertex AI.
	ProviderTypeCosy           = capability.ProviderTypeCosy           // Qoder COSY protocol.

	// QuotaDimension constants for spark shadow providers.
	QuotaDimensionGlobal = "global"
	QuotaDimensionSpark  = "spark"

	AntigravityPrivacySet    = "privacy_set"
	AntigravityPrivacyFailed = "privacy_set_failed"

	PrivacyModeTrainingOff = "training_off"
	PrivacyModeFailed      = "training_set_failed"
	PrivacyModeCFBlocked   = "training_set_cf_blocked"

	OpenAIImagesCapabilityBasic  OpenAIImagesCapability = "images-basic"
	OpenAIImagesCapabilityNative OpenAIImagesCapability = "images-native"

	thresholdTypeFixed = "fixed"
	quotaDimDaily      = "daily"
	quotaDimWeekly     = "weekly"
	quotaDimTotal      = "total"
)

// DefaultPoolModeRetryableStatusCodes lists statuses that retry the same provider in pool mode.
// It applies when Credentials lacks pool_mode_retry_status_codes.
var DefaultPoolModeRetryableStatusCodes = []int{401, 403, 429}

// Record holds provider configuration and runtime data used to build caller snapshots.
// Credentials are excluded from JSON; string formatting exposes the provider ID.
type Record struct {
	Proxy                   *egress.Proxy `json:"-"`
	Groups                  []*accessview.GroupConfig
	ProviderGroups          []GroupMembership
	ID                      int64
	Name                    string
	Notes                   *string
	Platform                string
	Type                    string
	Credentials             map[string]any `json:"-"`
	Extra                   map[string]any `json:"-"`
	ProxyID                 *int64
	ProxyFallbackOriginID   *int64
	ProxyFallbackOriginName *string
	Concurrency             int
	Priority                int
	RateMultiplier          *float64
	LoadFactor              *int
	Status                  string
	ErrorMessage            string
	LastUsedAt              *time.Time
	ExpiresAt               *time.Time
	AutoPauseOnExpired      bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
	Schedulable             bool
	RateLimitedAt           *time.Time
	RateLimitResetAt        *time.Time
	OverloadUntil           *time.Time
	TempUnschedulableUntil  *time.Time
	TempUnschedulableReason string
	QuotaAutoPaused         bool `json:"-"`
	SessionWindowStart      *time.Time
	SessionWindowEnd        *time.Time
	SessionWindowStatus     string
	ParentProviderID        *int64
	QuotaDimension          string
	GroupIDs                []int64
	Now                     func() time.Time                     `json:"-"`
	LoadLocation            func(string) (*time.Location, error) `json:"-"`
}

type TempUnschedulableRule struct {
	ErrorCode       int      `json:"error_code"`
	Keywords        []string `json:"keywords"`
	DurationMinutes int      `json:"duration_minutes"`
	Description     string   `json:"description"`
}

type OpenAIImagesCapability string

// GroupMembership associates a provider with a group.
type GroupMembership struct {
	ProviderID int64
	GroupID    int64
	CreatedAt  time.Time
	Provider   *Record
	Group      *accessview.GroupConfig
}

func (r *Record) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Record) String() string {
	if r == nil {
		return "provider <nil>"
	}
	return fmt.Sprintf("provider (id=%d)", r.ID)
}

func (r *Record) GoString() string { return r.String() }

func IsOpenAIPersonalAccessTokenAuthMode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "personalaccesstoken", "personal_access_token":
		return true
	default:
		return false
	}
}

func (r *Record) IsActive() bool {
	return r.Status == StatusActive
}

// BillingRateMultiplier returns the provider billing multiplier.
// - Missing values, including older cache entries, default to 1.0.
// - Zero is valid and produces zero provider charges.
// - Negative values are invalid and default to 1.0.
func (r *Record) BillingRateMultiplier() float64 {
	if r == nil || r.RateMultiplier == nil {
		return 1.0
	}
	if *r.RateMultiplier < 0 {
		return 1.0
	}
	return *r.RateMultiplier
}

func (r *Record) EffectiveLoadFactor() int {
	if r == nil {
		return 1
	}
	if r.LoadFactor != nil && *r.LoadFactor > 0 {
		return *r.LoadFactor
	}
	if r.Concurrency > 0 {
		return r.Concurrency
	}
	return 1
}

func (r *Record) IsSchedulable() bool {
	if !r.IsActive() || !r.Schedulable {
		return false
	}
	now := r.now()
	if r.AutoPauseOnExpired && r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
		return false
	}
	if r.OverloadUntil != nil && now.Before(*r.OverloadUntil) {
		return false
	}
	if r.RateLimitResetAt != nil && now.Before(*r.RateLimitResetAt) {
		return false
	}
	if r.TempUnschedulableUntil != nil && now.Before(*r.TempUnschedulableUntil) {
		return false
	}
	if r.IsAPIKeyOrBedrock() && r.IsQuotaExceeded() {
		return false
	}
	return true
}

// IsCredentialUsableForShadow checks parent availability for shared credentials and transport; nil or inactive
// parents fail.
// When expiry pausing is enabled, a nonnil ExpiresAt must be in the future.
// A temporary cooldown passes when unset or elapsed.
// Shadows share the parent token and proxy, so credential and transport cooldowns apply to both.
// Shadows use their own Spark quota window and scheduling switch; parent rate limits and overloads are checked
// separately.
func (r *Record) IsCredentialUsableForShadow() bool {
	if r == nil || !r.IsActive() {
		return false
	}
	now := r.now()
	if r.AutoPauseOnExpired && r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
		return false
	}
	if r.TempUnschedulableUntil != nil && now.Before(*r.TempUnschedulableUntil) {
		return false
	}
	return true
}

func (r *Record) IsRateLimited() bool {
	if r.RateLimitResetAt == nil {
		return false
	}
	return r.now().Before(*r.RateLimitResetAt)
}

func (r *Record) IsOAuth() bool {
	return r.Type == ProviderTypeOAuth || r.Type == ProviderTypeSetupToken
}

// IsPrivacySet reports whether the platform privacy setting was applied successfully.
// OpenAI: privacy_mode == "training_off"
// Antigravity: privacy_mode == "privacy_set"
// Other platforms pass this check without a privacy setting.
func (r *Record) IsPrivacySet() bool {
	switch r.Platform {
	case PlatformOpenAI:
		return r.GetExtraString("privacy_mode") == PrivacyModeTrainingOff
	case PlatformAntigravity:
		return r.GetExtraString("privacy_mode") == AntigravityPrivacySet
	default:
		return true
	}
}

func (r *Record) IsGemini() bool {
	return r.Platform == PlatformGemini
}

func (r *Record) IsGrok() bool {
	return r.Platform == PlatformGrok
}

func (r *Record) IsGrokOAuth() bool {
	return r.IsGrok() && r.Type == ProviderTypeOAuth
}

// IsCNProvider reports whether the provider is Kimi, Zhipu, or DeepSeek.
func (r *Record) IsCNProvider() bool {
	return r != nil && IsCNProvider(r.Platform)
}

// IsOpenAICompatible reports whether the provider uses the OpenAI gateway protocol family.
// OpenAI and Grok use this gateway, as do the OpenAI Chat Completions-compatible
// Kimi, Zhipu, and DeepSeek upstreams.
func (r *Record) IsOpenAICompatible() bool {
	return r != nil && (r.Platform == PlatformOpenAI || r.Platform == PlatformGrok ||
		r.Platform == PlatformKimi || r.Platform == PlatformZhipu || r.Platform == PlatformDeepseek)
}

func (r *Record) GeminiOAuthType() string {
	if r.Platform != PlatformGemini || r.Type != ProviderTypeOAuth {
		return ""
	}
	oauthType := strings.TrimSpace(r.GetCredential("oauth_type"))
	if oauthType == "" && strings.TrimSpace(r.GetCredential("project_id")) != "" {
		return "code_assist"
	}
	return oauthType
}

func (r *Record) GeminiTierID() string {
	tierID := strings.TrimSpace(r.GetCredential("tier_id"))
	return tierID
}

// IsGeminiThirdPartyProvider reports whether a Gemini API Key uses a third-party provider.
// This flag controls local official-quota simulation; authentication and forwarding still follow the provider type.
func (r *Record) IsGeminiThirdPartyProvider() bool {
	if r == nil || r.Platform != PlatformGemini || r.Type != ProviderTypeAPIKey {
		return false
	}
	return strings.EqualFold(r.GetCredential(GeminiProviderTypeCredentialKey), GeminiProviderTypeThirdParty)
}

// HasGeminiThirdPartyBaseURL reports whether a third-party Gemini API Key has a nonofficial endpoint.
// Requiring a separate endpoint prevents bypassing local official-quota checks while still sending to Google.
func (r *Record) HasGeminiThirdPartyBaseURL() bool {
	if !r.IsGeminiThirdPartyProvider() {
		return false
	}
	baseURL := strings.TrimSpace(r.GetCredential("base_url"))
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return false
	}
	return !strings.EqualFold(parsed.Hostname(), GeminiOfficialAPIHost)
}

func (r *Record) IsGeminiCodeAssist() bool {
	if r.Platform != PlatformGemini || r.Type != ProviderTypeOAuth {
		return false
	}
	oauthType := r.GeminiOAuthType()
	if oauthType == "" {
		return strings.TrimSpace(r.GetCredential("project_id")) != ""
	}
	return oauthType == "code_assist"
}

// IsGeminiGoogleOne identifies the legacy consumer Gemini CLI / Code Assist OAuth channel.
func (r *Record) IsGeminiGoogleOne() bool {
	return r.Platform == PlatformGemini && r.Type == ProviderTypeOAuth && r.GeminiOAuthType() == "google_one"
}

func (r *Record) CanGetUsage() bool {
	return r.Type == ProviderTypeOAuth
}

func (r *Record) GetCredential(key string) string {
	if r.Credentials == nil {
		return ""
	}
	v, ok := r.Credentials[key]
	if !ok || v == nil {
		return ""
	}

	// Historical credentials such as expires_at may contain either numbers or strings.
	switch val := v.(type) {
	case string:
		return val
	case json.Number:
		// JSON decoded with UseNumber represents numbers as json.Number.
		return val.String()
	case float64:
		// Standard JSON decoding represents numbers as float64.
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	default:
		return ""
	}
}

// GetCredentialAsTime parses credential timestamps in the following formats:
// Numeric values represent Unix seconds.
//   - RFC3339 string: "2025-01-01T00:00:00Z"
//   - Unix timestamp string: "1735689600"
//   - Unix timestamp number: 1735689600 (float64/int64/json.Number)
func (r *Record) GetCredentialAsTime(key string) *time.Time {
	s := r.GetCredential(key)
	if s == "" {
		return nil
	}
	// Accept RFC3339 before interpreting numeric timestamps.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	// Numeric timestamps use seconds since the Unix epoch.
	if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
		t := time.Unix(ts, 0)
		return &t
	}
	return nil
}

// GetCredentialAsInt64 parses an integer credential, returning zero for missing or invalid values.
// Internal fields such as _token_version use this representation.
func (r *Record) GetCredentialAsInt64(key string) int64 {
	if r == nil || r.Credentials == nil {
		return 0
	}
	val, ok := r.Credentials[key]
	if !ok || val == nil {
		return 0
	}
	switch v := val.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return i
		}
	}
	return 0
}

func (r *Record) IsTempUnschedulableEnabled() bool {
	if r.Credentials == nil {
		return false
	}
	raw, ok := r.Credentials["temp_unschedulable_enabled"]
	if !ok || raw == nil {
		return false
	}
	enabled, ok := raw.(bool)
	return ok && enabled
}

func (r *Record) GetTempUnschedulableRules() []TempUnschedulableRule {
	if r.Credentials == nil {
		return nil
	}
	raw, ok := r.Credentials["temp_unschedulable_rules"]
	if !ok || raw == nil {
		return nil
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil
	}

	rules := make([]TempUnschedulableRule, 0, len(arr))
	for _, item := range arr {
		entry, ok := item.(map[string]any)
		if !ok || entry == nil {
			continue
		}

		rule := TempUnschedulableRule{
			ErrorCode:       ParseExtraInt(entry["error_code"]),
			Keywords:        ParseTempUnschedStrings(entry["keywords"]),
			DurationMinutes: ParseExtraInt(entry["duration_minutes"]),
			Description:     ParseTempUnschedString(entry["description"]),
		}

		if rule.ErrorCode <= 0 || rule.DurationMinutes <= 0 || len(rule.Keywords) == 0 {
			continue
		}

		rules = append(rules, rule)
	}

	return rules
}

func ParseTempUnschedString(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func ParseTempUnschedStrings(value any) []string {
	if value == nil {
		return nil
	}

	var raw []string
	switch v := value.(type) {
	case []string:
		raw = v
	case []any:
		raw = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				raw = append(raw, s)
			}
		}
	default:
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s := strings.TrimSpace(item)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func NormalizeProviderNotes(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func StringMappingFromRaw(raw any) map[string]string {
	switch mapping := raw.(type) {
	case map[string]any:
		if len(mapping) == 0 {
			return nil
		}
		result := make(map[string]string, len(mapping))
		for key, value := range mapping {
			if str, ok := value.(string); ok {
				result[key] = str
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case map[string]string:
		if len(mapping) == 0 {
			return nil
		}
		result := make(map[string]string, len(mapping))
		maps.Copy(result, mapping)
		return result
	default:
		return nil
	}
}

// ResolveRequestedModelInMapping resolves exact names and trailing wildcard mappings.
func ResolveRequestedModelInMapping(mapping map[string]string, requestedModel string) (mappedModel string, matched bool) {
	return modelmap.Resolve(mapping, requestedModel)
}

// ExtractFinalModelWhitelist extracts final model names from legacy model_mapping entries.
// Exact identity mappings qualify when the source has no wildcard.
// This recognizes legacy key=value whitelist entries while excluding ordinary remapping rules.
func ExtractFinalModelWhitelist(mapping map[string]string) map[string]struct{} {
	if len(mapping) == 0 {
		return nil
	}
	whitelist := make(map[string]struct{})
	for rawFrom, rawTo := range mapping {
		if strings.Contains(rawFrom, "*") {
			continue
		}
		from := strings.TrimSpace(rawFrom)
		to := strings.TrimSpace(rawTo)
		if from == "" || to == "" || from != to {
			continue
		}
		whitelist[to] = struct{}{}
	}
	if len(whitelist) == 0 {
		return nil
	}
	return whitelist
}

// ExtractExplicitFinalModelWhitelist reads the separate model_whitelist field.
// Exact names and trailing wildcards are accepted; embedded wildcards are ignored.
func ExtractExplicitFinalModelWhitelist(rawWhitelist any) map[string]struct{} {
	if rawWhitelist == nil {
		return nil
	}
	values := make([]string, 0)
	switch typed := rawWhitelist.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, raw := range typed {
			if raw == nil {
				continue
			}
			if model, ok := raw.(string); ok {
				values = append(values, strings.TrimSpace(model))
			} else {
				values = append(values, strings.TrimSpace(fmt.Sprint(raw)))
			}
		}
	default:
		return nil
	}
	whitelist := make(map[string]struct{})
	for _, rawModel := range values {
		model := strings.TrimSpace(rawModel)
		if model == "" || strings.Contains(strings.TrimSuffix(model, "*"), "*") {
			continue
		}
		whitelist[model] = struct{}{}
	}
	if len(whitelist) == 0 {
		return nil
	}
	return whitelist
}

// ResolveFinalModelWhitelist prefers the separate model_whitelist field.
// When absent, platforms other than Qoder accept legacy identity mappings as a whitelist.
func ResolveFinalModelWhitelist(platform string, credentials map[string]any, mapping map[string]string) (map[string]struct{}, bool) {
	if credentials != nil {
		if rawWhitelist, exists := credentials["model_whitelist"]; exists {
			return ExtractExplicitFinalModelWhitelist(rawWhitelist), true
		}
	}
	if platform == PlatformQoder {
		return nil, false
	}
	return ExtractFinalModelWhitelist(mapping), false
}

// GetOpenAICompactMode returns the administrator switch for legacy compaction.
// OpenAI providers default to enabled; other platforms return disabled.
func (r *Record) GetOpenAICompactMode() string {
	if r == nil || !r.IsOpenAI() {
		return OpenAICompactModeForceOff
	}
	mode, _ := r.Extra["openai_compact_mode"].(string)
	if strings.EqualFold(strings.TrimSpace(mode), OpenAICompactModeForceOff) {
		return OpenAICompactModeForceOff
	}
	return OpenAICompactModeForceOn
}

// AllowsOpenAICompact reports whether legacy compaction is enabled for this OpenAI provider.
func (r *Record) AllowsOpenAICompact() bool {
	return r != nil && r.IsOpenAI() && r.GetOpenAICompactMode() == OpenAICompactModeForceOn
}

// GetOpenAINativeCompactionV2Mode returns the administrator switch for native V2 compaction.
func (r *Record) GetOpenAINativeCompactionV2Mode() string {
	if r == nil || !r.IsOpenAI() {
		return OpenAICompactModeForceOff
	}
	mode, _ := r.Extra[OpenAINativeCompactionV2ModeExtraKey].(string)
	if strings.EqualFold(strings.TrimSpace(mode), OpenAICompactModeForceOff) {
		return OpenAICompactModeForceOff
	}
	return OpenAICompactModeForceOn
}

// AllowsOpenAINativeCompactionV2 reports whether native V2 compaction is enabled.
func (r *Record) AllowsOpenAINativeCompactionV2() bool {
	return r != nil && r.IsOpenAI() && r.GetOpenAINativeCompactionV2Mode() == OpenAICompactModeForceOn
}

// GetCompactModelMapping returns compact-only model remapping configuration.
// This mapping is intended for /responses/compact only and does not affect
// normal /responses traffic.
func (r *Record) GetCompactModelMapping() map[string]string {
	if r == nil || r.Credentials == nil {
		return nil
	}
	return StringMappingFromRaw(r.Credentials["compact_model_mapping"])
}

// ResolveCompactMappedModel resolves compact-only model remapping and reports
// whether a compact-specific mapping rule matched.
func (r *Record) ResolveCompactMappedModel(requestedModel string) (mappedModel string, matched bool) {
	mapping := r.GetCompactModelMapping()
	if len(mapping) == 0 {
		return requestedModel, false
	}
	if mappedModel, matched := ResolveRequestedModelInMapping(mapping, requestedModel); matched {
		return mappedModel, true
	}
	return requestedModel, false
}

func (r *Record) GetBaseURL() string {
	if r.Type != ProviderTypeAPIKey {
		return ""
	}
	baseURL := r.GetCredential("base_url")
	if baseURL == "" {
		return "https://api.anthropic.com"
	}
	return baseURL
}

// GetGeminiBaseURL returns the configured Gemini endpoint or defaultBaseURL when blank.
func (r *Record) GetGeminiBaseURL(defaultBaseURL string) string {
	baseURL := strings.TrimSpace(r.GetCredential("base_url"))
	if baseURL == "" {
		return defaultBaseURL
	}
	return baseURL
}

func (r *Record) GetExtraString(key string) string {
	if r.Extra == nil {
		return ""
	}
	if v, ok := r.Extra[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (r *Record) GetClaudeUserID() string {
	if v := strings.TrimSpace(r.GetExtraString("claude_user_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.GetExtraString("anthropic_user_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.GetCredential("claude_user_id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.GetCredential("anthropic_user_id")); v != "" {
		return v
	}
	return ""
}

func MatchWildcard(pattern, str string) bool { return modelmap.Matches(pattern, str) }

func (r *Record) IsCustomErrorCodesEnabled() bool {
	if r.Type != ProviderTypeAPIKey || r.Credentials == nil {
		return false
	}
	if v, ok := r.Credentials["custom_error_codes_enabled"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// IsPoolMode reports whether pool mode is enabled for an API Key or Bedrock provider.
// Pool mode delegates upstream errors to the pool; administrator error policies take precedence and selected
// statuses permit same-provider retries.
func (r *Record) IsPoolMode() bool {
	if !r.IsAPIKeyOrBedrock() || r.Credentials == nil {
		return false
	}
	if v, ok := r.Credentials["pool_mode"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// GetPoolModeRetryCount returns the same-provider retry count for pool mode.
// Missing or invalid values default to 3; configured values are clamped to 0 through 10.
func (r *Record) GetPoolModeRetryCount() int {
	if r == nil || !r.IsPoolMode() || r.Credentials == nil {
		return DefaultPoolModeRetryCount
	}
	raw, ok := r.Credentials["pool_mode_retry_count"]
	if !ok || raw == nil {
		return DefaultPoolModeRetryCount
	}
	count := ParsePoolModeRetryCount(raw)
	if count < 0 {
		return 0
	}
	if count > MaxPoolModeRetryCount {
		return MaxPoolModeRetryCount
	}
	return count
}

func ParsePoolModeRetryCount(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return DefaultPoolModeRetryCount
}

// IsPoolModeRetryableStatus reports whether the status belongs to the default pool retry list.
func IsPoolModeRetryableStatus(statusCode int) bool {
	return slices.Contains(DefaultPoolModeRetryableStatusCodes, statusCode)
}

// GetPoolModeRetryStatusCodes returns the provider override for pool-mode retry statuses.
//
// The result distinguishes inherited defaults from disabled retries:
//
//   - nil means missing or invalid configuration; callers use the default [401, 403, 429].
//
//   - An empty slice disables status-triggered same-provider retries.
//
//   - A nonempty slice is sorted, deduplicated, and restricted to HTTP statuses 100 through 599.
func (r *Record) GetPoolModeRetryStatusCodes() []int {
	if r == nil || r.Credentials == nil {
		return nil
	}
	raw, ok := r.Credentials["pool_mode_retry_status_codes"]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	seen := make(map[int]struct{}, len(arr))
	codes := make([]int, 0, len(arr))
	for _, v := range arr {
		var code int
		switch n := v.(type) {
		case float64:
			code = int(n)
		case int:
			code = n
		case int64:
			code = int(n)
		case json.Number:
			i, err := n.Int64()
			if err != nil {
				continue
			}
			code = int(i)
		case string:
			i, err := strconv.Atoi(strings.TrimSpace(n))
			if err != nil {
				continue
			}
			code = i
		default:
			continue
		}
		if code < 100 || code > 599 {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	sort.Ints(codes)
	return codes
}

// IsPoolModeRetryableStatus reports whether the provider retries the given status.
// Missing override configuration uses the default status list.
func (r *Record) IsPoolModeRetryableStatus(statusCode int) bool {
	codes := r.GetPoolModeRetryStatusCodes()
	if codes == nil {
		return IsPoolModeRetryableStatus(statusCode)
	}
	return slices.Contains(codes, statusCode)
}

func (r *Record) GetCustomErrorCodes() []int {
	if r.Credentials == nil {
		return nil
	}
	raw, ok := r.Credentials["custom_error_codes"]
	if !ok || raw == nil {
		return nil
	}
	if arr, ok := raw.([]any); ok {
		result := make([]int, 0, len(arr))
		for _, v := range arr {
			if f, ok := v.(float64); ok {
				result = append(result, int(f))
			}
		}
		return result
	}
	return nil
}

func (r *Record) ShouldHandleErrorCode(statusCode int) bool {
	if !r.IsCustomErrorCodesEnabled() {
		return true
	}
	codes := r.GetCustomErrorCodes()
	if len(codes) == 0 {
		return true
	}
	return slices.Contains(codes, statusCode)
}

func (r *Record) IsInterceptWarmupEnabled() bool {
	if r.Credentials == nil {
		return false
	}
	if v, ok := r.Credentials["intercept_warmup_requests"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

func (r *Record) IsBedrock() bool {
	return r.Platform == PlatformAnthropic && r.Type == ProviderTypeBedrock
}

func (r *Record) IsBedrockAPIKey() bool {
	return r.IsBedrock() && r.GetCredential("auth_mode") == "apikey"
}

// IsAPIKeyOrBedrock reports whether the type supports quota limits and pool mode.
func (r *Record) IsAPIKeyOrBedrock() bool {
	return r.Type == ProviderTypeAPIKey || r.Type == ProviderTypeBedrock
}

func (r *Record) IsOpenAI() bool {
	return r.Platform == PlatformOpenAI
}

func (r *Record) IsAnthropic() bool {
	return r.Platform == PlatformAnthropic
}

func (r *Record) IsQoder() bool {
	return r.Platform == PlatformQoder
}

func (r *Record) IsQoderCosy() bool {
	return r.IsQoder() && r.Type == ProviderTypeCosy
}

func (r *Record) IsOpenAIOAuth() bool {
	return r.IsOpenAI() && r.Type == ProviderTypeOAuth
}

// IsOpenAIOAuthLike reports OpenAI credentials that use the ChatGPT/Codex
// inference protocol. Setup tokens share that forwarding contract but do not
// participate in the refreshable OAuth credential lifecycle.
func (r *Record) IsOpenAIOAuthLike() bool {
	return r != nil && r.IsOpenAI() && (r.Type == ProviderTypeOAuth || r.Type == ProviderTypeSetupToken)
}

// UsesOpenAICodexProtocol accepts OAuth providers with an implicit platform,
// and OpenAI providers using OAuth or Setup Token credentials.
func (r *Record) UsesOpenAICodexProtocol() bool {
	return r != nil && (r.Type == ProviderTypeOAuth || r.IsOpenAIOAuthLike())
}

func (r *Record) IsOpenAIChatGPTSubscription() bool {
	if !r.IsOpenAIOAuth() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.GetCredential("plan_type"))) {
	case "", "free", "abnormal":
		return false
	default:
		return true
	}
}

// IsOpenAIPersonalAccessToken reports whether OpenAI OAuth uses Codex PAT authentication.
func (r *Record) IsOpenAIPersonalAccessToken() bool {
	if r == nil || !r.IsOpenAIOAuth() {
		return false
	}
	return IsOpenAIPersonalAccessTokenAuthMode(r.GetCredential(OpenAIAuthModeCredentialKey)) ||
		IsOpenAIPersonalAccessTokenAuthMode(r.GetCredential(OpenAIAuthModeLegacyCredentialKey))
}

func (r *Record) IsOpenAIApiKey() bool {
	return r.IsOpenAI() && r.Type == ProviderTypeAPIKey
}

// GetProviderMode reads credentials.provider_mode for Chinese providers, defaulting to payg
// when missing or invalid. Other platforms return an empty string.
func (r *Record) GetProviderMode() string {
	if r == nil || !r.IsCNProvider() {
		return ""
	}
	mode := strings.TrimSpace(r.GetCredential("provider_mode"))
	if mode == ProviderModePayG || mode == ProviderModeCoding {
		return mode
	}
	return ProviderModePayG
}

// IsCodingPlan reports whether Coding Plan mode is selected for rolling usage-window cooldowns.
func (r *Record) IsCodingPlan() bool {
	return r.GetProviderMode() == ProviderModeCoding
}

// SupportsNativeCNResponses reports whether the Chinese provider has a native Responses endpoint.
// DeepSeek uses /responses; Kimi pay-as-you-go and Coding Plan use
// /v1/responses on their respective base URLs.
func (r *Record) SupportsNativeCNResponses() bool {
	if r == nil {
		return false
	}
	switch r.Platform {
	case PlatformDeepseek, PlatformKimi:
		return true
	default:
		return false
	}
}

func (r *Record) DefaultCNProtocolBaseURL(protocol string) string {
	switch protocol {
	case APIProtocolAnthropic:
		switch r.Platform {
		case PlatformKimi:
			if r.GetProviderMode() == ProviderModeCoding {
				return DefaultKimiCodingAnthropicBaseURL
			}
			return DefaultKimiPayGAnthropicBaseURL
		case PlatformZhipu:
			return DefaultZhipuAnthropicBaseURL
		case PlatformDeepseek:
			return DefaultDeepseekAnthropicBaseURL
		}
	case APIProtocolChatCompletions, APIProtocolResponses:
		switch r.Platform {
		case PlatformKimi:
			if r.GetProviderMode() == ProviderModeCoding {
				return DefaultKimiCodingBaseURL
			}
			return DefaultKimiPayGBaseURL
		case PlatformZhipu:
			if r.GetProviderMode() == ProviderModeCoding {
				return DefaultZhipuCodingBaseURL
			}
			return DefaultZhipuPayGBaseURL
		case PlatformDeepseek:
			return DefaultDeepseekBaseURL
		}
	}
	return ""
}

// IsDefaultCNAnthropicBaseURL matches a built-in endpoint, including its host and path.
func IsDefaultCNAnthropicBaseURL(baseURL string) bool {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch normalized {
	case DefaultKimiPayGAnthropicBaseURL,
		DefaultKimiCodingAnthropicBaseURL,
		DefaultZhipuAnthropicBaseURL,
		DefaultDeepseekAnthropicBaseURL:
		return true
	default:
		return false
	}
}

// StripCNAnthropicPathSuffix removes a relay URL's trailing /anthropic path segment.
// Parse failures return the trimmed input for the caller's URL validation to reject.
func StripCNAnthropicPathSuffix(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "/anthropic" {
		parsed.Path = ""
		parsed.RawPath = ""
	} else if before, ok := strings.CutSuffix(path, "/anthropic"); ok {
		parsed.Path = before
		parsed.RawPath = ""
	}
	return strings.TrimRight(parsed.String(), "/")
}

// GetCNAPIKey returns the api_key credential for Kimi, Zhipu, or DeepSeek.
// GetOpenAIApiKey handles the OpenAI platform separately.
func (r *Record) GetCNAPIKey() string {
	if r == nil || !r.IsCNProvider() {
		return ""
	}
	return r.GetCredential("api_key")
}

// GetCodingPlanProvider identifies a Coding Plan supplier by platform.
// Custom relay URLs are allowed, so their contents cannot identify the supplier.
func (r *Record) GetCodingPlanProvider() string {
	if r == nil || r.GetProviderMode() != ProviderModeCoding {
		return ""
	}
	switch r.Platform {
	case PlatformKimi:
		return PlatformKimi
	case PlatformZhipu:
		return PlatformZhipu
	default:
		return ""
	}
}

func (r *Record) GetOpenAIAccessToken() string {
	if !r.IsOpenAI() {
		return ""
	}
	return r.GetCredential("access_token")
}

func (r *Record) GetOpenAIRefreshToken() string {
	if !r.IsOpenAIOAuth() {
		return ""
	}
	return r.GetCredential("refresh_token")
}

func (r *Record) GetGrokAccessToken() string {
	if !r.IsGrok() {
		return ""
	}
	return r.GetCredential("access_token")
}

func (r *Record) GetGrokRefreshToken() string {
	if !r.IsGrokOAuth() {
		return ""
	}
	return r.GetCredential("refresh_token")
}

func (r *Record) GetOpenAIApiKey() string {
	if !r.IsOpenAIApiKey() {
		return ""
	}
	return r.GetCredential("api_key")
}

// GetOpenAIProtocolAPIKey returns an API key for OpenAI, Kimi, Zhipu, or DeepSeek.
// Forwarding and model sync share this accessor; OpenAI-specific eligibility uses IsOpenAIApiKey.
func (r *Record) GetOpenAIProtocolAPIKey() string {
	if r == nil {
		return ""
	}
	if r.IsCNProvider() {
		if r.Type != ProviderTypeAPIKey {
			return ""
		}
		return r.GetCredential("api_key")
	}
	return r.GetOpenAIApiKey()
}

func (r *Record) GetOpenAIUserAgent() string {
	if !r.IsOpenAI() {
		return ""
	}
	return r.GetCredential("user_agent")
}

func (r *Record) GetChatGPTAccountID() string {
	if !r.IsOpenAIOAuthLike() {
		return ""
	}
	return r.GetCredential("chatgpt_account_id")
}

// IsChatGPTAccountFedRAMP reports whether the ChatGPT credentials specify a FedRAMP environment.
func (r *Record) IsChatGPTAccountFedRAMP() bool {
	if !r.IsOpenAIOAuthLike() || r.Credentials == nil {
		return false
	}
	v, ok := r.Credentials["chatgpt_account_is_fedramp"]
	if !ok || v == nil {
		return false
	}
	switch value := v.(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return err == nil && parsed
	case json.Number:
		parsed, err := strconv.ParseBool(value.String())
		return err == nil && parsed
	case float64:
		return value != 0
	case int:
		return value != 0
	case int64:
		return value != 0
	default:
		return false
	}
}

func (r *Record) GetOpenAIDeviceID() string {
	if !r.IsOpenAIOAuth() {
		return ""
	}
	return strings.TrimSpace(r.GetExtraString("openai_device_id"))
}

func GrokMediaEligibilityOverride(extra map[string]any) (bool, bool) {
	if extra == nil {
		return false, false
	}
	raw, exists := extra[GrokMediaEligibleExtraKey]
	if !exists || raw == nil {
		return false, false
	}
	value, ok := raw.(bool)
	return value, ok
}

func (r *Record) OpenAIWorkloadCapabilitySet() (map[string]bool, bool) {
	if r == nil || r.Credentials == nil {
		return nil, false
	}
	raw, found := r.Credentials[OpenAIWorkloadCapabilitiesCredentialKey]
	if !found || raw == nil {
		return nil, false
	}

	result := make(map[string]bool)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		result[value] = true
	}

	// Empty OAuth and Setup Token containers mean unconfigured; an empty API Key set disables all capabilities.
	switch capabilities := raw.(type) {
	case []any:
		if len(capabilities) == 0 && r.IsOpenAIOAuthLike() {
			return nil, false
		}
		for _, item := range capabilities {
			if value, ok := item.(string); ok {
				add(value)
			}
		}
	case []string:
		if len(capabilities) == 0 && r.IsOpenAIOAuthLike() {
			return nil, false
		}
		for _, value := range capabilities {
			add(value)
		}
	case map[string]any:
		if len(capabilities) == 0 && r.IsOpenAIOAuthLike() {
			return nil, false
		}
		for key, value := range capabilities {
			enabled, ok := value.(bool)
			if ok && enabled {
				add(key)
			}
		}
	case map[string]bool:
		if len(capabilities) == 0 && r.IsOpenAIOAuthLike() {
			return nil, false
		}
		for key, enabled := range capabilities {
			if enabled {
				add(key)
			}
		}
	}

	return result, true
}

func (r *Record) SupportsOpenAIImageCapability(capability OpenAIImagesCapability) bool {
	if capability == "" {
		return true
	}
	if !r.IsOpenAI() {
		return false
	}
	switch capability {
	case OpenAIImagesCapabilityBasic, OpenAIImagesCapabilityNative:
		return r.Type == ProviderTypeOAuth || r.Type == ProviderTypeSetupToken || r.Type == ProviderTypeAPIKey
	default:
		return true
	}
}

// IsOveragesEnabled reports whether Antigravity AI Credits overage requests are enabled.
func (r *Record) IsOveragesEnabled() bool {
	if r.Platform != PlatformAntigravity {
		return false
	}
	if r.Extra == nil {
		return false
	}
	if v, ok := r.Extra["allow_overages"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// IsOpenAIPassthroughEnabled reports whether OpenAI passthrough with authentication replacement is enabled.
//
// The primary field is providers.extra.openai_passthrough.
// The legacy OAuth field providers.extra.openai_oauth_passthrough remains accepted.
// Missing or incorrectly typed fields default to false.
func (r *Record) IsOpenAIPassthroughEnabled() bool {
	if r == nil || !r.IsOpenAI() || r.Extra == nil {
		return false
	}
	if enabled, ok := r.Extra["openai_passthrough"].(bool); ok {
		return enabled
	}
	if enabled, ok := r.Extra["openai_oauth_passthrough"].(bool); ok {
		return enabled
	}
	return false
}

// IsOpenAIResponsesFlattenNamespacesEnabled reports whether Codex namespace tools are flattened.
// providers.extra.openai_responses_flatten_namespaces defaults to false, retaining namespaces.
// This OpenAI OAuth compatibility switch supports relays that do not accept namespaces.
func (r *Record) IsOpenAIResponsesFlattenNamespacesEnabled() bool {
	if r == nil || !r.IsOpenAIOAuth() || r.Extra == nil {
		return false
	}
	enabled, ok := r.Extra["openai_responses_flatten_namespaces"].(bool)
	return ok && enabled
}

// IsOpenAIWSAllowStoreRecoveryEnabled reports whether WebSocket store recovery is enabled.
// The setting is providers.extra.openai_ws_allow_store_recovery.
func (r *Record) IsOpenAIWSAllowStoreRecoveryEnabled() bool {
	if r == nil || !r.IsOpenAI() || r.Extra == nil {
		return false
	}
	enabled, ok := r.Extra["openai_ws_allow_store_recovery"].(bool)
	return ok && enabled
}

// IsOpenAIOAuthPassthroughEnabled exposes the legacy OAuth-only passthrough check.
func (r *Record) IsOpenAIOAuthPassthroughEnabled() bool {
	return r != nil && r.IsOpenAIOAuth() && r.IsOpenAIPassthroughEnabled()
}

// IsAnthropicAPIKeyPassthroughEnabled reports whether Anthropic API Key passthrough with authentication
// replacement is enabled.
// The setting is providers.extra.anthropic_passthrough.
// Missing or incorrectly typed values default to false.
func (r *Record) IsAnthropicAPIKeyPassthroughEnabled() bool {
	if r == nil || r.Platform != PlatformAnthropic || r.Type != ProviderTypeAPIKey || r.Extra == nil {
		return false
	}
	enabled, ok := r.Extra["anthropic_passthrough"].(bool)
	return ok && enabled
}

// IsCodexCLIOnlyEnabled reports whether the OpenAI OAuth client policy is Codex-only.
// openai_oauth_client_policy takes precedence over the legacy providers.extra.codex_cli_only flag.
func (r *Record) IsCodexCLIOnlyEnabled() bool {
	return r.GetOpenAIOAuthClientPolicy() == OpenAIOAuthClientPolicyCodexOnly
}

// GetOpenAIOAuthClientPolicy returns the client access policy for an OpenAI OAuth provider.
func (r *Record) GetOpenAIOAuthClientPolicy() string {
	if r == nil || !r.IsOpenAIOAuth() || r.Extra == nil {
		return OpenAIOAuthClientPolicyAny
	}
	if policy, ok := r.Extra["openai_oauth_client_policy"].(string); ok {
		switch strings.TrimSpace(policy) {
		case OpenAIOAuthClientPolicyCodexOnly:
			return OpenAIOAuthClientPolicyCodexOnly
		case OpenAIOAuthClientPolicyTLSRouterMatchedOnly:
			return OpenAIOAuthClientPolicyTLSRouterMatchedOnly
		case OpenAIOAuthClientPolicyAny:
			return OpenAIOAuthClientPolicyAny
		}
	}
	enabled, ok := r.Extra["codex_cli_only"].(bool)
	if ok && enabled {
		return OpenAIOAuthClientPolicyCodexOnly
	}
	return OpenAIOAuthClientPolicyAny
}

// IsOpenAIOAuthTLSRouterMatchedOnly reports whether client access requires a TLS router match.
func (r *Record) IsOpenAIOAuthTLSRouterMatchedOnly() bool {
	return r.GetOpenAIOAuthClientPolicy() == OpenAIOAuthClientPolicyTLSRouterMatchedOnly
}

// GetCodexCLIOnlyAllowedClients returns preset client IDs additionally allowed by the Codex-only policy.
// It applies to OpenAI OAuth and returns no IDs for missing or incorrectly typed settings.
// The OpenAI client registry defines matching rules; configuration references its preset IDs.
func (r *Record) GetCodexCLIOnlyAllowedClients() []string {
	if r == nil || !r.IsOpenAIOAuth() || r.Extra == nil {
		return nil
	}
	raw, ok := r.Extra["codex_cli_only_allowed_clients"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		result := make([]string, 0, len(v))
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				result = append(result, s)
			}
		}
		return result
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// IsAnthropicOAuthOrSetupToken identifies Anthropic OAuth and Setup Token providers.
// These types support five-hour window spending and session-count limits.
func (r *Record) IsAnthropicOAuthOrSetupToken() bool {
	if r == nil {
		return false
	}
	return r.Platform == PlatformAnthropic && (r.Type == ProviderTypeOAuth || r.Type == ProviderTypeSetupToken)
}

// SupportsTLSFingerprint reports whether the provider supports TLS fingerprint emulation.
// Supported types include Anthropic OAuth/Setup Token, OpenAI OAuth, Chinese API Key providers, and Qoder COSY.
func (r *Record) SupportsTLSFingerprint() bool {
	if r == nil {
		return false
	}
	if r.IsAnthropicOAuthOrSetupToken() {
		return true
	}
	return (r.Platform == PlatformOpenAI && r.Type == ProviderTypeOAuth) ||
		(r.IsCNProvider() && r.Type == ProviderTypeAPIKey) ||
		r.IsQoderCosy()
}

// IsTLSFingerprintEnabled reports whether TLS fingerprint emulation is enabled for a supported provider.
// The selected profile controls the emulated client handshake.
func (r *Record) IsTLSFingerprintEnabled() bool {
	if !r.SupportsTLSFingerprint() {
		return false
	}
	if r.Extra == nil {
		return false
	}
	if v, ok := r.Extra["enable_tls_fingerprint"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// GetTLSFingerprintProfileID returns the configured TLS fingerprint profile ID.
// Zero selects the built-in default profile.
func (r *Record) GetTLSFingerprintProfileID() int64 {
	if r.Extra == nil {
		return 0
	}
	v, ok := r.Extra["tls_fingerprint_profile_id"]
	if !ok {
		return 0
	}
	switch id := v.(type) {
	case float64:
		return int64(id)
	case int64:
		return id
	case int:
		return int64(id)
	case json.Number:
		if i, err := id.Int64(); err == nil {
			return i
		}
	}
	return 0
}

// GetTLSFingerprintRouterID returns the configured TLS router ID.
// Zero means no router is selected.
func (r *Record) GetTLSFingerprintRouterID() int64 {
	if r.Extra == nil {
		return 0
	}
	v, ok := r.Extra["tls_fingerprint_router_id"]
	if !ok {
		return 0
	}
	switch id := v.(type) {
	case float64:
		return int64(id)
	case int64:
		return id
	case int:
		return int64(id)
	case json.Number:
		if i, err := id.Int64(); err == nil {
			return i
		}
	}
	return 0
}

// IsSessionIDMaskingEnabled reports whether session ID masking is enabled
// for an Anthropic OAuth or Setup Token provider.
// Masking reuses the session ID in metadata.user_id across requests
// with a 15-minute idle TTL renewed on each request so upstream can associate them with one session.
func (r *Record) IsSessionIDMaskingEnabled() bool {
	if !r.IsAnthropicOAuthOrSetupToken() {
		return false
	}
	if r.Extra == nil {
		return false
	}
	if v, ok := r.Extra["session_id_masking_enabled"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// IsCustomBaseURLEnabled reports whether relay forwarding through a custom base URL is enabled.
// This setting applies to Anthropic OAuth and Setup Token providers.
func (r *Record) IsCustomBaseURLEnabled() bool {
	if !r.IsAnthropicOAuthOrSetupToken() {
		return false
	}
	if r.Extra == nil {
		return false
	}
	if v, ok := r.Extra["custom_base_url_enabled"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// GetCustomBaseURL returns the custom relay base URL.
func (r *Record) GetCustomBaseURL() string {
	return r.GetExtraString("custom_base_url")
}

// IsCacheTTLOverrideEnabled reports whether cache-creation tokens use a forced TTL category.
// This setting applies to Anthropic OAuth and Setup Token providers.
// The selected category is 5m or 1h.
func (r *Record) IsCacheTTLOverrideEnabled() bool {
	if !r.IsAnthropicOAuthOrSetupToken() {
		return false
	}
	if r.Extra == nil {
		return false
	}
	if v, ok := r.Extra["cache_ttl_override_enabled"]; ok {
		if enabled, ok := v.(bool); ok {
			return enabled
		}
	}
	return false
}

// GetCacheTTLOverrideTarget returns the forced cache TTL category.
// Valid values are "5m" and "1h"; missing or invalid values default to "5m".
func (r *Record) GetCacheTTLOverrideTarget() string {
	if r.Extra == nil {
		return "5m"
	}
	if v, ok := r.Extra["cache_ttl_override_target"]; ok {
		if target, ok := v.(string); ok && (target == "5m" || target == "1h") {
			return target
		}
	}
	return "5m"
}

// GetQuotaLimit returns the provider's total quota limit in USD.
// Zero disables the limit.
func (r *Record) GetQuotaLimit() float64 {
	return r.GetExtraFloat64("quota_limit")
}

// GetQuotaUsed returns the total quota used in USD.
func (r *Record) GetQuotaUsed() float64 {
	return r.GetExtraFloat64("quota_used")
}

// GetQuotaDailyLimit returns the daily quota limit in USD; zero disables it.
func (r *Record) GetQuotaDailyLimit() float64 {
	return r.GetExtraFloat64("quota_daily_limit")
}

// GetQuotaDailyUsed returns the daily quota used in USD.
func (r *Record) GetQuotaDailyUsed() float64 {
	return r.GetExtraFloat64("quota_daily_used")
}

// GetQuotaWeeklyLimit returns the weekly quota limit in USD; zero disables it.
func (r *Record) GetQuotaWeeklyLimit() float64 {
	return r.GetExtraFloat64("quota_weekly_limit")
}

// GetQuotaWeeklyUsed returns the weekly quota used in USD.
func (r *Record) GetQuotaWeeklyUsed() float64 {
	return r.GetExtraFloat64("quota_weekly_used")
}

// GetExtraFloat64 parses a numeric Extra value, returning zero when absent.
func (r *Record) GetExtraFloat64(key string) float64 {
	if r.Extra == nil {
		return 0
	}
	if v, ok := r.Extra[key]; ok {
		return ParseExtraFloat64(v)
	}
	return 0
}

// GetExtraTime reads an RFC3339 timestamp from Extra.
func (r *Record) GetExtraTime(key string) time.Time {
	if r.Extra == nil {
		return time.Time{}
	}
	if v, ok := r.Extra[key]; ok {
		return ParseExtraTime(v)
	}
	return time.Time{}
}

// GetExtraBool reads a boolean Extra value, returning false when absent or incorrectly typed.
func (r *Record) GetExtraBool(key string) bool {
	if r.Extra == nil {
		return false
	}
	if v, ok := r.Extra[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// GetExtraStringDefault returns defaultVal when the Extra string is missing or empty.
func (r *Record) GetExtraStringDefault(key, defaultVal string) string {
	if v := r.GetExtraString(key); v != "" {
		return v
	}
	return defaultVal
}

// GetExtraInt converts a numeric Extra value to an integer.
func (r *Record) GetExtraInt(key string) int {
	if r.Extra == nil {
		return 0
	}
	if v, ok := r.Extra[key]; ok {
		return int(ParseExtraFloat64(v))
	}
	return 0
}

// GetQuotaDailyResetMode returns "fixed" when selected, otherwise "rolling".
func (r *Record) GetQuotaDailyResetMode() string {
	if m := r.GetExtraString("quota_daily_reset_mode"); m == "fixed" {
		return "fixed"
	}
	return "rolling"
}

// GetQuotaDailyResetHour returns the configured reset hour (0-23), defaulting to zero.
func (r *Record) GetQuotaDailyResetHour() int {
	return r.GetExtraInt("quota_daily_reset_hour")
}

// GetQuotaWeeklyResetMode returns "fixed" when selected, otherwise "rolling".
func (r *Record) GetQuotaWeeklyResetMode() string {
	if m := r.GetExtraString("quota_weekly_reset_mode"); m == "fixed" {
		return "fixed"
	}
	return "rolling"
}

// GetQuotaWeeklyResetDay returns the reset weekday (Sunday=0 through Saturday=6), defaulting to Monday.
func (r *Record) GetQuotaWeeklyResetDay() int {
	if r.Extra == nil {
		return 1
	}
	if _, ok := r.Extra["quota_weekly_reset_day"]; !ok {
		return 1
	}
	return r.GetExtraInt("quota_weekly_reset_day")
}

// GetQuotaWeeklyResetHour returns the configured weekly reset hour (0-23), defaulting to zero.
func (r *Record) GetQuotaWeeklyResetHour() int {
	return r.GetExtraInt("quota_weekly_reset_hour")
}

// GetQuotaResetTimezone returns the IANA reset timezone, defaulting to "UTC".
func (r *Record) GetQuotaResetTimezone() string {
	if tz := r.GetExtraString("quota_reset_timezone"); tz != "" {
		return tz
	}
	return "UTC"
}

// QuotaNotifyConfig returns the notify configuration for a given quota dimension.
// dim must be one of quotaDimDaily, quotaDimWeekly, quotaDimTotal.
func (r *Record) QuotaNotifyConfig(dim string) (enabled bool, threshold float64, thresholdType string) {
	enabled = r.GetExtraBool("quota_notify_" + dim + "_enabled")
	threshold = r.GetExtraFloat64("quota_notify_" + dim + "_threshold")
	thresholdType = r.GetExtraStringDefault("quota_notify_"+dim+"_threshold_type", thresholdTypeFixed)
	return
}

func (r *Record) GetQuotaNotifyDailyEnabled() bool {
	e, _, _ := r.QuotaNotifyConfig(quotaDimDaily)
	return e
}

func (r *Record) GetQuotaNotifyDailyThreshold() float64 {
	_, t, _ := r.QuotaNotifyConfig(quotaDimDaily)
	return t
}

func (r *Record) GetQuotaNotifyDailyThresholdType() string {
	_, _, tt := r.QuotaNotifyConfig(quotaDimDaily)
	return tt
}

func (r *Record) GetQuotaNotifyWeeklyEnabled() bool {
	e, _, _ := r.QuotaNotifyConfig(quotaDimWeekly)
	return e
}

func (r *Record) GetQuotaNotifyWeeklyThreshold() float64 {
	_, t, _ := r.QuotaNotifyConfig(quotaDimWeekly)
	return t
}

func (r *Record) GetQuotaNotifyWeeklyThresholdType() string {
	_, _, tt := r.QuotaNotifyConfig(quotaDimWeekly)
	return tt
}

func (r *Record) GetQuotaNotifyTotalEnabled() bool {
	e, _, _ := r.QuotaNotifyConfig(quotaDimTotal)
	return e
}

func (r *Record) GetQuotaNotifyTotalThreshold() float64 {
	_, t, _ := r.QuotaNotifyConfig(quotaDimTotal)
	return t
}

func (r *Record) GetQuotaNotifyTotalThresholdType() string {
	_, _, tt := r.QuotaNotifyConfig(quotaDimTotal)
	return tt
}

func LastFixedDailyReset(hour int, tz *time.Location, now time.Time) time.Time {
	return billing.LastFixedDailyReset(hour, tz, now)
}

func LastFixedWeeklyReset(day, hour int, tz *time.Location, now time.Time) time.Time {
	return billing.LastFixedWeeklyReset(day, hour, tz, now)
}

// IsFixedDailyPeriodExpired reports whether the period started before the latest fixed daily reset.
func (r *Record) IsFixedDailyPeriodExpired(periodStart time.Time) bool {
	if periodStart.IsZero() {
		return true
	}
	tz, err := r.LoadLocation(r.GetQuotaResetTimezone())
	if err != nil {
		tz = time.UTC
	}
	lastReset := LastFixedDailyReset(r.GetQuotaDailyResetHour(), tz, r.now())
	return periodStart.Before(lastReset)
}

// IsFixedWeeklyPeriodExpired reports whether the period started before the latest fixed weekly reset.
func (r *Record) IsFixedWeeklyPeriodExpired(periodStart time.Time) bool {
	if periodStart.IsZero() {
		return true
	}
	tz, err := r.LoadLocation(r.GetQuotaResetTimezone())
	if err != nil {
		tz = time.UTC
	}
	lastReset := LastFixedWeeklyReset(r.GetQuotaWeeklyResetDay(), r.GetQuotaWeeklyResetHour(), tz, r.now())
	return periodStart.Before(lastReset)
}

// ValidateQuotaResetConfig validates reset modes, clock values, and the timezone.
func ValidateQuotaResetConfig(extra map[string]any, loadLocation func(string) (*time.Location, error)) error {
	if extra == nil {
		return nil
	}
	// Validate the IANA timezone name.
	if tz, ok := extra["quota_reset_timezone"].(string); ok && tz != "" {
		if _, err := loadLocation(tz); err != nil {
			return errors.New("invalid quota_reset_timezone: must be a valid IANA timezone name")
		}
	}
	// Daily reset mode.
	if mode, ok := extra["quota_daily_reset_mode"].(string); ok {
		if mode != "rolling" && mode != "fixed" {
			return errors.New("quota_daily_reset_mode must be 'rolling' or 'fixed'")
		}
	}
	// Daily reset hour.
	if v, ok := extra["quota_daily_reset_hour"]; ok {
		hour := int(ParseExtraFloat64(v))
		if hour < 0 || hour > 23 {
			return errors.New("quota_daily_reset_hour must be between 0 and 23")
		}
	}
	// Weekly reset mode.
	if mode, ok := extra["quota_weekly_reset_mode"].(string); ok {
		if mode != "rolling" && mode != "fixed" {
			return errors.New("quota_weekly_reset_mode must be 'rolling' or 'fixed'")
		}
	}
	// Weekly reset weekday.
	if v, ok := extra["quota_weekly_reset_day"]; ok {
		day := int(ParseExtraFloat64(v))
		if day < 0 || day > 6 {
			return errors.New("quota_weekly_reset_day must be between 0 (Sunday) and 6 (Saturday)")
		}
	}
	// Weekly reset hour.
	if v, ok := extra["quota_weekly_reset_hour"]; ok {
		hour := int(ParseExtraFloat64(v))
		if hour < 0 || hour > 23 {
			return errors.New("quota_weekly_reset_hour must be between 0 and 23")
		}
	}
	return nil
}

// HasAnyQuotaLimit reports whether any quota dimension has a positive limit.
func (r *Record) HasAnyQuotaLimit() bool {
	return r.GetQuotaLimit() > 0 || r.GetQuotaDailyLimit() > 0 || r.GetQuotaWeeklyLimit() > 0
}

// IsPeriodExpired reports whether dur has elapsed since periodStart; a zero start is expired.
func IsPeriodExpired(periodStart time.Time, dur time.Duration) bool {
	if periodStart.IsZero() {
		return true // The next usage increment initializes an unused period.
	}
	return time.Since(periodStart) >= dur
}

// IsDailyQuotaPeriodExpired reports whether daily usage should display as zero for an elapsed period.
func (r *Record) IsDailyQuotaPeriodExpired() bool {
	start := r.GetExtraTime("quota_daily_start")
	if r.GetQuotaDailyResetMode() == "fixed" {
		return r.IsFixedDailyPeriodExpired(start)
	}
	return IsPeriodExpired(start, 24*time.Hour)
}

// IsWeeklyQuotaPeriodExpired reports whether weekly usage should display as zero for an elapsed period.
func (r *Record) IsWeeklyQuotaPeriodExpired() bool {
	start := r.GetExtraTime("quota_weekly_start")
	if r.GetQuotaWeeklyResetMode() == "fixed" {
		return r.IsFixedWeeklyPeriodExpired(start)
	}
	return IsPeriodExpired(start, 7*24*time.Hour)
}

// IsQuotaExceeded reports whether any configured quota dimension has reached its limit.
func (r *Record) IsQuotaExceeded() bool {
	// Total quota has no reset period.
	if limit := r.GetQuotaLimit(); limit > 0 && r.GetQuotaUsed() >= limit {
		return true
	}
	// Expired daily usage is ignored; the next increment resets the period.
	if limit := r.GetQuotaDailyLimit(); limit > 0 {
		start := r.GetExtraTime("quota_daily_start")
		var expired bool
		if r.GetQuotaDailyResetMode() == "fixed" {
			expired = r.IsFixedDailyPeriodExpired(start)
		} else {
			expired = IsPeriodExpired(start, 24*time.Hour)
		}
		if !expired && r.GetQuotaDailyUsed() >= limit {
			return true
		}
	}
	// Weekly limits also ignore elapsed periods.
	if limit := r.GetQuotaWeeklyLimit(); limit > 0 {
		start := r.GetExtraTime("quota_weekly_start")
		var expired bool
		if r.GetQuotaWeeklyResetMode() == "fixed" {
			expired = r.IsFixedWeeklyPeriodExpired(start)
		} else {
			expired = IsPeriodExpired(start, 7*24*time.Hour)
		}
		if !expired && r.GetQuotaWeeklyUsed() >= limit {
			return true
		}
	}
	return false
}

// IsShadow reports whether parent_provider_id is set; Spark is the current shadow preset.
func (r *Record) IsShadow() bool { return r != nil && r.ParentProviderID != nil }

// IsCredentialShadow identifies shadows whose credential operations are handled through the parent.
func (r *Record) IsCredentialShadow() bool { return r.IsShadow() }

// QuotaDimensionOrDefault returns the provider usage dimension, defaulting to "global".
func (r *Record) QuotaDimensionOrDefault() string {
	if r == nil || strings.TrimSpace(r.QuotaDimension) == "" {
		return QuotaDimensionGlobal
	}
	return r.QuotaDimension
}

// IsCNProvider reports whether the platform is Kimi, Zhipu, or DeepSeek.
func IsCNProvider(platform string) bool {
	switch platform {
	case PlatformKimi, PlatformZhipu, PlatformDeepseek:
		return true
	default:
		return false
	}
}
