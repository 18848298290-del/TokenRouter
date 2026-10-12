package mediaentry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grokRealtimeAdapter opens a Realtime connection for the selected provider.
type grokRealtimeAdapter struct {
	h         *Runtime
	c         *gin.Context
	apiKey    *apikey.APIKey
	reqLog    *zap.Logger
	selection *gatewaycapture.SelectionResult
}

// grokVoiceAdapter connects provider selection, HTTP execution, and completion capture.
type grokVoiceAdapter struct {
	h            *Runtime
	c            *gin.Context
	apiKey       *apikey.APIKey
	subscription *billing.UserSubscription
	reqLog       *zap.Logger
	selection    *gatewaycapture.SelectionResult
}

func grokRealtimeBillingResult(model string, elapsed time.Duration, audioObserved bool) *forwardcore.OpenAIResult {
	usage := gatewaymedia.RealtimeAudioUsage(elapsed, audioObserved)
	if usage == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{RequestID: gatewaycapture.StableRealtimeBillingRequestID(""), Model: model, Duration: elapsed, AudioUsage: usage}
}

// recordGrokVoiceUsage settles TTS, STT, or Realtime audio usage using group audio pricing.
func (h *Runtime) recordGrokVoiceUsage(
	c *gin.Context,
	apiKey *apikey.APIKey,
	provider *gatewaycapture.ExecutionProvider,
	subscription *billing.UserSubscription,
	endpoint string,
	body []byte,
	result *forwardcore.OpenAIResult,
) {
	if h == nil || c == nil || apiKey == nil || provider == nil || result == nil {
		return
	}
	if result.AudioUsage == nil {
		return
	}
	// Generate a settlement ID for Realtime, TTS, and STT when the caller omitted one.
	if mode := strings.TrimSpace(result.AudioUsage.Mode); mode == "realtime" {
		result.RequestID = gatewaycapture.StableRealtimeBillingRequestID(result.RequestID)
	} else {
		result.RequestID = gatewaycapture.StableAudioBillingRequestID(result.RequestID)
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	sessionID := gatewayhttp.ExtractClientSessionID(c)
	requestPayloadHash := billing.HashUsageRequestPayload(body)
	if requestPayloadHash == "" {
		requestPayloadHash = billing.HashUsageRequestPayload([]byte(endpoint))
	}
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(c, provider.Record.Platform)

	model := strings.TrimSpace(result.Model)
	if model == "" {
		model = endpoint
	}

	pricingFields := gatewayhttp.ClientRequestedUsageFields(c, routing.GroupMappingResult{}, model, result.UpstreamModel)
	completionInput := gatewaycapture.CaptureOpenAI(c.Request.Context(), &gatewaycapture.OpenAICapture{
		Result:             result,
		APIKey:             apiKey,
		User:               apiKey.User,
		Provider:           gatewaycapture.ExecutionCompletionRecord(provider),
		Subscription:       subscription,
		InboundEndpoint:    inboundEndpoint,
		UpstreamEndpoint:   upstreamEndpoint,
		UserAgent:          userAgent,
		IPAddress:          clientIP,
		RequestPayloadHash: requestPayloadHash,
		APIKeyService:      h.bindings.Quota,

		ClientSessionID:    sessionID,
		PricingUsageFields: pricingFields,
	})
	completionRecorder := h.bindings.Common.Recorder
	completionLog := logging.L().With(
		zap.String("component", "handler.openai_gateway.grok_voice"),
		zap.Int64("user_id", apiKey.User.ID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("endpoint", endpoint),
		zap.Int64("provider_id", provider.Record.ID),
	)
	h.bindings.Common.Support.Submission.SubmitMandatory(c, func(ctx context.Context) {
		if err := completionRecorder.Record(ctx, completionInput, true); err != nil {
			completionLog.Error("grok_voice.record_usage_failed", zap.Error(err))
		}
	})
}

func (p *grokRealtimeAdapter) SelectRealtime(ctx context.Context, excluded map[int64]struct{}) (providercore.ProviderSnapshot, bool, error) {
	selected, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", "", "", excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityTextGeneration, false, false, capability.PlatformGrok)
	p.selection = selected
	if selected == nil || selected.Provider == nil {
		return providercore.ProviderSnapshot{}, false, err
	}
	return gatewaycapture.ExecutionSnapshot(selected.Provider), true, err
}

func (p *grokRealtimeAdapter) AcquireRealtime(_ context.Context, _ providercore.ProviderSnapshot) (func(), bool) {
	var started bool
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, "", p.selection, false, &started, p.reqLog)
}

func (p *grokRealtimeAdapter) RealtimeCredential(ctx context.Context, _ providercore.ProviderSnapshot) (string, error) {
	token, _, err := p.h.bindings.Platform.Credential(ctx, p.c, p.selection.Provider)
	return token, err
}

func (p *grokRealtimeAdapter) OpenRealtime(ctx context.Context, _ providercore.ProviderSnapshot, token, model string) (upstream.FrameConn, error) {
	conn, err := p.h.bindings.Platform.OpenRealtime(ctx, p.selection.Provider, token, model)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (p *grokRealtimeAdapter) RealtimeOpenFailed(ctx context.Context, selected providercore.ProviderSnapshot, err error) {
	p.reqLog.Warn("grok_realtime.pre_accept_failed", zap.Int64("provider_id", selected.ID), zap.Error(err))
	status := http.StatusBadGateway
	var dialErr *grok.RealtimeDialError
	if errors.As(err, &dialErr) && dialErr.StatusCode > 0 {
		status = dialErr.StatusCode
	}
	p.h.bindings.Platform.RealtimeError(ctx, p.selection.Provider, status, []byte(err.Error()))
}

func (p *grokVoiceAdapter) SelectVoice(ctx context.Context, excluded map[int64]struct{}) (providercore.ProviderSnapshot, bool, error) {
	selected, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", "", "grok-4.5", excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityTextGeneration, false, false, capability.PlatformGrok)
	p.selection = selected
	if selected == nil || selected.Provider == nil {
		return providercore.ProviderSnapshot{}, false, err
	}
	return gatewaycapture.ExecutionSnapshot(selected.Provider), true, err
}

func (p *grokVoiceAdapter) AcquireVoice(_ context.Context, _ providercore.ProviderSnapshot) (func(), bool) {
	var started bool
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, "", p.selection, false, &started, p.reqLog)
}

func (p *grokVoiceAdapter) ForwardVoice(ctx context.Context, _ providercore.ProviderSnapshot, request gatewaymedia.VoiceRequest) gatewaymedia.VoiceOutcome {
	result, err := p.h.bindings.Platform.Voice(ctx, p.c, p.selection.Provider, request.Endpoint, request.Body, request.ContentType)
	outcome := gatewaymedia.VoiceOutcome{Err: err}
	if result != nil {
		outcome.Result = &gatewaymedia.VoiceResult{RequestID: result.RequestID, Headers: http.Header(result.UpstreamHeaders).Clone(), Model: result.Model, UpstreamModel: result.UpstreamModel, Duration: result.Duration, AudioUsage: cloneGrokAudioUsage(result.AudioUsage)}
	}
	if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		outcome.RetryNext = failure.ShouldRetryNextProvider()
	}
	return outcome
}

