package bridge

import (
	"encoding/json"
)

// Chat json_schema response formats nest name, schema, and strict settings
// beneath json_schema, while Responses text.format puts those settings beside
// type. Conversion retains child values as raw JSON so schema numbers are not
// decoded through floating-point representations during the nesting change.
//
// Flattening selects the inner wrapper object and assigns its type; outer
// wrapper extensions are outside the resulting flat object. Wrapping moves all
// flat keys except type into json_schema, unless a json_schema key already
// exists. Empty or null formats are unset, and unsupported/malformed shapes
// retain normalized copied bytes. The helpers change supported nesting and
// leave validation of the complete request's response-format policy to callers.

// chatResponseFormatToResponsesTextFormat flattens a Chat json_schema wrapper
// into a Responses format. Empty or null input returns nil; other format types
// and failed inner decoding/encoding return normalized copied input.
//
// The inner object's raw fields become the output, with type assigned to
// json_schema. Outer wrapper extension keys are therefore discarded on this
// conversion path, while valid numeric values inside the inner fields retain
// their encoded representation. Callers supply the surrounding request context
// and decide whether that supported format is appropriate for the target.
func chatResponseFormatToResponsesTextFormat(raw json.RawMessage) json.RawMessage {
	raw = normalizedRawJSON(raw)
	if len(raw) == 0 {
		return nil
	}

	obj, ok := rawJSONObject(raw)
	if !ok || rawString(obj["type"]) != "json_schema" {
		return raw
	}

	schemaRaw := normalizedRawJSON(obj["json_schema"])
	if len(schemaRaw) == 0 {
		return raw
	}

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		// An incompatible inner wrapper cannot supply the flat format object.
		// Retain normalized source bytes so the caller can apply its own format validation.
		return raw
	}
	schema["type"] = rawJSONString("json_schema")

	out, err := json.Marshal(schema)
	if err != nil {
		// A failed flat-schema encoding leaves the original normalized wrapper
		// available to its caller instead of publishing only part of the nesting conversion.
		return raw
	}
	return out
}

// responsesTextFormatToChatResponseFormat nests a flat json_schema format
// beneath Chat's json_schema key. Empty or null input returns nil; another type,
// an already present json_schema key, or no other flat keys returns the
// normalized copied input.
//
// Every flat key except type moves into the inner object, retaining raw child
// JSON while adding the outer json_schema type label. Presence of the wrapper
// key takes precedence even when its value is null. Encoding failure returns
// the normalized source format, so callers cannot infer conversion solely from
// the presence of a returned byte slice.
func responsesTextFormatToChatResponseFormat(raw json.RawMessage) json.RawMessage {
	raw = normalizedRawJSON(raw)
	if len(raw) == 0 {
		return nil
	}

	obj, ok := rawJSONObject(raw)
	if !ok || rawString(obj["type"]) != "json_schema" {
		return raw
	}
	// Existing wrapper-key presence owns the nesting decision, even for
	// a null value, so this helper does not nest an already wrapped shape again.
	if _, alreadyChatShape := obj["json_schema"]; alreadyChatShape {
		return raw
	}

	schema := make(map[string]json.RawMessage, len(obj))
	for key, value := range obj {
		if key == "type" {
			continue
		}
		schema[key] = value
	}
	if len(schema) == 0 {
		return raw
	}

	schemaRaw, err := json.Marshal(schema)
	if err != nil {
		// Failed inner-object encoding cannot supply a complete Chat wrapper.
		// Retain the normalized flat format so the caller keeps its existing settings.
		return raw
	}
	out, err := json.Marshal(map[string]json.RawMessage{
		"type":        rawJSONString("json_schema"),
		"json_schema": schemaRaw,
	})
	if err != nil {
		// Failure to encode the outer wrapper leaves the nesting conversion
		// unpublished. Return normalized source bytes for the caller's separate format handling.
		return raw
	}
	return out
}

// normalizedRawJSON returns a copied, whitespace-trimmed raw value, or nil
// for empty or exact null input. The copy lets nesting helpers return an
// unchanged format without sharing its mutable byte storage with the caller.
func normalizedRawJSON(raw json.RawMessage) json.RawMessage {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// rawJSONObject decodes raw into a map with copied raw child values.
// Decoding errors return nil and false; JSON null succeeds with a nil map, so
// true alone does not establish a nonnil object for mutation.
func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// rawJSONString returns the standard JSON encoding of a supplied format label.
// Invalid UTF-8 is replaced during encoding, so callers use this string helper
// for a JSON label when the decoded label matters more than its byte spelling.
func rawJSONString(value string) json.RawMessage {
	// Encoding one string cannot encounter an unsupported value or reference
	// cycle. Discarding this error therefore cannot hide a failed format-label
	// conversion through the standard encoder.
	data, _ := json.Marshal(value)
	return data
}
