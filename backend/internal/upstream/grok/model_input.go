package grok

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Replayed tool calls can carry separate item IDs, call IDs, or no explicit
// pairing ID. A two-pass scan registers all calls before pairing outputs so
// an output can precede its call. Conflicting item aliases are removed from
// alias lookup, while explicit call identifiers remain usable even in
// malformed transcripts that reuse them.
//
// Tool output must also carry text and images through different input shapes.
// Output text is serialized into function_call_output, while extracted media
// is held until a contiguous output group ends and then appended as user
// image content labelled by call. JSON number decoding retains large numeric
// tokens. Completed assistant messages keep replay metadata, while incomplete
// assistant text and reasoning receive the adjustments used by this request
// normalizer.

// GrokToolOutputImage associates one extracted image URL with the supplied
// tool call identifier.
type GrokToolOutputImage struct {
	callID string // associated tool call identifier
	url    string // extracted tool image URL
}

// SanitizeGrokResponsesModelInput rebuilds object or array input into replay
// messages and paired tool items. Missing, string, and unsupported input
// shapes retain their body. The decoded array uses json.Number so large
// numeric arguments and outputs survive reserialization. Call/output IDs are
// established before filtering, allowing outputs to refer to calls later in
// the transcript.
//
// Tool replies become string-valued function_call_output items; their
// extracted images are appended after each contiguous reply group as labelled
// user content. Nonblank standalone text becomes user messages, incomplete
// assistant messages lose replay metadata, and reasoning loses status and
// null content. Custom/search calls become functions with normalized names
// and arguments. JSON mutation and encoding errors propagate; this normalizer
// does not validate whether every surviving replay item is accepted upstream.
func (m BodyCodec) SanitizeGrokResponsesModelInput(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || input.Type == gjson.String {
		return body, nil
	}
	// Treat an object as one replay item so the same pairing and filtering
	// rules can process it through the array path below.
	if input.IsObject() {
		var err error
		body, err = sjson.SetRawBytes(body, "input", []byte("["+input.Raw+"]"))
		if err != nil {
			return nil, fmt.Errorf("wrap Grok Responses model input: %w", err)
		}
		input = gjson.GetBytes(body, "input")
	}
	if !input.IsArray() {
		return body, nil
	}

	var items []any
	decoder := json.NewDecoder(bytes.NewReader([]byte(input.Raw)))
	// Preserve numeric spellings during replay reserialization so large IDs in
	// arguments and tool output do not pass through lossy float64 decoding.
	decoder.UseNumber()
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("parse Grok Responses model input: %w", err)
	}
	// Discover calls before consuming outputs; a transcript can place a tool reply
	// ahead of its call while still carrying an alias needed for correct pairing.
	callIDs, outputIDs := m.PairGrokReplayCallIDs(items)
	filtered := make([]any, 0, len(items))
	pendingOutputImages := make([]GrokToolOutputImage, 0)
	// Nonreply entries flush accumulated images before their own content. The
	// final flush also retains media when the transcript ends in tool replies.
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			filtered = m.AppendGrokToolOutputImageMessage(filtered, pendingOutputImages)
			pendingOutputImages = pendingOutputImages[:0]
			if text, ok := rawItem.(string); ok && strings.TrimSpace(text) != "" {
				filtered = append(filtered, map[string]any{"type": "message", "role": "user", "content": text})
			}
			continue
		}

		itemType := strings.ToLower(strings.TrimSpace(m.GrokStringValue(item["type"])))
		role := strings.ToLower(strings.TrimSpace(m.GrokStringValue(item["role"])))
		// Keep contiguous tool replies as output items and postpone their images until
		// the group ends, preserving pairing labels when media moves into user content.
		if role == "tool" || role == "function" || m.IsGrokReplayOutputType(itemType) {
			callID := outputIDs[index]
			rawOutput := m.FirstNonNilGrokJSONValue(item["output"], item["content"], item["results"])
			output, nestedImages := m.NormalizeGrokToolOutput(rawOutput, callID)
			filtered = append(filtered, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  output,
			})
			pendingOutputImages = append(pendingOutputImages, m.ExtractGrokToolOutputImages(item, callID)...)
			pendingOutputImages = append(pendingOutputImages, nestedImages...)
			continue
		}
		filtered = m.AppendGrokToolOutputImageMessage(filtered, pendingOutputImages)
		pendingOutputImages = pendingOutputImages[:0]

		// Convert standalone text and alternate tool-call shapes into message or
		// function forms, giving the remaining replay normalization one representation to handle.
		switch itemType {
		case "text", "input_text", "output_text":
			text := strings.TrimSpace(m.GrokStringValue(item["text"]))
			if text == "" {
				continue
			}
			if role == "" {
				role = "user"
			}
			item = map[string]any{"type": "message", "role": role, "content": text}
			itemType = "message"
		case "custom_tool_call", "tool_search_call":
			originalType := itemType
			item["type"] = "function_call"
			itemType = "function_call"
			if strings.TrimSpace(m.GrokStringValue(item["name"])) == "" {
				if originalType == "tool_search_call" {
					item["name"] = "tool_search"
				}
			}
		}

		if itemType == "" && role != "" {
			itemType = "message"
			item["type"] = itemType
		}
		if itemType == "message" {
			if role == "" {
				role = "user"
				item["role"] = role
			}
			content, keep := m.SanitizeGrokMessageContent(item["content"])
			if !keep {
				continue
			}
			item["content"] = content
			// Incomplete assistant items are replayed as content rather than retained
			// output records; collapsing text and dropping metadata prevents partial IDs from persisting.
			if role == "assistant" && !m.GrokIsCompleteOutputMessage(item) {
				if text, ok := m.CollapseGrokAssistantOutputText(content); ok {
					item["content"] = text
				}
				delete(item, "id")
				delete(item, "status")
			}
		} else if itemType == "reasoning" {
			// Reasoning replay retains its other fields while dropping status and null
			// content, avoiding unsupported output metadata in the normalized input representation.
			delete(item, "status")
			if content, exists := item["content"]; exists && content == nil {
				delete(item, "content")
			}
		} else if itemType == "function_call" {
			// Flatten alternate function names and arguments while assigning the paired
			// identifier; remove source-specific fields so one call shape carries the replay relationship.
			name := strings.TrimSpace(m.GrokStringValue(item["name"]))
			arguments := item["arguments"]
			if function, ok := item["function"].(map[string]any); ok {
				if name == "" {
					name = strings.TrimSpace(m.GrokStringValue(function["name"]))
				}
				if arguments == nil {
					arguments = function["arguments"]
				}
			}
			if arguments == nil {
				arguments = m.FirstNonNilGrokJSONValue(item["input"], item["query"])
				if _, custom := item["input"]; custom {
					arguments = map[string]any{"input": arguments}
				}
			}
			if name == "" {
				name = "unknown_tool"
			}
			item["call_id"] = callIDs[index]
			item["name"] = name
			item["arguments"] = m.GrokModelInputString(arguments, "{}")
			for _, field := range []string{"id", "status", "tool_call_id", "function", "input", "query", "execution"} {
				delete(item, field)
			}
		}
		// Nonpair output records must not carry a tool pairing identifier. Keep it
		// on call/output items, where the earlier two-pass scan established its relationship.
		if protocolopenai.ShouldStripNonPairCallID(itemType) {
			delete(item, "call_id")
		}
		filtered = append(filtered, item)
	}
	filtered = m.AppendGrokToolOutputImageMessage(filtered, pendingOutputImages)

	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("serialize Grok Responses model input: %w", err)
	}
	updated, err := sjson.SetRawBytes(body, "input", encoded)
	if err != nil {
		return nil, fmt.Errorf("set Grok Responses model input: %w", err)
	}
	return updated, nil
}

