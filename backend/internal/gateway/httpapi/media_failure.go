package httpapi

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
)

// media_failure.go selects final media error output from attempt stage,
// provider eligibility, and response commitment.
// Media selection failures distinguish an unavailable model from exhausted
// capacity, while forwarding failures can arrive after response output has
// begun. The HTTP presenter combines the stage with excluded-provider count
// and retained attempt evidence before choosing capacity reporting, failover
// exhaustion, or a final error.
//
// Images can carry Cyber warnings or an already communicated terminal error,
// so unexpected failure handling records warnings and compares writer state
// before attempting a generic response. Grok generation adds an eligibility
// outcome, while video lookup stays attached to its saved provider and
// reports missing ownership as not found. These decisions affect presentation
// and observation; the media attempt loop supplies their stage and output
// evidence.

// MediaNoProvider contains the HTTP classification for an empty media
// selection.
type MediaNoProvider struct {
	// ModelNotFound identifies a model-policy failure, which suppresses capacity
	// observation and retains the classification message in the image branch.
	ModelNotFound bool
	Status        int // classified HTTP failure status
	// Type: classified error category. Message: classified client message.
	Type, Message string
}

// MediaFailureContext supplies request and writer observations to terminal
// media presentation.
type MediaFailureContext struct {
	// Grok selects Grok presentation. Generation activates Grok eligibility
	// reporting, while StreamStarted carries the image stream state into terminal
	// presentation; these flags describe the current entry and its output.
	// Grok: current platform branch. Generation: generation endpoint flag.
	// StreamStarted: image stream startup state.
	Grok, Generation, StreamStarted bool
	// BoundProviderID names the saved video owner; ProviderID names the current
	// attempt in logs. A rejected selection can differ from both retained values.
	// BoundProviderID: saved video provider. ProviderID: current selected provider.
	BoundProviderID, ProviderID int64
	WriterBefore                int // preattempt writer byte count
	// Context supplies writer size for unexpected image failures. That branch
	// requires a usable context and writer after warning handling returns.
	Context *gin.Context
	// Log records terminal failure observations. Bound-provider and unexpected
	// paths require a usable logger even when no response is appended.
	Log *zap.Logger
}

// MediaFailurePorts supplies classification, observation and response
// operations for generation failures.
type MediaFailurePorts interface {
	// MediaClassify returns the route's model and capacity failure
	// classification.
	MediaClassify() MediaNoProvider
	// MediaNoAvailable reports whether selection failed because scheduler
	// candidates were unavailable.
	MediaNoAvailable(error) bool
	// MediaCapacity records capacity limitation, conditionally when the error
	// indicates unavailable providers. False requests unconditional marking,
	// including an empty selection whose absence has already established that
	// condition.
	MediaCapacity(error, bool)
	// MediaError presents the supplied status, category and message for terminal
	// failure. The final boolean selects stream-aware presentation; actual
	// stream startup remains part of the adapter's request state.
	MediaError(int, string, string, bool)
	// MediaFailover presents the retained classified upstream failure after
	// retries end. The boolean carries whether output has begun, which
	// constrains how the implementation can deliver its final error.
	MediaFailover(error, bool)
	// MediaSimpleExhausted presents the image entry's generic exhausted-provider
	// error.
	MediaSimpleExhausted()
	// MediaForwardCyber records recognized forward-warning evidence and reports
	// whether recognition occurred.
	MediaForwardCyber(error) bool
	// MediaCyber records terminal error text as image Cyber warning evidence.
	MediaCyber(error)
	// MediaReportUnexpected records scheduling failure from retained media
	// result and error.
	MediaReportUnexpected(*media.GenerationResult, error)
	// MediaCommunicated reports whether the adapter considers the upstream error
	// already presented.
	MediaCommunicated(error) bool
	// MediaEnsureFallback attempts a final protocol error when request output
	// still permits it. Its result describes attempted presentation for logging;
	// implementations need not guarantee that the client accepted bytes.
	MediaEnsureFallback(error) bool
	// MediaWarnFailure selects warning severity using current output and
	// fallback presentation state.
	MediaWarnFailure(bool) bool
}

