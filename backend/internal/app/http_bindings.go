package app

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	gatewayhttpapi "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
	routinghttpapi "github.com/TokenFlux/TokenRouter/internal/routing/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/server"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
	sitehttp "github.com/TokenFlux/TokenRouter/internal/site/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/web"
)

// httpRouteMount registers HTTP handlers built by app constructors.
type httpRouteMount func(*gin.Engine, httpRouteSecurity, gin.HandlerFunc, func(*gin.RouterGroup))

// provideRouterRuntime binds public data and HTTP handlers; each module owns its rules and state.
func provideRouterRuntime(requests *requestlog.Service, public *site.PublicService, pages *sitehttp.PageHandler, backendMode *admission.BackendMode, store *settings.Store, redisClient *redis.Client, manager *lifecycle.Manager, cfg *config.Config, mount httpRouteMount, panelSettings *runtimeconfig.PanelSettings, opsService *ops.OpsService, jwtAuth identityhttp.JWTAuthMiddleware, adminAuth identityhttp.AdminAuthMiddleware, auditLog middleware.AuditLogMiddleware, stepUpAuth identityhttp.StepUpAuthMiddleware,
) (*server.RouterRuntime, error) {
	manager.Register(lifecycle.Hook{Name: "SettingsUpdateAdmission", StopOrder: 14, Stop: func(context.Context) error { store.Updates().Seal(); return nil }})
	manager.Register(lifecycle.Hook{Name: "SettingsUpdates", StopOrder: 17, Stop: store.Updates().Stop})

	var defaultLanguage atomic.Value
	defaultLanguage.Store("en")
	refreshLanguage := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if value, err := store.GetValue(ctx, "default_locale"); err == nil {
			defaultLanguage.Store(value)
		}
	}
	refreshLanguage()
	store.Subscribe(refreshLanguage)
	var origins atomic.Pointer[[]string]
	empty := []string{}
	origins.Store(&empty)
	refresh := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		next, err := public.GetFrameSrcOrigins(ctx)
		if err == nil {
			origins.Store(&next)
		}
	}
	middleware.SetIngressRejectRecorder(opsService)
	rt := &server.RouterRuntime{Middleware: []gin.HandlerFunc{
		middleware.RequestLogger(requests.Observe),
		middleware.Locale(func() string { value, _ := defaultLanguage.Load().(string); return value }),
		identityhttp.SessionBindingContext(func() identityhttp.ForwardedIPSettings {
			value := cfg.ForwardedClientIPSettings()
			return identityhttp.ForwardedIPSettings{TrustForwardedIP: value.TrustForwardedIP, Headers: value.Headers}
		}), middleware.Logger(), middleware.CORS(cfg.CORS),
		middleware.SecurityHeaders(cfg.Security.CSP, func() []string { return *origins.Load() }), middleware.ServerTiming(cfg.Server.EnableServerTiming),
	}}
	endpoints := make(map[protocol.ProtocolID]string)
	for _, entry := range gatewayhttpapi.ProtocolEndpoints() {
		endpoints[entry.ID] = entry.Endpoint
	}
	protocolCatalog := routinghttpapi.NewProtocolCatalogHandler(endpoints)
	notify := refresh
	if web.HasEmbeddedFrontend() {
		frontend, err := web.NewFrontendServer(public)
		if err != nil {
			return nil, err
		}
		rt.Frontend = frontend.Middleware()
		notify = func() { frontend.InvalidateCache(); refresh() }
	}
	manager.Register(lifecycle.Hook{Name: "HTTPSettingsInitialization", StartOrder: 182, StopOrder: 800, Start: func(context.Context) error {
		refresh()
		return nil
	}})

	unsubscribe := store.Subscribe(notify)
	manager.Register(lifecycle.Hook{Name: "SettingsHTTPNotification", StopOrder: 800, Stop: func(context.Context) error { unsubscribe(); return nil }})
	counter := redisinfra.NewFixedWindowLimiter(redisClient, "rate_limit:")
	authLimiter := middleware.NewRateLimiter(counter)
	var panelCounter *middleware.RateLimiter
	if redisClient != nil {
		panelCounter = middleware.NewRateLimiter(counter)
	}
	panelLimiter := middleware.NewPanelRateLimiter(panelCounter, panelSettings)
	rt.Register = []func(*gin.Engine){func(r *gin.Engine) {
		mount(r, httpRouteSecurity{JWT: gin.HandlerFunc(jwtAuth), Admin: gin.HandlerFunc(adminAuth), Audit: gin.HandlerFunc(auditLog), StepUp: gin.HandlerFunc(stepUpAuth), BackendAuth: identityhttp.BackendModeAuthGuard(backendMode), BackendUser: identityhttp.BackendModeUserGuard(backendMode), Panel: panelLimiter, AuthLimiter: authLimiter}, protocolCatalog, func(v1 *gin.RouterGroup) {
			pages.Register(v1, gin.HandlerFunc(jwtAuth), gin.HandlerFunc(adminAuth))
			registerRequestLogRoutes(v1, requests, gin.HandlerFunc(adminAuth), gin.HandlerFunc(auditLog), panelLimiter.Heavy())
		})
	}}

	return rt, nil
}

// provideHTTPOptions reads listener settings and selects the body limit by configuration precedence.
func provideHTTPOptions(cfg *config.Config) server.Options {
	maxBody := cfg.Server.MaxRequestBodySize
	if maxBody <= 0 {
		maxBody = cfg.Gateway.MaxBodySize
	}
	h := cfg.Server.H2C
	return server.Options{
		Address: cfg.Server.Address(), Mode: cfg.Server.Mode, TrustedProxies: append([]string(nil), cfg.Server.TrustedProxies...), TrustedProxiesConfigured: cfg.Server.TrustedProxiesConfigured,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout, IdleTimeout: cfg.Server.IdleTimeout, MaxHeaderBytes: cfg.Server.MaxHeaderBytes, MaxRequestBodySize: maxBody,
		H2C: server.H2COptions{Enabled: h.Enabled, MaxConcurrentStreams: h.MaxConcurrentStreams, IdleTimeout: h.IdleTimeout, MaxReadFrameSize: h.MaxReadFrameSize, MaxUploadBufferPerConnection: h.MaxUploadBufferPerConnection, MaxUploadBufferPerStream: h.MaxUploadBufferPerStream},
	}
}
