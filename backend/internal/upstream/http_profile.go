package upstream

import "context"

// http_profile.go carries upstream HTTP profiles and destination-policy
// markers through request contexts.
// Provider-specific transports read context markers when selecting HTTP pool
// settings and destination restrictions. Each marker travels with the request;
// the shared transport applies the profile or redirect policy when
// constructing that request's client options.

// These profiles select the shared transport settings for ordinary requests,
// OpenAI exchanges, and Grok exchanges.
const (
	HTTPUpstreamProfileDefault HTTPUpstreamProfile = ""       // ordinary transport policy
	HTTPUpstreamProfileOpenAI  HTTPUpstreamProfile = "openai" // OpenAI transport policy
	HTTPUpstreamProfileGrok    HTTPUpstreamProfile = "grok"   // Grok transport policy
)

// HTTPUpstreamProfile selects the provider policy read by the shared HTTP
// transport.
type HTTPUpstreamProfile string

type (
	// httpUpstreamProfileContextKey isolates the provider profile from other
	// context values.
	httpUpstreamProfileContextKey struct{}
	// httpUpstreamDisableRedirectsContextKey identifies the request marker
	// forbidding automatic HTTP redirects.
	httpUpstreamDisableRedirectsContextKey struct{}
	// httpUpstreamPublicHostsOnlyContextKey identifies the request marker
	// requiring public destination addresses.
	httpUpstreamPublicHostsOnlyContextKey struct{}
)

// WithHTTPUpstreamProfile attaches a profile, using Background when ctx is
// nil. The default profile returns the existing context, so it does not clear
// a profile already stored there.
func WithHTTPUpstreamProfile(ctx context.Context, profile HTTPUpstreamProfile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile == HTTPUpstreamProfileDefault {
		return ctx
	}
	return context.WithValue(ctx, httpUpstreamProfileContextKey{}, profile)
}

// HTTPUpstreamProfileFromContext returns the recognized OpenAI or Grok request
// profile. Nil contexts, missing values, values of another type, and
// unrecognized profiles select the default transport policy.
func HTTPUpstreamProfileFromContext(ctx context.Context) HTTPUpstreamProfile {
	if ctx == nil {
		return HTTPUpstreamProfileDefault
	}
	profile, ok := ctx.Value(httpUpstreamProfileContextKey{}).(HTTPUpstreamProfile)
	if !ok {
		return HTTPUpstreamProfileDefault
	}
	switch profile {
	case HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileGrok:
		return profile
	default:
		return HTTPUpstreamProfileDefault
	}
}

// WithHTTPUpstreamRedirectsDisabled marks the request for the shared transport
// to forbid redirects. Nil ctx uses Background; the derived context leaves the
// cached client's redirect settings to per-request policy handling.
func WithHTTPUpstreamRedirectsDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamDisableRedirectsContextKey{}, true)
}

// HTTPUpstreamRedirectsDisabled reports whether ctx carries the true redirect
// restriction marker. Nil contexts and absent or differently valued markers
// return false for the shared transport's policy selection.
func HTTPUpstreamRedirectsDisabled(ctx context.Context) bool {
	return ctx != nil && ctx.Value(httpUpstreamDisableRedirectsContextKey{}) == true
}

// WithHTTPUpstreamPublicHostsOnly marks downloads for public destination
// checks in the shared transport. Nil ctx uses Background; request policy
// validates the initial destination and applies that restriction to redirects.
func WithHTTPUpstreamPublicHostsOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamPublicHostsOnlyContextKey{}, true)
}

// HTTPUpstreamPublicHostsOnly reports whether ctx carries the true public-host
// restriction marker. Nil contexts and absent or differently valued markers
// return false when the shared transport builds destination policy.
func HTTPUpstreamPublicHostsOnly(ctx context.Context) bool {
	return ctx != nil && ctx.Value(httpUpstreamPublicHostsOnlyContextKey{}) == true
}
