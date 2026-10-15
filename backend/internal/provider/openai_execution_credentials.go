package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// openai_execution_credentials.go resolves authentication values and modes for
// OpenAI-compatible provider execution.
// OpenAI-compatible execution can use a rotating OAuth token, a stored setup
// token, an API key or an Agent Identity signature. A Spark shadow holds model
// configuration while its parent owns authentication, so credential resolution
// first substitutes the validated parent record before choosing a source or
// authentication kind.
//
// Configured OAuth sources provide their successful results directly and own
// cache or refresh behavior; stored credentials supply the path when a source
// is absent. Agent Identity returns an empty token with its distinct kind so
// the execution adapter can build signed headers. Setup tokens require OpenAI
// OAuth-like eligibility and bypass refresh sources. Grok separately chooses
// its own token reader and API-key field, keeping platform authentication
// selection with the resolved credential record.

// OpenAIExecutionCredentials binds parent resolution and OAuth token sources
// for compatible request authentication.
type OpenAIExecutionCredentials struct {
	// Parent is required for shadow records and reads their credential owner.
	// Ordinary records bypass this callback and retain their supplied identity.
	Parent func(context.Context, int64) (*Record, error)
	// OpenAI is optional and owns cache or refresh behavior for ordinary OAuth
	// dispatch. A successful callback result is returned without further validation.
	OpenAI func(context.Context, *Record) (string, error)
	// Grok is optional and replaces stored-token access for Grok OAuth records.
	// Its successful result is returned directly with the oauth authentication kind.
	Grok func(context.Context, *Record) (string, error)
}

// Resolve returns an execution token and authentication kind after
// substituting a shadow's credential parent. Supply a nonnil record; shadow
// resolution requires a nonnil receiver and Parent reader, and rejects a
// missing, shadow or non-OpenAI-OAuth parent.
//
// OAuth dispatch recognizes Agent Identity first, returning an empty token and
// agentIdentity for later signing. Grok uses Grok when configured, while other
// OAuth records use OpenAI; successful callback results are returned as
// supplied. Absent sources use stored access tokens. OpenAI setup tokens also
// use stored access tokens and return oauth, while other setup-token platforms
// are rejected. API keys are trimmed and return apikey; required missing
// stored values and unsupported types return errors.
func (s *OpenAIExecutionCredentials) Resolve(ctx context.Context, provider *Record) (string, string, error) {
	// Shadow IDs identify model configuration, while the parent owns tokens;
	// use the parent's record for every subsequent authentication-kind and source decision.
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
		// The configured OAuth source owns refreshing and cache admission; propagate
		// its result so request authentication uses that source's chosen credential observation.
		if s != nil && s.OpenAI != nil {
			accessToken, err := s.OpenAI(ctx, provider)
			if err != nil {
				return "", "", err
			}
			return accessToken, "oauth", nil
		}
		// A missing OAuth source permits the stored-token path used by lightweight
		// consumers and fixtures; the record accessor selects the platform's credential field.
		accessToken := provider.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case capability.ProviderTypeSetupToken:
		if !provider.IsOpenAIOAuthLike() {
			return "", "", fmt.Errorf("unsupported provider type: %s", provider.Type)
		}
		// OpenAI setup tokens use the oauth forwarding kind but cannot be exchanged
		// by the token source, so read their stored access token directly.
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
