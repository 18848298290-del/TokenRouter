package requeststate

import "github.com/tidwall/gjson"

// IsRealUserMessage reports whether the last message is user input:
// its role is user and its content array contains no tool_result or tool_use_result blocks.
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
