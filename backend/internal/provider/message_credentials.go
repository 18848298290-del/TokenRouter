package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// MessageCredentialSource selects Messages credentials using the injected Claude/Vertex token source when applicable.
type MessageCredentialSource struct {
	Claude func(context.Context, *Record) (string, error)
}

// Resolve returns credentials and their authentication type; Bedrock returns an empty credential for downstream
// signing.
// @project-doc docs/interfaces/anthropic_upstream.md#anthropic_account_and_transport
func (s *MessageCredentialSource) Resolve(ctx context.Context, provider *Record) (string, string, error) {
	switch provider.Type {
	case capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken:
		return s.oauth(ctx, provider)
	case capability.ProviderTypeAPIKey:
		apiKey := provider.GetCredential("api_key")
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	case capability.ProviderTypeBedrock:
		return "", "bedrock", nil // The Bedrock forwarder supplies SigV4 signing or API-key authentication.
	case capability.ProviderTypeServiceAccount:
		if provider.Platform != capability.PlatformAnthropic {
			return "", "", fmt.Errorf("unsupported service account platform: %s", provider.Platform)
		}
		if s == nil || s.Claude == nil {
			return "", "", errors.New("claude token provider not configured")
		}
		accessToken, err := s.Claude(ctx, provider)
		if err != nil {
			return "", "", err
		}
		return accessToken, "service_account", nil
	default:
		return "", "", fmt.Errorf("unsupported provider type: %s", provider.Type)
	}
}

func (s *MessageCredentialSource) oauth(ctx context.Context, provider *Record) (string, string, error) {
	// Anthropic OAuth uses the injected source for cached or refreshed tokens.
	if provider.Platform == capability.PlatformAnthropic && provider.Type == capability.ProviderTypeOAuth && s != nil && s.Claude != nil {
		accessToken, err := s.Claude(ctx, provider)
		if err != nil {
			return "", "", err
		}
		return accessToken, "oauth", nil
	}

	// Grok OAuth uses the stored access token maintained by background refresh.
	if provider.Platform == capability.PlatformGrok && provider.Type == capability.ProviderTypeOAuth {
		accessToken, err := GrokStoredAccessToken(provider)
		if err != nil {
			return "", "", err
		}
		return accessToken, "oauth", nil
	}

	// Setup tokens and remaining cases use the stored access_token directly.
	accessToken := provider.GetCredential("access_token")
	if accessToken == "" {
		return "", "", errors.New("access_token not found in credentials")
	}
	return accessToken, "oauth", nil
}