// NormalizeGrokToolOutput returns text and separately associated images for
// one tool call. When no usable image is extracted, the original value is
// serialized as a whole, including structured text arrays and unextracted
// media data. Image-only content uses the empty marker.
//
// Once extraction supplies images, GrokStructuredToolOutputString serializes
// or flattens the residual output. The image records retain callID for a
// following user media message. Callers append that message after the
// contiguous reply group; this helper returns the pieces and leaves their
// ordering to the replay builder.
func (m BodyCodec) NormalizeGrokToolOutput(value any, callID string) (string, []GrokToolOutputImage) {
	stripped, images, keep := m.StripGrokToolOutputImages(value, callID)
	// With no extracted media, retain whole-value serialization so structured
	// output is not flattened solely because it resembles a list of text parts.
	if len(images) == 0 {
		return m.GrokModelInputString(value, "(empty)"), nil
	}
	if !keep {
		return "(empty)", images
	}
	return m.GrokStructuredToolOutputString(stripped, "(empty)"), images
}

// StripGrokToolOutputImages recursively separates media from arrays and known
// content, output, results, or images fields, retaining the supplied callID
// on each extracted image. Its boolean reports whether residual output
// remains. Recognized image parts with missing or empty-base64 URLs are
// discarded. Arrays and processed maps receive new outer storage; unrelated
// nested values can remain shared with the input.
//
// Image parts leave the textual representation so NormalizeGrokToolOutput can
// serialize text and AppendGrokToolOutputImageMessage can put media into user
// content. Images arrays are removed after URL extraction, and emptied nested
// output fields disappear. Unsupported scalar values remain as supplied, with
// nil reporting no retained content.
func (m BodyCodec) StripGrokToolOutputImages(value any, callID string) (any, []GrokToolOutputImage, bool) {
	switch typed := value.(type) {
	case []any:
		filtered := make([]any, 0, len(typed))
		images := make([]GrokToolOutputImage, 0)
		for _, item := range typed {
			stripped, nested, keep := m.StripGrokToolOutputImages(item, callID)
			images = append(images, nested...)
			if keep {
				filtered = append(filtered, stripped)
			}
		}
		return filtered, images, len(filtered) > 0
	case map[string]any:
		// Recognized image parts move to a separate media result; empty data URIs
		// are discarded so later user image messages do not contain unusable placeholders.
		partType := strings.ToLower(strings.TrimSpace(m.GrokStringValue(typed["type"])))
		if partType == "image" || partType == "image_url" || partType == "input_image" {
			url := m.GrokToolOutputImageURL(typed)
			if url == "" || protocolopenai.IsEmptyBase64DataURI(url) {
				return nil, nil, false
			}
			return nil, []GrokToolOutputImage{{callID: callID, url: url}}, false
		}

		filtered := make(map[string]any, len(typed))
		maps.Copy(filtered, typed)
		images := make([]GrokToolOutputImage, 0)
		// Move images arrays into separate media records before text serialization;
		// retaining the array here would duplicate that media inside the tool-output string.
		if rawImages, exists := filtered["images"]; exists {
			if values, ok := rawImages.([]any); ok {
				for _, rawImage := range values {
					url := m.GrokToolOutputImageURL(rawImage)
					if url != "" && !protocolopenai.IsEmptyBase64DataURI(url) {
						images = append(images, GrokToolOutputImage{callID: callID, url: url})
					}
				}
				delete(filtered, "images")
			}
		}
		// Descend through output-bearing fields after removing an images array,
		// retaining unrelated metadata and deleting a field when its residual content is empty.
		for _, field := range []string{"content", "output", "results"} {
			item, exists := filtered[field]
			if !exists {
				continue
			}
			stripped, nested, keep := m.StripGrokToolOutputImages(item, callID)
			images = append(images, nested...)
			if keep {
				filtered[field] = stripped
			} else {
				delete(filtered, field)
			}
		}
		return filtered, images, len(filtered) > 0
	default:
		return value, nil, value != nil
	}
}

