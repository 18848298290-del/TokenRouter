package bridge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResponseFormatJsonSchemaRoundTrip converts a named strict Chat schema
// to Responses and back. The converted format must be nonempty and the returned
// Chat value must remain JSON-equivalent to the supplied fixture.
func TestResponseFormatJsonSchemaRoundTrip(t *testing.T) {
	chatFormat := json.RawMessage(`{
		"type":"json_schema",
		"json_schema":{
			"name":"answer",
			"description":"structured answer",
			"schema":{"type":"object","properties":{"value":{"type":"string"}}},
			"strict":true
		}
	}`)

	responsesFormat := chatResponseFormatToResponsesTextFormat(chatFormat)
	require.NotEmpty(t, responsesFormat)
	roundTripped := responsesTextFormatToChatResponseFormat(responsesFormat)
	assert.JSONEq(t, string(chatFormat), string(roundTripped))
}

// TestResponseFormatPassesThroughCompatibleShapes supplies json_object, text,
// and a custom format. Both directions must retain each fixture as JSON-equivalent
// content because those shapes require no supported schema nesting change.
func TestResponseFormatPassesThroughCompatibleShapes(t *testing.T) {
	tests := []struct {
		name string          // response-format fixture label
		raw  json.RawMessage // supplied format JSON
	}{
		{name: "json_object", raw: json.RawMessage(`{"type":"json_object"}`)},
		{name: "text", raw: json.RawMessage(`{"type":"text"}`)},
		{name: "unknown", raw: json.RawMessage(`{"type":"custom","option":true}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.JSONEq(t, string(tt.raw), string(chatResponseFormatToResponsesTextFormat(tt.raw)))
			assert.JSONEq(t, string(tt.raw), string(responsesTextFormatToChatResponseFormat(tt.raw)))
		})
	}
}

// TestResponseFormatTreatsNullAsUnset supplies whitespace-padded null format
// values to both conversion directions. Each call must return nil so an unset
// format does not become an encoded wrapper.
func TestResponseFormatTreatsNullAsUnset(t *testing.T) {
	require.Nil(t, chatResponseFormatToResponsesTextFormat(json.RawMessage(" null ")))
	require.Nil(t, responsesTextFormatToChatResponseFormat(json.RawMessage(" null ")))
}
