package session

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// OpenAI-compatible requests can derive affinity from their initial user input or
// share a cache identity through reusable instruction and tool fields. The general
// content seed includes model and the first user anchor; an anchored wrapper
// requires meaningful first-user content before returning that seed. The stable
// prefix seed instead selects usable instructions, tools, and system/developer
// content, with the caller adding its own model and API Key scope.
//
// Chat content seeds include leading system/developer messages before another role
// closes that prefix. Responses content seeds include system/developer items across
// the input array while capturing one user item. Stable-prefix seeds keep each
// role label and JSON-encode values, distinguishing instruction text that contains
// field delimiters. General content scanning remains tolerant of malformed JSON
// and first duplicate fields, matching the existing reference fixtures rather
// than validating request syntax.

// These prefixes distinguish initial-input affinity from reusable-prefix cache identity,
// so consumers can label the selected evidence before deriving their final scoped key.
const (
	// contentSessionSeedPrefix separates content-derived seeds from explicit IDs such as sess-xxx.
	contentSessionSeedPrefix = "compat_cs_"

	// contentStablePrefixSessionSeedPrefix identifies cache seeds shared across independent prompts.
	contentStablePrefixSessionSeedPrefix = "compat_csp_"
)

// OpenAIContentSeed returns labelled model, tool, instruction, and first-input
// content for compatible HTTP session hashing. Empty bytes or no selected data
// returns empty; a model-only request can return a seed. Root scanning accepts
// the first object after non-JSON prefix bytes, rejects an earlier array root,
// and retains the first occurrence of each recognized field.
//
// A messages array wins over input, even when empty. Chat includes its leading
// system/developer prefix and first exact user role, while Responses includes
// system/developer items anywhere and captures its first exact user or input_text
// item. JSON content uses NormalizeCompatSeedJSON; model, instructions, and string
// input use gjson string values, so the result is an affinity representation
// rather than a full or lossless request serialization.
//
// The scan stops across a closing root brace and preserves tolerant extraction
// for the tested malformed forms. Callers validate complete request JSON elsewhere
// and keep body bytes stable while these borrowed views are read. Later turns
// can reuse a seed when its selected initial fields remain equal.
func OpenAIContentSeed(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	// Fixed field positions retain the first recognized root value and let the
	// scan stop once every seed field has been seen, including duplicate-key inputs.
	const (
		modelField = iota
		toolsField
		functionsField
		instructionsField
		messagesField
		inputField
		contentSessionSeedFieldCount
		allContentSessionSeedFields = 1<<contentSessionSeedFieldCount - 1
	)
	var fields [contentSessionSeedFieldCount]gjson.Result
	var seen uint8
	// Tolerate prefix bytes before an object to match reference gjson lookup;
	// an earlier array root stops extraction so nested or trailing object fields cannot supply identity.
	root := body
	for i := range body {
		switch body[i] {
		case '{':
			root = body[i:]
			goto scanRoot
		case '[':
			return ""
		}
	}
	return ""

scanRoot:
	nextKeyOffset := 1
	ParseRawJSONView(root).ForEach(func(key, value gjson.Result) bool {
		if key.Index < nextKeyOffset || key.Index > len(root) {
			return false
		}
		// A tolerant scan can reach fields after the root closes. Inspect the gap
		// after the prior value so braces inside that value do not terminate the root early.
		if bytes.IndexByte(root[nextKeyOffset:key.Index], '}') >= 0 {
			return false
		}
		nextKeyOffset = value.Index + len(value.Raw)

		field := -1
		switch key.Str {
		case "model":
			field = modelField
		case "tools":
			field = toolsField
		case "functions":
			field = functionsField
		case "instructions":
			field = instructionsField
		case "messages":
			field = messagesField
		case "input":
			field = inputField
		}
		if field < 0 {
			return true
		}
		mask := uint8(1 << field)
		// Retain the first duplicate value to match individual gjson lookups;
		// replacing it with a later occurrence would assign a different affinity seed.
		if seen&mask == 0 {
			fields[field] = value
			seen |= mask
		}
		return seen != allContentSessionSeedFields
	})

	var b strings.Builder

	if model := fields[modelField].String(); model != "" {
		_, _ = b.WriteString("model=")
		_, _ = b.WriteString(model)
	}

	// Normalize structured tool and function arrays so equivalent layouts share
	// affinity. The exact [] representation is omitted before normalization; other
	// empty-array layouts still contribute their normalized value.
	if tools := fields[toolsField]; tools.Exists() && tools.IsArray() && tools.Raw != "[]" {
		_, _ = b.WriteString("|tools=")
		_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(tools.Raw)))
	}

	if funcs := fields[functionsField]; funcs.Exists() && funcs.IsArray() && funcs.Raw != "[]" {
		_, _ = b.WriteString("|functions=")
		_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(funcs.Raw)))
	}

	if instr := fields[instructionsField].String(); instr != "" {
		_, _ = b.WriteString("|instructions=")
		_, _ = b.WriteString(instr)
	}

	firstUserCaptured := false

	// A messages array selects Chat history even when empty; otherwise input
	// selects the Responses representation, keeping one initial-content path in the seed.
	msgs := fields[messagesField]
	if msgs.Exists() && msgs.IsArray() {
		// Only the leading system/developer prefix contributes Chat instructions,
		// so a per-turn system message after user or assistant history does not change this identity.
		systemPrefixOpen := true
		msgs.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			switch role {
			case "system", "developer":
				if systemPrefixOpen {
					_, _ = b.WriteString("|system=")
					if c := msg.Get("content"); c.Exists() {
						_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
					}
				}
			case "user":
				systemPrefixOpen = false
				if !firstUserCaptured {
					_, _ = b.WriteString("|first_user=")
					if c := msg.Get("content"); c.Exists() {
						_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
					}
					firstUserCaptured = true
				}
			default:
				systemPrefixOpen = false
			}
			return true
		})
	} else if inp := fields[inputField]; inp.Exists() {
		if inp.Type == gjson.String {
			_, _ = b.WriteString("|input=")
			_, _ = b.WriteString(inp.String())
		} else if inp.IsArray() {
			// Responses retains instruction roles throughout input while capturing one
			// initial user item, allowing later conversation items to leave that anchor stable.
			inp.ForEach(func(_, item gjson.Result) bool {
				role := item.Get("role").String()
				switch role {
				case "system", "developer":
					_, _ = b.WriteString("|system=")
					if c := item.Get("content"); c.Exists() {
						_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
					}
				case "user":
					if !firstUserCaptured {
						_, _ = b.WriteString("|first_user=")
						if c := item.Get("content"); c.Exists() {
							_, _ = b.WriteString(openai.NormalizeCompatSeedJSON(json.RawMessage(c.Raw)))
						}
						firstUserCaptured = true
					}
				}
				if !firstUserCaptured && item.Get("type").String() == "input_text" {
					_, _ = b.WriteString("|first_user=")
					if text := item.Get("text").String(); text != "" {
						_, _ = b.WriteString(text)
					}
					firstUserCaptured = true
				}
				return true
			})
		}
	}

	if b.Len() == 0 {
		return ""
	}
	return contentSessionSeedPrefix + b.String()
}

