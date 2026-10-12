package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// HTTP lifecycle payloads can need wrapping or status-derived terminal labels
// before Responses readers use them. The fixtures check retained vendor fields,
// effective labels, repaired sequences, and duplicate detection while keeping
// exact-byte assertions separate from repaired-field assertions.

// TestResponseLifecycleNormalizer supplies bare, wrapped, legacy, invalid, and
// unidentified lifecycle payloads. Supported repairs must retain vendor detail
// and provide the expected type/status/sequence, while unchanged cases retain
// their supplied bytes and avoid a duplicate flag.
func TestResponseLifecycleNormalizer(t *testing.T) {
	tests := []struct {
		// header supplies the SSE event label when payload has no nonblank type.
		// The normalizer prefers the payload type, and an empty header leaves that
		// payload-derived decision in control.
		//
		// wantType is checked for every fixture as the returned effective event
		// label. wantStatus is checked inside the repaired response only when
		// unchanged is false; byte-preserving cases return before field assertions.
		// name: fixture subtest display label
		// header: input SSE event label
		// payload: supplied SSE payload text
		// wantType: expected effective event label
		// wantStatus: expected nested response status
		name, header, payload, wantType, wantStatus string
		// unchanged selects exact-byte comparison and skips repaired JSON field
		// checks. A false value requires expected type/status, retained vendor
		// details and a numeric sequence in the result.
		unchanged bool // supplied payload bytes retained
	}{
		{"bare created", "response.created", `{"object":"response","id":"resp_1","status":"in_progress","output":[],"vendor":{"opaque":"keep"}}`, "response.created", "in_progress", false},
		{"bare completed", "response.completed", `{"object":"response","id":"resp_1","status":"completed","output":[],"vendor":{"opaque":"keep"}}`, "response.completed", "completed", false},
		{"wrapped missing type", "response.completed", `{"response":{"id":"resp_1","status":"completed","vendor":{"opaque":"keep"}}}`, "response.completed", "completed", false},
		{"done completed", "response.done", `{"type":"response.done","response":{"id":"resp_1","status":"completed","vendor":{"opaque":"keep"}}}`, "response.completed", "completed", false},
		{"done failed", "response.done", `{"type":"response.done","response":{"id":"resp_1","status":"failed","error":{"code":"server_error"},"vendor":{"opaque":"keep"}}}`, "response.failed", "failed", false},
		{"done incomplete", "response.done", `{"type":"response.done","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"vendor":{"opaque":"keep"}}}`, "response.incomplete", "incomplete", false},
		{"missing status with error", "response.done", `{"type":"response.done","response":{"id":"resp_1","error":{"code":"server_error"},"vendor":{"opaque":"keep"}}}`, "response.failed", "failed", false},
		{"missing status with incomplete details", "response.done", `{"type":"response.done","response":{"id":"resp_1","error":null,"incomplete_details":{"reason":"max_output_tokens"},"vendor":{"opaque":"keep"}}}`, "response.incomplete", "incomplete", false},
		{"missing status without evidence", "response.done", `{"type":"response.done","response":{"id":"resp_1","error":null,"incomplete_details":null,"output":[]}}`, "response.done", "", true},
		{"standard unchanged", "response.completed", `{"type":"response.completed","sequence_number":7,"response":{"id":"resp_1","status":"completed"}}`, "response.completed", "completed", true},
		{"invalid JSON", "response.completed", `{"object":"response"`, "response.completed", "", true},
		{"unidentified object", "response.completed", `{"output":[]}`, "response.completed", "", true},
		{"unknown event", "custom.event", `{"object":"response","id":"resp_1"}`, "custom.event", "", true},
		{"done ongoing", "response.done", `{"type":"response.done","response":{"id":"resp_1","status":"in_progress"}}`, "response.done", "in_progress", true},
		{"sentinel unchanged", "", `[DONE]`, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var normalizer ResponseLifecycleNormalizer
			data, eventType, duplicate := normalizer.Normalize([]byte(tt.payload), tt.header)
			require.False(t, duplicate)
			require.Equal(t, tt.wantType, eventType)
			if tt.unchanged {
				require.Equal(t, tt.payload, string(data))
				return
			}
			require.Equal(t, tt.wantType, gjson.GetBytes(data, "type").String())
			require.Equal(t, tt.wantStatus, gjson.GetBytes(data, "response.status").String())
			require.Equal(t, "keep", gjson.GetBytes(data, "response.vendor.opaque").String())
			require.Equal(t, gjson.Number, gjson.GetBytes(data, "sequence_number").Type)
		})
	}
}

// TestResponseLifecycleNormalizerDuplicateKeepsUsage observes a numbered delta
// before repeated completed response identities. Repaired sequence must follow
// the delta, the repeated ID must be flagged, its revised usage must remain, and
// another completed ID must be accepted.
func TestResponseLifecycleNormalizerDuplicateKeepsUsage(t *testing.T) {
	var normalizer ResponseLifecycleNormalizer
	normalizer.Normalize([]byte(`{"type":"response.output_text.delta","sequence_number":20,"delta":"hi"}`), "")
	first, _, duplicate := normalizer.Normalize([]byte(`{"object":"response","id":"resp_1","status":"completed","usage":{"output_tokens":2}}`), "response.completed")
	require.False(t, duplicate)
	require.EqualValues(t, 21, gjson.GetBytes(first, "sequence_number").Int())
	second, eventType, duplicate := normalizer.Normalize([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"completed","usage":{"output_tokens":3}}}`), "response.done")
	require.True(t, duplicate)
	require.Equal(t, "response.completed", eventType)
	require.EqualValues(t, 3, gjson.GetBytes(second, "response.usage.output_tokens").Int())
	_, _, duplicate = normalizer.Normalize([]byte(`{"type":"response.done","response":{"id":"resp_2","status":"completed"}}`), "")
	require.False(t, duplicate)
}
