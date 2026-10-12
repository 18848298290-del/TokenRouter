package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestTargets reads a provider and selects its platform's test executor.
type TestTargets struct {
	Jev         *JevProviderTest
	Read        func(context.Context, int64) (*provider.Record, error)
	Qoder       *QoderProviderTest
	Gemini      *GeminiProviderTest
	Grok        *GrokProviderTest
	Anthropic   *AnthropicProviderTest
	OpenAI      *OpenAIProviderTest
	CN          *CNProviderTest
	Antigravity *AntigravityProviderTest
}

func (t *TestTargets) LoadTestTarget(ctx context.Context, request provider.TestRequest) (provider.TestTarget, error) {
	value, err := t.Read(ctx, request.ProviderID)
	if err != nil {
		return nil, err
	}
	switch value.Platform {
	case provider.PlatformJev:
		return t.Jev.Target(value), nil
	case provider.PlatformQoder:
		return t.Qoder.Target(value), nil
	case provider.PlatformGemini:
		return t.Gemini.Target(value), nil
	case provider.PlatformGrok:
		return t.Grok.Target(value), nil
	case provider.PlatformOpenAI:
		return t.OpenAI.Target(value), nil
	case provider.PlatformKimi, provider.PlatformZhipu, provider.PlatformDeepseek:
		return t.CN.Target(value), nil
	case provider.PlatformAntigravity:
		return t.Antigravity.Target(value), nil
	default:
		return t.Anthropic.Target(value), nil
	}
}
