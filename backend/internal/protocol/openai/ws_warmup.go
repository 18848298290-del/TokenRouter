package openai

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// IsWSWarmupPayload recognizes a valid response.create request with generate set to boolean false.
func IsWSWarmupPayload(payload []byte) bool {
	return gjson.ValidBytes(payload) && gjson.GetBytes(payload, "type").String() == "response.create" && gjson.GetBytes(payload, "generate").Type == gjson.False
}

// WSWarmupEvents builds empty warmup events sharing the caller-provided response ID and Unix timestamp.
// @project-doc docs/interfaces/openai_upstream.md#responses_ws_warmup
func WSWarmupEvents(responseID, model string, createdAt int64) ([][]byte, error) {
	response := ResponsesResponse{ID: responseID, Object: "response", CreatedAt: createdAt, Model: model, Output: []ResponsesOutput{}}
	events := make([][]byte, 0, 2)
	for sequence, status := range []string{"in_progress", "completed"} {
		response.Status = status
		eventType := "response.created"
		if status == "completed" {
			eventType = "response.completed"
			response.Usage = &ResponsesUsage{}
		}
		event := struct {
			Type           string            `json:"type"`
			SequenceNumber int               `json:"sequence_number"`
			Response       ResponsesResponse `json:"response"`
		}{eventType, sequence, response}
		body, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		events = append(events, body)
	}
	return events, nil
}
