package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// test_targets.go loads provider records and selects the platform executor for
// administrative tests.
// Provider test handles use the stored platform to choose execution after
// loading the requested provider ID. Jev, Qoder, Gemini, Grok, OpenAI, CN, and
// Antigravity have explicit routes; remaining platforms enter the Anthropic
// executor, which validates supported provider types during execution.

// TestTargets loads provider records and binds platform executors for prepared
// probes.
type TestTargets struct {
	Jev *JevProviderTest // Jev SystemOne probe executor
	// Read is required for target loading. A successful callback returns a nonnil
	// provider whose platform selects the executor.
	Read        func(context.Context, int64) (*provider.Record, error)
	Qoder       *QoderProviderTest       // Qoder session probe executor
	Gemini      *GeminiProviderTest      // Gemini platform probe executor
	Grok        *GrokProviderTest        // Grok platform probe executor
	Anthropic   *AnthropicProviderTest   // default Anthropic probe executor
	OpenAI      *OpenAIProviderTest      // OpenAI platform probe executor
	CN          *CNProviderTest          // CN native-protocol probe executor
	Antigravity *AntigravityProviderTest // Antigravity platform probe executor
}

// LoadTestTarget reads the requested provider and returns its platform-bound
// test handle. Read errors propagate; successful reads supply a nonnil record,
// and callers configure the executor selected by that platform, including the
// Anthropic default.
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
