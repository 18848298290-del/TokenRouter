package provider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"time"
)

const (
	GeminiQuotaPolicySettingKey = "gemini_quota_policy"
	geminiQuotaCacheTTL         = time.Minute
)

type GeminiTierQuotaOverride struct {
	ProRPD          *int64 `json:"pro_rpd"`
	FlashRPD        *int64 `json:"flash_rpd"`
	CooldownMinutes *int   `json:"cooldown_minutes"`
}

// GeminiQuotaOptions supplies static defaults and a loader for cached dynamic settings.
type GeminiQuotaOptions struct {
	StaticTiers  map[string]GeminiTierQuotaOverride
	StaticPolicy string
	LoadPolicy   func(context.Context) (string, error)
	NotFound     error
	Now          func() time.Time
	Log          func(string, ...any)
}

// GeminiQuotaService caches quota policy and loads settings on demand.
type GeminiQuotaService struct {
	options  GeminiQuotaOptions
	mu       sync.Mutex
	cachedAt time.Time
	policy   *GeminiQuotaPolicy
}

type GeminiTierPolicy struct {
	Quota    GeminiQuota
	Cooldown time.Duration
}

type GeminiQuotaPolicy struct {
	tiers map[string]GeminiTierPolicy
}

type geminiQuotaOverridesV1 struct {
	Tiers map[string]GeminiTierQuotaOverride `json:"tiers"`
}

type geminiQuotaOverridesV2 struct {
	QuotaRules map[string]GeminiQuotaRuleOverride `json:"quota_rules"`
}

type GeminiQuotaRuleOverride struct {
	SharedRPD   *int64                    `json:"shared_rpd,omitempty"`
	SharedRPM   *int64                    `json:"rpm,omitempty"`
	GeminiPro   *GeminiModelQuotaOverride `json:"gemini_pro,omitempty"`
	GeminiFlash *GeminiModelQuotaOverride `json:"gemini_flash,omitempty"`
	Desc        *string                   `json:"desc,omitempty"`
}

type GeminiModelQuotaOverride struct {
	RPD *int64 `json:"rpd,omitempty"`
	RPM *int64 `json:"rpm,omitempty"`
}

func cloneGeminiTierOverrides(values map[string]GeminiTierQuotaOverride) map[string]GeminiTierQuotaOverride {
	if values == nil {
		return nil
	}
	out := make(map[string]GeminiTierQuotaOverride, len(values))
	for id, v := range values {
		v.ProRPD = clonePointer(v.ProRPD)
		v.FlashRPD = clonePointer(v.FlashRPD)
		v.CooldownMinutes = clonePointer(v.CooldownMinutes)
		out[id] = v
	}
	return out
}

func NewGeminiQuotaService(options GeminiQuotaOptions) *GeminiQuotaService {
	options.StaticTiers = cloneGeminiTierOverrides(options.StaticTiers)
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Log == nil {
		options.Log = func(string, ...any) {}
	}
	return &GeminiQuotaService{options: options}
}

func (s *GeminiQuotaService) Policy(ctx context.Context) *GeminiQuotaPolicy {
	if s == nil {
		return NewGeminiQuotaPolicy()
	}

	now := s.options.Now()
	s.mu.Lock()
	if s.policy != nil && now.Sub(s.cachedAt) < geminiQuotaCacheTTL {
		policy := s.policy.Clone()
		s.mu.Unlock()
		return policy
	}
	s.mu.Unlock()

	policy := NewGeminiQuotaPolicy()
	{
		policy.ApplyOverrides(s.options.StaticTiers)
		if strings.TrimSpace(s.options.StaticPolicy) != "" {
			raw := []byte(s.options.StaticPolicy)
			var overridesV2 geminiQuotaOverridesV2
			if err := json.Unmarshal(raw, &overridesV2); err == nil && len(overridesV2.QuotaRules) > 0 {
				policy.ApplyQuotaRulesOverrides(overridesV2.QuotaRules)
			} else {
				var overridesV1 geminiQuotaOverridesV1
				if err := json.Unmarshal(raw, &overridesV1); err != nil {
					s.options.Log("gemini quota: parse config policy failed: %v", err)
				} else {
					policy.ApplyOverrides(overridesV1.Tiers)
				}
			}
		}
	}

	if s.options.LoadPolicy != nil {
		value, err := s.options.LoadPolicy(ctx)
		if err != nil && !errors.Is(err, s.options.NotFound) {
			s.options.Log("gemini quota: load setting failed: %v", err)
		} else if strings.TrimSpace(value) != "" {
			raw := []byte(value)
			var overridesV2 geminiQuotaOverridesV2
			if err := json.Unmarshal(raw, &overridesV2); err == nil && len(overridesV2.QuotaRules) > 0 {
				policy.ApplyQuotaRulesOverrides(overridesV2.QuotaRules)
			} else {
				var overridesV1 geminiQuotaOverridesV1
				if err := json.Unmarshal(raw, &overridesV1); err != nil {
					s.options.Log("gemini quota: parse setting failed: %v", err)
				} else {
					policy.ApplyOverrides(overridesV1.Tiers)
				}
			}
		}
	}

	s.mu.Lock()
	s.policy = policy
	s.cachedAt = now
	s.mu.Unlock()

	return policy.Clone()
}

