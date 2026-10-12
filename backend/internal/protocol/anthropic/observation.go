package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// Observation tracks reported usage, content output, and termination separately; zero usage counts as present.
type Observation struct {
	HasUsage, Semantic, Terminal bool
	Model                        string
}

func ObserveMessage(data string) Observation {
	value := gjson.Parse(data)
	observed := Observation{HasUsage: hasUsageFields(value.Get("usage")), Terminal: true, Model: protocol.NormalizeResponseModel(protocol.ResponseModelString([]byte(data), "model"))}
	value.Get("content").ForEach(func(_, block gjson.Result) bool {
		observed.Semantic = observed.Semantic || semanticBlock(block)
		return true
	})
	return observed
}

func ObserveEvent(data string) Observation {
	if strings.TrimSpace(data) == "[DONE]" {
		return Observation{Terminal: true}
	}
	value := gjson.Parse(data)
	observed := Observation{}
	switch value.Get("type").String() {
	case "message_start":
		observed = ObserveMessage(value.Get("message").Raw)
		observed.Terminal = false
		if !gjson.Valid(data) {
			observed.Model = ""
		}
	case "message_delta":
		observed.HasUsage = hasUsageFields(value.Get("usage"))
	case "content_block_start":
		observed.Semantic = semanticBlock(value.Get("content_block"))
	case "content_block_delta":
		delta := value.Get("delta")
		for _, field := range []string{"text", "thinking", "partial_json", "signature"} {
			if delta.Get(field).String() != "" {
				observed.Semantic = true
			}
		}
	case "message_stop", "error":
		observed.Terminal = true
	}
	return observed
}

func hasUsageFields(value gjson.Result) bool {
	for _, field := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cached_tokens", "cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens"} {
		v := value.Get(field)
		if v.Exists() && v.Type != gjson.Null {
			return true
		}
	}
	return false
}

func semanticBlock(value gjson.Result) bool {
	switch value.Get("type").String() {
	case "text":
		return value.Get("text").String() != ""
	case "thinking":
		return value.Get("thinking").String() != ""
	case "redacted_thinking":
		return value.Get("data").String() != ""
	case "tool_use", "server_tool_use":
		return value.Get("id").String() != "" || value.Get("name").String() != ""
	}
	return false
}
