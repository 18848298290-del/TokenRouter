package transport

// Options contains application-supplied transport settings.
// A nil Options pointer selects defaults; some zero fields disable limits.
type Options struct {
	ValidateResolvedIP          bool
	ConnectionPoolIsolation     string
	MaxUpstreamClients          int
	ClientIdleTTLSeconds        int
	MaxIdleConns                int
	MaxIdleConnsPerHost         int
	MaxConnsPerHost             int
	IdleConnTimeoutSeconds      int
	ResponseHeaderTimeout       int
	OpenAIResponseHeaderTimeout int
	GrokResponseHeaderTimeout   int
	OpenAIHTTP2                 HTTP2Options
}

// HTTP2Options configures HTTP/2 and proxy fallback thresholds.
// Egress owns the resulting fallback state.
type HTTP2Options struct {
	Enabled                   bool
	AllowProxyFallbackToHTTP1 bool
	FallbackErrorThreshold    int
	FallbackWindowSeconds     int
	FallbackTTLSeconds        int
}
