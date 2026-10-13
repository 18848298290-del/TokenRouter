package provider

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Responses failures need a protocol event even when an upstream body lacks a
// complete Response object. Event construction chooses a response ID and error
// message, retains available code/type evidence, and emits a failed Response
// in an SSE data frame.

// BuildOpenAIResponseFailedSSE encodes a failed Response event with trimmed
// model and response identity. A missing ID is generated, message extraction
// precedes the supplied default, and missing error code or message receives a
// stable generic value.
func BuildOpenAIResponseFailedSSE(responseID, model string, source []byte, fallbackMessage string) string {
	responseID = strings.TrimSpace(responseID)
	// Failure sources can omit response identity; generate one for the emitted
	// Response object so the terminal event still has a usable correlation key.
	if responseID == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	// Prefer the source error over caller and generic defaults so a rebuilt
	// terminal event continues to expose the observed failure to the client.
	message := strings.TrimSpace(openai.ExtractOpenAISSEErrorMessage(source))
	if message == "" {
		message = strings.TrimSpace(fallbackMessage)
	}
	if message == "" {
		message = "Upstream response failed"
	}
	// Sources can wrap the Response error or expose it directly; read both forms
	// before supplying the code used when no structured failure code was observed.
	code := strings.TrimSpace(gjson.GetBytes(source, "error.code").String())
	if code == "" {
		code = strings.TrimSpace(gjson.GetBytes(source, "response.error.code").String())
	}
	if code == "" {
		code = "upstream_error"
	}
	errorBody := map[string]any{"code": code, "message": message}
	if errorType := strings.TrimSpace(gjson.GetBytes(source, "error.type").String()); errorType != "" {
		errorBody["type"] = errorType
	}
	response := map[string]any{
		"id":     responseID,
		"object": "response",
		"model":  strings.TrimSpace(model),
		"status": "failed",
		"error":  errorBody,
	}
	// The event map contains strings and nested string maps, whose JSON encoding
	// cannot fail for unsupported values or cycles introduced by caller data.
	body, _ := json.Marshal(map[string]any{"type": "response.failed", "response": response})
	return "data: " + string(body) + "\n\n"
}
