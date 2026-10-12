package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// Grok endpoint selection gives saved provider addresses precedence over platform
// defaults. OAuth text normally uses the CLI endpoint, API keys use the public
// API, and OAuth media selection replaces an official CLI host with the media
// API while retaining custom endpoint routing.

// GrokProviderBaseURL selects a text endpoint for the Grok provider kind.
// Nil or non-Grok records return empty. OAuth selects the CLI default and other
// Grok kinds select the public default; callers still validate the returned URL
// under their endpoint policy.
func GrokProviderBaseURL(value *provider.Record) string {
	if value == nil || !value.IsGrok() {
		return ""
	}
	if value.IsGrokOAuth() {
		return GrokProviderBaseURLOr(value, grok.DefaultCLIBaseURL)
	}
	return GrokProviderBaseURLOr(value, grok.DefaultBaseURL)
}

// GrokProviderBaseURLOr resolves a saved address against the supplied default.
// Nil or non-Grok records return empty. The platform resolver normalizes the
// fallback and can replace malformed OAuth configuration with it; returned
// addresses still require caller endpoint validation.
func GrokProviderBaseURLOr(value *provider.Record, fallback string) string {
	if value == nil || !value.IsGrok() {
		return ""
	}
	return grok.ResolveProviderBaseURL(value.IsGrokOAuth(), value.GetCredential("base_url"), fallback)
}

// GrokProviderMediaBaseURL selects the provider endpoint used for media requests.
// A nonnil record is required, and non-Grok records return empty. OAuth endpoints
// recognized as official CLI hosts use the public media API; custom hosts retain
// their resolved address for caller validation.
func GrokProviderMediaBaseURL(value *provider.Record) string {
	if !value.IsGrok() {
		return ""
	}
	base := GrokProviderBaseURL(value)
	// Official CLI hosts carry subscription text traffic. Media requests use the
	// public API there, while a custom resolved host keeps its
	// operator-selected route.
	if value.IsGrokOAuth() && (grok.MediaCodec{}).IsGrokCLIProxyTarget(base) {
		return grok.DefaultBaseURL
	}
	return base
}
