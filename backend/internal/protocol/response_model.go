package protocol

import (
	"strings"

	"github.com/tidwall/gjson"
)

// response_model.go collects upstream model declarations and selects the
// observed name retained for an attempt or turn.
// Upstream responses can declare a model before generation and repeat it when
// the request reaches a terminal event. Administrator audits need the model
// reported by that upstream payload before conversion rewrites the client's
// visible name; they cannot derive it from the request's routing model.
//
// Responses terminal declarations supersede the first observed declaration,
// while Chat and Messages retain their first usable declaration. Gemini updates
// the preferred declaration on each recognized response payload, including
// nested Google wrappers. Invalid or absent declarations leave the observer's
// existing evidence intact. One observer belongs to one attempt or WebSocket
// turn, and callers serialize its updates rather than sharing its mutable
// fields across concurrently observed requests.

// ResponseModelObserver retains upstream model evidence for one request attempt or turn.
type ResponseModelObserver struct {
	// first records the first nonterminal declaration, while terminal records
	// the latest preferred declaration. Model returns terminal when populated;
	// otherwise it returns first, and a fresh observer leaves both empty.
	first    string // first nonterminal model
	terminal string // latest preferred model
}

// Observe records a normalized, nonempty upstream model declaration. Nil
// receivers and blank declarations are ignored. The first nonterminal model
// remains available until a terminal declaration supplies a preferred result.
//
// Later terminal declarations replace earlier terminal evidence, while later
// nonterminal declarations leave the first model unchanged. Callers choose the
// terminal classification for their protocol and serialize observer updates.
// Construct a new observer for each attempt or turn so previous request
// evidence cannot win the next request's model selection.
func (o *ResponseModelObserver) Observe(model string, terminal bool) {
	if o == nil {
		return
	}
	model = NormalizeResponseModel(model)
	if model == "" {
		return
	}
	if terminal {
		// A terminal declaration describes the terminal response state and can
		// supersede its initial model; retain that preferred evidence even after later nonterminal payloads.
		o.terminal = model
	} else if o.first == "" {
		o.first = model
	}
}

// NormalizeResponseModel trims outer whitespace and caps the declared model
// at 200 runes for upstream-response audit storage. Names within the limit keep
// their supplied spelling, and truncation retains complete multibyte characters.
func NormalizeResponseModel(model string) string {
	model = strings.TrimSpace(model)
	runes := []rune(model)
	if len(runes) > 200 {
		// Truncate by runes so multibyte model characters remain complete.
		return string(runes[:200])
	}
	return model
}

// ObserveOpenAI reads a model declaration from response.model before the
// top-level model field. Valid Responses terminal events supply preferred model
// evidence, while payloads with a choices array follow Chat's first-declaration rule.
//
// Event classification comes from the caller's SSE or response handling.
// A choices array prevents a Responses-shaped event label from replacing the
// Chat model with later preferred evidence. Missing or invalid declarations
// leave the observer unchanged; reuse the observer only within the same attempt
// or WebSocket turn.
func (o *ResponseModelObserver) ObserveOpenAI(payload []byte, event string) {
	terminal := IsResponseModelTerminalEvent(event) && !gjson.GetBytes(payload, "choices").IsArray()
	o.Observe(ResponseModelString(payload, "response.model", "model"), terminal)
}

// ObserveAnthropic reads message.model before the top-level Messages model.
// Usable declarations enter the first-observation path; later message events
// cannot replace that first name, and missing or invalid JSON leaves existing
// model evidence unchanged.
func (o *ResponseModelObserver) ObserveAnthropic(payload []byte) {
	o.Observe(ResponseModelString(payload, "message.model", "model"), false)
}

// ObserveGemini records the preferred model from modelVersion or supported
// nested Google response wrappers, in the declared path order. Each usable
// declaration replaces previous preferred evidence; missing or invalid metadata
// leaves the observer unchanged.
func (o *ResponseModelObserver) ObserveGemini(payload []byte) {
	o.Observe(ResponseModelString(payload, "modelVersion", "response.modelVersion", "response.response.modelVersion"), true)
}

// Model returns the latest preferred declaration when present, otherwise the
// first nonterminal declaration. Nil receivers or observers with no usable
// declarations return an empty string for callers' absent-model handling.
func (o *ResponseModelObserver) Model() string {
	if o == nil {
		return ""
	}
	if o.terminal != "" {
		return o.terminal
	}
	return o.first
}

// ResponseModelString returns the first nonblank string at paths in their
// supplied order, trimming surrounding whitespace. A missing usable string or
// invalid JSON returns empty; numeric and null values cannot provide model identity.
func ResponseModelString(payload []byte, paths ...string) string {
	for _, path := range paths {
		value := gjson.GetBytes(payload, path)
		if value.Type == gjson.String && strings.TrimSpace(value.Str) != "" {
			// Path extraction can expose a model from a truncated document.
			// Validate the complete payload before treating that text as an upstream identity declaration.
			if !gjson.ValidBytes(payload) {
				return ""
			}
			return strings.TrimSpace(value.Str)
		}
	}
	return ""
}

// IsResponseModelTerminalEvent reports whether a trimmed event name is a
// recognized Responses completion, failure, incomplete, or cancellation event.
// Both cancellation spellings are accepted; other names return false for
// nonterminal model observation.
func IsResponseModelTerminalEvent(event string) bool {
	switch strings.TrimSpace(event) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}
