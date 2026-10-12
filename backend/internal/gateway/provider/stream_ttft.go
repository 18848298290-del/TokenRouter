package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAIStreamDataStartsTTFT uses the configured event or visible-output rule to identify the TTFT start.
func OpenAIStreamDataStartsTTFT(data, eventType string, forceOutput bool, mode string) bool {
	if gateway.NormalizeOpenAITTFTMode(mode) == gateway.OpenAITTFTModeVisible {
		return protocolopenai.StreamDataStartsVisibleOutput(data, eventType)
	}
	return forceOutput || openai.OpenAIStreamDataStartsSemanticTTFT(data, eventType)
}
