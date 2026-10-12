package protocol

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ResponseModelObserver records upstream model declarations for one attempt or WebSocket turn.
type ResponseModelObserver struct {
	first    string
	terminal string
}

// Observe records a nonempty normalized model; terminal declarations take precedence over the first declaration.
func (o *ResponseModelObserver) Observe(model string, terminal bool) {
	if o == nil {
		return
	}
	model = NormalizeResponseModel(model)
	if model == "" {
		return
	}
	if terminal {
		o.terminal = model
	} else if o.first == "" {
		o.first = model
	}
}

// NormalizeResponseModel trims whitespace and limits the model to 200 runes without rewriting its name.
func NormalizeResponseModel(model string) string {
	model = strings.TrimSpace(model)
	runes := []rune(model)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return model
}

// ObserveOpenAI reads model metadata; Chat choices retain the first declaration.
func (o *ResponseModelObserver) ObserveOpenAI(payload []byte, event string) {
	terminal := IsResponseModelTerminalEvent(event) && !gjson.GetBytes(payload, "choices").IsArray()
	o.Observe(ResponseModelString(payload, "response.model", "model"), terminal)
}

// ObserveAnthropic reads model declarations from a Message or message_start event.
func (o *ResponseModelObserver) ObserveAnthropic(payload []byte) {
	o.Observe(ResponseModelString(payload, "message.model", "model"), false)
}

// ObserveGemini retains the last model version and accepts Google response wrappers.
func (o *ResponseModelObserver) ObserveGemini(payload []byte) {
	o.Observe(ResponseModelString(payload, "modelVersion", "response.modelVersion", "response.response.modelVersion"), true)
}

// Model returns the observed model, or an empty string when no model was declared.
func (o *ResponseModelObserver) Model() string {
	if o == nil {
		return ""
	}
	if o.terminal != "" {
		return o.terminal
	}
	return o.first
}

// ResponseModelString returns the first nonempty string at the supplied paths in valid JSON.
func ResponseModelString(payload []byte, paths ...string) string {
	for _, path := range paths {
		value := gjson.GetBytes(payload, path)
		if value.Type == gjson.String && strings.TrimSpace(value.Str) != "" {
			if !gjson.ValidBytes(payload) {
				return ""
			}
			return strings.TrimSpace(value.Str)
		}
	}
	return ""
}

// IsResponseModelTerminalEvent recognizes Responses terminal events for model observation.
func IsResponseModelTerminalEvent(event string) bool {
	switch strings.TrimSpace(event) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}
