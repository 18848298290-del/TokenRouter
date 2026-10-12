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

// ManualCredentialExchange refreshes and merges platform credentials; its caller owns locking and persistence.
type ManualCredentialExchange struct {
	Claude      *ClaudeAuthorization
	OpenAI      *OpenAIAuthorization
	Gemini      *GeminiAuthorization
	Antigravity *AntigravityAuthorization
	Grok        GrokRefreshTokenService
	Qoder       func(context.Context, *Record) (map[string]any, error)
}

// Refresh returns merged credentials and whether Antigravity project discovery is incomplete.
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
			// The caller may still attempt privacy settings with the existing token after this failure.
			return nil, false, err
		}

		newCredentials = BuildOpenAIProviderCredentials(tokenInfo)
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
		for k, v := range provider.Credentials {
			if _, exists := newCredentials[k]; !exists {
				newCredentials[k] = v
			}
		}

		// Return refreshed credentials with a missing-project signal for the caller to handle.
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
		if baseURL := strings.TrimSpace(provider.GetCredential("base_url")); baseURL != "" {
			newCredentials["base_url"] = baseURL
		}
	} else {
		// Claude stores expiry fields as decimal strings and ignores empty optional token fields.
		tokenInfo, err := h.Claude.RefreshProviderToken(ctx, provider)
		if err != nil {
			return nil, false, err
		}

		// Copy operational settings such as intercept_warmup_requests before replacing token fields.
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
