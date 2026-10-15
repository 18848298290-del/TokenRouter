package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// cyber_transcript.go hashes cumulative visible transcript prefixes for Cyber
// continuation blocking. A cumulative SHA-256 stream includes API Key identity,
// root instructions, and each visible transcript item. The saved context before
// a later user turn can match a continuation whose latest user input has
// changed when earlier items carry model-generated labels. Those labels are a
// heuristic supplied in the request.
//
// Lookup keeps the newest 256 cumulative prefixes and records whether older
// ones were dropped. The ring is reordered chronologically before returning it,
// while the full key remains the final prefix. CyberBlocks rejects this
// truncation when the source scope is active, preventing appended history from
// hiding a previously blocked prefix. Messages take precedence over Responses
// input when the messages field exists.

// maxOpenAICyberTranscriptLookupKeys caps stored lookup prefixes; CyberBlocks rejects
// truncation under active scope so discarded history cannot establish an unblocked request.
const maxOpenAICyberTranscriptLookupKeys = 256

// openAICyberTranscriptBlockKeys carries retained transcript prefixes and edited-turn evidence.
type openAICyberTranscriptBlockKeys struct {
	// lookupKeys retains chronological prefixes and lookupKeysTruncated reports
	// discarded older prefixes; CyberBlocks uses the flag before trusting this bounded candidate list.
	lookupKeys []string // retained cumulative transcript hashes
	// preLatestUserKey holds context before the latest qualifying user turn;
	// prior model-labelled history is required before this edited-input candidate is set.
	preLatestUserKey    string
	lookupKeysTruncated bool // older prefixes were discarded
}

