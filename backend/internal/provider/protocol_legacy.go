package provider

import (
	"maps"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func (r *Record) LegacyUpstreamProtocols(legacyMode string) []capability.ProtocolID {
	options := r.NativeProtocolOptions()
	if r.IsCNProvider() {
		switch legacyMode {
		case APIProtocolAdaptive:
			return options
		case APIProtocolAnthropic:
			return []capability.ProtocolID{capability.ProtocolAnthropicMessages}
		case APIProtocolResponses:
			return []capability.ProtocolID{capability.ProtocolOpenAIResponses}
		default:
			return []capability.ProtocolID{capability.ProtocolOpenAIChatCompletions}
		}
	}
	if r.IsOpenAIApiKey() {
		workloads, found := r.OpenAIWorkloadCapabilitySet()
		if found && !workloads["text_generation"] {
			options = slices.DeleteFunc(options, func(p capability.ProtocolID) bool {
				return p != capability.ProtocolEmbeddings && p != capability.ProtocolImagesGenerations && p != capability.ProtocolImagesEdits
			})
		}
		if found && !workloads["embeddings"] {
			options = slices.DeleteFunc(options, func(p capability.ProtocolID) bool { return p == capability.ProtocolEmbeddings })
		}
		mode := ResolveUpstreamTextProtocol(r.Extra, TextProtocolResponses)
		if mode == TextProtocolChatCompletions {
			options = slices.DeleteFunc(options, func(p capability.ProtocolID) bool { return p == capability.ProtocolOpenAIResponses })
		}
		if ResolveUpstreamTextProtocol(r.Extra, TextProtocolChatCompletions) == TextProtocolResponses {
			options = slices.DeleteFunc(options, func(p capability.ProtocolID) bool { return p == capability.ProtocolOpenAIChatCompletions })
		}
	}
	return options
}

func HasLegacyProtocolPatch(credentials, extra map[string]any) bool {
	for _, key := range []string{"api_protocol", OpenAIWorkloadCapabilitiesCredentialKey, LegacyOpenAICapabilitiesCredentialKey} {
		if _, ok := credentials[key]; ok {
			return true
		}
	}
	for _, key := range []string{ExtraKeyTextRouteMode, LegacyOpenAIResponsesModeExtraKey} {
		if _, ok := extra[key]; ok {
			return true
		}
	}
	return false
}

func ApplyLegacyProtocolPatch(provider *Record, credentials, extra map[string]any) {
	if _, explicit := credentials[UpstreamProtocolsKey]; explicit || !HasLegacyProtocolPatch(credentials, extra) {
		return
	}
	if _, unified := provider.Credentials[UpstreamProtocolsKey]; !unified {
		return
	}
	// Legacy input replaces text and optional embeddings selections while retaining independent media protocols.
	selected := provider.UpstreamProtocols()
	legacy := *provider
	legacy.Credentials = maps.Clone(provider.Credentials)
	delete(legacy.Credentials, UpstreamProtocolsKey)
	legacyProtocols := legacy.LegacyUpstreamProtocols(legacy.ConfiguredAPIProtocol())
	isText := func(p capability.ProtocolID) bool {
		return p == capability.ProtocolAnthropicMessages || p == capability.ProtocolOpenAIResponses || p == capability.ProtocolOpenAIChatCompletions
	}
	hasWorkload := false
	for _, key := range []string{OpenAIWorkloadCapabilitiesCredentialKey, LegacyOpenAICapabilitiesCredentialKey} {
		if _, ok := credentials[key]; ok {
			hasWorkload = true
		}
	}
	selected = slices.DeleteFunc(selected, func(p capability.ProtocolID) bool {
		return isText(p) || (hasWorkload && p == capability.ProtocolEmbeddings)
	})
	for _, p := range legacyProtocols {
		if isText(p) || (hasWorkload && p == capability.ProtocolEmbeddings) {
			selected = append(selected, p)
		}
	}
	provider.Credentials = maps.Clone(provider.Credentials)
	provider.Credentials[UpstreamProtocolsKey] = selected
}

func PreserveProtocolCredentials(existing, incoming map[string]any) map[string]any {
	out := maps.Clone(incoming)
	if out == nil {
		out = map[string]any{}
	}
	if !HasLegacyProtocolPatch(incoming, nil) {
		for _, key := range []string{UpstreamProtocolsKey, "api_base_urls"} {
			if _, supplied := out[key]; !supplied {
				if value, exists := existing[key]; exists {
					out[key] = value
				}
			}
		}
	}
	return out
}

// MigrateLegacyProtocolCredentials copies legacy endpoints and removes old fields after saving the unified set.
func MigrateLegacyProtocolCredentials(provider *Record) {
	// Copy a CN fixed-protocol base_url into its protocol-specific address slot when that slot is absent.
	if provider.IsCNProvider() {
		legacy := provider.GetCredential("api_protocol")
		if legacy != "" && legacy != APIProtocolAdaptive {
			urls, _ := provider.Credentials["api_base_urls"].(map[string]any)
			urls = maps.Clone(urls)
			if urls == nil {
				urls = map[string]any{}
			}
			if base := strings.TrimSpace(provider.GetCredential("base_url")); base != "" {
				if _, exists := urls[legacy]; !exists {
					urls[legacy] = base
				}
			}
			provider.Credentials["api_base_urls"] = urls
		}
	}
	delete(provider.Credentials, "api_protocol")
	delete(provider.Credentials, OpenAIWorkloadCapabilitiesCredentialKey)
	delete(provider.Credentials, LegacyOpenAICapabilitiesCredentialKey)
	provider.Extra = maps.Clone(provider.Extra)
	delete(provider.Extra, ExtraKeyTextRouteMode)
	delete(provider.Extra, LegacyOpenAIResponsesModeExtraKey)
}