func (s *GeminiQuotaService) QuotaForProvider(ctx context.Context, provider *Record) (GeminiQuota, bool) {
	if provider == nil || provider.Platform != PlatformGemini {
		return GeminiQuota{}, false
	}

	// Normalize OAuth type and legacy tier aliases to the keys used by the policy table.
	tierKey := GeminiQuotaTierKeyForProvider(provider)
	if tierKey == "" {
		return GeminiQuota{}, false
	}

	policy := s.Policy(ctx)
	return policy.QuotaForTier(tierKey)
}

func (s *GeminiQuotaService) CooldownForTier(ctx context.Context, tierID string) time.Duration {
	policy := s.Policy(ctx)
	return policy.CooldownForTier(tierID)
}

func (s *GeminiQuotaService) CooldownForProvider(ctx context.Context, provider *Record) time.Duration {
	if s == nil || provider == nil || provider.Platform != PlatformGemini {
		return 5 * time.Minute
	}
	tierKey := GeminiQuotaTierKeyForProvider(provider)
	if strings.TrimSpace(tierKey) == "" {
		return 5 * time.Minute
	}
	return s.CooldownForTier(ctx, tierKey)
}

func NewGeminiQuotaPolicy() *GeminiQuotaPolicy {
	return &GeminiQuotaPolicy{
		tiers: map[string]GeminiTierPolicy{
			// AI Studio and API Key defaults use separate quotas for Pro and Flash.
			GeminiTierAIStudioFree: {Quota: GeminiQuota{ProRPD: 50, ProRPM: 2, FlashRPD: 1500, FlashRPM: 15}, Cooldown: 30 * time.Minute},
			// A daily limit of -1 represents unlimited or usage-based billing.
			GeminiTierAIStudioPaid: {Quota: GeminiQuota{ProRPD: -1, ProRPM: 1000, FlashRPD: -1, FlashRPM: 2000}, Cooldown: 5 * time.Minute},

			// Google One shares quota across model classes.
			GeminiTierGoogleOneFree: {Quota: GeminiQuota{SharedRPD: 1000, SharedRPM: 60}, Cooldown: 30 * time.Minute},
			GeminiTierGoogleAIPro:   {Quota: GeminiQuota{SharedRPD: 1500, SharedRPM: 120}, Cooldown: 5 * time.Minute},
			GeminiTierGoogleAIUltra: {Quota: GeminiQuota{SharedRPD: 2000, SharedRPM: 120}, Cooldown: 5 * time.Minute},

			// Code Assist shares quota across model classes.
			GeminiTierGCPStandard:   {Quota: GeminiQuota{SharedRPD: 1500, SharedRPM: 120}, Cooldown: 5 * time.Minute},
			GeminiTierGCPEnterprise: {Quota: GeminiQuota{SharedRPD: 2000, SharedRPM: 120}, Cooldown: 5 * time.Minute},
		},
	}
}

func (p *GeminiQuotaPolicy) ApplyOverrides(tiers map[string]GeminiTierQuotaOverride) {
	if p == nil || len(tiers) == 0 {
		return
	}
	for rawID, override := range tiers {
		tierID := NormalizeGeminiTierID(rawID)
		if tierID == "" {
			continue
		}
		policy, ok := p.tiers[tierID]
		if !ok {
			policy = GeminiTierPolicy{Cooldown: 5 * time.Minute}
		}
		// Legacy pro_rpd overrides apply to shared_rpd when the tier has a positive shared limit.
		// Otherwise, Pro and Flash limits are overridden separately.
		if override.ProRPD != nil {
			if policy.Quota.SharedRPD > 0 {
				policy.Quota.SharedRPD = clampGeminiQuotaInt64WithUnlimited(*override.ProRPD)
			} else {
				policy.Quota.ProRPD = clampGeminiQuotaInt64WithUnlimited(*override.ProRPD)
			}
		}
		if override.FlashRPD != nil {
			if policy.Quota.SharedRPD > 0 {
				// A positive shared daily limit takes precedence over the Flash override.
			} else {
				policy.Quota.FlashRPD = clampGeminiQuotaInt64WithUnlimited(*override.FlashRPD)
			}
		}
		if override.CooldownMinutes != nil {
			minutes := clampGeminiQuotaInt(*override.CooldownMinutes)
			policy.Cooldown = time.Duration(minutes) * time.Minute
		}
		p.tiers[tierID] = policy
	}
}

