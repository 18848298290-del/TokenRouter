package openai

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponseLifecycleNormalizer repairs lifecycle events in one HTTP Responses stream while retaining unknown fields.
// It flags duplicate successful terminal events; callers still need their usage and observation data.
// @project-doc docs/interfaces/openai_upstream.md#openai_protocol_dispatch
type ResponseLifecycleNormalizer struct {
	nextSequence int64
	completedID  string
}

// IsResponseLifecycleEvent recognizes event types carrying a complete Response object.
func IsResponseLifecycleEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress", "response.completed", "response.done",
		"response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

// Normalize wraps bare Response objects and converts legacy done events according to response status.
// It returns the payload, event type, and duplicate-success flag. Invalid JSON and unknown events pass through.
func (n *ResponseLifecycleNormalizer) Normalize(data []byte, eventType string) ([]byte, string, bool) {
	eventType = EffectiveOpenAISSEEventType(data, eventType)
	sequence := n.nextSequence
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
		// Wrap bare objects only when their fields identify a Response.
		if gjson.GetBytes(data, "object").String() != "response" || gjson.GetBytes(data, "id").String() == "" {
			return data, eventType, false
		}
		response = gjson.ParseBytes(data)
		wrapped, err := json.Marshal(struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}{Type: eventType, Response: data})
		if err != nil {
			return data, eventType, false
		}
		data = wrapped
		changed = true
	}
	if eventType == "response.done" {
		status := response.Get("status").String()
		if status == "" {
			// Infer a missing status from failure evidence; missing status alone does not imply success.
			switch {
			case response.Get("error").Exists() && response.Get("error").Type != gjson.Null:
				status = "failed"
			case response.Get("incomplete_details").Exists() && response.Get("incomplete_details").Type != gjson.Null:
				status = "incomplete"
			default:
				return data, eventType, false
			}
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
			// Unknown and ongoing statuses do not imply successful completion.
			return data, eventType, false
		}
	}
	if gjson.GetBytes(data, "type").String() != eventType {
		data, _ = sjson.SetBytes(data, "type", eventType)
		changed = true
	}
	if changed && !gjson.GetBytes(data, "sequence_number").Exists() {
		data, _ = sjson.SetBytes(data, "sequence_number", sequence)
	}

	id := response.Get("id").String()
	duplicate := eventType == "response.completed" && id != "" && n.completedID == id
	if eventType == "response.completed" && id != "" {
		n.completedID = id
	}
	return data, eventType, duplicate
}
