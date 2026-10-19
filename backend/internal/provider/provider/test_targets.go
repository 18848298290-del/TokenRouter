package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// test_targets.go loads provider records and selects the platform executor for
// administrative tests.
// Provider test handles use the stored platform to choose execution after
// loading the requested provider ID. Jev, Qoder, Gemini, Grok, OpenAI, CN, and
// Antigravity have explicit routes; remaining platforms enter the Anthropic
// executor, which validates supported provider types during execution. Automatic
// text and image probes select their payload using declared final-model output;
// explicit image probes require the same positive capability evidence.

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

// ModelAttributeLookup resolves the final provider model for a probe protocol.
// Implementations retain a catalog snapshot for the probe and propagate profile
// failures before the executor sends a request to its selected upstream.
type ModelAttributeLookup func(context.Context, *provider.Record, string, string) (modelcatalog.Attributes, error)

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

// resolveProbeType selects automatic test payloads from declared output.
// Explicit text probes retain their chosen operation. Image probes require
// confirmed image support even when selected explicitly; unknown automatic
// probes require a type selection or a model attribute declaration.
//
// Callers complete group and provider mapping before this lookup. A mixed
// text/image declaration selects the image probe automatically, while explicit
// text keeps its chosen payload. Resolver errors stop preparation immediately;
// an empty output set cannot be rescued by a model name or a selected image
// test type.
func resolveProbeType(ctx context.Context, lookup ModelAttributeLookup, value *provider.Record, model, protocol string, testTypes ...string) (string, error) {
	kind, explicit := provider.ProviderTestTypeFromArgs(testTypes...)
	if explicit && kind != provider.ProviderTestTypeImage {
		return kind, nil
	}
	attrs := modelcatalog.Attributes{}
	if lookup != nil {
		var err error
		attrs, err = lookup(ctx, value, model, protocol)
		if err != nil {
			return "", err
		}
	}
	image := attrs.OutputSupport("image")
	if image == nil {
		return "", apperror.BadRequest("MODEL_CAPABILITY_UNKNOWN", "model output capability is unknown; configure image attributes or explicitly select a text test")
	}
	if *image {
		return provider.ProviderTestTypeImage, nil
	}
	if explicit || attrs.OutputSupport("text") == nil || !*attrs.OutputSupport("text") {
		return "", apperror.BadRequest("MODEL_CAPABILITY_UNSUPPORTED", "the model does not declare the output required by this test")
	}
	return provider.ProviderTestTypeText, nil
}
