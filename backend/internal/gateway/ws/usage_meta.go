package ws

import (
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
)

// UsageDecoder parses service-tier and reasoning-effort metadata.
type UsageDecoder interface {
	ServiceTier([]byte) *string
	ReasoningEffort([]byte) *string
	RequestedReasoningEffort([]byte) *string
}

// UsageMeta holds atomic session defaults shared by both relay directions; TurnPayload owns per-turn snapshots.
type UsageMeta struct {
	decoder                  UsageDecoder
	ServiceTier              atomic.Pointer[string]
	ReasoningEffort          atomic.Pointer[string]
	RequestedReasoningEffort atomic.Pointer[string]

	// Shared session default; each turn retains its own model snapshot.
	sessionRequestModel atomic.Pointer[string]
}

func NewUsageMeta(initialRequestModel string, firstFrame []byte, decoder UsageDecoder) *UsageMeta {
	meta := &UsageMeta{decoder: decoder}
	model := strings.TrimSpace(initialRequestModel)
	if model == "" {
		model = RequestModelForFrame(firstFrame)
	}
	meta.sessionRequestModel.Store(&model)
	return meta
}

func (m *UsageMeta) InitFromFirstFrame(policyOutput []byte) {
	if m == nil {
		return
	}
	m.ServiceTier.Store(m.decoder.ServiceTier(policyOutput))
	m.ReasoningEffort.Store(m.decoder.ReasoningEffort(policyOutput))
}

// CaptureRequestedReasoningEffort records client effort before policy and model rewrites.
func (m *UsageMeta) CaptureRequestedReasoningEffort(originalBody []byte) {
	if m == nil {
		return
	}
	m.RequestedReasoningEffort.Store(m.decoder.RequestedReasoningEffort(originalBody))
}

func (m *UsageMeta) UpdateSessionRequestModel(payload []byte) {
	if m == nil {
		return
	}
	if model := RequestModelFromSessionFrame(payload); model != "" {
		m.sessionRequestModel.Store(&model)
	}
}

func (m *UsageMeta) RequestModelForFrame(payload []byte) string {
	if m == nil {
		return RequestModelForFrame(payload)
	}
	if model := RequestModelForFrame(payload); model != "" {
		return model
	}
	return m.LoadSessionRequestModel()
}

func (m *UsageMeta) UpdateFromResponseCreate(policyOutput []byte) {
	if m == nil {
		return
	}
	m.ServiceTier.Store(m.decoder.ServiceTier(policyOutput))
	m.ReasoningEffort.Store(m.decoder.ReasoningEffort(policyOutput))
}

func RequestModelForFrame(payload []byte) string {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "model").String())
}

func RequestModelFromSessionFrame(payload []byte) string {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "session.update" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "session.model").String())
}

// LoadSessionRequestModel atomically reads the shared client model, returning empty when none is stored.
func (m *UsageMeta) LoadSessionRequestModel() string {
	if m == nil {
		return ""
	}
	if value := m.sessionRequestModel.Load(); value != nil {
		return *value
	}
	return ""
}
