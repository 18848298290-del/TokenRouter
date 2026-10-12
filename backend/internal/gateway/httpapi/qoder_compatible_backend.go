package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

func (h *QoderCompatibleRuntime) Enter() (func(), error) {
	if h.options.Enter != nil {
		return h.options.Enter()
	}
	return func() {}, nil
}

func (h *QoderCompatibleRuntime) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	key, ok := h.options.ReadAccess(c)
	return apikey.CopyAPIKey(key), ok
}

func (h *QoderCompatibleRuntime) BindErrors(c *gin.Context) {
	if h.options.Rules != nil {
		BindErrorPassthroughService(c, h.options.Rules)
	}
}

func (h *QoderCompatibleRuntime) PrepareClient(c *gin.Context, body []byte, endpoint QoderEndpoint) {
	prepareQoderRequestContext(c, body, endpoint)
}

func (h *QoderCompatibleRuntime) ObserveRequest(c *gin.Context, model string, stream bool) {
	SetOpsRequestContext(c, model, stream)
}

func (h *QoderCompatibleRuntime) ObserveEndpoint(c *gin.Context, stream bool) {
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
}

func (h *QoderCompatibleRuntime) ObserveAuthLatency(c *gin.Context, d time.Duration) {
	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, d.Milliseconds())
}

func (h *QoderCompatibleRuntime) Plan(c *gin.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	if h.options.Execution == nil {
		return routing.RoutePlan{}
	}
	old := apikey.CopyAPIKey(key)
	plan := h.options.Execution.Plan(c.Request.Context(), old, model)
	c.Request = c.Request.WithContext(requeststate.WithRoutePlan(c.Request.Context(), plan))
	return plan
}

func (h *QoderCompatibleRuntime) AttemptBody(body []byte, plan routing.RoutePlan) []byte {
	mapping := plan.Mapping()
	if h.options.Execution != nil && mapping.Mapped {
		return openaiprotocol.ReplaceModelInBody(body, mapping.MappedModel)
	}
	return body
}

func (h *QoderCompatibleRuntime) Eligibility(ctx context.Context, key *apikey.APIKey, sub *billing.UserSubscription) error {
	if h.options.Funding == nil {
		return nil
	}
	old := apikey.CopyAPIKey(key)
	return h.options.Funding.CheckKey(ctx, old, sub, "", false)
}

func (h *QoderCompatibleRuntime) SessionHash(c *gin.Context, endpoint QoderEndpoint, body []byte, id int64) string {
	return h.qoderSessionHash(c, endpoint, body, id)
}

func (h *QoderCompatibleRuntime) ConcurrencyError(c *gin.Context, err error, kind string, started bool, endpoint QoderEndpoint) {
	h.handleConcurrencyError(c, err, kind, started, endpoint)
}

func (h *QoderCompatibleRuntime) Execution(c *gin.Context, call QoderCompatibleCall) textflow.QoderCompatiblePorts {
	apiKey := apikey.CopyAPIKey(call.Key)
	subscription := call.Subscription
	body := call.Body
	reqModel := call.Model
	reqLog := call.Log
	endpoint := call.Endpoint
	groupMapping := call.Plan.Mapping()

	recordUsage := func(provider QoderCompatibleTarget, result *forwardcore.MessagesResult) {
		userAgent := c.GetHeader("User-Agent")
		clientIP := clientip.GetClientIP(c)
		requestPayloadHash := billing.HashUsageRequestPayload(body)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, provider.Snapshot().Platform)

		// Capture funding and request data before queueing so the worker uses this snapshot.
		completionInput := provider.Completion(CompletionContext(c), QoderCompletionCapture{
			Result: result, Key: apiKey, Subscription: subscription,
			InboundEndpoint: inboundEndpoint, UpstreamEndpoint: upstreamEndpoint, UserAgent: userAgent, ClientIP: clientIP,
			PayloadHash: requestPayloadHash, Body: append([]byte(nil), body...), Pricing: groupMapping.ToUsageFields(reqModel, result.UpstreamModel),
		})
		completionRuntime := h.options.Recorder
		h.submitUsageRecordTask(c, func(ctx context.Context) {
			if err := completionRuntime.Record(ctx, completionInput, false); err != nil {
				reqLog.Error("qoder.record_usage_failed", zap.Int64("provider_id", completionInput.Provider.ID), zap.Error(err))
			}
		})
	}

	// finishPartial records observed usage and ends the request with its error after any partial result.
	finishPartial := func(provider QoderCompatibleTarget, result *forwardcore.MessagesResult, forwardErr error) bool {
		if forwardErr == nil || result == nil {
			return false
		}
		recordUsage(provider, result)
		if qoderRequestCanceled(c.Request.Context(), forwardErr) {
			return true
		}
		status, kind, message, ok := h.qoderGatewayErrorDetails(c, forwardErr)
		if !ok {
			status = http.StatusBadGateway
			kind = "upstream_error"
			message = "Upstream request failed"
		}
		SetOpsUpstreamError(c, h.options.Errors.Describe(forwardErr).SourceStatus, message, "")
		h.streamingAwareError(c, status, kind, message, true, endpoint)
		return true
	}
	return &qoderCompatibleAttemptBridge{h: h, c: c, endpoint: endpoint, key: apiKey, subjectID: call.Subject.UserID, hash: call.SessionHash, model: reqModel, stream: call.Stream, streamStarted: call.StreamStarted, body: call.AttemptBody, log: reqLog, record: recordUsage, partial: finishPartial}
}
