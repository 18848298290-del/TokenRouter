package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAIExecutionCredentials resolves parent records and delegates to configured token sources.
type OpenAIExecutionCredentials struct {
	Parent func(context.Context, int64) (*Record, error)
	OpenAI func(context.Context, *Record) (string, error)
	Grok   func(context.Context, *Record) (string, error)
}

// Resolve returns the execution credential and authentication kind, resolving shadows through their parent.
func (s *OpenAIExecutionCredentials) Resolve(ctx context.Context, provider *Record) (string, string, error) {
	if provider.IsShadow() {
		credProvider, err := ResolveCredentialRecord(ctx, s.Parent, provider)
		if err != nil {
			return "", "", err
		}
		provider = credProvider
	}
	switch provider.Type {
	case capability.ProviderTypeOAuth:
		if provider.IsOpenAIAgentIdentity() {
			return "", OpenAIAuthModeAgentIdentity, nil
		}
		if provider.Platform == capability.PlatformGrok {
			if s != nil && s.Grok != nil {
				accessToken, err := s.Grok(ctx, provider)
				if err != nil {
					return "", "", err
				}
				return accessToken, "oauth", nil
			}
			accessToken := provider.GetGrokAccessToken()
			if accessToken == "" {
				return "", "", errors.New("access_token not found in credentials")
			}
			return accessToken, "oauth", nil
		}
		// Prefer the configured source for cached or refreshed OAuth tokens.
		if s != nil && s.OpenAI != nil {
			accessToken, err := s.OpenAI(ctx, provider)
			if err != nil {
				return "", "", err
			}
			return accessToken, "oauth", nil
		}
		// Use stored credentials when no token source is configured.
		accessToken := provider.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case capability.ProviderTypeSetupToken:
		if !provider.IsOpenAIOAuthLike() {
			return "", "", fmt.Errorf("unsupported provider type: %s", provider.Type)
		}
		// Setup tokens use OAuth forwarding but bypass token refresh.
		accessToken := provider.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case capability.ProviderTypeAPIKey:
		if provider.Platform == capability.PlatformGrok {
			apiKey := strings.TrimSpace(provider.GetCredential("api_key"))
			if apiKey == "" {
				return "", "", errors.New("api_key not found in credentials")
			}
			return apiKey, "apikey", nil
		}
		apiKey := strings.TrimSpace(provider.GetOpenAIProtocolAPIKey())
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	default:
		return "", "", fmt.Errorf("unsupported provider type: %s", provider.Type)
	}
}
