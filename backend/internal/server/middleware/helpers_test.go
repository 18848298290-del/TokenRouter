package middleware

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

type testLogSink struct {
	mu     sync.Mutex
	events []*logging.LogEvent
}

func init() {
	// TestMain sets Gin's test mode.
}

func init() {
}

// NewAPIKeyAuthMiddleware combines API-key authentication and subscription checks for middleware tests.
func NewAPIKeyAuthMiddleware(apiKeyService *apikey.APIKeyService, subscriptionService *billing.SubscriptionService, cfg *config.Config) keyhttp.APIKeyAuthMiddleware {
	return keyhttp.APIKeyAuthMiddleware(apiKeyAuthWithSubscription(apiKeyService, subscriptionService, cfg))
}

func apiKeyAuthWithSubscription(apiKeyService *apikey.APIKeyService, subscriptionService *billing.SubscriptionService, cfg *config.Config) gin.HandlerFunc {
	return newGatewayAuthorization(apiKeyService, subscriptionService, cfg, false)
}

// newGatewayAuthorization assembles API-key authentication and subscription checks for tests.
func newGatewayAuthorization(keys *apikey.APIKeyService, subscriptions *billing.SubscriptionService, cfg *config.Config, google bool) gin.HandlerFunc {
	options := gatewayhttp.APIKeyAuthorizationOptions{
		Authentication: keyhttp.AuthenticationOptions{
			Google: google, Context: func(c *gin.Context) context.Context { return c.Request.Context() },
			ClientIP: func(c *gin.Context) string {
				return clientip.GetSecurityClientIP(c, cfg.TrustForwardedIPForAPIKeyACL())
			},
			AbuseClientKey: invalidAuthClientKey, NonConsuming: func(c *gin.Context) bool {
				return gatewayhttp.IsAPIKeyNonConsumingRequest(c.Request.Method, c.Request.URL.Path)
			},
			Rejected: func(c *gin.Context, reason string) { MarkIngressRejected(c, IngressRejectReason(reason)) },
			BusinessLimited: func(c *gin.Context, reason string) {
				gatewayhttp.MarkOpsClientBusinessLimited(c, reason)
			},
			Loaded: func(c *gin.Context, key *apikey.APIKey) { keyhttp.SetOpsFallbackAPIKey(c, apikey.CopyAPIKey(key)) },
		},
		BindLegacyKey: func(c *gin.Context, key *apikey.APIKey) {
			legacy := apikey.CopyAPIKey(key)
			c.Set(string(keyhttp.ContextKeyAPIKey), legacy)
			setGroupContext(c, legacy.Group)
		},
	}
	var reader gatewayhttp.AuthorizationSubscriptions
	if subscriptions != nil {
		reader = subscriptions
	}
	var nativeKeys *apikey.APIKeyService
	if keys != nil {
		nativeKeys = keys
	}
	if google {
		return gatewayhttp.NewGoogleAPIKeyAuthorization(nativeKeys, reader, options)
	}
	return gatewayhttp.NewAPIKeyAuthorization(nativeKeys, reader, options)
}

// setGroupContext attaches a valid group to the test request context.
func setGroupContext(c *gin.Context, group *routing.Group) {
	if !routing.IsGroupContextValid(group) {
		return
	}
	if existing, ok := requeststate.GroupFromContext(c.Request.Context()); ok && existing != nil && existing.ID == group.ID && routing.IsGroupContextValid(existing) {
		return
	}
	ctx := requeststate.WithGroup(c.Request.Context(), group)
	c.Request = c.Request.WithContext(ctx)
}

// invalidAuthClientKey returns the client bucket for invalid-authentication requests.
func invalidAuthClientKey(c *gin.Context) string { return InvalidAuthClientKey(c) }

func (s *testLogSink) WriteLogEvent(event *logging.LogEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *testLogSink) list() []*logging.LogEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*logging.LogEvent, len(s.events))
	copy(out, s.events)
	return out
}

func initMiddlewareTestLogger(t *testing.T) *testLogSink {
	return initMiddlewareTestLoggerWithLevel(t, "debug")
}

func initMiddlewareTestLoggerWithLevel(t *testing.T, level string) *testLogSink {
	t.Helper()
	level = strings.TrimSpace(level)
	if level == "" {
		level = "debug"
	}
	if err := logging.Init(logging.InitOptions{
		Level:       level,
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: logging.OutputOptions{
			ToStdout: false,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}
	sink := &testLogSink{}
	logging.SetSink(sink)
	t.Cleanup(func() {
		logging.SetSink(nil)
	})
	return sink
}
