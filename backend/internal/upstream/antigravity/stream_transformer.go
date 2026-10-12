package antigravity

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// StreamingProcessor unwraps platform SSE payloads for the shared stream converter.
type StreamingProcessor struct {
	*bridge.GeminiToAnthropicStreamProcessor
}

// NewStreamingProcessor configures conversion with the platform ID generators.
func NewStreamingProcessor(model string) *StreamingProcessor {
	return &StreamingProcessor{bridge.NewGeminiToAnthropicStreamProcessor(model, geminiConversionRuntime())}
}

// ProcessLine converts a data line to Claude SSE events, ignoring invalid input.
func (p *StreamingProcessor) ProcessLine(line string) []byte {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return nil
	}

	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}

	var v1Resp V1InternalResponse
	if err := json.Unmarshal([]byte(data), &v1Resp); err != nil {
		// Try a direct Gemini response when envelope decoding fails.
		var directResp GeminiResponse
		if err2 := json.Unmarshal([]byte(data), &directResp); err2 != nil {
			return nil
		}
		v1Resp.Response = directResp
		v1Resp.ResponseID = directResp.ResponseID
		v1Resp.ModelVersion = directResp.ModelVersion
	}

	result := p.ProcessResponse(&bridge.GeminiResponseInput{Response: v1Resp.Response, ResponseID: v1Resp.ResponseID, ModelVersion: v1Resp.ModelVersion})
	for _, message := range p.TakeDiagnostics() {
		log.Printf("%s", message)
	}
	return result
}
