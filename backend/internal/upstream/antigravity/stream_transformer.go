package antigravity

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// stream_transformer.go decodes Antigravity SSE data lines and delegates
// ordered Claude event conversion.
// Antigravity stream data lines wrap shared Gemini responses before Claude
// conversion. Prefix removal feeds a per-stream processor, while blank,
// completion, nondata, or undecodable lines produce no output; processor
// diagnostics are logged after each participating conversion.

// StreamingProcessor unwraps one data-line payload for shared mutable Gemini
// stream conversion.
type StreamingProcessor struct {
	// The embedded processor retains event and content state for one ordered
	// stream; callers serialize processing and Finish on this instance.
	*bridge.GeminiToAnthropicStreamProcessor
}

// NewStreamingProcessor creates per-stream conversion state for model using
// platform ID callbacks. Callers keep it within one ordered stream and
// serialize processing/finalization through the shared processor
// implementation.
func NewStreamingProcessor(model string) *StreamingProcessor {
	return &StreamingProcessor{bridge.NewGeminiToAnthropicStreamProcessor(model, geminiConversionRuntime())}
}

// ProcessLine returns Claude events for one data line and logs shared
// diagnostics. Empty, DONE, nondata, and undecodable input returns nil; direct
// decoding is tried after an envelope error rather than absent inner
// candidates.
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
		// Try direct decoding only after an envelope error; an object decoded as an
		// empty envelope remains subject to the shared processor empty-response
		// handling.
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
