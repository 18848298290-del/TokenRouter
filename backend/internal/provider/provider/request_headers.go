package provider

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// ApplyProviderHeaderOverrides applies provider policy using the protocol's wire header casing.
func ApplyProviderHeaderOverrides(value *provider.Record, headers http.Header) {
	if headers == nil {
		return
	}
	policy := egress.RequestPolicy(egress.RequestPolicyInput{Headers: value.HeaderOverrides()})
	egressprovider.ApplyRequestHeaders(headers, policy, anthropic.ResolveWireCasing)
}

// HeaderOverrideValue looks up an enabled override by its lowercase header name.
func HeaderOverrideValue(value *provider.Record, lowerName string) (string, bool) {
	result, ok := value.HeaderOverrides()[lowerName]
	return result, ok
}