func (p *grokVoiceAdapter) CompleteVoice(_ context.Context, _ providercore.ProviderSnapshot, request gatewaymedia.VoiceRequest, result *gatewaymedia.VoiceResult) {
	if result == nil {
		return
	}
	value := &forwardcore.OpenAIResult{RequestID: result.RequestID, UpstreamHeaders: http.Header(result.Headers).Clone(), Model: result.Model, UpstreamModel: result.UpstreamModel, Duration: result.Duration, AudioUsage: cloneGrokAudioUsage(result.AudioUsage)}
	p.h.recordGrokVoiceUsage(p.c, p.apiKey, p.selection.Provider, p.subscription, request.Endpoint, request.Body, value)
}

// cloneGrokAudioUsage gives completion its own usage snapshot.
func cloneGrokAudioUsage(value *protocol.AudioUsage) *protocol.AudioUsage {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (p *grokRealtimeAdapter) Relay(ctx context.Context, client, server upstream.FrameConn) (bool, error) {
	return p.h.bindings.Platform.RelayRealtime(ctx, client, server)
}

func (p *grokRealtimeAdapter) CompleteRealtime(_ context.Context, _ providercore.ProviderSnapshot, model string, elapsed time.Duration) {
	subscription, _ := gatewayhttp.SubscriptionFromContext(p.c)
	result := grokRealtimeBillingResult(model, elapsed, true)
	if result != nil {
		p.h.recordGrokVoiceUsage(p.c, p.apiKey, p.selection.Provider, subscription, "realtime", nil, result)
	}
}
