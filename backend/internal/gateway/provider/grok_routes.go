package provider

import (
	"context"

	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// GrokRoutes builds validated endpoints using provider settings and runtime defaults.
type GrokRoutes struct {
	Validate    grok.BaseURLValidator
	DefaultMode func(context.Context) string
}

func (p GrokRoutes) Responses(target *ExecutionProvider, runtimeDefault bool) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	base := provideradapter.GrokProviderBaseURL(ExecutionRecord(target))
	if runtimeDefault && p.DefaultMode != nil {
		fallback := GrokBaseURLForMode(p.DefaultMode(context.Background()))
		base = provideradapter.GrokProviderBaseURLOr(ExecutionRecord(target), fallback)
	}
	return grok.BuildResponsesURLWithValidator(base, validate)
}

func (p GrokRoutes) Chat(target *ExecutionProvider, runtimeDefault bool) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	base := provideradapter.GrokProviderBaseURL(ExecutionRecord(target))
	if runtimeDefault && p.DefaultMode != nil {
		fallback := GrokBaseURLForMode(p.DefaultMode(context.Background()))
		base = provideradapter.GrokProviderBaseURLOr(ExecutionRecord(target), fallback)
	}
	return grok.BuildChatCompletionsURLWithValidator(base, validate)
}

func (p GrokRoutes) Media(target *ExecutionProvider, endpoint grok.GrokMediaEndpoint, requestID string) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	return grok.BuildMediaEndpointURL(provideradapter.GrokProviderMediaBaseURL(ExecutionRecord(target)), endpoint, requestID, validate)
}

func (p GrokRoutes) Voice(target *ExecutionProvider, endpoint string) (string, error) {
	validate, err := provideradapter.GrokBaseURLValidator(ExecutionRecord(target), p.Validate)
	if err != nil {
		return "", err
	}
	return grok.BuildVoiceEndpointURL(provideradapter.GrokProviderMediaBaseURL(ExecutionRecord(target)), endpoint, validate)
}
