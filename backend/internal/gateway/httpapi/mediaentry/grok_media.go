package mediaentry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_media.go restores Grok video ownership and submits image or claimed
// video usage for completion.
// A video status request needs the provider and group that accepted its task,
// even after a composite Key loses that group mapping. The adapter restores
// saved ownership into a request-local Key view and delegates completion
// claiming to the shared video-task service.
//
// Image usage can be recorded immediately, while video usage waits for
// completed observations and a claimed task. Completion captures funding,
// client metadata and pricing before queue submission. The task identifier
// used to release a failed claim remains separate from the stable financial
// request ID derived for settlement, and an unsuccessful recording attempts
// claim release so later polls can retry.

// grokVideoObserver writes recognized video-task notices to the supplied
// request logger.
type grokVideoObserver struct {
	log *zap.Logger // request diagnostic logger
}

// resolveCompositeGrokVideoAPIKey restores task-scoped group and provider
// identity for a composite Key query. A missing runtime/Gateway binding or Key
// returns an error. Saved ownership is resolved before current mappings,
// permitting queries for a task whose mapping has since been removed.
//
// The returned Key copies the outer record and installs a new group-ID
// pointer. It retains the existing matching Group pointer when available;
// otherwise it creates a minimal active hydrated group view for this old-task
// lookup. The original Key's GroupID remains unchanged, and callers use the
// returned view for the bound task rather than new media admission.
func (h *Runtime) resolveCompositeGrokVideoAPIKey(
	ctx context.Context,
	apiKey *apikey.APIKey,
	requestID string,
	userID int64,
) (*apikey.APIKey, int64, error) {
	if h == nil || !h.bindings.Dependencies.Gateway || apiKey == nil {
		return nil, 0, errors.New("grok video request binding is unavailable")
	}
	bindings := make([]gatewaymedia.VideoBinding, len(apiKey.CompositeGroups))
	for i, binding := range apiKey.CompositeGroups {
		bindings[i] = gatewaymedia.VideoBinding{GroupID: binding.GroupID, Present: binding.Group != nil}
	}
	owner, err := h.bindings.VideoTasks().ResolveCompositeVideo(ctx, requestID, userID, apiKey.ID, bindings)
	if err != nil {
		return nil, 0, err
	}
	selected := *apiKey
	selected.GroupID = &owner.GroupID
	if owner.BindingIndex >= 0 {
		selected.Group = apiKey.CompositeGroups[owner.BindingIndex].Group
	} else {
		// Saved ownership can outlive the Key's mappings; this minimal group
		// supplies the old task's identity for query handling after that mapping disappeared.
		selected.Group = &routing.Group{ID: owner.GroupID, Status: billing.StatusActive, Hydrated: true}
	}
	return &selected, owner.ProviderID, nil
}

// grokMediaRequiredCapability returns the media-generation scheduler
// capability for creation endpoints. Status and content queries return empty
// capability because their task-bound provider is reused rather than
// requalified for new media generation.
func grokMediaRequiredCapability(endpoint grok.GrokMediaEndpoint) providercore.OpenAIEndpointCapability {
	if endpoint.IsGenerationRequest() {
		return providercore.OpenAIEndpointCapabilityGrokMediaGeneration
	}
	return ""
}

// grokMediaScheduleModel prefers a nonblank observed upstream model for
// scheduling feedback. Without that result identity, it applies provider
// mapping to routingModel, or returns its trimmed value when no provider
// exists.
func grokMediaScheduleModel(provider *gatewaycapture.ExecutionProvider, routingModel string, result *forwardcore.OpenAIResult) string {
	if result != nil && strings.TrimSpace(result.UpstreamModel) != "" {
		return result.UpstreamModel
	}
	if provider == nil {
		return strings.TrimSpace(routingModel)
	}
	return gatewaycapture.ExecutionModelPolicy(provider).Mapped(routingModel)
}

// isGrokVideoCreateEndpoint reports whether this media endpoint starts
// asynchronous video work.
func isGrokVideoCreateEndpoint(endpoint grok.GrokMediaEndpoint) bool {
	return gatewaymedia.IsVideoCreate(string(endpoint))
}

