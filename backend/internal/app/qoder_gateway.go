package app

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// qoder_gateway.go assembles Qoder Chat HTTP handling and captures request
// inputs for execution. Qoder Chat preparation captures body, headers, Key
// funding, and HTTP metadata before handing the request to its executor.
// Observation methods keep Gin access in the request path, while request and
// platform attempts share a Qoder activity barrier in addition to the general
// gateway barrier.

// qoderHTTPObservation binds Qoder preparation and waiting observations to the
// current HTTP request.
type qoderHTTPObservation struct {
	// c identifies the active HTTP request, stream controls wait output, and
	// started is updated by HTTP waiting. These values remain in the request observer while completion input is captured separately.
	c               *gin.Context // observed HTTP request
	stream, started bool         // stream: request streaming flag; started: heartbeat commitment flag
}

// qoderRequestActivity tracks accepted Qoder requests and attempts for
// application shutdown.
type qoderRequestActivity struct {
	*lifecycle.Operations // accepted Qoder operations
}

// provideQoderChat assembles Chat execution with three switches and a
// thirty-second wait input. Required activity and request barriers are attached
// before serving; preparation checks Key and subject first, copies
// body/headers, and captures the effective Key view with the resolved
// subscription.
func provideQoderChat(planner *gatewayprovider.RoutePlanner, q *gatewayprovider.QoderRuntime, refresh *provideradapter.QoderRequestRefresh, c *scheduler.ConcurrencyService, b *admission.FundingAdmission, k *apikey.APIKeyService, r *errorpolicy.ErrorPassthroughService, pool *completion.UsageRecordWorkerPool, recorders GatewayCompletionRecorders, activity *qoderRequestActivity, requests *gatewayRequestActivity, choices *selection.Generic) *gatewayhttp.QoderChatHandler {
	runtime := &qoderRuntime{Routes: planner, Choices: choices, Qoder: q, Refresh: refresh, Billing: b, Keys: k, Completions: pool, Recorder: recorders.Forward}
	useCase := gateway.NewQoderExecutor(3, 30*time.Second, c, runtime)
	useCase.Enter = activity.Enter
	var matcher gatewayhttp.ErrorRuleMatcher
	if r != nil {
		matcher = r
	}
	presenter := gatewayhttp.QoderErrorPresenter{Rules: matcher, Describe: gatewayprovider.DescribeQoderError, ReadAccess: keyhttp.GetAPIKeyFromContext, Catalogue: gatewayprovider.ModelDisplayCatalogue{}}
	result := &gatewayhttp.QoderChatHandler{Executor: useCase, Failure: presenter.Failure}
	result.BindRequestActivity(requests.Enter)
	result.Preflight = qoderPreflight
	result.PrepareRequest = func(c *gin.Context, parsed gatewayhttp.ParsedRequest) (gateway.Request, error) {
		if err := qoderPreflight(c); err != nil {
			return gateway.Request{}, err
		}
		key, _ := keyhttp.GetAPIKeyFromContext(c)
		subject, _ := authctx.GetAuthSubjectFromContext(c)
		subscription, _ := gatewayhttp.SubscriptionFromContext(c)
		if r != nil {
			gatewayhttp.BindErrorPassthroughService(c, r)
		}
		gatewayhttp.SetOpsRequestContext(c, parsed.Model, parsed.Stream)
		gatewayhttp.SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(parsed.Stream, false)))
		var access *apikey.AccessSnapshot
		if value, ok := c.Get("apikey_access_snapshot"); ok {
			access, _ = value.(*apikey.AccessSnapshot)
		}
		keyView, ok := gatewayhttp.EffectiveAPIKey(c)
		if !ok {
			// Preparation can lack an effective request Key binding. Use the
			// authenticated Key's copied view so funding preparation still receives the known request identity.
			keyView = apikey.CopyAPIKey(key)
		}
		inbound, outbound := gatewayhttp.GetInboundEndpoint(c), gatewayhttp.GetUpstreamEndpoint(c, capability.PlatformQoder)
		request := gateway.Request{
			Access: access, UserID: subject.UserID, Concurrency: subject.Concurrency, Stream: parsed.Stream,
			Body: append([]byte(nil), parsed.Body...), Model: parsed.Model,
			Funding:  gateway.FundingState{Key: keyView, Subscription: subscription},
			Metadata: gateway.RequestMetadata{Headers: c.Request.Header.Clone(), UserAgent: c.GetHeader("User-Agent"), ClientIP: clientip.GetClientIP(c), InboundEndpoint: inbound, UpstreamEndpoint: outbound, ClaudeCode: requeststate.IsClaudeCodeClient(c.Request.Context()), StartedAt: parsed.StartedAt},
		}
		request.SessionHash = session.QoderRequestHash(request.Metadata.Headers, request.Body, "anthropic", &requeststate.SessionContext{ClientIP: request.Metadata.ClientIP, UserAgent: request.Metadata.UserAgent, APIKeyID: key.ID}, slog.Info)
		return request, nil
	}
	result.Observer = func(c *gin.Context, request gateway.Request) gateway.ExecutionObserver {
		return &qoderHTTPObservation{c: c, stream: request.Stream}
	}
	return result
}