// deriveOpenAICyberTranscriptBlockKeys hashes cumulative visible history with API Key
// identity and root instructions. Empty bytes or a nonobject request view produce
// no keys. RequestPayloadView unwraps response.create when it contains a response
// object; an existing messages field wins over input, including unusable messages.
//
// JSON objects and arrays use NormalizeCompatSeedJSON, while string items are
// JSON-encoded and string instructions contribute their decoded text. Model and
// tools are omitted from this transcript seed. Before a user turn following
// model-labelled history, the preceding prefix is remembered for edited-input
// matching; the labels are heuristic rather than authenticated generation evidence.
//
// At most 256 newest prefixes remain in chronological order. Dropping an older
// prefix sets the truncation flag so CyberBlocks can reject an incomplete lookup
// under active scope. A nonblank string input instead yields one cumulative key;
// no recognized usable sequence or string yields an empty result.
func deriveOpenAICyberTranscriptBlockKeys(apiKeyID int64, body []byte) openAICyberTranscriptBlockKeys {
	if len(body) == 0 {
		return openAICyberTranscriptBlockKeys{}
	}
	root := openai.RequestPayloadView(body)
	if !root.Exists() || !root.IsObject() {
		return openAICyberTranscriptBlockKeys{}
	}

	h := sha256.New()
	_, _ = h.Write([]byte("cyber-transcript:v3|api_key="))
	_, _ = h.Write([]byte(strconv.FormatInt(apiKeyID, 10)))
	// Include visible root instructions in each prefix while leaving model and
	// tool configuration out, so routing or tool declarations alone do not split
	// this transcript identity.
	for _, field := range []string{"instructions"} {
		v := root.Get(field)
		if !v.Exists() || (v.Type == gjson.String && strings.TrimSpace(v.String()) == "") {
			continue
		}
		canonical := openai.NormalizeCompatSeedJSON(json.RawMessage(v.Raw))
		if v.Type == gjson.String {
			canonical = v.String()
		}
		_, _ = h.Write([]byte("|"))
		_, _ = h.Write([]byte(field))
		_, _ = h.Write([]byte("="))
		_, _ = h.Write([]byte(canonical))
	}

	appendSequence := func(sequence gjson.Result) openAICyberTranscriptBlockKeys {
		if !sequence.Exists() || !sequence.IsArray() {
			return openAICyberTranscriptBlockKeys{}
		}
		result := openAICyberTranscriptBlockKeys{
			lookupKeys: make([]string, 0, maxOpenAICyberTranscriptLookupKeys),
		}
		nextLookupKey := 0
		lookupKeysRotated := false
		lastLookupKey := ""
		// Model-labelled history enables a pre-user context candidate; request
		// labels are heuristic, so fixed assistant examples can also satisfy this condition.
		hasModelGeneratedItem := false
		sequence.ForEach(func(_, item gjson.Result) bool {
			// Normalize JSON representation before cumulative hashing so object
			// formatting differences can reuse prefixes; the shared normalizer defines numeric representation.
			canonical := item.Raw
			switch item.Type {
			case gjson.String:
				encoded, _ := json.Marshal(item.String())
				canonical = string(encoded)
			case gjson.JSON:
				canonical = openai.NormalizeCompatSeedJSON(json.RawMessage(item.Raw))
			}
			if strings.TrimSpace(canonical) == "" {
				return true
			}
			// Capture the preceding prefix before adding this user input, allowing
			// an edited latest question to match previously blocked generated-history context.
			if openAICyberTranscriptItemStartsUserTurn(item) && hasModelGeneratedItem && lastLookupKey != "" {
				result.preLatestUserKey = lastLookupKey
			}
			_, _ = h.Write([]byte("|item="))
			_, _ = h.Write([]byte(canonical))
			lastLookupKey = hex.EncodeToString(h.Sum(nil))
			if len(result.lookupKeys) < maxOpenAICyberTranscriptLookupKeys {
				result.lookupKeys = append(result.lookupKeys, lastLookupKey)
			} else {
				// Retain the newest prefixes while marking discarded evidence; active
				// scopes reject this flag before an incomplete candidate list reaches Redis.
				result.lookupKeys[nextLookupKey] = lastLookupKey
				nextLookupKey = (nextLookupKey + 1) % maxOpenAICyberTranscriptLookupKeys
				lookupKeysRotated = true
				result.lookupKeysTruncated = true
			}
			if openAICyberTranscriptItemIsModelGenerated(item) {
				hasModelGeneratedItem = true
			}
			return true
		})
		// Restore chronological order after ring replacement so callers find the
		// full transcript at the end and probe retained prefixes in history order.
		if lookupKeysRotated {
			ordered := make([]string, 0, len(result.lookupKeys))
			ordered = append(ordered, result.lookupKeys[nextLookupKey:]...)
			ordered = append(ordered, result.lookupKeys[:nextLookupKey]...)
			result.lookupKeys = ordered
		}
		return result
	}

	// Prefer the declared Messages history even when unusable; combining it
	// with Responses input would assign a different transcript identity to the same request.
	if messages := root.Get("messages"); messages.Exists() {
		return appendSequence(messages)
	}
	input := root.Get("input")
	if input.IsArray() {
		return appendSequence(input)
	}
	if input.Type == gjson.String && strings.TrimSpace(input.String()) != "" {
		encoded, _ := json.Marshal(input.String())
		_, _ = h.Write([]byte("|item="))
		_, _ = h.Write(encoded)
		return openAICyberTranscriptBlockKeys{lookupKeys: []string{hex.EncodeToString(h.Sum(nil))}}
	}
	return openAICyberTranscriptBlockKeys{}
}

// openAICyberTranscriptItemStartsUserTurn recognizes user-labelled content or input_text
// items. For user content arrays, tool-result and call-output blocks alone do not
// start a user turn; non-array user content needs an existing content field.
func openAICyberTranscriptItemStartsUserTurn(item gjson.Result) bool {
	if strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "user") {
		content := item.Get("content")
		if content.IsArray() {
			hasUserContent := false
			content.ForEach(func(_, block gjson.Result) bool {
				switch strings.ToLower(strings.TrimSpace(block.Get("type").String())) {
				case "tool_result", "function_call_output", "custom_tool_call_output", "computer_call_output":
				default:
					hasUserContent = true
				}
				return !hasUserContent
			})
			return hasUserContent
		}
		return content.Exists()
	}
	return strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "input_text")
}

// openAICyberTranscriptItemIsModelGenerated recognizes assistant/model roles and the
// listed output or tool-call item types after case normalization. The result is
// request-supplied history evidence and does not authenticate actual model generation.
func openAICyberTranscriptItemIsModelGenerated(item gjson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(item.Get("role").String())) {
	case "assistant", "model":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "output_text", "function_call", "tool_call", "custom_tool_call", "computer_call":
		return true
	default:
		return false
	}
}
