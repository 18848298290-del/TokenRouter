package provider

import (
	"context"
	"maps"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// CRS imports can save a provider before its OAuth token has been refreshed.
// Post-import exchange therefore returns replacement credentials separately
// from import success, and an exchange failure leaves the saved provider
// available for later maintenance.
//
// Platform builders replace token fields while retaining omitted stored
// metadata, with inherited nested values remaining shared until
// supported-container copying. The coordinated callback uses platform lock keys
// and RefreshImported to reread and compare persisted identity before saving.
// Missing platform clients skip exchange. That persistent owner observes
// cancellation and competing administrator changes, while this selector keeps
// each platform's map construction rules.

// CRSAuthorization selects post-import OAuth exchanges for Anthropic, OpenAI,
// and Gemini records.
type CRSAuthorization struct {
	// Claude, OpenAI, and Gemini are optional platform clients for callback
	// selection and exchange. A missing client makes its platform a
	// successful
	// skip before exchange is attempted.
	Claude *ClaudeAuthorization // Anthropic post-import token exchange
	OpenAI *OpenAIAuthorization // OpenAI post-import token exchange
	Gemini *GeminiAuthorization // Gemini post-import token exchange
}

// Coordinated returns a refresh callback using each supported platform's normal
// lock key and RefreshImported persistence. Callers supply a nonnil record and
// coordinator when the platform client exists; Gemini also requires its key
// callback. Missing clients or unsupported platforms skip successfully.
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

// Refresh exchanges an imported OAuth record and returns replacement
// credentials or nil when skipped or failed. Callers supply a nonnil record; it
// does not persist the returned map. Coordinated delegates persistence and
// identity comparison to RefreshImported after the import has already been
// accepted.
//
// Anthropic copies existing top-level values before replacing returned token
// fields, retaining absent refresh token or scope. OpenAI and Gemini build
// replacement fields first and inherit stored keys the response omitted.
// Inherited nested values stay shared; the coordinator copies supported
// containers before writing. Exchange failure becomes nil so import success
// remains independent of refresh success.
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
			// Begin from stored values so omitted refresh token,
			// scope, and
			// provider metadata remain available when this exchange
			// supplies
			// replacement token fields.
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
			// The builder owns returned token fields; inherit
			// absent keys so
			// saved platform metadata remains available without
			// overriding the
			// exchange response.
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
			// Gemini metadata can replace token fields in its
			// builder; retain that
			// completed map precedence while inheriting only stored
			// keys it omitted.
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
		// Refresh is post-import maintenance; nil skips credential
		// persistence
		// so a failed exchange cannot change the accepted import item
		// outcome.
		return nil
	}

	return newCredentials
}
