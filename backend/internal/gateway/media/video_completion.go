package media

import (
	"context"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// VideoCompletion holds observations needed to bill a completed video.
type VideoCompletion struct {
	RequestID, ResponseID, Model, BillingModel, UpstreamModel, VideoResolution string
	ImageCount, VideoCount, VideoDurationSeconds                               int
	Duration                                                                   time.Duration
}

// VideoNotice supplies event fields for entry-layer logging.
type VideoNotice struct {
	ProviderID      int64
	Kind            string
	TaskID          string
	DurationSeconds int
	Err             error
}
type VideoObserver interface{ ObserveVideo(VideoNotice) }

func videoNotice(o VideoObserver, n VideoNotice) {
	if o != nil {
		o.ObserveVideo(n)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// PrepareCompletion loads the creation snapshot before claiming billing and skips claims when observations are insufficient.
func (s *VideoTasks) PrepareCompletion(ctx context.Context, userID, keyID int64, taskRequestID string, statusResult *VideoCompletion, now func() time.Time, observer VideoObserver) *VideoCompletion {
	if now == nil {
		now = time.Now
	}
	if statusResult == nil || statusResult.VideoCount <= 0 {
		return nil
	}
	taskRequestID = firstNonEmpty(taskRequestID, statusResult.ResponseID)
	if taskRequestID == "" {
		return nil
	}
	pending, err := s.LoadGrokVideoPendingBilling(ctx, taskRequestID, userID, keyID)
	if err != nil {
		videoNotice(observer, VideoNotice{Kind: "load_failed", TaskID: taskRequestID, Err: err})
	}
	if pending == nil {
		if statusResult.VideoDurationSeconds <= 0 {
			videoNotice(observer, VideoNotice{Kind: "missing_pending", TaskID: taskRequestID})
			return nil
		}
		videoNotice(observer, VideoNotice{Kind: "without_pending", TaskID: taskRequestID, DurationSeconds: statusResult.VideoDurationSeconds})
	}
	claimed, err := s.ClaimGrokVideoBilling(ctx, taskRequestID, userID, keyID)
	if err != nil {
		videoNotice(observer, VideoNotice{Kind: "claim_failed", TaskID: taskRequestID, Err: err})
		return nil
	}
	if !claimed {
		videoNotice(observer, VideoNotice{Kind: "already_claimed", TaskID: taskRequestID})
		return nil
	}
	// Merge the creation snapshot: prefer its resolution and fill missing models and duration.
	merged := *statusResult
	if pending != nil {
		if strings.TrimSpace(merged.Model) == "" {
			merged.Model = firstNonEmpty(pending.BillingModel, pending.Model, pending.OriginalModel)
		}
		if strings.TrimSpace(merged.BillingModel) == "" {
			merged.BillingModel = firstNonEmpty(pending.BillingModel, pending.Model, merged.Model)
		}
		if strings.TrimSpace(merged.UpstreamModel) == "" {
			merged.UpstreamModel = pending.UpstreamModel
		}
		// Use the creation request's resolution when available; the status response omits it.
		if strings.TrimSpace(pending.VideoResolution) != "" {
			merged.VideoResolution = pending.VideoResolution
		}
		if merged.VideoDurationSeconds <= 0 {
			merged.VideoDurationSeconds = pending.VideoDurationSeconds
		}
		if strings.TrimSpace(merged.ResponseID) == "" {
			merged.ResponseID = taskRequestID
		}
	}
	if strings.TrimSpace(merged.Model) == "" {
		merged.Model = "grok-imagine-video"
	}
	if strings.TrimSpace(merged.BillingModel) == "" {
		merged.BillingModel = merged.Model
	}
	// Use a task-stable ID so usage_billing_dedup covers repeated polls with different request IDs.
	merged.RequestID = StableGrokVideoBillingRequestID(firstNonEmpty(merged.ResponseID, taskRequestID))
	merged.ResponseID = firstNonEmpty(merged.ResponseID, taskRequestID)
	merged.VideoCount = 1
	// Clear image units so this completion is billed as video.
	merged.ImageCount = 0
	// Default an omitted resolution to 480p.
	merged.VideoResolution = pricing.NormalizeVideoBillingResolutionOrDefault(merged.VideoResolution)
	// Default duration to 8 seconds when neither status nor creation supplies it.
	merged.VideoDurationSeconds = pricing.NormalizeVideoBillingDurationSecondsOrDefault(merged.VideoDurationSeconds)
	// Measure from creation acceptance to observed completion, rather than the duration of one poll.
	if pending != nil {
		if e2e := GrokVideoE2EDuration(pending.CreatedAt, now()); e2e > 0 {
			merged.Duration = e2e
		}
	}

	return &merged
}

// GrokVideoRequestState normalizes upstream task status for the request summary.
func GrokVideoRequestState(statusBody []byte) string {
	if !gjson.ValidBytes(statusBody) {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(statusBody, "status").String())) {
	case "done":
		return "completed"
	case "failed", "expired":
		return "failed"
	case "cancelled", "canceled":
		return "canceled"
	case "pending", "queued", "running", "in_progress":
		return "running"
	default:
		return ""
	}
}

// IsGrokVideoStatusBillable requires done status and a nonempty video.url.
// Pending, expired, failed, and done responses lacking a URL are not billable.
func IsGrokVideoStatusBillable(statusBody []byte) bool {
	if len(statusBody) == 0 || !gjson.ValidBytes(statusBody) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(gjson.GetBytes(statusBody, "status").String()), "done") {
		return false
	}
	return strings.TrimSpace(gjson.GetBytes(statusBody, "video.url").String()) != ""
}

// ExtractGrokVideoBillingFromStatusBody builds video units from a billable done response.
// It reads seconds from video.duration and the model from the top-level model field.
// Resolution comes from the creation snapshot; PrepareCompletion supplies the default when absent.
func ExtractGrokVideoBillingFromStatusBody(statusBody []byte, pending *GrokVideoPendingBilling, requestID, observedResponseID string) *VideoCompletion {
	if !IsGrokVideoStatusBillable(statusBody) {
		return nil
	}
	model := ""
	billingModel := ""
	upstreamModel := ""
	resolution := ""
	durationSeconds := 0

	if gjson.ValidBytes(statusBody) {
		// The model is a top-level status field.
		model = strings.TrimSpace(gjson.GetBytes(statusBody, "model").String())
		// video.duration is measured in seconds.
		if v := gjson.GetBytes(statusBody, "video.duration"); v.Exists() && v.Type == gjson.Number {
			durationSeconds = int(v.Int())
			if durationSeconds == 0 && v.Float() > 0 {
				// Fractional seconds are truncated; values below one second remain zero.
				durationSeconds = int(v.Float())
			}
		}
	}
	if pending != nil {
		if model == "" {
			model = firstNonEmpty(pending.BillingModel, pending.Model, pending.OriginalModel)
		}
		if billingModel == "" {
			billingModel = firstNonEmpty(pending.BillingModel, pending.Model)
		}
		if upstreamModel == "" {
			upstreamModel = pending.UpstreamModel
		}
		// Resolution is supplied by the creation snapshot because the status response omits it.
		resolution = pending.VideoResolution
		if durationSeconds <= 0 {
			durationSeconds = pending.VideoDurationSeconds
		}
	}
	if model == "" {
		// Use the default video model family when the status omits its model.
		model = "grok-imagine-video"
	}
	if billingModel == "" {
		billingModel = model
	}
	// Leave missing resolution empty for PrepareCompletion to default to 480p.
	if resolution != "" {
		resolution = pricing.NormalizeVideoBillingResolutionOrDefault(resolution)
	}
	if durationSeconds > 0 {
		durationSeconds = pricing.NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
	}
	responseID := observedResponseID
	if responseID == "" {
		responseID = strings.TrimSpace(requestID)
	}
	return &VideoCompletion{
		ResponseID: responseID,

		Model: model,

		BillingModel: billingModel,

		UpstreamModel: upstreamModel,

		VideoCount: 1,

		VideoResolution: resolution,

		VideoDurationSeconds: durationSeconds,
	}
}
