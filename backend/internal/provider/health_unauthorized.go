package provider

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// UnauthorizedObservation carries authentication failure signals classified by an upstream adapter.
type UnauthorizedObservation struct {
	Message                 string
	TokenRevoked            bool
	PermanentlyUnauthorized bool
}

// ApplyUnauthorized resolves the credential owner, then invalidates tokens and applies refresh cooldowns or
// account errors.
func (s *HealthService) ApplyUnauthorized(ctx context.Context, provider *Record, observation UnauthorizedObservation) bool {
	// Spark shadows share parent credentials, so 401 handling targets the parent when it can be loaded.
	// This avoids treating a shadow's missing refresh token as an unrecoverable account failure.
	// If parent resolution fails, use the current record.
	authProvider := provider
	if resolved, rerr := ResolveCredentialRecord(ctx, func(ctx context.Context, id int64) (*Record, error) { return s.providerRepo.GetByID(ctx, id) }, provider); rerr == nil && resolved != nil {
		authProvider = resolved
	}
	// An explicitly revoked OpenAI token requires account recovery rather than an expiry refresh.
	if authProvider.Platform == capability.PlatformOpenAI && observation.TokenRevoked {
		msg := "Token revoked (401): provider authentication permanently revoked"
		if observation.Message != "" {
			msg = "Token revoked (401): " + observation.Message
		}
		s.ApplyAuthenticationFailure(ctx, authProvider, msg)
		return true
	}
	// The adapter classifies permanently unauthorized OpenAI responses as account errors.
	if authProvider.Platform == capability.PlatformOpenAI && observation.PermanentlyUnauthorized {
		msg := "Unauthorized (401): provider authentication failed permanently"
		if observation.Message != "" {
			msg = "Unauthorized (401): " + observation.Message
		}
		s.ApplyAuthenticationFailure(ctx, authProvider, msg)
		return true
	}
	// OAuth providers with refresh credentials receive a temporary pause so refresh can recover them.
	if authProvider.Type == capability.ProviderTypeOAuth {
		if s.options.InvalidateUnauthorizedToken != nil {
			if err := s.options.InvalidateUnauthorizedToken(ctx, authProvider); err != nil {
				s.options.Warn("oauth_401_invalidate_cache_failed", "provider_id", authProvider.ID, "error", err)
			}
		}
		// Without a refresh token, the OAuth provider cannot recover during a cooldown.
		// Mark it as errored so scheduling excludes it until credentials are repaired.
		if strings.TrimSpace(authProvider.GetCredential("refresh_token")) == "" {
			msg := "Authentication failed (401): refresh_token missing, cannot recover"
			if observation.Message != "" {
				msg = "OAuth 401 (no refresh_token): " + observation.Message
			}
			s.ApplyAuthenticationFailure(ctx, authProvider, msg)
			return true
		}
		// Keep the provider active during its pause so refresh workers can claim it.
		// The refresh coordinator updates credentials under its distributed lock; writing the request's
		// stale credential map here could overwrite a refresh token rotated by another worker.
		msg := "Authentication failed (401): invalid or expired credentials"
		if observation.Message != "" {
			msg = "OAuth 401: " + observation.Message
		}
		if authProvider.Platform == capability.PlatformAntigravity {
			extraUpdates := AntigravityForceTokenRefreshExtra("401_invalid")
			if err := s.options.SessionWindows.UpdateExtra(ctx, authProvider.ID, extraUpdates); err != nil {
				s.options.Warn("antigravity_401_force_refresh_mark_failed", "provider_id", authProvider.ID, "error", err)
			} else {
				if authProvider.Extra == nil {
					authProvider.Extra = make(map[string]any, len(extraUpdates))
				}
				maps.Copy(authProvider.Extra, extraUpdates)
				s.options.Info("antigravity_401_force_refresh_marked", "provider_id", authProvider.ID)
			}
		}
		cooldownMinutes := s.options.UnauthorizedCooldownMinutes
		if cooldownMinutes <= 0 {
			cooldownMinutes = 10
		}
		until := s.options.Now().Add(time.Duration(cooldownMinutes) * time.Minute)
		s.notifyProviderSchedulingBlocked(authProvider, until, "oauth_401")
		if err := s.providerRepo.SetTempUnschedulable(ctx, authProvider.ID, until, msg); err != nil {
			s.options.Warn("oauth_401_set_temp_unschedulable_failed", "provider_id", authProvider.ID, "error", err)
		}
		return true
	}
	// Other provider types are marked as errored.
	msg := "Authentication failed (401): invalid or expired credentials"
	if observation.Message != "" {
		msg = "Authentication failed (401): " + observation.Message
	}
	s.ApplyAuthenticationFailure(ctx, authProvider, msg)
	return true
}
