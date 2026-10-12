package ws

import (
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
)

// Bidirectional passthrough needs a session model default while each accepted
// turn keeps its own usage metadata. A session.update can replace that default,
// and a later response.create can omit model without losing the client
// identifier needed for policy or output restoration.
//
// UsageMeta publishes each optional value through a separate atomic pointer.
// Those stores are individually safe across relay directions, but they do not
// create one atomic snapshot of all fields; the serialized client filter reads
// the values into TurnPayloadQueue after applying each accepted request's
// policy. Requested effort is captured before that rewriting, while service
// tier and recorded effort come from the resulting payload.

// UsageDecoder supplies optional accounting values extracted from request frame
// bytes.
type UsageDecoder interface {
	// ServiceTier returns canonical accepted service-tier metadata from request
	// bytes. Nil represents absent, blank, or unrecognized metadata; callers
	// capture the value after policy processing and keep published values
	// immutable.
	ServiceTier([]byte) *string
	// ReasoningEffort returns optional reporting metadata from accepted request
	// bytes. Nil means no retained reporting label, including a supplied none or
	// minimal; callers capture client intent separately before policy rewriting.
	ReasoningEffort([]byte) *string
	// RequestedReasoningEffort returns canonical client effort from bytes captured
	// before policy rewriting. Nil represents omission or an unrecognized value,
	// while a supplied none remains distinguishable from absence in completion.
	RequestedReasoningEffort([]byte) *string
}

// UsageMeta publishes session defaults while queued turns retain their own
// accounting snapshots.
type UsageMeta struct {
	decoder UsageDecoder // request metadata extraction dependency
	// ServiceTier and ReasoningEffort are replaced after accepted response.create
	// filtering; RequestedReasoningEffort is captured before reasoning policy.
	// Separate atomic stores do not form a combined snapshot.
	ServiceTier              atomic.Pointer[string] // latest accepted service tier
	ReasoningEffort          atomic.Pointer[string] // latest accepted recorded effort
	RequestedReasoningEffort atomic.Pointer[string] // latest captured client effort

	// sessionRequestModel is published by session.update and read by both relay
	// directions. A nonempty model in response.create overrides it for that frame.
	sessionRequestModel atomic.Pointer[string] // shared client model default
}

// NewUsageMeta initializes the trimmed session model and retains the supplied
// decoder. A blank initial model uses the first response.create model; decoder
// may be nil only when callers avoid metadata extraction methods.
func NewUsageMeta(initialRequestModel string, firstFrame []byte, decoder UsageDecoder) *UsageMeta {
	meta := &UsageMeta{decoder: decoder}
	model := strings.TrimSpace(initialRequestModel)
	if model == "" {
		model = RequestModelForFrame(firstFrame)
	}
	meta.sessionRequestModel.Store(&model)
	return meta
}

// InitFromFirstFrame stores accepted tier and recorded effort from
// policyOutput. A nil receiver is ignored; a usable decoder is required, and
// original requested effort is captured separately before policy rewriting.
func (m *UsageMeta) InitFromFirstFrame(policyOutput []byte) {
	if m == nil {
		return
	}
	m.ServiceTier.Store(m.decoder.ServiceTier(policyOutput))
	m.ReasoningEffort.Store(m.decoder.ReasoningEffort(policyOutput))
}

// CaptureRequestedReasoningEffort stores client effort from bytes captured
// before policy rewriting. A nil receiver is ignored; callers supply a decoder
// and capture this value separately from accepted tier and recorded effort.
func (m *UsageMeta) CaptureRequestedReasoningEffort(originalBody []byte) {
	if m == nil {
		return
	}
	m.RequestedReasoningEffort.Store(m.decoder.RequestedReasoningEffort(originalBody))
}

// UpdateSessionRequestModel publishes a nonblank session.update model for later
// omitted-model requests. Nil receivers, other event types, and empty model
// values leave the current client model default available. Passthrough calls
// this before mapping and later policy checks, so rejecting that session.update
// does not undo the stored default.
func (m *UsageMeta) UpdateSessionRequestModel(payload []byte) {
	if m == nil {
		return
	}
	if model := RequestModelFromSessionFrame(payload); model != "" {
		m.sessionRequestModel.Store(&model)
	}
}

// RequestModelForFrame prefers a nonblank response.create model and otherwise
// reads the session default. A nil receiver falls back to frame extraction;
// session.update changes the default through UpdateSessionRequestModel.
func (m *UsageMeta) RequestModelForFrame(payload []byte) string {
	if m == nil {
		return RequestModelForFrame(payload)
	}
	if model := RequestModelForFrame(payload); model != "" {
		return model
	}
	return m.LoadSessionRequestModel()
}

// UpdateFromResponseCreate replaces accepted tier and recorded effort after
// policy processing. Missing metadata stores nil, so values from an earlier
// turn are cleared; requested effort is updated by its separate capture method.
func (m *UsageMeta) UpdateFromResponseCreate(policyOutput []byte) {
	if m == nil {
		return
	}
	m.ServiceTier.Store(m.decoder.ServiceTier(policyOutput))
	m.ReasoningEffort.Store(m.decoder.ReasoningEffort(policyOutput))
}

// RequestModelForFrame extracts a trimmed model when the frame type is
// response.create. Empty bytes or another type return empty; this label-based
// extraction does not validate the entire JSON document.
func RequestModelForFrame(payload []byte) string {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "model").String())
}

// RequestModelFromSessionFrame extracts the trimmed session.model from a
// session.update frame. Other types and empty bytes return empty; callers
// separately validate or forward the complete request payload.
func RequestModelFromSessionFrame(payload []byte) string {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "session.update" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "session.model").String())
}

// LoadSessionRequestModel reads the client model default through its atomic
// pointer. A nil receiver or unpublished model returns empty; frame-specific
// model selection remains the responsibility of RequestModelForFrame.
func (m *UsageMeta) LoadSessionRequestModel() string {
	if m == nil {
		return ""
	}
	if value := m.sessionRequestModel.Load(); value != nil {
		return *value
	}
	return ""
}