// GrokStructuredToolOutputString joins a nonempty array of supported text
// parts with newlines. Nonarray values use GrokModelInputString, and an all-
// blank supported text array returns the caller's fallback marker.
//
// Every part must be a recognized text object before its text is flattened. A
// nontext or nonobject part delegates whole-value serialization, keeping that
// mixed structure available instead of extracting a partial text subset.
// Blank supported text parts are omitted, while retained text is trimmed
// before joining. This choice applies after tool images have been separated.
func (m BodyCodec) GrokStructuredToolOutputString(value any, fallback string) string {
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return m.GrokModelInputString(value, fallback)
	}

	// Flatten only an entirely recognized text-part array. Any different part
	// returns whole-value JSON serialization so its structure remains available in tool output.
	texts := make([]string, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return m.GrokModelInputString(value, fallback)
		}
		partType := strings.ToLower(strings.TrimSpace(m.GrokStringValue(part["type"])))
		if partType != "text" && partType != "input_text" && partType != "output_text" {
			return m.GrokModelInputString(value, fallback)
		}
		if text := strings.TrimSpace(m.GrokStringValue(part["text"])); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return fallback
	}
	return strings.Join(texts, "\n")
}

// ExtractGrokToolOutputImages reads an item's images array and associates
// accepted URLs with callID. Missing arrays, empty URLs, and empty-base64
// data URIs contribute no images. Callers append these after the contiguous
// tool reply group.
func (m BodyCodec) ExtractGrokToolOutputImages(item map[string]any, callID string) []GrokToolOutputImage {
	rawImages, ok := item["images"].([]any)
	if !ok || len(rawImages) == 0 {
		return nil
	}

	images := make([]GrokToolOutputImage, 0, len(rawImages))
	for _, rawImage := range rawImages {
		url := m.GrokToolOutputImageURL(rawImage)
		if url == "" || protocolopenai.IsEmptyBase64DataURI(url) {
			continue
		}
		images = append(images, GrokToolOutputImage{callID: callID, url: url})
	}
	return images
}