// WriteGenerationFailure presents a terminal image or Grok outcome using the
// supplied request observations. Callers provide usable ports and request
// state for the selected stage. Grok handling delegates before the image
// branch; image selection with no prior exclusions can classify an
// unavailable model separately from limited capacity.
//
// Unexpected image errors first record Cyber evidence and scheduling failure,
// then check whether forwarding already communicated the error. A generic
// response is attempted only when communication is absent and warning
// handling has not changed the writer in the relevant case. Logs distinguish
// an attempted fallback from an existing upstream response.
func WriteGenerationFailure(f media.GenerationFailure, state MediaFailureContext, p MediaFailurePorts) {
	if state.Grok {
		writeGrokGenerationFailure(f, state, p)
		return
	}
	switch f.Stage {
	case "selection", "empty_selection":
		// Empty selections and selections with no exclusions use route classification.
		// This keeps model denial separate from capacity observation and retained errors.
		if f.Stage == "empty_selection" || f.Excluded == 0 {
			classification := p.MediaClassify()
			if !classification.ModelNotFound {
				p.MediaCapacity(f.Err, f.Stage != "empty_selection")
			}
			message := classification.Message
			if !classification.ModelNotFound {
				message = "No available compatible providers"
			}
			p.MediaError(classification.Status, classification.Type, message, true)
			return
		}
		if f.Outcome.Failure != nil {
			p.MediaFailover(f.Outcome.Err, state.StreamStarted)
		} else {
			p.MediaSimpleExhausted()
		}
	case "exhausted":
		p.MediaFailover(f.Outcome.Err, f.Outcome.OutputChanged || state.StreamStarted)
	case "unexpected":
		recorded := p.MediaForwardCyber(f.Err)
		if !recorded {
			p.MediaCyber(f.Err)
		}
		p.MediaReportUnexpected(f.Outcome.Result, f.Err)
		communicated := p.MediaCommunicated(f.Err)
		fallback := false
		// Cyber handling can already write a response. Request a generic error only
		// when neither communication evidence nor that branch's changed writer rules it out.
		if !communicated && (!recorded || state.Context.Writer.Size() == state.WriterBefore) {
			fallback = p.MediaEnsureFallback(f.Err)
		}
		fields := []zap.Field{zap.Int64("provider_id", state.ProviderID), zap.Bool("fallback_error_response_written", fallback), zap.Bool("upstream_error_response_already_written", communicated), zap.Error(f.Err)}
		if p.MediaWarnFailure(fallback) {
			state.Log.Warn("openai.images.forward_failed", fields...)
		} else {
			state.Log.Error("openai.images.forward_failed", fields...)
		}
	}
}

// writeGrokGenerationFailure presents terminal Grok media selection and
// forwarding outcomes. It requires a logger for bound-provider and unexpected
// failures, while the media loop supplies excluded-count, eligibility and
// output observations. Generation can report a specialized
// unavailable-eligibility error before ordinary no-provider classification.
//
// A saved video provider that cannot be selected produces not found.
// Forwarding errors append a generic response only when communication and
// changed output are both absent. Exhaustion carries output change into
// failover presentation, which determines whether a response can still be
// written.
func writeGrokGenerationFailure(f media.GenerationFailure, state MediaFailureContext, p MediaFailurePorts) {
	switch f.Stage {
	case "ineligible":
		p.MediaCapacity(nil, false)
		p.MediaError(503, "grok_media_no_eligible_provider", "No eligible Grok media providers", false)
	case "bound_unavailable":
		state.Log.Warn("grok_media.video_lookup_bound_provider_unavailable", zap.Int64("bound_provider_id", state.BoundProviderID), zap.Int64("selected_provider_id", f.SelectedID))
		p.MediaError(404, "not_found_error", "Video request not found", false)
	case "selection", "empty_selection":
		if state.Generation && (f.Stage == "empty_selection" || (p.MediaNoAvailable(f.Err) && (f.Excluded == 0 || (f.EligibilityRejected && f.Outcome.Err == nil)))) {
			p.MediaCapacity(f.Err, f.Stage != "empty_selection")
			p.MediaError(503, "grok_media_no_eligible_provider", "No eligible Grok media providers", false)
			return
		}
		// Empty selections and selections with no exclusions use route classification.
		// This keeps model denial separate from capacity observation and retained errors.
		if f.Stage == "empty_selection" || f.Excluded == 0 {
			classification := p.MediaClassify()
			if !classification.ModelNotFound {
				p.MediaCapacity(f.Err, f.Stage != "empty_selection")
			}
			p.MediaError(classification.Status, classification.Type, classification.Message, false)
			return
		}
		if f.Outcome.Failure != nil {
			p.MediaFailover(f.Outcome.Err, false)
		} else {
			p.MediaError(502, "api_error", "Upstream request failed", false)
		}
	case "exhausted":
		p.MediaFailover(f.Outcome.Err, f.Outcome.OutputChanged)
	case "unexpected":
		// Grok output may have already reached the client before the error. Preserve
		// that document or content stream and present a generic error before output only.
		if !p.MediaCommunicated(f.Err) && !f.Outcome.OutputChanged {
			p.MediaError(502, "upstream_error", "Upstream request failed", false)
		}
		state.Log.Warn("grok_media.forward_failed", zap.Int64("provider_id", state.ProviderID), zap.Error(f.Err))
	}
}
