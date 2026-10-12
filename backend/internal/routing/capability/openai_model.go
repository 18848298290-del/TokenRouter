package capability

import (
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// LastOpenAIModelSegment 返回能力分类使用的模型路径尾段。
func LastOpenAIModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if index := strings.LastIndexByte(model, '/'); index >= 0 {
		return strings.TrimSpace(model[index+1:])
	}
	return model
}

// CanonicalizeOpenAIModelAliasSpelling 规范查询大小写，保留型号拼写及供应商前缀。
func CanonicalizeOpenAIModelAliasSpelling(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// NormalizeRecordedOpenAIEffortForModel 归一化已经发送的推理档位；能力判断由请求解析器完成。
func NormalizeRecordedOpenAIEffortForModel(raw string, _ string) string {
	return protocolopenai.NormalizeRecordedReasoningEffort(raw)
}
