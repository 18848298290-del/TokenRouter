package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// GrokProviderBaseURL selects OAuth or API key endpoint defaults. The caller must validate the returned URL.
func GrokProviderBaseURL(value *provider.Record) string {
	if value == nil || !value.IsGrok() {
		return ""
	}
	if value.IsGrokOAuth() {
		return GrokProviderBaseURLOr(value, grok.DefaultCLIBaseURL)
	}
	return GrokProviderBaseURLOr(value, grok.DefaultBaseURL)
}

// GrokProviderBaseURLOr prefers the configured address and otherwise uses the supplied default.
func GrokProviderBaseURLOr(value *provider.Record, fallback string) string {
	if value == nil || !value.IsGrok() {
		return ""
	}
	return grok.ResolveProviderBaseURL(value.IsGrokOAuth(), value.GetCredential("base_url"), fallback)
}

// GrokProviderMediaBaseURL maps the official OAuth CLI host to the media API and keeps custom hosts
// unchanged.
func GrokProviderMediaBaseURL(value *provider.Record) string {
	if !value.IsGrok() {
		return ""
	}
	base := GrokProviderBaseURL(value)
	if value.IsGrokOAuth() && (grok.MediaCodec{}).IsGrokCLIProxyTarget(base) {
		return grok.DefaultBaseURL
	}
	return base
}