// shouldRecordGrokMediaUsage permits immediate recording for a nonnil image
// result with a usable request model and positive image count. Video creation,
// status and content paths use their separate completion logic.
func shouldRecordGrokMediaUsage(endpoint grok.GrokMediaEndpoint, requestModel string, result *forwardcore.OpenAIResult) bool {
	return result != nil && gatewaymedia.RecordImmediateImages(string(endpoint), requestModel, result.ImageCount)
}

// prepareGrokVideoCompletionBilling merges completed task observations with
// saved pricing data after a successful billing claim. Missing
// runtime/Gateway, Key or status result returns nil. The task service scopes
// lookup and claiming by subject.UserID and Key ID; callers supply a logger
// when service notices can be emitted.
//
// A nil service result means no recording should proceed, including missing
// completion evidence, claim failure or an existing claim. A successful result
// copies the outer status result and replaces its financial identity, model
// and video dimensions with prepared values. Other nested data remains from
// statusResult; recording owns the later settlement and failure-release
// attempt.
func prepareGrokVideoCompletionBilling(
	ctx context.Context,
	h *Runtime,
	reqLog *zap.Logger,
	apiKey *apikey.APIKey,
	subject authctx.AuthSubject,
	taskRequestID string,
	statusResult *forwardcore.OpenAIResult,
) *forwardcore.OpenAIResult {
	if h == nil || !h.bindings.Dependencies.Gateway || apiKey == nil || statusResult == nil {
		return nil
	}
	value := h.bindings.VideoTasks().PrepareCompletion(ctx, subject.UserID, apiKey.ID, taskRequestID,
		&gatewaymedia.VideoCompletion{RequestID: statusResult.RequestID, ResponseID: statusResult.ResponseID, Model: statusResult.Model, BillingModel: statusResult.BillingModel, UpstreamModel: statusResult.UpstreamModel, ImageCount: statusResult.ImageCount, VideoCount: statusResult.VideoCount, VideoResolution: statusResult.VideoResolution, VideoDurationSeconds: statusResult.VideoDurationSeconds, Duration: statusResult.Duration}, time.Now, grokVideoObserver{log: reqLog})
	if value == nil {
		return nil
	}
	merged := *statusResult
	merged.RequestID = value.RequestID
	merged.ResponseID = value.ResponseID
	merged.Model = value.Model
	merged.BillingModel = value.BillingModel
	merged.UpstreamModel = value.UpstreamModel
	merged.ImageCount = value.ImageCount
	merged.VideoCount = value.VideoCount
	merged.VideoResolution = value.VideoResolution
	merged.VideoDurationSeconds = value.VideoDurationSeconds
	merged.Duration = value.Duration
	return &merged
}

