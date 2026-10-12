package compact

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Legacy compact exchanges return one JSON response even when the downstream
// client requests Responses streaming. The bridge emits an output-item event
// for each object in output, followed by a completed event containing the full
// response. Unknown JSON fields remain in the completed response and copied
// output objects.
//
// SSE data occupies one line, so JSON is compacted before event framing. A
// missing or unusable response ID is replaced through the caller's generator,
// and usage is retained only when all required token counts parse as unsigned
// decimal integers. These patches keep the generated response shape usable
// while leaving other captured fields available to downstream consumers.

// StreamPayload converts a unary compact JSON object into ordered SSE events.
// Invalid or nonobject input returns nil and false; callers provide newID when
// the response lacks a nonempty string ID, and the function returns false if
// any JSON patch fails.
//
// Output-item events retain each object and assign sequential indexes while
// skipping nonobject array entries. The completed event contains the compacted
// response, including unknown fields, with its ID patched when needed. Usage
// that fails the unsigned-integer shape check is removed before emission so
// malformed token counts do not reach the generated completed event.
func StreamPayload(finalResponse []byte, newID func() string) ([]byte, bool) {
	if len(finalResponse) == 0 || !gjson.ValidBytes(finalResponse) {
		return nil, false
	}
	if !gjson.ParseBytes(finalResponse).IsObject() {
		return nil, false
	}
	// SSE treats each raw newline as a framing break. Compact pretty-printed
	// JSON onto one data line so the downstream decoder receives the complete object.
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, finalResponse); err != nil {
		return nil, false
	}
	response := compacted.Bytes()
	root := gjson.ParseBytes(response)
	responseID := root.Get("id")
	// Use the caller's generator when the captured response lacks a usable
	// string ID. Patch the response before embedding it in the completed event.
	if responseID.Type != gjson.String || strings.TrimSpace(responseID.String()) == "" {
		next, err := sjson.SetBytes(response, "id", newID())
		if err != nil {
			return nil, false
		}
		response = next
	}
	if usage := gjson.GetBytes(response, "usage"); usage.Exists() && !UsageParsableByCodex(usage) {
		// The completed event uses the unsigned token-count shape. Omit unusable
		// usage while retaining the response's output and unknown metadata fields.
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
		// Output-item events carry objects, so skip scalar candidates here while
		// the completed event still retains the captured output value, including those entries.
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

// UsageParsableByCodex reports whether usage contains all three required token
// counts as unsigned decimal uint64 literals. Nonobjects, absent or nonnumeric
// fields, negative values, fractions, exponent notation, and overflow return false.
func UsageParsableByCodex(usage gjson.Result) bool {
	if !usage.IsObject() {
		return false
	}
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		value := usage.Get(field)
		if value.Type != gjson.Number {
			return false
		}
		// The numeric JSON type alone admits fractions and exponent notation.
		// Parsing the raw literal enforces the unsigned decimal form used when
		// deciding whether the completed event can retain these counts.
		if _, err := strconv.ParseUint(value.Raw, 10, 64); err != nil {
			return false
		}
	}
	return true
}
