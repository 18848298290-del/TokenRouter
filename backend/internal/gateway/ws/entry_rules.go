package ws

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// entry_rules.go selects retry payloads and billing models and classifies local
// WebSocket entry failures. A failed turn supplies its own replay request,
// while billing can select a model different from the client or upstream
// identifier.
//
// A recognized later-turn failure needs its own nonempty retry payload, and
// selected payloads are copied for the next provider attempt. Billing first
// chooses an observed result model, then upstream and requested fallbacks,
// before applying the group's requested or group-mapped override. Local routing
// rejection, reasoning policy, and session preemption are excluded from
// provider health reporting because those failures do not establish a failed
// upstream exchange.

// ErrEntryLocalRoutingRejected identifies gateway model eligibility or pricing
// rejection so entry health reporting can exclude these local decisions from
// upstream failure feedback.
var ErrEntryLocalRoutingRejected = errors.New("local websocket routing rejected")

// EntryFallbackSeed builds the group, user, and API Key isolation seed used by
// ingress. A nil group uses zero; SessionHash consumes this value only when it
// needs a derived session identity.
func EntryFallbackSeed(userID, keyID int64, groupID *int64) string {
	var group int64
	if groupID != nil {
		group = *groupID
	}
	return fmt.Sprintf("openai_ws_ingress:%d:%d:%d", group, userID, keyID)
}

// entrySeedHash derives the scheduler hash used to isolate a previous response
// identifier.
func entrySeedHash(seed string) string {
	value, _ := scheduler.DeriveSessionHashes(seed)
	return value
}

// EntryNextAttemptMessage selects copied request bytes for a replacement
// provider attempt. An initial-attempt failure uses current because that
// payload already belongs to the request being retried.
//
// A recognized current-turn failure instead requires nonempty retry bytes.
// Returning false when those bytes are missing prevents replaying the session's
// first request after a later turn failed. The returned copy can be rewritten
// for the next provider without changing the retained source payload.
func EntryNextAttemptMessage(current, retry []byte, currentTurn bool) ([]byte, bool) {
	if !currentTurn {
		return append([]byte(nil), current...), true
	}
	if len(retry) == 0 {
		return nil, false
	}
	return append([]byte(nil), retry...), true
}

// EntryBillingModel selects the model used to bill a completed WebSocket turn.
// It starts with a trimmed result BillingModel, then upstream, then requested,
// allowing callers to supply a nil result.
//
// The group's requested source overrides that candidate when requested is
// nonblank. The group-mapped source overrides it when the mapped name is
// nonblank and differs from requested; an identity mapping leaves the earlier
// result or upstream choice available. Each comparison uses trimmed strings, so
// blank metadata cannot erase a usable candidate.
func EntryBillingModel(result *ForwardResult, mapping routing.GroupMappingResult, requested, upstream string) string {
	model := ""
	if result != nil {
		model = strings.TrimSpace(result.BillingModel)
	}
	// Fill an absent result billing model from upstream and then requested
	// metadata, so missing observations retain a usable pricing identity.
	if model == "" {
		model = strings.TrimSpace(upstream)
	}
	if model == "" {
		model = strings.TrimSpace(requested)
	}
	requested = strings.TrimSpace(requested)
	// Apply the configured billing source after selecting observed metadata,
	// retaining that selection when the configured source supplies no distinct
	// usable model.
	switch mapping.BillingModelSource {
	case routing.BillingModelSourceRequested:
		if requested != "" {
			model = requested
		}
	case routing.BillingModelSourceGroupMapped:
		if mapped := strings.TrimSpace(mapping.MappedModel); mapped != "" && mapped != requested {
			model = mapped
		}
	}
	return model
}

// entrySucceeded reports success for scheduling feedback from the available
// result metadata. Nil, non-WS, and missing-terminal results default to
// success; a declared WS terminal succeeds only for response.completed or
// response.done.
func entrySucceeded(r *ForwardResult) bool {
	if r == nil || !r.OpenAIWSMode || r.UpstreamTerminalEvent == "" {
		return true
	}
	return r.UpstreamTerminalEvent == "response.completed" || r.UpstreamTerminalEvent == "response.done"
}

// EntryLocalRoutingReason formats the client-facing model eligibility rejection
// using a trimmed model.
func EntryLocalRoutingReason(model string) string {
	return fmt.Sprintf("model %s is not available for this websocket group or provider", strings.TrimSpace(model))
}

// EntryLocalRoutingErrorReason chooses the missing-price message when err wraps
// the pricing sentinel. Other failures use the model eligibility message; the
// caller separately wraps the cause to suppress provider health penalties.
func EntryLocalRoutingErrorReason(model string, err error) string {
	if errors.Is(err, pricing.ErrModelPricingUnavailable) {
		return admission.ModelPricingUnavailableMessage
	}
	return EntryLocalRoutingReason(model)
}

// EntryLocalRoutingCause wraps both the local rejection sentinel and its
// underlying error.
func EntryLocalRoutingCause(err error) error {
	return fmt.Errorf("%w: %w", ErrEntryLocalRoutingRejected, err)
}

// EntryShouldReportFailure excludes nil, local routing, session preemption, and
// reasoning-cap or model-attribute failures from provider feedback. Other errors return true;
// RunEntry combines this classification with its separate clean-client-close
// handling.
func EntryShouldReportFailure(err error) bool {
	if err == nil || routing.IsModelAttributeError(err) || errors.Is(err, ErrEntryLocalRoutingRejected) || IsSessionPreemptedError(err) {
		return false
	}
	var policy *routing.ReasoningEffortOverLimitError
	return !errors.As(err, &policy)
}

// EntryModelAttributeClose translates local attribute errors into a client close.
// Unsupported or unknown capabilities require a changed request; unavailable
// configuration permits a later retry. Unrelated errors return an absent close.
func EntryModelAttributeClose(err error) EntryClose {
	if !routing.IsModelAttributeError(err) {
		return EntryClose{}
	}
	status := 1008
	if apperror.Reason(err) == "MODEL_ATTRIBUTES_UNAVAILABLE" {
		status = 1013
	}
	return EntryClose{Present: true, Status: status, Reason: err.Error()}
}
