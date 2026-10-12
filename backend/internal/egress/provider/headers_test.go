package provider

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// TestFilterHeadersDisabledUsesDefaultAllowlist supplies disabled custom
// removal alongside default metadata, request identity, and framing headers. It
// checks default values pass, custom removal is ignored, and extra/framing
// headers are omitted.
func TestFilterHeadersDisabledUsesDefaultAllowlist(t *testing.T) {
	src := http.Header{}
	src.Add("Content-Type", "application/json")
	src.Add("X-Request-Id", "req-123")
	src.Add("X-Test", "ok")
	src.Add("Connection", "keep-alive")
	src.Add("Content-Length", "123")

	cfg := egress.ResponseHeaderOptions{
		Enabled:     false,
		ForceRemove: []string{"x-request-id"},
	}

	filtered := FilterHeaders(src, egress.CompileHeaderFilter(cfg))
	if filtered.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type passthrough, got %q", filtered.Get("Content-Type"))
	}
	if filtered.Get("X-Request-Id") != "req-123" {
		t.Fatalf("expected X-Request-Id allowed, got %q", filtered.Get("X-Request-Id"))
	}
	if filtered.Get("X-Test") != "" {
		t.Fatalf("expected X-Test removed, got %q", filtered.Get("X-Test"))
	}
	if filtered.Get("Connection") != "" {
		t.Fatalf("expected Connection to be removed, got %q", filtered.Get("Connection"))
	}
	if filtered.Get("Content-Length") != "" {
		t.Fatalf("expected Content-Length to be removed, got %q", filtered.Get("Content-Length"))
	}
}

// TestFilterHeadersAllowsReasoningIncludedByDefault supplies the upstream
// reasoning-accounting marker with default configuration. It checks the
// filtered response retains that header value so downstream consumers can use
// the upstream accounting signal.
func TestFilterHeadersAllowsReasoningIncludedByDefault(t *testing.T) {
	src := http.Header{}
	src.Set("X-Reasoning-Included", "1")

	filtered := FilterHeaders(src, egress.CompileHeaderFilter(egress.ResponseHeaderOptions{}))
	if got := filtered.Get("X-Reasoning-Included"); got != "1" {
		t.Fatalf("expected X-Reasoning-Included passthrough, got %q", got)
	}
}

// TestFilterHeadersForceRemoveOverridesReasoningIncluded enables configured
// removal of the normally allowed reasoning marker. It checks that forced
// removal takes precedence and omits that response value.
func TestFilterHeadersForceRemoveOverridesReasoningIncluded(t *testing.T) {
	src := http.Header{}
	src.Set("X-Reasoning-Included", "1")

	filtered := FilterHeaders(src, egress.CompileHeaderFilter(egress.ResponseHeaderOptions{
		Enabled:     true,
		ForceRemove: []string{"x-reasoning-included"},
	}))
	if got := filtered.Get("X-Reasoning-Included"); got != "" {
		t.Fatalf("expected X-Reasoning-Included removal, got %q", got)
	}
}

// TestFilterHeadersEnabledUsesAllowlist supplies default, additional, removed,
// and unknown response headers with enabled configuration. It checks default
// and added values pass while removed and unlisted names are absent.
func TestFilterHeadersEnabledUsesAllowlist(t *testing.T) {
	src := http.Header{}
	src.Add("Content-Type", "application/json")
	src.Add("X-Extra", "ok")
	src.Add("X-Remove", "nope")
	src.Add("X-Blocked", "nope")

	cfg := egress.ResponseHeaderOptions{
		Enabled:           true,
		AdditionalAllowed: []string{"x-extra"},
		ForceRemove:       []string{"x-remove"},
	}

	filtered := FilterHeaders(src, egress.CompileHeaderFilter(cfg))
	if filtered.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type allowed, got %q", filtered.Get("Content-Type"))
	}
	if filtered.Get("X-Extra") != "ok" {
		t.Fatalf("expected X-Extra allowed, got %q", filtered.Get("X-Extra"))
	}
	if filtered.Get("X-Remove") != "" {
		t.Fatalf("expected X-Remove removed, got %q", filtered.Get("X-Remove"))
	}
	if filtered.Get("X-Blocked") != "" {
		t.Fatalf("expected X-Blocked removed, got %q", filtered.Get("X-Blocked"))
	}
}

// TestEgressHeadersPreserveWireCasingAndUnrelatedValues applies an override to
// duplicate case variants beside credential text. It checks the selected wire
// spelling replaces both variants and policy filtering leaves the existing
// authorization value intact.
func TestEgressHeadersPreserveWireCasingAndUnrelatedValues(t *testing.T) {
	headers := http.Header{"x-allowed": {"one"}, "X-Allowed": {"two"}, "AUTHORIZATION": {"credential"}}
	policy := egress.RequestPolicy(egress.RequestPolicyInput{Headers: map[string]string{"X-Allowed": "override", "Authorization": "forbidden"}})
	ApplyRequestHeaders(headers, policy, func(name string) string {
		if name == "x-allowed" {
			return "X-ALLOWED"
		}
		return name
	})
	require.Equal(t, http.Header{"X-ALLOWED": {"override"}, "AUTHORIZATION": {"credential"}}, headers)
}