// OpenAIAnchoredContentSeed returns OpenAIContentSeed when the first recognized
// user anchor is meaningful, otherwise empty. The anchor guard trims role labels,
// while the underlying seed uses exact labels; callers still apply request validation
// and tenant/model scope before using the selected seed.
func OpenAIAnchoredContentSeed(body []byte) string {
	if !hasOpenAIContentSessionUserAnchor(body) {
		return ""
	}
	return OpenAIContentSeed(body)
}

// hasOpenAIContentSessionUserAnchor reports whether the selected first user input
// has meaningful content. A messages array takes precedence over input, and
// role/type checks trim surrounding whitespace. Empty bytes, missing input, and
// unsupported input shapes return false.
//
// The first user message or input_text item ends the search even when its content
// is empty, so a later question cannot replace that missing initial anchor.
// String input needs nonblank text; other user content delegates to
// hasMeaningfulOpenAIContent. With exact seed-supported role/type labels, this
// guard keeps Grok's fallback anchored to user content rather than model alone.
func hasOpenAIContentSessionUserAnchor(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		anchored := false
		messages.ForEach(func(_, message gjson.Result) bool {
			if strings.TrimSpace(message.Get("role").String()) != "user" {
				return true
			}
			// The first user owns this anchor even when empty; later questions
			// cannot replace it and create affinity from a different initial conversation.
			anchored = hasMeaningfulOpenAIContent(message.Get("content"))
			return false
		})
		return anchored
	}

	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String()) != ""
	}
	if !input.IsArray() {
		return false
	}

	anchored := false
	input.ForEach(func(_, item gjson.Result) bool {
		if strings.TrimSpace(item.Get("role").String()) == "user" {
			anchored = hasMeaningfulOpenAIContent(item.Get("content"))
			return false
		}
		if strings.TrimSpace(item.Get("type").String()) == "input_text" {
			anchored = strings.TrimSpace(item.Get("text").String()) != ""
			return false
		}
		return true
	})
	return anchored
}

