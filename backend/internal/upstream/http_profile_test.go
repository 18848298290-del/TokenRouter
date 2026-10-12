package upstream

import (
	"context"
	"testing"
)

// TestWithHTTPUpstreamProfile_DefaultKeepsContext passes the default profile
// with a Background context. It checks pointer identity so the no-op profile
// selection does not add a wrapper.
func TestWithHTTPUpstreamProfile_DefaultKeepsContext(t *testing.T) {
	ctx := context.Background()
	got := WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileDefault)
	if got != ctx {
		t.Fatal("default profile should not wrap context")
	}
}

// TestWithHTTPUpstreamProfile_OpenAI stores the OpenAI marker in a request
// context. It checks that transport profile lookup returns the same recognized
// profile value.
func TestWithHTTPUpstreamProfile_OpenAI(t *testing.T) {
	ctx := WithHTTPUpstreamProfile(context.TODO(), HTTPUpstreamProfileOpenAI)
	if profile := HTTPUpstreamProfileFromContext(ctx); profile != HTTPUpstreamProfileOpenAI {
		t.Fatalf("expected profile %q, got %q", HTTPUpstreamProfileOpenAI, profile)
	}
}

// TestWithHTTPUpstreamRedirectsDisabled marks a nil context and reads the
// result. It checks that the restriction is true there and absent on an
// ordinary Background context.
func TestWithHTTPUpstreamRedirectsDisabled(t *testing.T) {
	//nolint:staticcheck // Exercise the nil-context fallback.
	ctx := WithHTTPUpstreamRedirectsDisabled(nil)
	if !HTTPUpstreamRedirectsDisabled(ctx) {
		t.Fatal("expected redirects to be disabled")
	}
	if HTTPUpstreamRedirectsDisabled(context.Background()) {
		t.Fatal("redirects should remain enabled by default")
	}
}
