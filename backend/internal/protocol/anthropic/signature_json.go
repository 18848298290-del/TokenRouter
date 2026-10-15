package anthropic

import (
	"bytes"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// signature_json.go removes replayed thinking signatures while retaining
// decoded numeric precision.
// Grok OAuth can reject encrypted reasoning replayed as a Claude thinking
// signature. Recovery removes that signature from historical thinking blocks
// while retaining the rest of the request object, including large integer
// metadata that ordinary float64 decoding would round before reserialization.
//
// The repair decodes with json.Number and edits only object-shaped blocks in
// message content arrays. It returns the supplied bytes when decoding fails,
// no signature is removed, or serialization fails. A successful repair
// reserializes the complete object, so JSON whitespace and key order can
// change while supported decoded numeric values keep their decimal precision.

// StripThinkingSignaturesJSON removes signatures from historical thinking
// blocks for the Grok decryption retry path. The boolean reports a successfully
// serialized repair; false returns the supplied body unchanged.
//
// The decoder retains JSON numbers as json.Number so unrelated large integer
// fields survive reserialization. String content, nonobject blocks, and
// signatures on other block types remain untouched. Empty, malformed, or
// incompatible request shapes produce no repair, allowing callers to decide
// whether another retry strategy applies.
// Decoding starts only when the quoted literal "signature" occurs in body;
// property names expressed solely through JSON escapes bypass this repair.
func StripThinkingSignaturesJSON(body []byte) ([]byte, bool) {
	// Quoted values can also pass this preliminary byte scan. The decoded object
	// and block-type checks decide whether an actual thinking signature field is
	// removed, keeping candidate detection separate from field selection.
	if len(body) == 0 || !bytes.Contains(body, []byte(`"signature"`)) {
		return body, false
	}
	var req map[string]any
	// Number-preserving decoding protects unrelated request metadata from the
	// precision loss that a float64 object decoder would introduce during this repair.
	if err := wirejson.DecodeUseNumber(body, &req); err != nil {
		// An undecodable request cannot be selectively repaired. Return its
		// supplied bytes so the caller retains the complete input for further error handling.
		return body, false
	}
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return body, false
	}
	changed := false
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := block["type"].(string); typ != "thinking" {
				continue
			}
			if _, has := block["signature"]; has {
				// The retry drops encrypted replay data from thinking blocks while
				// keeping their readable thinking text available to the converted request.
				delete(block, "signature")
				changed = true
			}
		}
	}
	if !changed {
		return body, false
	}
	out, err := wirejson.Marshal(req)
	if err != nil {
		// Serialization failure leaves the caller with its complete supplied
		// body and a false repair flag, preventing a retry with partially encoded content.
		return body, false
	}
	return out, true
}
