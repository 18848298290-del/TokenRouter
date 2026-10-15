package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// stream_ttft.go selects the event rule that starts OpenAI time-to-first-token
// measurement.
// OpenAI TTFT can begin at semantic activity or at visible output, according
// to runtime policy. Visible mode delegates to protocol content detection;
// other normalized modes let forceOutput or the upstream semantic-event
// classifier start timing, keeping these measurement rules distinct.

// OpenAIStreamDataStartsTTFT selects the configured TTFT event rule after mode
// normalization. Visible mode uses content detection and ignores forceOutput;
// semantic mode accepts forced output or an event classified as semantic
// activity.
func OpenAIStreamDataStartsTTFT(data, eventType string, forceOutput bool, mode string) bool {
	if gateway.NormalizeOpenAITTFTMode(mode) == gateway.OpenAITTFTModeVisible {
		return protocolopenai.StreamDataStartsVisibleOutput(data, eventType)
	}
	return forceOutput || openai.OpenAIStreamDataStartsSemanticTTFT(data, eventType)
}
