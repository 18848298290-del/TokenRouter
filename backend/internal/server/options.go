package server

// HTTP listeners serve short panel calls and long-lived generation streams.
// App resolves process configuration into these values before construction so
// the server can bound headers, uploads, and idle connections while request
// owners choose deadlines for active generation.

// Options contains listener and HTTP parameters resolved by app from configuration.
type Options struct {
	Address string // listener host and port
	// Mode selects Gin release mode when equal to "release"; other values leave
	// Gin's current process-wide mode intact during ProvideRouter.
	Mode string
	// TrustedProxies and TrustedProxiesConfigured distinguish a supplied peer
	// policy from absent configuration. configureTrustedProxies disables Gin
	// forwarding trust for absent, empty, or invalid lists.
	TrustedProxies           []string // trusted peer CIDRs or IPs
	TrustedProxiesConfigured bool     // supplied proxy policy flag
	// ReadHeaderTimeout measures the header budget in seconds. Zero leaves
	// header reads without a shared listener deadline because ReadTimeout is zero.
	ReadHeaderTimeout int
	// IdleTimeout measures HTTP/1 waiting between requests in seconds. Zero
	// leaves that wait without an idle deadline because ReadTimeout is also zero.
	IdleTimeout int
	// MaxHeaderBytes bounds the HTTP request line and headers. Zero selects the
	// standard library's DefaultMaxHeaderBytes when the server parses requests.
	MaxHeaderBytes int
	// MaxRequestBodySize wraps the router with an absolute byte limit when
	// positive. Nonpositive values leave body limits to handlers and middleware.
	MaxRequestBodySize int64
	H2C                H2COptions // cleartext HTTP/2 connection settings
}

// H2COptions configures cleartext HTTP/2 limits passed to x/net/http2.
type H2COptions struct {
	// Enabled invokes HTTP/2 configuration. ProvideHTTPServer advertises
	// unencrypted HTTP/2 after that call succeeds and logs configuration errors.
	Enabled bool
	// MaxConcurrentStreams bounds open streams per connection. Zero asks the
	// transport to select its default stream limit during connection setup.
	MaxConcurrentStreams uint32
	// IdleTimeout is the H2C idle budget in seconds. At zero, x/net/http2
	// inherits the HTTP server's idle value, which may itself disable that deadline.
	IdleTimeout int
	// MaxReadFrameSize, MaxUploadBufferPerConnection, and MaxUploadBufferPerStream
	// are byte counts passed to x/net/http2. Zero values select transport defaults;
	// callers supply positive values within that adapter's valid ranges.
	MaxReadFrameSize             int // maximum accepted frame bytes
	MaxUploadBufferPerConnection int // connection upload window bytes
	MaxUploadBufferPerStream     int // stream upload window bytes
}
