package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Manual renewal receives a provider snapshot and produces merged credentials
// for the management coordinator to persist. Platform token builders return
// different metadata shapes, so the merge retains existing settings and
// optional token fields according to the owning authorization service.
//
// OpenAI, Gemini, and Antigravity add old keys omitted by their builders; Grok
// uses its credential merge and retains a trimmed base URL. Claude starts with
// existing entries, writes decimal expiry strings, and replaces optional
// refresh_token or scope only when the result is nonblank. Antigravity can
// return refreshed credentials with incomplete project discovery as a separate
// successful signal. Locking, identity checks, persistence, and follow-up
// privacy actions belong to the caller.

// ManualCredentialExchange combines authorization services for management
// credential renewal.
type ManualCredentialExchange struct {
	// Claude, OpenAI, Gemini, and Antigravity supply their platform exchanges.
	// Refresh dereferences the dispatched service, so callers bind each service
	// for the provider kinds that this instance will receive.
	Claude      *ClaudeAuthorization      // Claude credential authorization service
	OpenAI      *OpenAIAuthorization      // OpenAI credential authorization service
	Gemini      *GeminiAuthorization      // Gemini credential authorization service
	Antigravity *AntigravityAuthorization // Antigravity credential authorization service
	// Grok supplies Build OAuth renewal. A missing service produces a configured
	// service error before any Grok credential exchange can start.
	Grok GrokRefreshTokenService
	// Qoder returns COSY credentials when the provider matches that dispatch
	// branch. Callers configure it before passing Qoder COSY records to Refresh.
	Qoder func(context.Context, *Record) (map[string]any, error)
}

// Refresh exchanges one nonnil provider's credentials and returns the merged
// object plus an Antigravity missing-project signal. Callers configure the
// dispatched authorization service and own provider locking, identity
// comparison, and eventual persistence.
//
// Qoder COSY uses its supplied function directly. OpenAI, Gemini, and
// Antigravity retain existing keys omitted by token builders, with OpenAI PAT
// normalization applied afterward. Grok requires its service and retains the
// provider's trimmed base URL. The remaining branch uses Claude expiry strings
// and keeps an old refresh token or scope when the exchange supplies a blank
// optional value.
//
// The missing-project flag returns refreshed Antigravity credentials with nil
// error so management can handle discovery separately. Nested retained values
// may still reference the input record; callers copy or otherwise retain
// ownership until the write path finishes.
func (h *ManualCredentialExchange) Refresh(ctx context.Context, provider *Record) (map[string]any, bool, error) {
	var newCredentials map[string]any
	if provider.IsQoderCosy() {
		credentials, err := h.Qoder(ctx, provider)
		if err != nil {
			return nil, false, err
		}
		newCredentials = credentials
	} else if provider.IsOpenAI() {
		tokenInfo, err := h.OpenAI.RefreshProviderToken(ctx, provider)
		if err != nil {
			return nil, false, err
		}

		newCredentials = BuildOpenAIProviderCredentials(tokenInfo)
		// Token builders can omit retained provider settings or identity hints.
		// Keep those existing keys while giving every supplied exchange value
		// priority over the corresponding saved credential.
		for k, v := range provider.Credentials {
			if _, exists := newCredentials[k]; !exists {
				newCredentials[k] = v
			}
		}
		newCredentials = NormalizeOpenAIPersonalAccessTokenCredentials(provider, tokenInfo, newCredentials)
	} else if provider.Platform == capability.PlatformGemini {
		tokenInfo, err := h.Gemini.RefreshProviderToken(ctx, provider)
		if err != nil {
			return nil, false, fmt.Errorf("failed to refresh credentials: %w", err)
		}

		newCredentials = h.Gemini.BuildProviderCredentials(tokenInfo)
		// Gemini's exchange supplies token metadata while stored credentials also
		// contain channel and request settings. Inherit omitted keys so renewal
		// keeps those settings, with exchange values taking precedence.
		for k, v := range provider.Credentials {
			if _, exists := newCredentials[k]; !exists {
				newCredentials[k] = v
			}
		}
	} else if provider.Platform == capability.PlatformAntigravity {
		tokenInfo, err := h.Antigravity.RefreshProviderToken(ctx, provider)
		if err != nil {
			return nil, false, err
		}

		newCredentials = h.Antigravity.BuildProviderCredentials(tokenInfo)
		// Project discovery may provide only part of the credential metadata.
		// Retain stored keys the builder omits so a successful renewal can still
		// carry the provider's existing identity hints and request settings.
		for k, v := range provider.Credentials {
			if _, exists := newCredentials[k]; !exists {
				newCredentials[k] = v
			}
		}

		// Discovery can be incomplete after a successful token exchange. Return
		// those renewed credentials and the separate signal so management can
		// save the token while handling the missing project state.
		if tokenInfo.ProjectIDMissing {
			return newCredentials, true, nil
		}
	} else if provider.Platform == capability.PlatformGrok {
		if h.Grok == nil {
			return nil, false, errors.New("grok OAuth service is not configured")
		}
		tokenInfo, err := h.Grok.RefreshProviderToken(ctx, provider)
		if err != nil {
			return nil, false, fmt.Errorf("failed to refresh Grok credentials: %w", err)
		}

		newCredentials = MergeCredentials(provider.Credentials, h.Grok.BuildProviderCredentials(tokenInfo))
		// Grok's builder writes its default CLI endpoint. Restore a nonblank saved
		// address after merging so token renewal keeps the administrator's selected
		// endpoint rather than replacing it with the builder default.
		if baseURL := strings.TrimSpace(provider.GetCredential("base_url")); baseURL != "" {
			newCredentials["base_url"] = baseURL
		}
	} else {
		// The remaining dispatch uses Claude's stored credential representation:
		// expiry values are decimal strings, and blank optional renewal fields
		// leave the provider's existing refresh token or scope in place.
		tokenInfo, err := h.Claude.RefreshProviderToken(ctx, provider)
		if err != nil {
			return nil, false, err
		}

		// Begin with existing settings because Claude updates token fields in
		// place. This retains controls such as intercept_warmup_requests that
		// its exchange response does not describe.
		newCredentials = make(map[string]any)
		maps.Copy(newCredentials, provider.Credentials)

		newCredentials["access_token"] = tokenInfo.AccessToken
		newCredentials["token_type"] = tokenInfo.TokenType
		newCredentials["expires_in"] = strconv.FormatInt(tokenInfo.ExpiresIn, 10)
		newCredentials["expires_at"] = strconv.FormatInt(tokenInfo.ExpiresAt, 10)
		if strings.TrimSpace(tokenInfo.RefreshToken) != "" {
			newCredentials["refresh_token"] = tokenInfo.RefreshToken
		}
		if strings.TrimSpace(tokenInfo.Scope) != "" {
			newCredentials["scope"] = tokenInfo.Scope
		}
	}

	return newCredentials, false, nil
}
