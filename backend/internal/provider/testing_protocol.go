package provider

import (
	"context"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// testing_protocol.go describes the entry protocols supported by group probes
// and checks their resolved provider routes against existing test executors.
// Group routing chooses the upstream protocol before testing begins. A test
// retains both identities so attribute rules can match the client entry while
// request construction uses the selected upstream endpoint.
//
// The zero route retains ordinary provider-test inference. A populated route
// requires a supported entry, an enabled upstream protocol, and an executor
// for that exact combination. These checks also run after loading credentials,
// preventing a changed provider configuration from silently selecting its
// default endpoint between scheduling and execution.

// TestProtocolRoute identifies the client entry and resolved upstream test endpoint.
type TestProtocolRoute struct {
	ClientProtocol   capability.ProtocolID // selected group entry protocol
	UpstreamProtocol capability.ProtocolID // resolved provider execution protocol
}

// testProtocolRouteKey separates test route metadata from other context values.
type testProtocolRouteKey struct{}

// GroupProbeProtocols returns the supported group probe entries in catalogue order.
// Callers may modify the returned slice. Compact and operations needing inputs
// beyond the probe prompt remain outside this list.
func GroupProbeProtocols() []capability.ProtocolID {
	return []capability.ProtocolID{
		capability.ProtocolAnthropicMessages,
		capability.ProtocolOpenAIResponses,
		capability.ProtocolOpenAIChatCompletions,
		capability.ProtocolGeminiGenerateContent,
		capability.ProtocolSystemOne,
		capability.ProtocolImagesGenerations,
	}
}

// SupportsProvider reports whether an existing test executor can execute this route.
// Both protocol identities must be present, and provider enablement is checked
// through the shared single-step resolver. A different resolved endpoint fails
// rather than changing the endpoint selected by group routing.
//
// Executor restrictions are narrower than gateway capabilities: Grok tests
// currently implement Responses and image generation, for example. Keeping that
// distinction here lets selection skip such candidates before sending requests,
// while execution repeats the same check against the loaded provider snapshot.
func (r TestProtocolRoute) SupportsProvider(value ProviderSnapshot) bool {
	if !slices.Contains(GroupProbeProtocols(), r.ClientProtocol) || r.UpstreamProtocol == "" {
		return false
	}
	resolved, ok := capability.ResolveRoute(value.Protocols(), r.ClientProtocol,
		map[capability.ProtocolID][]capability.ProtocolID{r.ClientProtocol: {r.UpstreamProtocol}})
	if !ok || resolved != r.UpstreamProtocol {
		return false
	}
	switch value.Platform {
	case PlatformOpenAI:
		return r.UpstreamProtocol == capability.ProtocolOpenAIResponses ||
			r.UpstreamProtocol == capability.ProtocolOpenAIChatCompletions ||
			r.UpstreamProtocol == capability.ProtocolImagesGenerations
	case PlatformKimi, PlatformZhipu, PlatformDeepseek:
		return r.UpstreamProtocol == capability.ProtocolAnthropicMessages ||
			r.UpstreamProtocol == capability.ProtocolOpenAIResponses ||
			r.UpstreamProtocol == capability.ProtocolOpenAIChatCompletions
	case PlatformAnthropic:
		return r.UpstreamProtocol == capability.ProtocolAnthropicMessages
	case PlatformGemini, PlatformAntigravity:
		return r.UpstreamProtocol == capability.ProtocolGeminiGenerateContent
	case PlatformQoder:
		return r.UpstreamProtocol == capability.ProtocolQoderChat
	case PlatformGrok:
		return r.UpstreamProtocol == capability.ProtocolOpenAIResponses ||
			r.UpstreamProtocol == capability.ProtocolImagesGenerations
	case PlatformJev:
		return r.UpstreamProtocol == capability.ProtocolSystemOne
	default:
		return false
	}
}

// TestProtocolRouteFromContext returns the selected test route or its zero value.
// The zero value permits existing endpoint inference. TestService supplies a
// populated route only after validating both protocol identities against the target.
func TestProtocolRouteFromContext(ctx context.Context) TestProtocolRoute {
	route, _ := ctx.Value(testProtocolRouteKey{}).(TestProtocolRoute)
	return route
}