func (p *GeminiQuotaPolicy) ApplyQuotaRulesOverrides(rules map[string]GeminiQuotaRuleOverride) {
	if p == nil || len(rules) == 0 {
		return
	}
	for rawID, override := range rules {
		tierID := NormalizeGeminiTierID(rawID)
		if tierID == "" {
			continue
		}
		policy, ok := p.tiers[tierID]
		if !ok {
			policy = GeminiTierPolicy{Cooldown: 5 * time.Minute}
		}

		if override.SharedRPD != nil {
			policy.Quota.SharedRPD = clampGeminiQuotaInt64WithUnlimited(*override.SharedRPD)
		}
		if override.SharedRPM != nil {
			policy.Quota.SharedRPM = clampGeminiQuotaRPM(*override.SharedRPM)
		}
		if override.GeminiPro != nil {
			if override.GeminiPro.RPD != nil {
				policy.Quota.ProRPD = clampGeminiQuotaInt64WithUnlimited(*override.GeminiPro.RPD)
			}
			if override.GeminiPro.RPM != nil {
				policy.Quota.ProRPM = clampGeminiQuotaRPM(*override.GeminiPro.RPM)
			}
		}
		if override.GeminiFlash != nil {
			if override.GeminiFlash.RPD != nil {
				policy.Quota.FlashRPD = clampGeminiQuotaInt64WithUnlimited(*override.GeminiFlash.RPD)
			}
			if override.GeminiFlash.RPM != nil {
				policy.Quota.FlashRPM = clampGeminiQuotaRPM(*override.GeminiFlash.RPM)
			}
		}

		p.tiers[tierID] = policy
	}
}

func (p *GeminiQuotaPolicy) QuotaForTier(tierID string) (GeminiQuota, bool) {
	policy, ok := p.policyForTier(tierID)
	if !ok {
		return GeminiQuota{}, false
	}
	return policy.Quota, true
}

func (p *GeminiQuotaPolicy) CooldownForTier(tierID string) time.Duration {
	policy, ok := p.policyForTier(tierID)
	if ok && policy.Cooldown > 0 {
		return policy.Cooldown
	}
	return 5 * time.Minute
}

func (p *GeminiQuotaPolicy) policyForTier(tierID string) (GeminiTierPolicy, bool) {
	if p == nil {
		return GeminiTierPolicy{}, false
	}
	normalized := NormalizeGeminiTierID(tierID)
	if policy, ok := p.tiers[normalized]; ok {
		return policy, true
	}
	return GeminiTierPolicy{}, false
}

func NormalizeGeminiTierID(tierID string) string {
	tierID = strings.TrimSpace(tierID)
	if tierID == "" {
		return ""
	}
	// Resolve canonical IDs and legacy aliases first.
	if canonical := CanonicalGeminiTierID(tierID); canonical != "" {
		return canonical
	}
	// Accept uppercase policy keys as well.
	switch strings.ToUpper(tierID) {
	case "AISTUDIO_FREE":
		return GeminiTierAIStudioFree
	case "AISTUDIO_PAID":
		return GeminiTierAIStudioPaid
	case "GOOGLE_ONE_FREE":
		return GeminiTierGoogleOneFree
	case "GOOGLE_AI_PRO":
		return GeminiTierGoogleAIPro
	case "GOOGLE_AI_ULTRA":
		return GeminiTierGoogleAIUltra
	case "GCP_STANDARD":
		return GeminiTierGCPStandard
	case "GCP_ENTERPRISE":
		return GeminiTierGCPEnterprise
	}
	return strings.ToLower(tierID)
}

func clampGeminiQuotaInt64WithUnlimited(value int64) int64 {
	if value < -1 {
		return 0
	}
	return value
}

func clampGeminiQuotaInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func clampGeminiQuotaRPM(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func GeminiCooldownForTier(tierID string) time.Duration {
	policy := NewGeminiQuotaPolicy()
	return policy.CooldownForTier(tierID)
}

func GeminiQuotaTierKeyForProvider(provider *Record) string {
	if provider == nil || provider.Platform != PlatformGemini {
		return ""
	}
	// Third-party providers have no Google tier, so local Google quota estimates do not apply.
	if provider.IsGeminiThirdPartyProvider() {
		return ""
	}

	// Legacy records with a project_id default to code_assist in GeminiOAuthType.
	oauthType := strings.ToLower(strings.TrimSpace(provider.GeminiOAuthType()))
	rawTier := strings.TrimSpace(provider.GeminiTierID())

	// Prefer a recognized tier compatible with the provider's OAuth type.
	if tierID := CanonicalGeminiTierIDForOAuthType(oauthType, rawTier); tierID != "" && tierID != GeminiTierGoogleOneUnknown {
		return tierID
	}

	// Use the OAuth type's default when its tier is missing or unknown.
	switch oauthType {
	case "google_one":
		return GeminiTierGoogleOneFree
	case "code_assist":
		return GeminiTierGCPStandard
	case "ai_studio":
		return GeminiTierAIStudioFree
	default:
		// API keys with no OAuth type use the AI Studio policy.
		return GeminiTierAIStudioFree
	}
}

// Clone returns a policy with an independent tier map.
func (p *GeminiQuotaPolicy) Clone() *GeminiQuotaPolicy {
	if p == nil {
		return nil
	}
	out := *p
	out.tiers = maps.Clone(p.tiers)
	return &out
}
