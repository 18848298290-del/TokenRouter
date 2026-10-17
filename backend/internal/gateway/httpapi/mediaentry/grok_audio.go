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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_audio.go adapts Grok Voice and Realtime selection and captures observed
// audio usage for completion.
// Voice completion starts from measured audio units, and Realtime produces
// units only after audio has actually been observed during a positive session
// duration. The saved provider selection connects slot acquisition, credential
// resolution and execution for one HTTP or WebSocket request.
//
// Before submitting usage, the adapter retains or generates a prefixed
// financial request identity and captures caller, provider, endpoint and
// payload evidence. A body-free operation can use the endpoint as its payload
// fingerprint input. Forwarded Voice results clone headers and scalar
// AudioUsage records as they cross adapter layers, so completion consumes the
// captured request data after the original execution returns.

// grokRealtimeAdapter retains one request's selected provider through Realtime
// setup and completion.
type grokRealtimeAdapter struct {
	h      *Runtime       // shared media runtime
	c      *gin.Context   // active HTTP request
	apiKey *apikey.APIKey // authenticated caller key
	reqLog *zap.Logger    // request diagnostic logger
	// selection is replaced by SelectRealtime. Later operations require a
	// successful selection and use its provider rather than snapshot arguments.
	selection *gatewaycapture.SelectionResult
}

// grokVoiceAdapter retains HTTP funding and provider selection for Voice
// completion.
type grokVoiceAdapter struct {
	h      *Runtime       // shared media runtime
	c      *gin.Context   // active HTTP request
	apiKey *apikey.APIKey // authenticated caller key
	// subscription is captured for this Voice request and can be nil for
	// balance funding; completion receives the same optional funding choice.
	subscription *billing.UserSubscription // optional request funding subscription
	reqLog       *zap.Logger               // request diagnostic logger
	// selection is replaced by SelectVoice. Later operations require a
	// successful selection and use its provider rather than snapshot arguments.
	selection *gatewaycapture.SelectionResult
}

// grokRealtimeBillingResult returns a new Realtime billing result from elapsed
// time and observed audio. Missing audio or nonpositive duration returns nil;
// a new connection receives a generated prefixed request ID and minutes-based
// usage.
func grokRealtimeBillingResult(model string, elapsed time.Duration, audioObserved bool) *forwardcore.OpenAIResult {
	usage := gatewaymedia.RealtimeAudioUsage(elapsed, audioObserved)
	if usage == nil {
		return nil
	}
	return &forwardcore.OpenAIResult{RequestID: gatewaycapture.StableRealtimeBillingRequestID(""), Model: model, Duration: elapsed, AudioUsage: usage}
}

// recordGrokVoiceUsage captures measured TTS, STT or Realtime usage for
// mandatory completion submission. Missing outer dependencies or AudioUsage
// returns immediately; callers provide a hydrated Key.User when recording
// proceeds. Existing prefixed request IDs are retained, and missing IDs are
// generated through the mode-specific helper.
//
// The payload fingerprint uses body bytes, then endpoint text when no body
// hash exists. Endpoint and provider identities, client metadata, funding and
// pricing fields are captured before the queued callback runs. Recording
// errors are logged after execution; this operation performs no additional
// upstream request and leaves completion persistence to the shared recorder.
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
	// Keep an existing prefixed financial key when completion receives it; blank
	// identities need one generated seed so later recording can retain that same key.
	if mode := strings.TrimSpace(result.AudioUsage.Mode); mode == "realtime" {
		result.RequestID = gatewaycapture.StableRealtimeBillingRequestID(result.RequestID)
	} else {
		result.RequestID = gatewaycapture.StableAudioBillingRequestID(result.RequestID)
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	sessionID := gatewayhttp.ExtractClientSessionID(c)
	requestPayloadHash := billing.HashUsageRequestPayload(body)
	// Body-free resource/session calls still need fingerprint input for completion;
	// the endpoint identifies that operation when no request body supplies a hash.
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
	// Capture request metadata before submission; the callback reads completionInput
	// and its captured logger after Gin request state may have been released.
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

// SelectRealtime selects a Grok provider under the current Key group and
// exclusion set. It saves the selected execution target; absent
// selection/provider returns false with its error, and later setup requires
// true.
func (p *grokRealtimeAdapter) SelectRealtime(ctx context.Context, excluded map[int64]struct{}) (providercore.ProviderSnapshot, bool, error) {
	selected, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", "", "", excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityTextGeneration, false, false, capability.PlatformGrok)
	p.selection = selected
	if selected == nil || selected.Provider == nil {
		return providercore.ProviderSnapshot{}, false, err
	}
	return gatewaycapture.ExecutionSnapshot(selected.Provider), true, err
}

// AcquireRealtime acquires or waits for the saved selection's provider slot
// through the HTTP support adapter. It ignores context/snapshot arguments,
// uses the active request and requires SelectRealtime to have captured a
// provider.
func (p *grokRealtimeAdapter) AcquireRealtime(_ context.Context, _ providercore.ProviderSnapshot) (func(), bool) {
	var started bool
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, "", p.selection, false, &started, p.reqLog)
}

