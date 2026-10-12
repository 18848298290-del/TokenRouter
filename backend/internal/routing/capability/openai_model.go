package capability

import (
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// LastOpenAIModelSegment returns the final path segment used for capability classification.
func LastOpenAIModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if index := strings.LastIndexByte(model, '/'); index >= 0 {
		return strings.TrimSpace(model[index+1:])
	}
	return model
}

// CanonicalizeOpenAIModelAliasSpelling trims and lowercases an ID while retaining its spelling and provider
// prefix.
func CanonicalizeOpenAIModelAliasSpelling(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// NormalizeRecordedOpenAIEffortForModel normalizes an effort already sent; the request resolver checks
// capabilities.
func NormalizeRecordedOpenAIEffortForModel(raw string, _ string) string {
	return protocolopenai.NormalizeRecordedReasoningEffort(raw)
}