// hasMeaningfulOpenAIContent reports usable content for the anchored seed guard.
// Missing or null content and blank strings return false. Non-array JSON uses
// the nonempty normalization rule; arrays accept the first meaningful string,
// text field, or other normalized item.
//
// An existing text field controls its array item's decision even when other
// fields are populated, so a blank text field can make that item unhelpful.
// Numbers, booleans, and nonempty objects can satisfy the normalization rule;
// meaningful here identifies selected seed data, rather than asserting textual
// user intent or validating an input modality.
func hasMeaningfulOpenAIContent(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return false
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	if !content.IsArray() {
		normalized, ok := normalizeNonEmptyCompatSeedJSON(content)
		return ok && strings.TrimSpace(normalized) != ""
	}

	meaningful := false
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			meaningful = strings.TrimSpace(item.String()) != ""
		} else if text := item.Get("text"); text.Exists() {
			meaningful = strings.TrimSpace(text.String()) != ""
		} else {
			_, meaningful = normalizeNonEmptyCompatSeedJSON(item)
		}
		return !meaningful
	})
	return meaningful
}

// OpenAIStablePrefixSeed returns reusable tools, functions, instructions, and
// system/developer content under a stable-prefix label. Empty or unusable prefix
// data returns empty so the cache-identity caller can choose an anchored input
// instead. User and assistant content is omitted so separate questions can reuse
// a prefix; the consuming cache-identity builder adds model and tenant scope.
//
// Messages arrays take precedence over Responses input arrays. System/developer
// roles are trimmed and retained as separate field labels wherever they occur.
// Values use JSON normalization, including encoding instruction strings, so text
// containing delimiter syntax remains distinct from a separate labelled field.
// The output preserves selected item order, uses the normalizer's numeric
// representation, and supplies no tenant or model authorization by itself.
func OpenAIStablePrefixSeed(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var b strings.Builder
	hasStablePrefix := false
	// Encode selected prefix values as JSON under their labels so delimiter
	// text inside instructions does not masquerade as another system or tool field.
	appendJSON := func(label string, value gjson.Result) {
		normalized, ok := normalizeNonEmptyCompatSeedJSON(value)
		if !ok {
			return
		}
		_, _ = b.WriteString("|")
		_, _ = b.WriteString(label)
		_, _ = b.WriteString("=")
		_, _ = b.WriteString(normalized)
		hasStablePrefix = true
	}

	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		appendJSON("tools", tools)
	}
	if funcs := gjson.GetBytes(body, "functions"); funcs.Exists() && funcs.IsArray() {
		appendJSON("functions", funcs)
	}
	if instructions := gjson.GetBytes(body, "instructions"); strings.TrimSpace(instructions.String()) != "" {
		appendJSON("instructions", instructions)
	}

	appendSystemMessages := func(items gjson.Result) {
		items.ForEach(func(_, item gjson.Result) bool {
			role := strings.TrimSpace(item.Get("role").String())
			switch role {
			case "system", "developer":
				appendJSON(role, item.Get("content"))
			}
			return true
		})
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		appendSystemMessages(messages)
	} else if input := gjson.GetBytes(body, "input"); input.Exists() && input.IsArray() {
		appendSystemMessages(input)
	}

	if !hasStablePrefix {
		return ""
	}
	return contentStablePrefixSessionSeedPrefix + b.String()
}

// normalizeNonEmptyCompatSeedJSON returns normalized JSON and whether it carries
// usable seed data. Missing or null values and normalized empty strings, arrays,
// or objects return false; other representations pass through the shared normalizer.
func normalizeNonEmptyCompatSeedJSON(value gjson.Result) (string, bool) {
	if !value.Exists() || value.Type == gjson.Null {
		return "", false
	}
	normalized := openai.NormalizeCompatSeedJSON(json.RawMessage(value.Raw))
	switch normalized {
	case "", `""`, "[]", "{}", "null":
		return "", false
	default:
		return normalized, true
	}
}
