package media

import (
	"context"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Grok status extraction requires done state and nonblank video.url
// evidence before returning video billing units. PrepareCompletion accepts
// already classified observations, then uses creation snapshots to fill
// missing model, resolution or duration while a cache claim gates that
// polling observation.
//
// Preparation loads the snapshot before claiming and skips a claim when
// both creation evidence and observed duration are insufficient. It copies
// scalar status observations, prefers creation resolution, preserves
// separate model identities and replaces poll request IDs with a billing
// identity derived from ResponseID or task ID. The caller keeps that
// identity associated with one task and handles recording or claim release;
// obtaining a claim does not establish settlement. A saved creation origin
// can replace one-poll duration with positive end-to-end elapsed time.

// VideoCompletion carries scalar completed-video observations into claim
// preparation and recording.
type VideoCompletion struct {
	// RequestID becomes the billing correlation ID while ResponseID supplies
	// task/response evidence. Model, BillingModel and UpstreamModel remain
	// separate, and VideoResolution selects the video price tier rather than
	// an image size.
	RequestID, ResponseID, Model, BillingModel, UpstreamModel, VideoResolution string // billing request identifier, observed task identifier, response model name, billing model name, provider model name and video pricing resolution
	// ImageCount and VideoCount distinguish media units before preparation.
	// PrepareCompletion normalizes video count to one and clears image units;
	// VideoDurationSeconds supplies the shared per-video duration selection.
	ImageCount, VideoCount, VideoDurationSeconds int // observed image count, observed video count and video duration seconds
	// Duration initially retains forwarding elapsed time. PrepareCompletion
	// replaces it only when a saved creation origin produces positive
	// end-to-end duration, leaving polling evidence otherwise.
	Duration time.Duration // poll or end-to-end elapsed time
}

// VideoNotice carries video-cache decisions and available task evidence to
// observers.
type VideoNotice struct {
	ProviderID      int64  // associated upstream provider identifier
	Kind            string // video handling event label
	TaskID          string // associated video task identifier
	DurationSeconds int    // observed video duration seconds
	Err             error  // optional cache operation error
}

// VideoObserver receives synchronous video task/cache decisions for
// entry-layer observation.
type VideoObserver interface {
	// ObserveVideo handles a decision notice synchronously during video task
	// work. The notice carries only available fields for its Kind; the
	// observer owns logging or any later retention of error evidence.
	ObserveVideo(VideoNotice)
}

// videoNotice delivers a notice when the observer is nonnil and otherwise
// does nothing. Observation is synchronous and returns no error or
// acceptance signal, leaving task decisions with the caller.
func videoNotice(o VideoObserver, n VideoNotice) {
	if o != nil {
		o.ObserveVideo(n)
	}
}

// firstNonEmpty returns the first trimmed nonblank string in supplied
// order. An entirely blank or empty sequence returns empty, allowing
// callers to keep field-precedence and later default decisions separate.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// PrepareCompletion obtains a cache claim and returns merged scalar
// observations for later video recording. It accepts already classified
// statusResult with positive VideoCount and nonblank task identity; now
// defaults to time.Now and nil observer skips notices.
//
// Creation state is loaded before claiming. Missing snapshot, including a
// failed load, requires positive observed duration before claiming. The
// copied result prefers creation resolution and fills missing
// model/duration; a missing Model uses grok-imagine-video and missing
// BillingModel follows Model. Resolution and duration use shared pricing
// defaults, and units become one video with no images. Billing RequestID
// prefers ResponseID over task ID, so callers keep ResponseID associated
// with the same task across polls. The recording adapter releases claims on
// Record error; this method performs no settlement or rollback.
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
	// Load creation evidence before consuming a claim; cache load failure is
	// observed and then follows the same measured-duration requirement as a
	// missing snapshot.
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
	// Claim only after required task observations are available, skipping
	// recording on claim error or false while leaving persistent billing
	// deduplication to the recorder.
	claimed, err := s.ClaimGrokVideoBilling(ctx, taskRequestID, userID, keyID)
	if err != nil {
		videoNotice(observer, VideoNotice{Kind: "claim_failed", TaskID: taskRequestID, Err: err})
		return nil
	}
	if !claimed {
		videoNotice(observer, VideoNotice{Kind: "already_claimed", TaskID: taskRequestID})
		return nil
	}
	// Merge into a scalar copy after successful claiming, preferring saved
	// creation resolution and filling missing model/duration fields without
	// mutating the polling observation.
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
		// A saved nonblank creation resolution takes precedence over status
		// resolution, retaining the request's pricing choice when the status
		// protocol omits that field.
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
	// Derive billing identity from nonblank ResponseID before task ID; callers
	// keep that response identity associated with this task across polls for
	// persistent deduplication.
	merged.RequestID = StableGrokVideoBillingRequestID(firstNonEmpty(merged.ResponseID, taskRequestID))
	merged.ResponseID = firstNonEmpty(merged.ResponseID, taskRequestID)
	merged.VideoCount = 1
	// Clear image units after selecting one video completion, keeping its
	// media-unit classification separate from any image observations carried
	// by the status result.
	merged.ImageCount = 0
	// Normalize resolution to the shared video tier; unresolved input uses
	// 480p.
	merged.VideoResolution = pricing.NormalizeVideoBillingResolutionOrDefault(merged.VideoResolution)
	// Use eight seconds for missing duration and cap positive duration at
	// fifteen seconds.
	merged.VideoDurationSeconds = pricing.NormalizeVideoBillingDurationSecondsOrDefault(merged.VideoDurationSeconds)
	// Prefer positive creation-to-discovery elapsed time from the saved
	// creation timestamp, retaining polling duration when the timestamp is
	// absent, invalid or after discovery.
	if pending != nil {
		if e2e := GrokVideoE2EDuration(pending.CreatedAt, now()); e2e > 0 {
			merged.Duration = e2e
		}
	}

	return &merged
}

