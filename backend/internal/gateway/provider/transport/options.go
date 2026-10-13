package transport

// Transport settings distinguish an absent configuration from explicit zero
// limits. The generic pool, platform header waits, and OpenAI HTTP/2 policy
// each apply their own defaults before the client combines them with per-
// request destination and redirect restrictions.

// Options contains application-supplied transport settings.
// Client reads these values synchronously through its options source.
type Options struct {
	ValidateResolvedIP bool // global DNS validation flag
	// ConnectionPoolIsolation chooses proxy, provider, or provider_proxy cache
	// identity; blank or unrecognized values select provider_proxy.
	ConnectionPoolIsolation string
	// MaxUpstreamClients limits cached clients; Client uses its built-in positive
	// default when this configured value is nonpositive.
	MaxUpstreamClients int
	// ClientIdleTTLSeconds sets cached-client idle lifetime in seconds; Client
	// substitutes its default when this configured value is nonpositive.
	ClientIdleTTLSeconds int
	// MaxIdleConns caps idle connections across hosts; positive provider
	// concurrency overrides it in provider-isolated pools.
	MaxIdleConns int
	// MaxIdleConnsPerHost caps idle connections per host; positive provider
	// concurrency overrides it in provider-isolated pools.
	MaxIdleConnsPerHost int
	// MaxConnsPerHost caps active and idle connections per host; zero removes the
	// cap, while provider-isolated concurrency can override it.
	MaxConnsPerHost int
	// IdleConnTimeoutSeconds bounds idle connection lifetime in seconds;
	// nonpositive values select the default in defaultPoolSettings.
	IdleConnTimeoutSeconds int
	// ResponseHeaderTimeout sets the generic header wait in seconds; zero disables
	// it, negative values select the default, and platform profiles override it.
	ResponseHeaderTimeout int
	// OpenAIResponseHeaderTimeout sets the OpenAI header wait in seconds; positive
	// values enable it and all other configured values disable it.
	OpenAIResponseHeaderTimeout int
	// GrokResponseHeaderTimeout sets the Grok header wait in seconds when options
	// exist; absent options use 120 seconds, while a configured zero disables it.
	GrokResponseHeaderTimeout int
	OpenAIHTTP2               HTTP2Options // OpenAI HTTP/2 policy settings
}

// HTTP2Options configures HTTP/2 and proxy fallback thresholds.
// Egress records proxy compatibility failures and decides later request modes.
type HTTP2Options struct {
	Enabled                   bool // enable OpenAI HTTP/2
	AllowProxyFallbackToHTTP1 bool // permit proxy HTTP/1 recovery
	// FallbackErrorThreshold counts compatibility failures before fallback
	// activates; Client replaces nonpositive values with its default threshold.
	FallbackErrorThreshold int
	// FallbackWindowSeconds sets the failure-count window in seconds; Client
	// replaces nonpositive values with its default window.
	FallbackWindowSeconds int
	// FallbackTTLSeconds sets fallback lifetime in seconds; Client replaces
	// nonpositive values with its default lifetime.
	FallbackTTLSeconds int
}