// GrokToolOutputImageURL extracts a trimmed string URL or the first usable
// url, image_url, or file_url field. Each object field can hold a string or a
// nested url object; unsupported or empty values return empty.
func (m BodyCodec) GrokToolOutputImageURL(value any) string {
	// Replay shapes can place the same image link under different fields.
	// Choose a usable string in the configured order before returning an empty media result.
	switch image := value.(type) {
	case string:
		return strings.TrimSpace(image)
	case map[string]any:
		for _, field := range []string{"url", "image_url", "file_url"} {
			raw := image[field]
			switch typed := raw.(type) {
			case string:
				if url := strings.TrimSpace(typed); url != "" {
					return url
				}
			case map[string]any:
				if url := strings.TrimSpace(m.GrokStringValue(typed["url"])); url != "" {
					return url
				}
			}
		}
	}
	return ""
}

// AppendGrokToolOutputImageMessage appends one user message containing the
// collected image parts. An empty image list returns the supplied items.
// Callers finish the contiguous function-output group before invoking it,
// keeping text replies together ahead of their media.
//
// Each change in adjacent callID adds a text label before that call's images,
// so replayed media remains associated with its tool reply when moved out of
// the output string. The append can reuse items backing storage; callers
// retain normal slice ownership responsibilities when sharing the result.
func (m BodyCodec) AppendGrokToolOutputImageMessage(items []any, images []GrokToolOutputImage) []any {
	if len(images) == 0 {
		return items
	}

	content := make([]any, 0, len(images)*2)
	lastCallID := ""
	for _, image := range images {
		if image.callID != lastCallID {
			content = append(content, map[string]any{
				"type": "input_text",
				"text": fmt.Sprintf("[Tool output media for call %s]", image.callID),
			})
			lastCallID = image.callID
		}
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": image.url,
		})
	}
	return append(items, map[string]any{
		"type":    "message",
		"role":    "user",
		"content": content,
	})
}

