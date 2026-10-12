package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

type ManagedReauthorizationInput struct {
	Type               string
	Credentials, Extra map[string]any
}

// Reauthorize saves credentials, merges extra, then attempts error and token-cache cleanup. Extra merge
// failures are logged.
func (s *ManagedRefreshService) Reauthorize(ctx context.Context, existing *Record, req ManagedReauthorizationInput) (*Record, error) {
	if existing == nil {
		return nil, ErrProviderNotFound
	}
	providerID := existing.ID
	DiscardDeprecatedProviderExtra(req.Extra)
	if !existing.IsOAuth() {
		return nil, apperror.BadRequest("NOT_OAUTH", "cannot apply oauth credentials to non-OAuth provider")
	}
	if err := ValidateUpstreamRequestIDHeaderExtra(req.Extra); err != nil {
		return nil, err
	}

	// Sanitize newly authorized credentials before persisting them.
	req.Credentials = SanitizeStoredCredentials(existing.Platform, req.Credentials)
	updatedProvider, err := s.options.Store.UpdateProvider(ctx, providerID, &UpdateProviderInput{
		Type:        req.Type,
		Credentials: req.Credentials,
	})
	if err != nil {
		return nil, err
	}

	// Merge extra fields separately; a failed merge leaves the saved tokens in place.
	if len(req.Extra) > 0 {
		if extraErr := s.options.Store.UpdateProviderExtra(ctx, providerID, req.Extra); extraErr != nil {
			extraKeys := make([]string, 0, len(req.Extra))
			for k := range req.Extra {
				extraKeys = append(extraKeys, k)
			}
			s.options.Error("apply_oauth_credentials.update_extra_failed",
				"provider_id", providerID,
				"extra_keys", extraKeys,
				"err", extraErr,
			)
		}
	}

	// Successful reauthorization clears Grok spending-limit reauthorization markers.
	if existing.Platform == PlatformGrok {
		if clearErr := s.options.Store.UpdateProviderExtra(ctx, providerID, map[string]any{
			"grok_needs_reauth":        false,
			"grok_needs_reauth_reason": "",
			"grok_needs_reauth_at":     "",
		}); clearErr != nil {
			s.options.Warn("apply_oauth_credentials.clear_grok_reauth_failed",
				"provider_id", providerID,
				"err", clearErr,
			)
		}
	}

	if cleared, clearErr := s.options.Store.ClearProviderError(ctx, providerID); clearErr != nil {
		s.options.Warn("apply_oauth_credentials.clear_error_failed",
			"provider_id", providerID,
			"err", clearErr,
		)
	} else if cleared != nil {
		updatedProvider = cleared
	}

	if s.options.Invalidate != nil && updatedProvider != nil && updatedProvider.IsOAuth() {
		if invalidateErr := s.options.Invalidate(ctx, updatedProvider); invalidateErr != nil {
			s.options.Warn("apply_oauth_credentials.invalidate_token_failed",
				"provider_id", providerID,
				"err", invalidateErr,
			)
		}
	}

	return updatedProvider, nil
}
