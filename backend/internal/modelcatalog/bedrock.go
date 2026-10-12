package modelcatalog

import "strings"

// BedrockExecutionRules 登记当前适配器已核实的缓存、工具搜索与 thinking 编码。
// 型号与日期来自 AWS 模型清单，新增型号时需同步对应的请求夹具。
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
