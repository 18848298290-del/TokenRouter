package provider

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestUpstreamRequestIDFromHeaders_UnconfiguredProviderRecordsNothing supplies
// common platform and proxy request-ID headers with no configured header name.
// Nil, unconfigured, and blank-configured providers all return an empty
// upstream identifier.
func TestUpstreamRequestIDFromHeaders_UnconfiguredProviderRecordsNothing(t *testing.T) {
	h := http.Header{}
	h.Set("X-Client-Request-ID", "tokenrouter-client")
	h.Set("X-Request-ID", "tokenrouter-local")
	h.Set("X-Oneapi-Request-Id", "oneapi-1")
	h.Set("Request-Id", "req_official")
	h.Set("xai-request-id", "xai-1")
	h.Set("x-goog-request-id", "goog-1")

	require.Equal(t, "", UpstreamRequestIDFromHeaders(nil, h))
	for _, platform := range []string{capability.PlatformAnthropic, capability.PlatformOpenAI, capability.PlatformGemini, capability.PlatformAntigravity, capability.PlatformGrok} {
		require.Equal(t, "", UpstreamRequestIDFromHeaders(&providercore.Record{Platform: platform}, h), platform)
	}
	blank := &providercore.Record{Platform: capability.PlatformOpenAI, Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "   "}}
	require.Equal(t, "", UpstreamRequestIDFromHeaders(blank, h))
}

// TestUpstreamRequestIDFromHeaders_ReadsOnlyConfiguredHeader places an ID in
// an unrelated header before filling the configured one. Lookup trims the
// configured value, ignores other headers, and handles nil headers plus an
// Anthropic header name.
func TestUpstreamRequestIDFromHeaders_ReadsOnlyConfiguredHeader(t *testing.T) {
	provider := &providercore.Record{
		Platform: capability.PlatformOpenAI,
		Extra:    map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: " x-oneapi-request-id "},
	}
	h := http.Header{}
	h.Set("X-Request-ID", "passthrough-from-real-upstream")
	require.Equal(t, "", UpstreamRequestIDFromHeaders(provider, h))

	h.Set("X-Oneapi-Request-Id", " oneapi-2 ")
	require.Equal(t, "oneapi-2", UpstreamRequestIDFromHeaders(provider, h))
	require.Equal(t, "", UpstreamRequestIDFromHeaders(provider, nil))

	official := &providercore.Record{Platform: capability.PlatformAnthropic, Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "request-id"}}
	only := http.Header{}
	only.Set("Request-Id", "req_official")
	require.Equal(t, "req_official", UpstreamRequestIDFromHeaders(official, only))
}

// TestUsageUpstreamRequestIDPtr supplies a long ASCII ID and providers with
// missing header configuration. WS mode and missing IDs yield nil, while an
// HTTP ID is truncated to the configured byte cap.
func TestUsageUpstreamRequestIDPtr(t *testing.T) {
	provider := &providercore.Record{Extra: map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "X-Request-ID"}}
	h := http.Header{}
	h.Set("X-Request-ID", strings.Repeat("a", 200))
	require.Nil(t, usageUpstreamRequestIDPtr(provider, h, true))
	require.Nil(t, usageUpstreamRequestIDPtr(provider, http.Header{}, false))
	require.Nil(t, usageUpstreamRequestIDPtr(nil, h, false))
	require.Nil(t, usageUpstreamRequestIDPtr(&providercore.Record{}, h, false))

	got := usageUpstreamRequestIDPtr(provider, h, false)
	require.NotNil(t, got)
	require.Len(t, *got, maxUsageUpstreamRequestIDLen)
}

// TestValidateUpstreamRequestIDHeaderExtra checks absent, blank, padded,
// wrongly typed, malformed, and oversized header settings. Validation removes
// blank settings, trims a valid name, and rejects invalid types or syntax.
func TestValidateUpstreamRequestIDHeaderExtra(t *testing.T) {
	require.NoError(t, providercore.ValidateUpstreamRequestIDHeaderExtra(nil))
	require.NoError(t, providercore.ValidateUpstreamRequestIDHeaderExtra(map[string]any{}))

	blank := map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "   "}
	require.NoError(t, providercore.ValidateUpstreamRequestIDHeaderExtra(blank))
	_, present := blank[providercore.ProviderExtraUpstreamRequestIDHeader]
	require.False(t, present, "blank header name must be removed")

	valid := map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: " X-Oneapi-Request-Id "}
	require.NoError(t, providercore.ValidateUpstreamRequestIDHeaderExtra(valid))
	require.Equal(t, "X-Oneapi-Request-Id", valid[providercore.ProviderExtraUpstreamRequestIDHeader])

	require.Error(t, providercore.ValidateUpstreamRequestIDHeaderExtra(map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: 1}))
	require.Error(t, providercore.ValidateUpstreamRequestIDHeaderExtra(map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "X Request Id"}))
	require.Error(t, providercore.ValidateUpstreamRequestIDHeaderExtra(map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: "X-Request-Id:"}))
	require.Error(t, providercore.ValidateUpstreamRequestIDHeaderExtra(map[string]any{providercore.ProviderExtraUpstreamRequestIDHeader: strings.Repeat("x", 65)}))
}
