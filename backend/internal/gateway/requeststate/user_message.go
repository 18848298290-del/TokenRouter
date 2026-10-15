package requeststate

import "github.com/tidwall/gjson"

// user_message.go classifies the final Anthropic message as a user turn or
// tool-result reply. The final message supplies this content classification;
// earlier tool results leave a later ordinary user turn eligible for that
// content check.

// IsRealUserMessage reports whether the final captured message is an exact
// user-role object suitable for ordinary serial-queue input. Missing requests,
// unavailable message arrays, empty arrays, and nonobject final entries return
// false.
//
// Missing or nonarray content counts as ordinary user input. Array content is
// rejected when any block has exact type tool_result or tool_use_result,
// regardless of accompanying text. Earlier messages do not participate in that
// decision, so the entry can admit a later user turn after historical tool
// replies.
func IsRealUserMessage(parsed *ParsedRequest) bool {
	if parsed == nil {
		return false
	}
	messagesRaw := parsed.MessagesRaw()
	if len(messagesRaw) == 0 {
		return false
	}

	messages := gjson.ParseBytes(messagesRaw)
	if !messages.IsArray() {
		return false
	}
	lastMsg := gjson.Result{}
	messages.ForEach(func(_, msg gjson.Result) bool {
		lastMsg = msg
		return true
	})
	if !lastMsg.Exists() || !lastMsg.IsObject() {
		return false
	}
	if lastMsg.Get("role").String() != "user" {
		return false
	}

	content := lastMsg.Get("content")
	if !content.Exists() {
		return true
	}
	if !content.IsArray() {
		return true
	}

	isReal := true
	content.ForEach(func(_, item gjson.Result) bool {
		itemType := item.Get("type").String()
		if itemType == "tool_result" || itemType == "tool_use_result" {
			isReal = false
			return false
		}
		return true
	})
	return isReal
}