// RealtimeCredential resolves credentials for the provider saved by successful
// Realtime selection. It returns that callback's token and error, ignoring the
// supplied snapshot because execution continues against the captured request
// target.
func (p *grokRealtimeAdapter) RealtimeCredential(ctx context.Context, _ providercore.ProviderSnapshot) (string, error) {
	token, _, err := p.h.bindings.Platform.Credential(ctx, p.c, p.selection.Provider)
	return token, err
}

// OpenRealtime opens the saved provider using supplied token and model. A
// platform opening error returns no connection; the successful result is
// passed to Realtime orchestration, which owns closing its acquired session.
func (p *grokRealtimeAdapter) OpenRealtime(ctx context.Context, _ providercore.ProviderSnapshot, token, model string) (upstream.FrameConn, error) {
	conn, err := p.h.bindings.Platform.OpenRealtime(ctx, p.selection.Provider, token, model)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// RealtimeOpenFailed logs failed pre-accept setup and observes provider health
// for the saved selection. A positive RealtimeDialError status overrides the
// 502 default; the snapshot argument supplies the diagnostic provider ID.
func (p *grokRealtimeAdapter) RealtimeOpenFailed(ctx context.Context, selected providercore.ProviderSnapshot, err error) {
	p.reqLog.Warn("grok_realtime.pre_accept_failed", zap.Int64("provider_id", selected.ID), zap.Error(err))
	status := http.StatusBadGateway
	var dialErr *grok.RealtimeDialError
	if errors.As(err, &dialErr) && dialErr.StatusCode > 0 {
		status = dialErr.StatusCode
	}
	p.h.bindings.Platform.RealtimeError(ctx, p.selection.Provider, status, []byte(err.Error()))
}

// SelectVoice selects a Grok provider using grok-4.5 as the scheduling model.
// It saves the execution target and returns false for absent
// selection/provider, with the error retained for Voice orchestration.
func (p *grokVoiceAdapter) SelectVoice(ctx context.Context, excluded map[int64]struct{}) (providercore.ProviderSnapshot, bool, error) {
	selected, _, err := p.h.bindings.Common.Selection.SelectProviderWithSchedulerForCapability(ctx, p.apiKey.GroupID, "", "", "grok-4.5", excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityTextGeneration, false, false, capability.PlatformGrok)
	p.selection = selected
	if selected == nil || selected.Provider == nil {
		return providercore.ProviderSnapshot{}, false, err
	}
	return gatewaycapture.ExecutionSnapshot(selected.Provider), true, err
}

// AcquireVoice acquires or waits for the saved Voice selection's provider
// slot. It uses the active HTTP request and ignores supplied context/snapshot
// arguments, so callers first complete successful SelectVoice execution.
func (p *grokVoiceAdapter) AcquireVoice(_ context.Context, _ providercore.ProviderSnapshot) (func(), bool) {
	var started bool
	return p.h.bindings.Common.Support.AcquireResponsesProviderSlot(p.c, p.apiKey.GroupID, "", p.selection, false, &started, p.reqLog)
}

// ForwardVoice invokes the saved provider with endpoint, body and Content-Type
// from the Voice request. Any returned result clones headers and AudioUsage
// for the outcome; typed failover errors separately supply retry eligibility.
func (p *grokVoiceAdapter) ForwardVoice(ctx context.Context, _ providercore.ProviderSnapshot, request gatewaymedia.VoiceRequest) gatewaymedia.VoiceOutcome {
	ctx = telemetry.WithRequestProvider(ctx, p.selection.Provider.Record.ID, providercore.UpstreamRequestIDHeaderName(p.selection.Provider.View()))
	result, err := p.h.bindings.Platform.Voice(ctx, p.c, p.selection.Provider, request.Endpoint, request.Body, request.ContentType)
	outcome := gatewaymedia.VoiceOutcome{Err: err}
	if result != nil {
		outcome.Result = &gatewaymedia.VoiceResult{
			RequestAttempt: result.RequestAttempt,
			RequestID:      result.RequestID,
			Headers:        http.Header(result.UpstreamHeaders).Clone(),
			Model:          result.Model,
			UpstreamModel:  result.UpstreamModel,
			Duration:       result.Duration,
			AudioUsage:     cloneGrokAudioUsage(result.AudioUsage),
		}
	}
	if failure, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		outcome.RetryNext = failure.ShouldRetryNextProvider()
	}
	return outcome
}

