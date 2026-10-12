package provider

import (
	"context"
	"maps"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// CRSAuthorization completes imported credentials using the provider's platform.
type CRSAuthorization struct {
	Claude *ClaudeAuthorization
	OpenAI *OpenAIAuthorization
	Gemini *GeminiAuthorization
}

// Coordinated refreshes imports using platform lock keys and conditional writes.
func (s *CRSAuthorization) Coordinated(coordinator *OAuthRefreshAPI, geminiKey func(*Record) string) func(context.Context, *Record) error {
	return func(ctx context.Context, record *Record) error {
		var key string
		switch record.Platform {
		case capability.PlatformAnthropic:
			if s.Claude == nil {
				return nil
			}
			key = ClaudeTokenCacheKey(record)
		case capability.PlatformOpenAI:
			if s.OpenAI == nil {
				return nil
			}
			key = OpenAITokenCacheKey(record)
		case capability.PlatformGemini:
			if s.Gemini == nil {
				return nil
			}
			key = geminiKey(record)
		default:
			return nil
		}
		return coordinator.RefreshImported(ctx, record, key, s.Refresh)
	}
}

// Refresh attempts a post-import token exchange; failure leaves the saved import intact.
func (s *CRSAuthorization) Refresh(ctx context.Context, provider *Record) map[string]any {
	if provider.Type != capability.ProviderTypeOAuth {
		return nil
	}

	var newCredentials map[string]any
	var err error

	switch provider.Platform {
	case capability.PlatformAnthropic:
		if s.Claude == nil {
			return nil
		}
		tokenInfo, refreshErr := s.Claude.RefreshProviderToken(ctx, provider)
		if refreshErr != nil {
			err = refreshErr
		} else {
			// Start with the existing non-token configuration.
			newCredentials = make(map[string]any)
			maps.Copy(newCredentials, provider.Credentials)
			// Apply token fields returned by this exchange.
			newCredentials["access_token"] = tokenInfo.AccessToken
			newCredentials["token_type"] = tokenInfo.TokenType
			newCredentials["expires_in"] = tokenInfo.ExpiresIn
			newCredentials["expires_at"] = tokenInfo.ExpiresAt
			if tokenInfo.RefreshToken != "" {
				newCredentials["refresh_token"] = tokenInfo.RefreshToken
			}
			if tokenInfo.Scope != "" {
				newCredentials["scope"] = tokenInfo.Scope
			}
		}
	case capability.PlatformOpenAI:
		if s.OpenAI == nil {
			return nil
		}
		tokenInfo, refreshErr := s.OpenAI.RefreshProviderToken(ctx, provider)
		if refreshErr != nil {
			err = refreshErr
		} else {
			newCredentials = BuildOpenAIProviderCredentials(tokenInfo)
			// Retain stored fields omitted by the response.
			for k, v := range provider.Credentials {
				if _, exists := newCredentials[k]; !exists {
					newCredentials[k] = v
				}
			}
		}
	case capability.PlatformGemini:
		if s.Gemini == nil {
			return nil
		}
		tokenInfo, refreshErr := s.Gemini.RefreshProviderToken(ctx, provider)
		if refreshErr != nil {
			err = refreshErr
		} else {
			newCredentials = s.Gemini.BuildProviderCredentials(tokenInfo)
			for k, v := range provider.Credentials {
				if _, exists := newCredentials[k]; !exists {
					newCredentials[k] = v
				}
			}
		}
	default:
		return nil
	}

	if err != nil {
		// Exchange failure leaves the completed import intact.
		return nil
	}

	return newCredentials
}
