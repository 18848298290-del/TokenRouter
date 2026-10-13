package provider

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A 401 on a Spark shadow can belong to its credential parent, so account
// handling resolves that owner before choosing recovery. Failed or incompatible
// parent resolution retains the supplied record for this command. OpenAI
// revocation and permanent-unauthorized classifications select account error
// before OAuth token invalidation or refresh-pause handling.
//
// Other OAuth records first attempt optional token invalidation. Missing
// refresh credentials select account error; usable refresh credentials choose
// a temporary pause whose production write leaves status unchanged. Active
// records can therefore remain candidates for refresh maintenance.
// Antigravity's force-refresh Extra write precedes its pause independently and
// updates the request record only when that write succeeds. Runtime notification
// precedes temporary persistence, and logged invalidation/Extra/pause failures
// retain the selected handled result. These
// writes use provider IDs; identity-conditioned refresh publication belongs to
// its separate coordinator.

// UnauthorizedObservation holds adapter-classified authentication failure
// evidence for credential-owner health handling.
type UnauthorizedObservation struct {
	Message string // classified upstream diagnostic
	// TokenRevoked and PermanentlyUnauthorized select OpenAI account-error paths
	// before OAuth refresh handling. TokenRevoked wins when both flags are set;
	// other platforms continue their ordinary type-based handling.
	TokenRevoked            bool // revoked token classification
	PermanentlyUnauthorized bool // permanent denial classification
}

// ApplyUnauthorized handles a classified 401 through its resolved credential
// owner and returns true for the chosen health path. Supply a usable service,
// provider and stable mutable record data. Parent lookup failure or
// incompatibility retains the original record; OpenAI revoked/permanent
// classifications precede OAuth invalidation.
//
// An OAuth owner attempts optional cache invalidation, then selects account
// error when its refresh token is blank. Otherwise Antigravity first attempts
// its force-refresh Extra marker and mirrors a successful marker into the
// record. A temporary pause uses the configured minutes or ten for a
// nonpositive duration, with runtime notification before storage. Logged
// auxiliary failures leave the handled result true; the command does not roll
// earlier effects back if a later write fails.
func (s *HealthService) ApplyUnauthorized(ctx context.Context, provider *Record, observation UnauthorizedObservation) bool {
	// Resolve the one credential-parent link before examining refresh data. A
	// missing or incompatible parent returns an error; retaining the input keeps
	// this failure on a usable record instead of discarding health handling.
	authProvider := provider
	if resolved, rerr := ResolveCredentialRecord(ctx, func(ctx context.Context, id int64) (*Record, error) { return s.providerRepo.GetByID(ctx, id) }, provider); rerr == nil && resolved != nil {
		authProvider = resolved
	}
	if authProvider.Platform == capability.PlatformOpenAI && observation.TokenRevoked {
		msg := "Token revoked (401): provider authentication permanently revoked"
		if observation.Message != "" {
			msg = "Token revoked (401): " + observation.Message
		}
		s.ApplyAuthenticationFailure(ctx, authProvider, msg)
		return true
	}
	if authProvider.Platform == capability.PlatformOpenAI && observation.PermanentlyUnauthorized {
		msg := "Unauthorized (401): provider authentication failed permanently"
		if observation.Message != "" {
			msg = "Unauthorized (401): " + observation.Message
		}
		s.ApplyAuthenticationFailure(ctx, authProvider, msg)
		return true
	}
	if authProvider.Type == capability.ProviderTypeOAuth {
		// Token invalidation is attempted before refresh eligibility is checked. A
		// failed invalidation is logged, and the owner still proceeds to
		// account-error or temporary-pause handling.
		if s.options.InvalidateUnauthorizedToken != nil {
			if err := s.options.InvalidateUnauthorizedToken(ctx, authProvider); err != nil {
				s.options.Warn("oauth_401_invalidate_cache_failed", "provider_id", authProvider.ID, "error", err)
			}
		}
		// A blank refresh_token cannot enter this command's refresh-pause path.
		// Select account-error handling so the missing exchange input is recorded for
		// the provider's administrative recovery.
		if strings.TrimSpace(authProvider.GetCredential("refresh_token")) == "" {
			msg := "Authentication failed (401): refresh_token missing, cannot recover"
			if observation.Message != "" {
				msg = "OAuth 401 (no refresh_token): " + observation.Message
			}
			s.ApplyAuthenticationFailure(ctx, authProvider, msg)
			return true
		}
		// Pause storage leaves status and credentials unchanged, allowing an already
		// active owner to remain a refresh candidate. Writing the request's
		// credential map here would compete with token rotation performed by the
		// refresh coordinator.
		msg := "Authentication failed (401): invalid or expired credentials"
		if observation.Message != "" {
			msg = "OAuth 401: " + observation.Message
		}
		// Persist the Antigravity refresh marker separately before the pause. Mirror
		// only a successful marker into this record; a logged Extra error still
		// leaves the later pause attempt available.
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
		// Notify the blocker before the pause write, whose production path leaves
		// status untouched. Failed persistence leaves earlier invalidation and any
		// successful force-refresh marker intact.
		s.notifyProviderSchedulingBlocked(authProvider, until, "oauth_401")
		if err := s.providerRepo.SetTempUnschedulable(ctx, authProvider.ID, until, msg); err != nil {
			s.options.Warn("oauth_401_set_temp_unschedulable_failed", "provider_id", authProvider.ID, "error", err)
		}
		return true
	}
	msg := "Authentication failed (401): invalid or expired credentials"
	if observation.Message != "" {
		msg = "Authentication failed (401): " + observation.Message
	}
	s.ApplyAuthenticationFailure(ctx, authProvider, msg)
	return true
}
