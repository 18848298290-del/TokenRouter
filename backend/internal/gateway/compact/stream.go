package compact

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StreamPayload converts a unary compact response to ordered SSE events while retaining unknown fields.
// It generates an ID only when the response lacks a nonempty string ID.
func StreamPayload(finalResponse []byte, newID func() string) ([]byte, bool) {
	if len(finalResponse) == 0 || !gjson.ValidBytes(finalResponse) {
		return nil, false
	}
	if !gjson.ParseBytes(finalResponse).IsObject() {
		return nil, false
	}
	// SSE data lines cannot contain raw newlines. Compact pretty-printed JSON
	// to one line before embedding it.
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, finalResponse); err != nil {
		return nil, false
	}
	response := compacted.Bytes()
	root := gjson.ParseBytes(response)
	responseID := root.Get("id")
	if responseID.Type != gjson.String || strings.TrimSpace(responseID.String()) == "" {
		next, err := sjson.SetBytes(response, "id", newID())
		if err != nil {
			return nil, false
		}
		response = next
	}
	if usage := gjson.GetBytes(response, "usage"); usage.Exists() && !UsageParsableByCodex(usage) {
		next, err := sjson.DeleteBytes(response, "usage")
		if err != nil {
			return nil, false
		}
		response = next
	}

	var buf bytes.Buffer
	outputIndex := 0
	appendEvent := func(eventType string, data []byte) {
		_, _ = buf.WriteString("event: ")
		_, _ = buf.WriteString(eventType)
		_, _ = buf.WriteString("\ndata: ")
		_, _ = buf.Write(data)
		_, _ = buf.WriteString("\n\n")
	}
	for _, item := range gjson.GetBytes(response, "output").Array() {
		if !item.IsObject() {
			continue
		}
		event, err := sjson.SetBytes([]byte(`{"type":"response.output_item.done"}`), "output_index", outputIndex)
		if err != nil {
			return nil, false
		}
		event, err = sjson.SetRawBytes(event, "item", []byte(item.Raw))
		if err != nil {
			return nil, false
		}
		appendEvent("response.output_item.done", event)
		outputIndex++
	}

	completed, err := sjson.SetRawBytes([]byte(`{"type":"response.completed"}`), "response", response)
	if err != nil {
		return nil, false
	}
	appendEvent("response.completed", completed)
	return buf.Bytes(), true
}

func UsageParsableByCodex(usage gjson.Result) bool {
	if !usage.IsObject() {
		return false
	}
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		value := usage.Get(field)
		if value.Type != gjson.Number {
			return false
		}
		// Codex parses token counts as unsigned integers. Fractions, negatives, and exponent
		// notation invalidate response.completed, so validate the raw JSON number literal.
		if _, err := strconv.ParseUint(value.Raw, 10, 64); err != nil {
			return false
		}
	}
	return true
}