// PairGrokReplayCallIDs returns per-index call and output identifiers after
// scanning all calls before outputs. A supplied call_id wins before
// tool_call_id; missing call identifiers receive counter-derived IDs. Calls
// register their call, tool, and item aliases so later or earlier outputs can
// use the same canonical pairing value.
//
// An alias naming different calls is removed from the alias map and marked
// conflicting. Explicit output call identifiers remain authoritative; an
// ambiguous item-only alias receives a synthetic orphan ID. An output lacking
// a usable alias consumes the next unconsumed call in encounter order, then
// receives a synthetic ID when none remain. Inputs are read directly, so
// callers keep item maps stable during both scans.
//
// Counter-derived IDs use grok_replay_call_N without checking supplied
// identifiers. Callers needing distinct synthesized pairings keep explicit
// IDs outside that namespace; an orphan counter value can otherwise match a
// transcript's explicit call ID.
func (m BodyCodec) PairGrokReplayCallIDs(items []any) (map[int]string, map[int]string) {
	callIDs := make(map[int]string)
	outputIDs := make(map[int]string)
	aliases := make(map[string]string)
	conflictingAliases := make(map[string]struct{})
	pendingCalls := make([]string, 0)
	nextID := 0
	synthetic := func() string {
		nextID++
		return fmt.Sprintf("grok_replay_call_%d", nextID)
	}
	registerAlias := func(alias, canonical string) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return
		}
		if _, conflict := conflictingAliases[alias]; conflict {
			return
		}
		// An alias naming another canonical call cannot identify a unique pairing.
		// Remove its lookup and retain the conflict so outputs take their explicit-ID
		// or counter path instead of resolving through that shared alias.
		if existing, exists := aliases[alias]; exists && existing != canonical {
			delete(aliases, alias)
			conflictingAliases[alias] = struct{}{}
			return
		}
		aliases[alias] = canonical
	}

	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(m.GrokStringValue(item["type"])))
		if !m.IsGrokReplayCallType(itemType) {
			continue
		}
		callID := m.FirstNonEmptyGrokString(item["call_id"], item["tool_call_id"])
		if callID == "" {
			callID = synthetic()
		}
		callIDs[index] = callID
		pendingCalls = append(pendingCalls, callID)
		registerAlias(callID, callID)
		registerAlias(m.GrokStringValue(item["call_id"]), callID)
		registerAlias(m.GrokStringValue(item["tool_call_id"]), callID)
		registerAlias(m.GrokStringValue(item["id"]), callID)
	}

	consumedCalls := make(map[string]struct{})
	consumeNextCall := func() string {
		for _, callID := range pendingCalls {
			if _, consumed := consumedCalls[callID]; consumed {
				continue
			}
			consumedCalls[callID] = struct{}{}
			return callID
		}
		return synthetic()
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(m.GrokStringValue(item["type"])))
		role := strings.ToLower(strings.TrimSpace(m.GrokStringValue(item["role"])))
		if role != "tool" && role != "function" && !m.IsGrokReplayOutputType(itemType) {
			continue
		}
		alias := m.FirstNonEmptyGrokString(item["call_id"], item["tool_call_id"], item["id"])
		_, conflict := conflictingAliases[alias]
		if canonical := aliases[alias]; canonical != "" && !conflict {
			outputIDs[index] = canonical
			consumedCalls[canonical] = struct{}{}
		} else if m.FirstNonEmptyGrokString(item["call_id"], item["tool_call_id"]) != "" {
			// An explicit output call identifier remains the caller's pairing signal,
			// even when a malformed transcript made its alias ambiguous across multiple calls.
			outputIDs[index] = alias
		} else if conflict {
			// An item-only conflict supplies no authoritative call identifier. Choose
			// the next counter value instead of either alias mapping; explicit IDs can
			// still collide with this synthetic namespace as described above.
			outputIDs[index] = synthetic()
		} else {
			// An unidentified output consumes the next unconsumed call only after all
			// aliases are known, allowing missing-ID replies that precede their calls to pair.
			outputIDs[index] = consumeNextCall()
		}
	}
	return callIDs, outputIDs
}

// IsGrokReplayCallType reports whether an exact item label denotes a replay
// tool call.
func (m BodyCodec) IsGrokReplayCallType(itemType string) bool {
	switch itemType {
	case "function_call", "custom_tool_call", "tool_search_call":
		return true
	default:
		return false
	}
}

// IsGrokReplayOutputType reports whether an exact item label denotes a replay
// tool output.
func (m BodyCodec) IsGrokReplayOutputType(itemType string) bool {
	switch itemType {
	case "function_call_output", "custom_tool_call_output", "tool_search_output", "tool_search_call_output":
		return true
	default:
		return false
	}
}

// FirstNonEmptyGrokString returns the first nonblank trimmed string among
// values. Nonstring values are ignored by GrokStringValue; an absent usable
// string returns empty for the caller's next identity or naming choice.
func (m BodyCodec) FirstNonEmptyGrokString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(m.GrokStringValue(value)); text != "" {
			return text
		}
	}
	return ""
}

