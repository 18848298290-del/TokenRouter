package server

// Options contains listener and HTTP parameters resolved by app from configuration.
type Options struct {
	Address                  string
	Mode                     string
	TrustedProxies           []string
	TrustedProxiesConfigured bool
	ReadHeaderTimeout        int
	IdleTimeout              int
	MaxHeaderBytes           int
	MaxRequestBodySize       int64
	H2C                      H2COptions
}

// H2COptions configures cleartext HTTP/2 connections and upload buffers.
type H2COptions struct {
	Enabled                      bool
	MaxConcurrentStreams         uint32
	IdleTimeout                  int
	MaxReadFrameSize             int
	MaxUploadBufferPerConnection int
	MaxUploadBufferPerStream     int
}
