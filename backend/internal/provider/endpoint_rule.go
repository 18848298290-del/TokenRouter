package provider

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// endpoint_rule.go evaluates OpenAI-compatible endpoint eligibility from
// provider protocols, authentication, and workload settings.
// Endpoint eligibility combines a provider protocol set with type-specific
// capability rules and workload configuration. An empty requested capability
// means no extra restriction, while named capabilities require OpenAI-compatible
// providers. Grok follows its own text/media path, including unobserved billing
// candidates that forwarding must verify before media generation.
//
// Responses operations require a usable Responses route, and native compaction
// adds its independent administrator switch before inheriting text-generation
// checks. Live eligibility narrows OAuth authentication types; Alpha Search and
// Embeddings impose their own type and configured-protocol requirements.
// Workload defaults remain available when no capability set was saved, while an
// explicit set determines the final eligibility result.

// Capability values distinguish compatible operations whose platform, credential
// and protocol requirements are checked before a provider reaches execution.
const (
	OpenAIEndpointCapabilityTextGeneration OpenAIEndpointCapability = "text_generation" // compatible text-generation operation
	OpenAIEndpointCapabilityEmbeddings     OpenAIEndpointCapability = "embeddings"      // API Key embeddings operation
	OpenAIEndpointCapabilityAlphaSearch    OpenAIEndpointCapability = "alpha_search"    // compatible Alpha Search operation
	// OpenAIEndpointCapabilityLive identifies Frameless Live support for eligible ChatGPT OAuth providers.
	OpenAIEndpointCapabilityLive OpenAIEndpointCapability = "live"
	// OpenAIEndpointCapabilityGrokMediaGeneration excludes disabled or billing-ineligible Grok providers.
	// Video status queries do not require it so submitted tasks remain queryable.
	OpenAIEndpointCapabilityGrokMediaGeneration OpenAIEndpointCapability = "grok_media_generation"
	// OpenAIEndpointCapabilityResponses requires a Responses route.
	// Eligibility is derived from configured protocols and routing rules.
	OpenAIEndpointCapabilityResponses OpenAIEndpointCapability = "responses"
	// OpenAIEndpointCapabilityRemoteCompactionV2 identifies native remote_compaction_v2 support.
	// It requires ordinary Responses capability and the provider's V2 switch.
	OpenAIEndpointCapabilityRemoteCompactionV2 OpenAIEndpointCapability = "remote_compaction_v2"
)

// OpenAIEndpointCapability identifies an operation requested during compatible
// provider eligibility checks.
type OpenAIEndpointCapability string

// SupportsOpenAIEndpointCapability checks the requested operation against
// configured protocols, authentication type and workload choices. Nil records
// fail, while an empty request imposes no capability restriction. Keep the
// record and any media callback inputs stable during evaluation.
//
// Grok media needs a callback and admits billing_unobserved candidates for a
// later forwarding probe; other Grok capabilities use its dedicated text rule.
// Responses and native compaction require Responses routing before text workload
// checks. Unknown capabilities fail, and an absent workload set permits the
// applicable default behavior.
func (r *Record) SupportsOpenAIEndpointCapability(requested OpenAIEndpointCapability, grokMedia func() (bool, string)) bool {
	if r == nil {
		return false
	}
	if requested == "" {
		return true
	}
	if !r.IsOpenAICompatible() {
		return false
	}
	// A saved native set remains authoritative even when empty. Workload flags
	// cannot re-enable an operation excluded by that set before its type-specific
	// checks run.
	if _, unified := r.Credentials[UpstreamProtocolsKey]; unified && !r.IsGrok() {
		enabled := r.UpstreamProtocols()
		has := func(p capability.ProtocolID) bool { return slices.Contains(enabled, p) }
		switch requested {
		case OpenAIEndpointCapabilityTextGeneration:
			if !has(capability.ProtocolOpenAIResponses) && !has(capability.ProtocolOpenAIChatCompletions) && !has(capability.ProtocolAnthropicMessages) {
				return false
			}
		case OpenAIEndpointCapabilityResponses, OpenAIEndpointCapabilityRemoteCompactionV2:
			if !has(capability.ProtocolOpenAIResponses) {
				return false
			}
		case OpenAIEndpointCapabilityEmbeddings:
			if !has(capability.ProtocolEmbeddings) {
				return false
			}
		case OpenAIEndpointCapabilityLive:
			if !has(capability.ProtocolLive) {
				return false
			}
		case OpenAIEndpointCapabilityAlphaSearch:
			// Personal access tokens use the Responses route for converted Alpha Search,
			// so their native selection can qualify through Responses while ordinary
			// records need Alpha Search itself.
			if !has(capability.ProtocolAlphaSearch) && (!r.IsOpenAIPersonalAccessToken() || !has(capability.ProtocolOpenAIResponses)) {
				return false
			}
		}
	}
	if r.IsGrok() {
		switch requested {
		case OpenAIEndpointCapabilityTextGeneration:
			return true
		case OpenAIEndpointCapabilityGrokMediaGeneration:
			if grokMedia == nil {
				return false
			}
			eligible, reason := grokMedia()
			// Unobserved OAuth providers remain candidates so forwarding can perform the billing probe.
			// The forwarding gate rejects providers without affirmative paid-eligibility evidence.
			return eligible || reason == "billing_unobserved"
		default:
			return false
		}
	}
	switch requested {
	case OpenAIEndpointCapabilityTextGeneration:
	case OpenAIEndpointCapabilityLive:
		return r.Platform == PlatformOpenAI &&
			r.Type == ProviderTypeOAuth &&
			!r.IsOpenAIPersonalAccessToken() &&
			!r.IsOpenAIAgentIdentity()
	case OpenAIEndpointCapabilityRemoteCompactionV2:
		if !r.AllowsOpenAINativeCompactionV2() {
			return false
		}
		fallthrough
	case OpenAIEndpointCapabilityResponses:
		// Native Responses operations such as image generation cannot use a Chat-only route.
		// Exclude API Key providers whose configured route resolves Responses to another protocol.
		if r.Type == ProviderTypeAPIKey && ResolveUpstreamTextProtocol(
			r.Extra,
			TextProtocolResponses,
		) != TextProtocolResponses {
			return false
		}
		// Responses uses the text-generation workload after its route checks, so
		// an explicitly disabled text workload also rejects the Responses operation.
		requested = OpenAIEndpointCapabilityTextGeneration
	case OpenAIEndpointCapabilityAlphaSearch:
		// Alpha Search requires OAuth or API Key authentication before workload
		// admission; the execution adapter chooses its endpoint and handles rejection.
		if r.Type != ProviderTypeOAuth && r.Type != ProviderTypeAPIKey {
			return false
		}
	case OpenAIEndpointCapabilityEmbeddings:
		if r.Type != ProviderTypeAPIKey {
			return false
		}
	default:
		return false
	}

	configured, found := r.OpenAIWorkloadCapabilitySet()
	if !found {
		return true
	}
	// Alpha Search inherits an enabled text workload so stored workload choices
	// can admit it even when no separate alpha_search flag was saved.
	if requested == OpenAIEndpointCapabilityAlphaSearch && configured[string(OpenAIEndpointCapabilityTextGeneration)] {
		return true
	}
	return configured[string(requested)]
}
