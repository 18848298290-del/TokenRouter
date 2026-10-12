package provider

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	OpenAIEndpointCapabilityTextGeneration OpenAIEndpointCapability = "text_generation"
	OpenAIEndpointCapabilityEmbeddings     OpenAIEndpointCapability = "embeddings"
	OpenAIEndpointCapabilityAlphaSearch    OpenAIEndpointCapability = "alpha_search"
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

type OpenAIEndpointCapability string

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
		// Responses also requires ordinary text-generation capability;
		// reuse the text_generation configuration check below.
		requested = OpenAIEndpointCapabilityTextGeneration
	case OpenAIEndpointCapabilityAlphaSearch:
		// Alpha Search uses the provider type to select its endpoint:
		// OAuth credentials use chatgpt.com/backend-api/codex/alpha/search, while API keys use
		// {base_url}/v1/alpha/search (see openAIAlphaSearchURL).
		// The forwarding layer handles upstream endpoint rejection and failover.
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
	if requested == OpenAIEndpointCapabilityAlphaSearch && configured[string(OpenAIEndpointCapabilityTextGeneration)] {
		return true
	}
	return configured[string(requested)]
}