// FirstNonNilGrokJSONValue returns the first value whose interface is not
// nil. Empty strings, arrays, and maps still win, as do typed nil values held
// in nonnil interfaces. All nil interfaces return nil for output or argument
// handling.
func (m BodyCodec) FirstNonNilGrokJSONValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// SanitizeGrokMessageContent reports whether message content remains usable
// after filtering blank text and unusable image parts. String content retains
// its supplied whitespace when nonblank. Arrays receive new outer storage but
// retained maps and other mutable values remain shared.
//
// Known text labels require nonblank text, and image_url/input_image labels
// require recognized image evidence. Other part shapes survive, including
// unsupported nonstring scalars. Nil or an emptied array reports false;
// callers decide whether the containing message should be omitted.
func (m BodyCodec) SanitizeGrokMessageContent(content any) (any, bool) {
	switch value := content.(type) {
	case nil:
		return nil, false
	case string:
		return value, strings.TrimSpace(value) != ""
	case []any:
		// Filter known empty parts while retaining unfamiliar shapes, since replay
		// content can mix text and images with values this codec does not interpret.
		filtered := make([]any, 0, len(value))
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				if text, isText := rawPart.(string); !isText || strings.TrimSpace(text) != "" {
					filtered = append(filtered, rawPart)
				}
				continue
			}
			switch strings.ToLower(strings.TrimSpace(m.GrokStringValue(part["type"]))) {
			case "text", "input_text", "output_text":
				if strings.TrimSpace(m.GrokStringValue(part["text"])) == "" {
					continue
				}
			case "image_url", "input_image":
				if !m.GrokContentPartHasImageURL(part) {
					continue
				}
			}
			filtered = append(filtered, part)
		}
		return filtered, len(filtered) > 0
	default:
		return content, true
	}
}

// GrokContentPartHasImageURL accepts nonblank file_id or file_data, then a
// usable image_url or file_url representation. The first nonnil URL field
// wins even if unusable; URL strings and nested url objects reject empty-
// base64 data URIs but receive no destination-policy validation here.
func (m BodyCodec) GrokContentPartHasImageURL(part map[string]any) bool {
	for _, field := range []string{"file_id", "file_data"} {
		if strings.TrimSpace(m.GrokStringValue(part[field])) != "" {
			return true
		}
	}
	// A supplied image_url remains selected even when blank or malformed, so
	// a later file_url cannot silently replace that representation of the replayed image input.
	raw := m.FirstNonNilGrokJSONValue(part["image_url"], part["file_url"])
	switch value := raw.(type) {
	case string:
		value = strings.TrimSpace(value)
		return value != "" && !protocolopenai.IsEmptyBase64DataURI(value)
	case map[string]any:
		url := strings.TrimSpace(m.GrokStringValue(value["url"]))
		return url != "" && !protocolopenai.IsEmptyBase64DataURI(url)
	default:
		return false
	}
}

// GrokStringValue returns a string value directly, otherwise empty text. It
// performs no formatting or JSON conversion for other types, allowing callers
// to distinguish actual string fields from unsupported representations.
func (m BodyCodec) GrokStringValue(value any) string {
	text, _ := value.(string)
	return text
}

// GrokModelInputString retains nonblank strings and JSON-encodes other nonnil
// values. Blank strings, nil, null encoding, or marshal failures return
// fallback; callers choose whether that marker represents empty tool output
// or empty arguments.
func (m BodyCodec) GrokModelInputString(value any, fallback string) string {
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return fallback
		}
		return text
	}
	if value == nil {
		return fallback
	}
	encoded, err := json.Marshal(value)
	// The caller's marker also covers unencodable values, keeping string-only
	// tool output construction usable when arbitrary Go values cannot be marshaled.
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return fallback
	}
	return string(encoded)
}

// GrokIsCompleteOutputMessage reports whether id and status are both nonblank
// string fields. It does not inspect the status value; callers use presence
// to decide whether assistant replay metadata is retained.
func (m BodyCodec) GrokIsCompleteOutputMessage(item map[string]any) bool {
	return strings.TrimSpace(m.GrokStringValue(item["id"])) != "" &&
		strings.TrimSpace(m.GrokStringValue(item["status"])) != ""
}

// CollapseGrokAssistantOutputText joins an array consisting entirely of
// string-valued output_text objects. Missing, malformed, mixed, or wholly
// blank content returns false; successful output retains each text's supplied
// whitespace and inserts no separator.
func (m BodyCodec) CollapseGrokAssistantOutputText(content any) (string, bool) {
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return "", false
	}
	var text strings.Builder
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok || strings.ToLower(strings.TrimSpace(m.GrokStringValue(part["type"]))) != "output_text" {
			return "", false
		}
		value, ok := part["text"].(string)
		if !ok {
			return "", false
		}
		_, _ = text.WriteString(value)
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", false
	}
	return text.String(), true
}
