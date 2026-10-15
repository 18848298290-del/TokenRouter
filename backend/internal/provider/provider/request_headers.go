package provider

import (
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// request_headers.go applies filtered provider header overrides and reads
// individual normalized overrides.
// Provider header overrides pass through egress policy before reaching an
// upstream request. The provider record supplies an enabled, filtered map, and
// the applicator uses Anthropic wire casing so permitted replacements retain
// the platform's expected header spelling.

// ApplyProviderHeaderOverrides applies the record's filtered header policy with
// Anthropic wire casing. Nil destination headers are skipped; allowed keys can
// replace existing values, while protected keys are excluded by provider and
// egress filtering.
func ApplyProviderHeaderOverrides(value *provider.Record, headers http.Header) {
	if headers == nil {
		return
	}
	policy := egress.RequestPolicy(egress.RequestPolicyInput{Headers: value.HeaderOverrides()})
	egressprovider.ApplyRequestHeaders(headers, policy, anthropic.ResolveWireCasing)
}

// HeaderOverrideValue returns one enabled override under its normalized
// lowercase key. The lookup uses the record's filtered map and reports false
// for absent entries; callers supply the lowercase spelling.
func HeaderOverrideValue(value *provider.Record, lowerName string) (string, bool) {
	result, ok := value.HeaderOverrides()[lowerName]
	return result, ok
}
