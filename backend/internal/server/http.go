package server

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/wire"
	"golang.org/x/net/http2"

	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
)

// ProviderSet supplies the router and HTTP server to Wire.
var ProviderSet = wire.NewSet(
	ProvideRouter,
	ProvideHTTPServer,
)

// ProvideRouter builds the router with recovery and trusted-proxy settings.
func ProvideRouter(cfg Options, runtime *RouterRuntime) *gin.Engine {
	if cfg.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(middleware2.Recovery())
	configureTrustedProxies(r, cfg)

	return SetupRouter(r, runtime)
}

func configureTrustedProxies(r *gin.Engine, cfg Options) {
	if cfg.TrustedProxiesConfigured {
		if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
			log.Printf("Failed to set trusted proxies: %v", err)
			_ = r.SetTrustedProxies(nil)
		}
		if len(cfg.TrustedProxies) == 0 && cfg.Mode == "release" {
			log.Printf("Warning: server.trusted_proxies is explicitly empty; forwarded client IP trust is disabled")
		}
	} else {
		if err := r.SetTrustedProxies(nil); err != nil {
			log.Printf("Failed to disable trusted proxies: %v", err)
		}
		if cfg.Mode == "release" {
			log.Printf("Warning: server.trusted_proxies is not configured; disabling the forwarded-IP compatibility switch will use direct peer addresses only")
		}
	}
}

// ProvideHTTPServer configures HTTP serving limits and optional H2C.
// @project-doc docs/operations/edge_security.md#http_ingress_limits
func ProvideHTTPServer(cfg Options, router *gin.Engine) *http.Server {
	httpHandler := http.Handler(router)
	server := &http.Server{
		Addr:           cfg.Address,
		Handler:        httpHandler,
		MaxHeaderBytes: cfg.MaxHeaderBytes,
		// Bound header reads to limit slow-header attacks.
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeout) * time.Second,
		// Release inactive connections after the configured idle timeout.
		IdleTimeout: time.Duration(cfg.IdleTimeout) * time.Second,
		// Leave WriteTimeout unset because streaming responses can last many minutes.
		// Leave ReadTimeout unset because large request bodies can take longer to upload.
	}

	globalMaxSize := cfg.MaxRequestBodySize
	if globalMaxSize > 0 {
		httpHandler = http.MaxBytesHandler(httpHandler, globalMaxSize)
		log.Printf("Global max request body size: %d bytes (%.2f MB)", globalMaxSize, float64(globalMaxSize)/(1<<20))
	}

	// Enable cleartext HTTP/2 when configured.
	if cfg.H2C.Enabled {
		h2cConfig := cfg.H2C
		//nolint:staticcheck // HTTP/1 and h2c need separate idle timeouts; http.HTTP2Config has no separate IdleTimeout field.
		if err := http2.ConfigureServer(server, &http2.Server{
			MaxConcurrentStreams:         h2cConfig.MaxConcurrentStreams,
			IdleTimeout:                  time.Duration(h2cConfig.IdleTimeout) * time.Second,
			MaxReadFrameSize:             uint32(h2cConfig.MaxReadFrameSize),
			MaxUploadBufferPerConnection: int32(h2cConfig.MaxUploadBufferPerConnection),
			MaxUploadBufferPerStream:     int32(h2cConfig.MaxUploadBufferPerStream),
		}); err != nil {
			log.Printf("Failed to configure HTTP/2 Cleartext (h2c): %v", err)
		} else {
			protocols := new(http.Protocols)
			protocols.SetHTTP1(true)
			protocols.SetUnencryptedHTTP2(true)
			server.Protocols = protocols
			log.Printf("HTTP/2 Cleartext (h2c) enabled: max_concurrent_streams=%d, idle_timeout=%ds, max_read_frame_size=%d, max_upload_buffer_per_connection=%d, max_upload_buffer_per_stream=%d",
				h2cConfig.MaxConcurrentStreams,
				h2cConfig.IdleTimeout,
				h2cConfig.MaxReadFrameSize,
				h2cConfig.MaxUploadBufferPerConnection,
				h2cConfig.MaxUploadBufferPerStream,
			)
		}
	}

	server.Handler = httpHandler
	return server
}
