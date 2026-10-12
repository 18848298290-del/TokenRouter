package antigravity

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// fallbackCounter adds per-call variation when random IDs use a time-based seed.
var fallbackCounter uint64

// NonStreamingProcessor adds platform diagnostics to the shared response processor.
type NonStreamingProcessor struct {
	*bridge.GeminiToAnthropicResponseProcessor
}

// TransformGeminiToClaude converts a wrapped or direct Gemini response to Claude JSON.
func TransformGeminiToClaude(geminiResp []byte, originalModel string) ([]byte, *ClaudeUsage, error) {
	var v1Resp V1InternalResponse
	if err := json.Unmarshal(geminiResp, &v1Resp); err != nil {
		// Try the direct Gemini response format when envelope decoding fails.
		var directResp GeminiResponse
		if err2 := json.Unmarshal(geminiResp, &directResp); err2 != nil {
			return nil, nil, fmt.Errorf("parse gemini response: %w", err)
		}
		v1Resp.Response = directResp
		v1Resp.ResponseID = directResp.ResponseID
		v1Resp.ModelVersion = directResp.ModelVersion
	} else if len(v1Resp.Response.Candidates) == 0 {
		// An envelope with no candidates may instead contain a direct Gemini response.
		var directResp GeminiResponse
		if err2 := json.Unmarshal(geminiResp, &directResp); err2 != nil {
			return nil, nil, fmt.Errorf("parse gemini response as direct: %w", err2)
		}
		v1Resp.Response = directResp
		v1Resp.ResponseID = directResp.ResponseID
		v1Resp.ModelVersion = directResp.ModelVersion
	}

	processor := NewNonStreamingProcessor()
	claudeResp := processor.Process(&v1Resp.Response, v1Resp.ResponseID, originalModel)

	respBytes, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal claude response: %w", err)
	}

	return respBytes, &claudeResp.Usage, nil
}

// generateRandomID returns a temporary 12-character alphanumeric identifier.
func generateRandomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	id := make([]byte, 12)
	randBytes := make([]byte, 12)
	if _, err := rand.Read(randBytes); err != nil {
		// If the entropy source fails, use time and a counter to generate a temporary ID.
		// These response and tool-call IDs are not authentication secrets.
		cnt := atomic.AddUint64(&fallbackCounter, 1)
		seed := uint64(time.Now().UnixNano()) ^ cnt
		seed ^= uint64(len(err.Error())) << 32
		for i := range id {
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			id[i] = chars[int(seed)%len(chars)]
		}
		return string(id)
	}
	for i, b := range randBytes {
		id[i] = chars[int(b)%len(chars)]
	}
	return string(id)
}

// generateAnthropicMsgID returns msg_01 followed by 22 alphanumeric characters.
func generateAnthropicMsgID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		return "msg_01" + generateRandomID() + generateRandomID()[:10]
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}

func NewNonStreamingProcessor() *NonStreamingProcessor {
	return &NonStreamingProcessor{bridge.NewGeminiToAnthropicResponseProcessor(geminiConversionRuntime())}
}

func (p *NonStreamingProcessor) Process(response *GeminiResponse, responseID, model string) *ClaudeResponse {
	result := p.GeminiToAnthropicResponseProcessor.Process(response, responseID, model)
	for _, message := range p.TakeDiagnostics() {
		log.Printf("%s", message)
	}
	return result
}

// geminiConversionRuntime supplies response and tool-call ID generators.
func geminiConversionRuntime() bridge.GeminiConversionRuntime {
	return bridge.GeminiConversionRuntime{RandomID: generateRandomID, MessageID: generateAnthropicMsgID}
}
