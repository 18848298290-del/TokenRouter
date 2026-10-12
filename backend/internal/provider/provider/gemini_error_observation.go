package provider

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// GeminiErrorObserver applies provider health updates using platform-specific reset times or
// third-party cooldowns.
type GeminiErrorObserver struct {
	Other          *UpstreamHealth
	Precheck       *provider.GeminiPrecheck
	SetRateLimited func(context.Context, int64, time.Time) error
	ResetTime      func([]byte) *int64
	DailyReset     func() *int64
}

func (s *GeminiErrorObserver) Observe(ctx context.Context, value *provider.Record, statusCode int, headers http.Header, body []byte, observation HealthObservation) *provider.Record {
	// A custom status-code policy can exclude this response from health updates.
	if !value.ShouldHandleErrorCode(statusCode) {
		return nil
	}
	if s.Other != nil && (statusCode == 401 || statusCode == 403 || statusCode == 529) {
		s.Other.ApplyUpstreamError(ctx, value, observation)
		return value
	}
	if statusCode != 429 {
		return nil
	}
	// Pool-mode 429s use request retries or provider switching unless a custom status policy is enabled.
	if value.IsPoolMode() && !value.IsCustomErrorCodesEnabled() {
		return nil
	}

	oauthType := value.GeminiOAuthType()
	tierID := value.GeminiTierID()
	projectID := strings.TrimSpace(value.GetCredential("project_id"))
	isCodeAssist := value.IsGeminiCodeAssist()

	if value.IsGeminiThirdPartyProvider() {
		// Third-party endpoints use a generic cooldown because Google daily-quota messages do not describe
		// their limits.
		cooldown := 5 * time.Minute
		if s.Precheck != nil {
			cooldown = s.Precheck.GeminiCooldown(ctx, value)
		}
		ra := time.Now().Add(cooldown)
		_ = s.SetRateLimited(ctx, value.ID, ra)
		logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Provider %d (third-party API Key) rate limited, cooldown=%v", value.ID, time.Until(ra).Truncate(time.Second))
		return nil
	}

	resetAt := s.ResetTime(body)
	if resetAt == nil {
		var ra time.Time
		if isCodeAssist || oauthType == "google_one" {
			// Code Assist and Google One use tier-based cooldowns when no reset time is available.
			cooldown := provider.GeminiCooldownForTier(tierID)
			if s.Precheck != nil {
				cooldown = s.Precheck.GeminiCooldown(ctx, value)
			}
			ra = time.Now().Add(cooldown)
			if isCodeAssist {
				logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Provider %d (Code Assist, tier=%s, project=%s) rate limited, cooldown=%v", value.ID, tierID, projectID, time.Until(ra).Truncate(time.Second))
			} else {
				logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Provider %d (Google One OAuth, tier=%s, project=%s) rate limited, cooldown=%v", value.ID, tierID, projectID, time.Until(ra).Truncate(time.Second))
			}
		} else {
			// API key and AI Studio OAuth providers use the configured daily reset.
			if ts := s.DailyReset(); ts != nil {
				ra = time.Unix(*ts, 0)
				logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Provider %d (API Key/AI Studio, type=%s) rate limited, reset at PST midnight (%v)", value.ID, value.Type, ra)
			} else {
				ra = time.Now().Add(5 * time.Minute)
				logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Provider %d rate limited, fallback to 5min", value.ID)
			}
		}
		_ = s.SetRateLimited(ctx, value.ID, ra)
		return nil
	}

	resetTime := time.Unix(*resetAt, 0)
	_ = s.SetRateLimited(ctx, value.ID, resetTime)
	logging.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Provider %d rate limited until %v (oauth_type=%s, tier=%s)",
		value.ID, resetTime, oauthType, tierID)
	return nil
}
