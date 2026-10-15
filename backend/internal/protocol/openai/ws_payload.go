package openai

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ws_payload.go classifies token events, rewrites WebSocket response models,
// and exposes request and usage payloads.
// Gateway WebSocket streaming and its HTTP bridge start first-token timing from
// an accepted token-event label. The classifier reads no payload content, so
// an accepted empty delta or output annotation can start that timing. The
// passthrough relay accepts a .delta suffix, response.output_text.done, or
// response.function_call_arguments.done, so the two label sets differ in both
// directions.
//
// Model replacement requires matching source strings at the root or nested
// response model, after literal byte screens select the message. Those screens
// can skip escaped spellings, and independent best-effort patches can leave
// one model changed while another remains. Usage population has a different
// update rule from terminal-aware merging: any usage object accepted by the
// complete-JSON extractor replaces the destination, including an empty object
// whose counters decode to zero. Callers retain responsibility for request
// validation, output timing, and serialized access to mutable usage state.

// IsWSTokenEvent selects trimmed labels used by gateway WebSocket streaming
// and the HTTP bridge to set first-token time. Created, in-progress, and the
// output-item added/done labels are excluded; remaining labels containing
// .delta or starting with response.output are accepted.
// Response terminal labels return false. Payload content is not inspected,
// and the passthrough relay uses a different label predicate.
func IsWSTokenEvent(eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return false
	}
	switch eventType {
	case "response.created", "response.in_progress", "response.output_item.added", "response.output_item.done":
		return false
	}
	if strings.Contains(eventType, ".delta") {
		return true
	}
	if strings.HasPrefix(eventType, "response.output_text") {
		return true
	}
	if strings.HasPrefix(eventType, "response.output") {
		return true
	}
	// Gateway callers set first-token time from the first accepted label.
	// Excluding terminal labels keeps terminal-only streams from recording
	// completion time as a first-token observation.
	return false
}

// ReplaceWSMessageModel patches matching root and nested response model strings
// from fromModel to toModel. Empty messages, blank model arguments, equal model
// strings, failed byte screens, or missing matching fields retain the supplied
// message.
//
// Arguments are compared as supplied after blank checks. Literal model/source
// byte screens can skip escaped spellings or select unrelated quoted values;
// extracted fields determine the actual replacements. Patches fail independently,
// so one completed replacement can remain when another edit fails. Complete
// message validity and mapping policy remain with the caller.
func ReplaceWSMessageModel(message []byte, fromModel, toModel string) []byte {
	if len(message) == 0 {
		return message
	}
	if strings.TrimSpace(fromModel) == "" || strings.TrimSpace(toModel) == "" || fromModel == toModel {
		return message
	}
	if !bytes.Contains(message, []byte(`"model"`)) || !bytes.Contains(message, []byte(fromModel)) {
		// Messages missing the quoted model marker or source-model bytes
		// bypass path extraction. Escaped spellings can fail this screen
		// even when their decoded value would match a target model path.
		return message
	}
	modelValues := gjson.GetManyBytes(message, "model", "response.model")
	replaceModel := modelValues[0].Exists() && modelValues[0].Str == fromModel
	replaceResponseModel := modelValues[1].Exists() && modelValues[1].Str == fromModel
	if !replaceModel && !replaceResponseModel {
		return message
	}
	updated := message
	// Root and response model edits are independent best-effort patches.
	// Retain each completed replacement even when another field patch cannot
	// be applied, including messages with only one matching model field.
	if replaceModel {
		if next, err := sjson.SetBytes(updated, "model", toModel); err == nil {
			updated = next
		}
	}
	if replaceResponseModel {
		if next, err := sjson.SetBytes(updated, "response.model", toModel); err == nil {
			updated = next
		}
	}
	return updated
}

// PopulateUsageFromResponseJSON replaces usage with the shared extractor's
// parsed snapshot, including zero counters from an empty usage object.
// Nil destinations, empty or invalid bodies, and absent usage objects retain
// previous counts; callers serialize access to the destination.
func PopulateUsageFromResponseJSON(body []byte, usage *ForwardUsage) {
	if usage == nil || len(body) == 0 {
		return
	}
	if parsedUsage, ok := ExtractOpenAIUsageFromJSONBytes(body); ok {
		*usage = parsedUsage
	}
}

// RequestPayloadView returns the nested response object of a response.create
// payload when that object is present, otherwise the parsed root value.
// Nonobject roots pass through; callers validate complete JSON and required
// request fields separately from this gjson view selection.
func RequestPayloadView(body []byte) gjson.Result {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return root
	}
	if root.Get("type").String() == "response.create" && root.Get("response").IsObject() {
		return root.Get("response")
	}
	return root
}
