package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// message_credentials.go selects authentication credentials and downstream
// authentication labels for Messages forwarding.
// Messages forwarding uses provider type to choose both an authentication value
// and the label expected by downstream adapters. Bedrock returns the bedrock
// label with an empty credential so its forwarder can supply SigV4 signing or
// API Key authentication.
//
// Anthropic OAuth and service accounts use the injected Claude token source
// when their branches permit it. Setup Tokens use their stored access token,
// and Grok OAuth uses its stored-token validator. Other OAuth cases use the
// stored access_token, including Anthropic OAuth when no source is configured.
// These precedence rules keep each credential lifecycle with its owning
// provider implementation.

// MessageCredentialSource selects Messages credentials using the injected
// Claude/Vertex token source when applicable.
type MessageCredentialSource struct {
	// Claude supplies cached or refreshed Anthropic OAuth and Vertex credentials.
	// Resolve requires it for service accounts and uses stored OAuth credentials
	// when this source is absent from the ordinary Anthropic OAuth branch.
	Claude func(context.Context, *Record) (string, error)
}

// Resolve returns the provider credential and downstream authentication label
// for Messages execution. Supply a nonnil record; API Keys require a stored
// api_key, while Bedrock returns an empty credential with its bedrock label.
//
// Anthropic service accounts require the configured Claude source and return
// service_account authentication. OAuth and Setup Token handling delegates to
// oauth, which selects refreshed or stored credentials by platform and type.
// Unsupported types and non-Anthropic service accounts return errors before
// forwarding begins.
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
		// Bedrock selects signing or API-key authentication downstream. Return its
		// label with no token so the forwarder resolves that provider-specific
		// authentication using the complete record.
		return "", "bedrock", nil
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

// oauth selects the access token used by OAuth-like Messages requests. An
// Anthropic OAuth record uses the injected Claude source when configured; Setup
// Token records keep their stored token.
//
// Grok OAuth delegates to GrokStoredAccessToken for its missing-token error.
// Remaining cases use GetCredential directly, including Anthropic OAuth with an
// absent source. Source errors are returned to the caller, and a missing direct
// access_token fails instead of producing an empty successful authentication
// value.
func (s *MessageCredentialSource) oauth(ctx context.Context, provider *Record) (string, string, error) {
	// The injected source owns Anthropic OAuth refresh and cache access. Setup
	// Tokens bypass this branch because their stored inference token has a
	// separate lifecycle from refreshable OAuth credentials.
	if provider.Platform == capability.PlatformAnthropic && provider.Type == capability.ProviderTypeOAuth && s != nil && s.Claude != nil {
		accessToken, err := s.Claude(ctx, provider)
		if err != nil {
			return "", "", err
		}
		return accessToken, "oauth", nil
	}

	// Grok's stored-token helper supplies its platform-specific missing-token
	// failure. It leaves renewal with the Grok token lifecycle rather than
	// asking the Anthropic source to interpret this credential.
	if provider.Platform == capability.PlatformGrok && provider.Type == capability.ProviderTypeOAuth {
		accessToken, err := GrokStoredAccessToken(provider)
		if err != nil {
			return "", "", err
		}
		return accessToken, "oauth", nil
	}

	// Stored-token resolution keeps Setup Tokens usable with an absent source
	// and also supports the compatibility Anthropic OAuth path when no Claude
	// source was bound by the caller.
	accessToken := provider.GetCredential("access_token")
	if accessToken == "" {
		return "", "", errors.New("access_token not found in credentials")
	}
	return accessToken, "oauth", nil
}