// GrokVideoRequestState maps trimmed, case-insensitive done,
// failure/expiry, cancellation and active statuses to request-summary
// states. Invalid JSON or unrecognized status returns empty; this display
// state does not establish billable video output.
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

// IsGrokVideoStatusBillable requires valid JSON with trimmed,
// case-insensitive done status and nonblank video.url extraction. It checks
// presence evidence rather than URL validity; terminal status without that
// evidence returns false.
func IsGrokVideoStatusBillable(statusBody []byte) bool {
	if len(statusBody) == 0 || !gjson.ValidBytes(statusBody) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(gjson.GetBytes(statusBody, "status").String()), "done") {
		return false
	}
	return strings.TrimSpace(gjson.GetBytes(statusBody, "video.url").String()) != ""
}

// ExtractGrokVideoBillingFromStatusBody returns one video unit only for
// status satisfying IsGrokVideoStatusBillable. Status model wins for Model;
// pending BillingModel/Model fill billing identity, pending UpstreamModel
// stays separate, and pending resolution supplies creation evidence.
//
// Duration uses gjson.Int followed by Go int conversion. Representable positive
// fractions truncate toward zero, and subsecond values leave zero before pending
// fallback. Numeric range is not validated before those conversions, so pricing
// bounds apply to their result rather than validating the source number.
// Positive duration and nonempty resolution are normalized, while missing
// values remain for PrepareCompletion defaults. Missing model uses
// grok-imagine-video and missing billing model follows Model. Nonempty
// observedResponseID is retained as supplied; otherwise trimmed requestID
// supplies ResponseID, whose task association remains a caller obligation.
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
		// Read integer seconds before pricing normalization; fractional
		// extraction truncates positive subsecond values to zero, allowing
		// creation duration or later defaults to supply missing units.
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
		// Retain creation resolution with pending model/duration evidence because
		// this status extraction does not read a response resolution field into
		// billing observations.
		resolution = pending.VideoResolution
		if durationSeconds <= 0 {
			durationSeconds = pending.VideoDurationSeconds
		}
	}
	if model == "" {
		// Use grok-imagine-video only after status and pending model candidates
		// are empty, retaining a video-family model for the completion resolver's
		// later billing decisions.
		model = "grok-imagine-video"
	}
	if billingModel == "" {
		billingModel = model
	}
	// Keep missing resolution unresolved here so PrepareCompletion can apply
	// its shared default after merging; normalize only a supplied creation
	// resolution at extraction time.
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