// qoderPreflight checks Key lookup before authenticated subject lookup. Missing
// Key returns the authentication failure, missing subject returns the internal
// context failure, and these checks run before the Chat handler reads its
// request body.
func qoderPreflight(c *gin.Context) error {
	if _, ok := keyhttp.GetAPIKeyFromContext(c); !ok {
		return &gatewayhttp.HTTPFailure{Status: 401, Type: "authentication_error", Message: "Invalid API key"}
	}
	if _, ok := authctx.GetAuthSubjectFromContext(c); !ok {
		return &gatewayhttp.HTTPFailure{Status: 500, Type: "api_error", Message: "User context not found"}
	}
	return nil
}

// Prepared publishes the resolved route and elapsed request time to HTTP
// observations.
func (o *qoderHTTPObservation) Prepared(request gateway.Request) {
	o.c.Request = o.c.Request.WithContext(requeststate.WithRoutePlan(o.c.Request.Context(), request.Route))
	// Observations store elapsed milliseconds from the parsed request start time.
	gatewayhttp.SetOpsLatencyMs(o.c, gatewayhttp.OpsAuthLatencyMsKey, time.Since(request.Metadata.StartedAt).Milliseconds())
}

// Selected publishes provider identity and diagnostic header selection to HTTP
// request observations.
func (o *qoderHTTPObservation) Selected(snapshot provider.ProviderSnapshot) {
	gatewayhttp.SetOpsSelectedProvider(o.c, snapshot)
}

// Waiting returns request-bound wait observation with ten-second comment pings.
// The stream and started fields are passed to HTTP waiting so heartbeat output
// can record response commitment for this request.
func (o *qoderHTTPObservation) Waiting(string) scheduler.WaitObserver {
	return gatewayhttp.WaitObserver(o.c, gatewayhttp.SSEPingFormatComment, 10*time.Second, o.stream, &o.started, true)
}

// provideQoderRequestActivity registers a shared request/attempt barrier before
// lifecycle startup. It binds the same Enter callback to Qoder runtime attempts
// and stops at level fifteen before completion queues and shared dependencies
// drain.
func provideQoderRequestActivity(manager *lifecycle.Manager, runtime *gatewayprovider.QoderRuntime) *qoderRequestActivity {
	activity := lifecycle.NewOperations("QoderRequestsAndAttempts")
	// Stop request and attempt admission together, then wait for accepted work
	// before completion recording and the shared platform dependencies enter later cleanup phases.
	manager.Register(lifecycle.Hook{Name: "QoderRequestsAndAttempts", StopOrder: shutdownStageRequests, Stop: activity.StopContext})
	runtime.BindAttemptActivity(activity.Enter)
	return &qoderRequestActivity{Operations: activity}
}
