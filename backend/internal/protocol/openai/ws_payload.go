package openai

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsWSTokenEvent recognizes delta and output event types used for first-token timing.
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
	// IsWSTerminalEvent handles response.completed, done, failed, and other terminal types.
	// Counting them as token events would set firstTokenMs to completion time when
	// no recognizable delta arrived, misreporting total duration as first-token latency.
	return false
}

func ReplaceWSMessageModel(message []byte, fromModel, toModel string) []byte {
	if len(message) == 0 {
		return message
	}
	if strings.TrimSpace(fromModel) == "" || strings.TrimSpace(toModel) == "" || fromModel == toModel {
		return message
	}
	if !bytes.Contains(message, []byte(`"model"`)) || !bytes.Contains(message, []byte(fromModel)) {
		return message
	}
	modelValues := gjson.GetManyBytes(message, "model", "response.model")
	replaceModel := modelValues[0].Exists() && modelValues[0].Str == fromModel
	replaceResponseModel := modelValues[1].Exists() && modelValues[1].Str == fromModel
	if !replaceModel && !replaceResponseModel {
		return message
	}
	updated := message
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

func PopulateUsageFromResponseJSON(body []byte, usage *ForwardUsage) {
	if usage == nil || len(body) == 0 {
		return
	}
	if parsedUsage, ok := ExtractOpenAIUsageFromJSONBytes(body); ok {
		*usage = parsedUsage
	}
}

// RequestPayloadView unwraps a nested response.create request object when present.
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