// CompleteVoice captures the saved provider's returned audio result for
// completion. A nil result is ignored, while headers and scalar AudioUsage are
// copied before mode-specific financial identity and request metadata are
// recorded.
func (p *grokVoiceAdapter) CompleteVoice(_ context.Context, _ providercore.ProviderSnapshot, request gatewaymedia.VoiceRequest, result *gatewaymedia.VoiceResult) {
	if result == nil {
		return
	}
	value := &forwardcore.OpenAIResult{
		RequestAttempt:  result.RequestAttempt,
		RequestID:       result.RequestID,
		UpstreamHeaders: http.Header(result.Headers).Clone(),
		Model:           result.Model,
		UpstreamModel:   result.UpstreamModel,
		Duration:        result.Duration,
		AudioUsage:      cloneGrokAudioUsage(result.AudioUsage),
	}
	p.h.recordGrokVoiceUsage(p.c, p.apiKey, p.selection.Provider, p.subscription, request.Endpoint, request.Body, value)
}

// cloneGrokAudioUsage copies the scalar audio mode and quantity for the next
// adapter consumer. Nil input returns nil; the returned struct can be changed
// independently because AudioUsage currently contains no mutable references.
func cloneGrokAudioUsage(value *protocol.AudioUsage) *protocol.AudioUsage {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Relay delegates the saved runtime's bidirectional Realtime frame handling
// and audio-observed result.
func (p *grokRealtimeAdapter) Relay(ctx context.Context, client, server upstream.FrameConn) (bool, error) {
	return p.h.bindings.Platform.RelayRealtime(ctx, client, server)
}

// CompleteRealtime records a selected provider's audio session using elapsed
// duration. Callers invoke it after relay has confirmed audio; the method
// supplies that observed flag itself, reads the current optional subscription
// and skips a nonpositive duration before capturing completion.
func (p *grokRealtimeAdapter) CompleteRealtime(_ context.Context, _ providercore.ProviderSnapshot, model string, elapsed time.Duration) {
	subscription, _ := gatewayhttp.SubscriptionFromContext(p.c)
	result := grokRealtimeBillingResult(model, elapsed, true)
	if result != nil {
		p.h.recordGrokVoiceUsage(p.c, p.apiKey, p.selection.Provider, subscription, "realtime", nil, result)
	}
}
