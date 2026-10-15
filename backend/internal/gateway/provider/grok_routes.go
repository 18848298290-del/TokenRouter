package provider

import (
	"context"

	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_routes.go builds validated Grok text, media, and voice endpoint URLs.
// Grok text endpoints combine saved provider URLs with optional dynamic
// defaults, while media and voice use the media endpoint rules. Endpoint
// construction selects an OAuth-aware validator before joining paths, so
// configured custom hosts still pass their operator URL policy.

// GrokRoutes binds endpoint validation and optional runtime defaults for Grok
// requests.
type GrokRoutes struct {
	// Validate checks custom OAuth and API Key endpoints. A nil callback selects
	// the platform validator; recognized official OAuth URLs use trusted validation.
	Validate grok.BaseURLValidator
	// DefaultMode is read with Background for text routes when runtimeDefault is
	// enabled. Media and voice routes use their separate provider endpoint rules.
	DefaultMode func(context.Context) string
}

// Responses builds a validated Responses URL for a Grok execution target.
// Runtime defaults are consulted with Background only when runtimeDefault is
// true and DefaultMode is configured; saved provider URL resolution still
// takes precedence.
func (p GrokRoutes) Responses(target *ExecutionProvider, runtimeDefault bool) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	base := provideradapter.GrokProviderBaseURL(ExecutionRecord(target))
	// Resolve the live text default after validator selection; saved provider URLs
	// retain precedence and are still checked when the complete endpoint is built.
	if runtimeDefault && p.DefaultMode != nil {
		fallback := GrokBaseURLForMode(p.DefaultMode(context.Background()))
		base = provideradapter.GrokProviderBaseURLOr(ExecutionRecord(target), fallback)
	}
	return grok.BuildResponsesURLWithValidator(base, validate)
}

// Chat builds a validated Chat Completions URL for a Grok execution target.
// The optional runtime default is read with Background when requested, then
// provider URL resolution selects the base before path construction.
func (p GrokRoutes) Chat(target *ExecutionProvider, runtimeDefault bool) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	base := provideradapter.GrokProviderBaseURL(ExecutionRecord(target))
	// Resolve the live text default after validator selection; saved provider URLs
	// retain precedence and are still checked when the complete endpoint is built.
	if runtimeDefault && p.DefaultMode != nil {
		fallback := GrokBaseURLForMode(p.DefaultMode(context.Background()))
		base = provideradapter.GrokProviderBaseURLOr(ExecutionRecord(target), fallback)
	}
	return grok.BuildChatCompletionsURLWithValidator(base, validate)
}

// Media builds the selected media endpoint and optional task path after
// choosing the target's validator. Provider media-base rules replace
// recognized OAuth CLI endpoints and retain custom routes for URL validation.
func (p GrokRoutes) Media(target *ExecutionProvider, endpoint grok.GrokMediaEndpoint, requestID string) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	return grok.BuildMediaEndpointURL(provideradapter.GrokProviderMediaBaseURL(ExecutionRecord(target)), endpoint, requestID, validate)
}

// Voice builds a validated voice endpoint using the provider's media base.
// Validator selection rejects nil or unsupported targets first, and OAuth
// media-base rules determine which configured host receives the request.
func (p GrokRoutes) Voice(target *ExecutionProvider, endpoint string) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	return grok.BuildVoiceEndpointURL(provideradapter.GrokProviderMediaBaseURL(ExecutionRecord(target)), endpoint, validate)
}
