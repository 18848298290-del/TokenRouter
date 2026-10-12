package modelcatalog

import "strings"

// BedrockExecutionRules returns registered cache, tool-search, and thinking encodings for Bedrock models.
// Model IDs follow the AWS catalog. Add matching request fixtures when registering a model.
func BedrockExecutionRules(model string) ExecutionRules {
	model = strings.TrimPrefix(normalize(model), "amazon-bedrock/")
	for _, prefix := range []string{"us.", "eu.", "apac.", "jp.", "au.", "us-gov.", "global."} {
		if name, found := strings.CutPrefix(model, prefix); found {
			model = name
			break
		}
	}
	model = strings.TrimPrefix(model, "anthropic.")
	result := ExecutionRules{}
	switch model {
	case "claude-opus-4-5", "claude-opus-4-5-20251101-v1:0", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929-v1:0":
		result.ThinkingMode = "budget"
		result.ToolSearch = valuePointer(true)
	case "claude-opus-4-6", "claude-opus-4-6-v1", "claude-sonnet-4-6":
		result.ThinkingMode = "adaptive_or_budget"
		result.ToolSearch = valuePointer(true)
	case "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5":
		result.ThinkingMode = "adaptive"
		result.ToolSearch = valuePointer(true)
	case "claude-sonnet-5-5":
		result.ThinkingMode = "adaptive_between_tools"
		result.ToolSearch = valuePointer(true)
	case "claude-haiku-4-5", "claude-haiku-4-5-20251001-v1:0":
		result.ThinkingMode = "budget"
		result.ToolSearch = valuePointer(false)
	case "claude-haiku-5-5":
		result.ThinkingMode = "adaptive"
		result.ToolSearch = valuePointer(false)
	case "claude-fable-5", "claude-fable-5-1":
		result.ThinkingMode = "adaptive"
		result.ToolSearch = valuePointer(false)
	default:
		return result
	}
	result.CacheTTL = []string{"5m", "1h"}
	return result
}