// firstNonEmptyString returns the first trimmed nonblank value from its
// arguments. Empty or whitespace-only inputs are skipped, and an entirely
// empty sequence returns an empty string for the caller's fallback.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// recordGrokMediaUsage captures media observations and funding before
// submitting completion recording. A nil result is ignored; other dependencies
// and a hydrated Key.User are caller requirements. Body-free queries can use
// task identity as their payload fingerprint input.
//
// Positive video units derive a stable financial request ID from
// result.ResponseID or the supplied task ID, while claim release prefers the
// supplied task ID. The captured completion input and user/Key IDs reach the
// queued callback. A recorder error attempts to release the video claim, logs
// a failed release and leaves client presentation unchanged because execution
// has already returned its response.
func recordGrokMediaUsage(
	c *gin.Context,
	h *Runtime,
	reqLog *zap.Logger,
	apiKey *apikey.APIKey,
	subject authctx.AuthSubject,
	subscription *billing.UserSubscription,
	provider *gatewaycapture.ExecutionProvider,
	result *forwardcore.OpenAIResult,
	requestModel string,
	groupMapping routing.GroupMappingResult,
	body []byte,
	requestID string,
) {
	// A missing result has no usage to settle or model metadata to read.
	if result == nil {
		return
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	sessionID := gatewayhttp.ExtractClientSessionID(c)
	payloadForHash := body
	// Queries can have no body; include their task identity in the captured
	// payload fingerprint so settlement evidence still identifies the requested operation.
	if len(payloadForHash) == 0 && strings.TrimSpace(requestID) != "" {
		payloadForHash = []byte(requestID)
	}
	inboundEndpoint := gatewayhttp.GetInboundEndpoint(c)
	upstreamEndpoint := gatewayhttp.GetUpstreamEndpoint(c, provider.Record.Platform)

	pricingUsageFields := gatewayhttp.ClientRequestedUsageFields(c, groupMapping, requestModel, result.UpstreamModel)
	videoTaskID := ""
	// Video settlement uses a task-stable financial key, while the separately
	// captured task ID remains available to undo this request's failed billing claim.
	if result.VideoCount > 0 {
		videoTaskID = strings.TrimSpace(firstNonEmptyString(requestID, result.ResponseID))
		if stable := gatewaymedia.StableGrokVideoBillingRequestID(firstNonEmptyString(result.ResponseID, requestID)); stable != "" {
			result.RequestID = stable
		}
		if len(body) == 0 && videoTaskID != "" {
			payloadForHash = []byte(videoTaskID)
		}
	}
	requestPayloadHash := billing.HashUsageRequestPayload(payloadForHash)
	// Capture values before queueing so completion uses this request's selected
	// provider, funding and pricing even after the HTTP handler releases its state.
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
		PricingUsageFields: pricingUsageFields,
	})
	completionRecorder := h.bindings.Common.Recorder
	completionLog := logging.L().With(
		zap.String("component", "handler.openai_gateway.grok_media"),
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("model", requestModel),
		zap.Int64("provider_id", provider.Record.ID),
	)
	videoTasks := h.bindings.VideoTasks()
	actorID, keyID := subject.UserID, apiKey.ID
	h.submitOpenAIUsageRecordTask(c, result, func(ctx context.Context) {
		if err := completionRecorder.Record(ctx, completionInput, true); err != nil {
			// Failed persistence leaves this completion unrecorded; attempt to release
			// its claim so a later observation can retry, and report release failures.
			if videoTaskID != "" {
				if releaseErr := videoTasks.ReleaseGrokVideoBilling(ctx, videoTaskID, actorID, keyID); releaseErr != nil {
					reqLog.Warn("grok_media.video_billing_claim_release_failed",
						zap.String("request_id", videoTaskID),
						zap.Error(releaseErr),
					)
				}
			}
			completionLog.Error("grok_media.record_usage_failed", zap.Error(err))
			reqLog.Debug("grok_media.record_usage_failed", zap.Error(err))
		}
	})
}

// ObserveVideo logs the recognized bind, storage, loading and billing-claim
// notice kinds. Unknown kinds are ignored; a usable logger is required when a
// recognized notice emits its task and failure metadata.
func (o grokVideoObserver) ObserveVideo(n gatewaymedia.VideoNotice) {
	switch n.Kind {
	case "bind_failed":
		o.log.Warn("grok_media.bind_video_request_provider_failed", zap.Int64("provider_id", n.ProviderID), zap.String("request_id", n.TaskID), zap.Error(n.Err))
	case "store_retry":
		o.log.Warn("grok_media.store_video_pending_billing_failed_retrying", zap.Int64("provider_id", n.ProviderID), zap.String("request_id", n.TaskID), zap.Error(n.Err))
	case "store_failed":
		o.log.Error("grok_media.store_video_pending_billing_failed", zap.Int64("provider_id", n.ProviderID), zap.String("request_id", n.TaskID), zap.Error(n.Err))
	case "load_failed":
		o.log.Warn("grok_media.video_pending_billing_load_failed", zap.String("request_id", n.TaskID), zap.Error(n.Err))
	case "missing_pending":
		o.log.Error("grok_media.video_billing_skipped_missing_pending", zap.String("request_id", n.TaskID), zap.String("reason", "no create-time snapshot and status has no video.duration"))
	case "without_pending":
		o.log.Error("grok_media.video_billing_without_pending", zap.String("request_id", n.TaskID), zap.Int("status_duration_seconds", n.DurationSeconds), zap.String("note", "resolution falls back to default 480p; investigate pending store failures"))
	case "claim_failed":
		o.log.Warn("grok_media.video_billing_claim_failed", zap.String("request_id", n.TaskID), zap.Error(n.Err))
	case "already_claimed":
		o.log.Debug("grok_media.video_billing_already_claimed", zap.String("request_id", n.TaskID))
	}
}
