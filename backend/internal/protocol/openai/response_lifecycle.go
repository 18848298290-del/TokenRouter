package openai

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// response_lifecycle.go normalizes Responses lifecycle envelopes and terminal
// labels while tracking sequence and completed-response identity.
// HTTP Responses streams can carry bare Response objects beside SSE lifecycle
// headers or legacy response.done events with status-specific outcomes.
// Normalization repairs those representations before the HTTP readers decide
// which terminal event to deliver and which usage observations to retain.
//
// Each invocation selects a sequence and records its next int64 value,
// including invalid or unrelated events. A numeric sequence field is considered
// through gjson's integer conversion before that value is incremented.
// A legacy done event becomes a status-specific terminal only when its status
// or failure evidence supports that choice. Duplicate-success detection compares
// the current nonempty completed ID with the last recorded completed ID;
// another completed ID replaces that memory. Field patches are best effort,
// and callers retain responsibility for stream ordering and duplicate delivery.

// ResponseLifecycleNormalizer repairs lifecycle payloads for one HTTP Responses stream.
// @project-doc docs/interfaces/openai_upstream.md#openai_protocol_dispatch
type ResponseLifecycleNormalizer struct {
	// nextSequence stores the selected sequence plus one before event
	// validation. Supplied numbers are compared through gjson's Int conversion
	// against the current value before that next value is recorded.
	nextSequence int64
	// completedID is the last nonempty ID classified as response.completed.
	// It remains across other event types and is replaced by another completed ID.
	completedID string
}

// IsResponseLifecycleEvent reports whether a trimmed event type belongs to
// the supported Responses lifecycle set. It recognizes created, in-progress,
// and terminal labels; callers inspect the payload separately to determine
// whether a Response object is present.
func IsResponseLifecycleEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress", "response.completed", "response.done",
		"response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

// Normalize returns a repaired payload, its effective event type, and whether
// its completed ID repeats the last recorded completed ID. Callers use one
// nonnil normalizer per ordered HTTP stream and continue extracting usage from
// duplicate terminal payloads even when suppressing their delivery.
//
// Sequence state updates before validity checks, using gjson's integer
// conversion for number fields and int64 addition without a range check.
// Bare response objects require object=response and a nonempty ID before
// wrapping; legacy done events require
// a recognized status or nonnull failure evidence before terminal conversion.
// Invalid and unsupported events can retain their bytes after updating state.
// JSON field patches suppress errors, so repairs can be partial; Normalize
// reports the effective type and duplicate decision separately from patch success.
func (n *ResponseLifecycleNormalizer) Normalize(data []byte, eventType string) ([]byte, string, bool) {
	eventType = EffectiveOpenAISSEEventType(data, eventType)
	sequence := n.nextSequence
	// Invalid and unrelated events still consume a selected sequence. This
	// keeps them in the counter used when later lifecycle repairs insert a
	// missing sequence, although int64 overflow is not checked.
	if value := gjson.GetBytes(data, "sequence_number"); value.Type == gjson.Number && value.Int() >= sequence {
		sequence = value.Int()
	}
	n.nextSequence = sequence + 1
	if !IsResponseLifecycleEvent(eventType) || !gjson.ValidBytes(data) {
		return data, eventType, false
	}

	response := gjson.GetBytes(data, "response")
	changed := false
	if !response.IsObject() {
		// A bare payload needs both the response marker and an ID before
		// wrapping; otherwise unrelated JSON could be mistaken for a lifecycle response object.
		if gjson.GetBytes(data, "object").String() != "response" || gjson.GetBytes(data, "id").String() == "" {
			return data, eventType, false
		}
		response = gjson.ParseBytes(data)
		wrapped, err := json.Marshal(struct {
			Type     string          `json:"type"`     // lifecycle event type
			Response json.RawMessage `json:"response"` // supplied response object
		}{Type: eventType, Response: data})
		if err != nil {
			// Failed wrapping leaves the supplied payload intact after the sequence
			// update. Return it so callers retain the response evidence for their
			// own handling.
			return data, eventType, false
		}
		data = wrapped
		changed = true
	}
	if eventType == "response.done" {
		status := response.Get("status").String()
		if status == "" {
			// A missing status can establish failure or incompletion from nonnull
			// evidence. No such evidence leaves done unchanged rather than
			// inventing a successful terminal.
			switch {
			case response.Get("error").Exists() && response.Get("error").Type != gjson.Null:
				status = "failed"
			case response.Get("incomplete_details").Exists() && response.Get("incomplete_details").Type != gjson.Null:
				status = "incomplete"
			default:
				return data, eventType, false
			}
			// Status patching is best effort: the chosen terminal classification
			// still proceeds when the JSON edit fails, so callers cannot infer
			// complete repair from its type.
			data, _ = sjson.SetBytes(data, "response.status", status)
			changed = true
		}
		switch status {
		case "completed":
			eventType = "response.completed"
		case "failed":
			eventType = "response.failed"
		case "incomplete":
			eventType = "response.incomplete"
		case "cancelled", "canceled":
			eventType = "response." + status
		default:
			// Unknown and ongoing statuses supply no supported terminal outcome.
			// Return the current payload, which can already contain a
			// bare-response wrapper added above.
			return data, eventType, false
		}
	}
	if gjson.GetBytes(data, "type").String() != eventType {
		// Synchronize the payload label with the effective terminal classification
		// as a best-effort edit; its ignored error leaves the type result
		// independently available to callers.
		data, _ = sjson.SetBytes(data, "type", eventType)
		changed = true
	}
	if changed && !gjson.GetBytes(data, "sequence_number").Exists() {
		// Repaired events need a missing sequence filled from this stream's
		// observation order. The best-effort insertion does not overwrite a
		// sequence field already supplied.
		data, _ = sjson.SetBytes(data, "sequence_number", sequence)
	}

	id := response.Get("id").String()
	// Retain the most recent successful response identity across other events.
	// Repeated completed IDs can suppress duplicate delivery while their
	// usage remains available for accounting.
	duplicate := eventType == "response.completed" && id != "" && n.completedID == id
	if eventType == "response.completed" && id != "" {
		n.completedID = id
	}
	return data, eventType, duplicate
}
