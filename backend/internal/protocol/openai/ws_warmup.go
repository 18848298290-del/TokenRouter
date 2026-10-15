package openai

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// ws_warmup.go recognizes nongenerating Responses warmups and constructs their
// created and completed response events.
// A Responses WebSocket warmup asks for generate=false before a business turn
// continues from the returned response ID. Local HTTP bridges answer that
// warmup with an empty created/completed pair, leaving replay history and
// inference execution to the connection's caller.

// IsWSWarmupPayload reports whether valid JSON carries response.create and
// boolean false at generate. Missing, null, or string-valued generate fields
// do not select warmup, and invalid trailing JSON returns false.
func IsWSWarmupPayload(payload []byte) bool {
	return gjson.ValidBytes(payload) && gjson.GetBytes(payload, "type").String() == "response.create" && gjson.GetBytes(payload, "generate").Type == gjson.False
}

// WSWarmupEvents builds ordered created and completed events sharing the supplied
// response ID, model, and Unix timestamp in seconds. Both have empty output;
// completed includes zero usage. Callers provide the identity saved for later
// continuation and deliver the pair in its returned order.
// @project-doc docs/interfaces/openai_upstream.md#responses_ws_warmup
func WSWarmupEvents(responseID, model string, createdAt int64) ([][]byte, error) {
	response := ResponsesResponse{ID: responseID, Object: "response", CreatedAt: createdAt, Model: model, Output: []ResponsesOutput{}}
	events := make([][]byte, 0, 2)
	for sequence, status := range []string{"in_progress", "completed"} {
		// The pair describes one warmup response progressing to completion.
		// Reuse its identity while sequence zero and one distinguish the
		// lifecycle events for continuation clients.
		response.Status = status
		eventType := "response.created"
		if status == "completed" {
			eventType = "response.completed"
			response.Usage = &ResponsesUsage{}
		}
		event := struct {
			Type           string            `json:"type"`            // warmup lifecycle type
			SequenceNumber int               `json:"sequence_number"` // ordered event position
			Response       ResponsesResponse `json:"response"`        // shared warmup response
		}{eventType, sequence, response}
		body, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		events = append(events, body)
	}
	return events, nil
}
