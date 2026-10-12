package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
)

const (
	BlockTypeNone BlockType = iota
	BlockTypeText
	BlockTypeThinking
	BlockTypeFunction
)

// BlockType identifies the currently open Anthropic content block.
type BlockType int

// GeminiToAnthropicStreamProcessor converts Gemini events into an Anthropic SSE stream.
type GeminiToAnthropicStreamProcessor struct {
	runtime           GeminiConversionRuntime
	diagnostics       []string
	blockType         BlockType
	blockIndex        int
	messageStartSent  bool
	messageStopSent   bool
	usedTool          bool
	pendingSignature  string
	trailingSignature string
	originalModel     string
	webSearchQueries  []string
	groundingChunks   []GeminiGroundingChunk

	// Latest cumulative usage reported by Gemini.
	inputTokens       int
	outputTokens      int
	cacheReadTokens   int
	imageOutputTokens int
}

// GeminiResponseInput contains the Gemini response and IDs extracted from the platform envelope.
type GeminiResponseInput struct {
	Response                 GeminiResponse
	ResponseID, ModelVersion string
}

// NewGeminiToAnthropicStreamProcessor creates state for one response stream.
func NewGeminiToAnthropicStreamProcessor(originalModel string, runtime GeminiConversionRuntime) *GeminiToAnthropicStreamProcessor {
	return &GeminiToAnthropicStreamProcessor{
		runtime:       runtime,
		blockType:     BlockTypeNone,
		originalModel: originalModel,
	}
}

// ProcessResponse converts an unpacked Gemini event to Anthropic SSE bytes.
func (p *GeminiToAnthropicStreamProcessor) ProcessResponse(v1Resp *GeminiResponseInput) []byte {
	geminiResp := &v1Resp.Response

	var result bytes.Buffer

	if !p.messageStartSent {
		_, _ = result.Write(p.emitMessageStart(v1Resp))
	}

	// Gemini promptTokenCount includes cached tokens; Claude input_tokens excludes them.
	if geminiResp.UsageMetadata != nil {
		cached := geminiResp.UsageMetadata.CachedContentTokenCount
		p.inputTokens = geminiResp.UsageMetadata.PromptTokenCount - cached
		p.outputTokens = geminiResp.UsageMetadata.CandidatesTokenCount + geminiResp.UsageMetadata.ThoughtsTokenCount
		p.cacheReadTokens = cached
		p.imageOutputTokens = geminiResp.UsageMetadata.ImageOutputTokens()
	}

	if len(geminiResp.Candidates) > 0 && geminiResp.Candidates[0].Content != nil {
		for _, part := range geminiResp.Candidates[0].Content.Parts {
			_, _ = result.Write(p.processPart(&part))
		}
	}

	if len(geminiResp.Candidates) > 0 {
		p.captureGrounding(geminiResp.Candidates[0].GroundingMetadata)
	}

	if len(geminiResp.Candidates) > 0 {
		finishReason := geminiResp.Candidates[0].FinishReason
		if finishReason == "MALFORMED_FUNCTION_CALL" {
			p.addDiagnostic("[Antigravity] MALFORMED_FUNCTION_CALL detected in stream for model %s", p.originalModel)
			if geminiResp.Candidates[0].Content != nil {
				if b, err := json.Marshal(geminiResp.Candidates[0].Content); err == nil {
					p.addDiagnostic("[Antigravity] Malformed content: %s", string(b))
				}
			}
		}
		if finishReason != "" {
			_, _ = result.Write(p.emitFinish(finishReason))
		}
	}

	return result.Bytes()
}

// Finish returns closing events and the observed usage.
// An unstarted stream produces no closing events, so message_stop cannot appear
// without a preceding message_start.
func (p *GeminiToAnthropicStreamProcessor) Finish() ([]byte, *ClaudeUsage) {
	usage := &ClaudeUsage{
		InputTokens:          p.inputTokens,
		OutputTokens:         p.outputTokens,
		CacheReadInputTokens: p.cacheReadTokens,
		ImageOutputTokens:    p.imageOutputTokens,
	}

	if !p.messageStartSent {
		return nil, usage
	}

	var result bytes.Buffer
	if !p.messageStopSent {
		_, _ = result.Write(p.emitFinish(""))
	}

	return result.Bytes(), usage
}

// MessageStartSent reports whether a message_start event has been emitted.
func (p *GeminiToAnthropicStreamProcessor) MessageStartSent() bool {
	return p.messageStartSent
}

func (p *GeminiToAnthropicStreamProcessor) emitMessageStart(v1Resp *GeminiResponseInput) []byte {
	if p.messageStartSent {
		return nil
	}

	usage := ClaudeUsage{}
	if v1Resp.Response.UsageMetadata != nil {
		cached := v1Resp.Response.UsageMetadata.CachedContentTokenCount
		usage.InputTokens = v1Resp.Response.UsageMetadata.PromptTokenCount - cached
		usage.OutputTokens = v1Resp.Response.UsageMetadata.CandidatesTokenCount + v1Resp.Response.UsageMetadata.ThoughtsTokenCount
		usage.CacheReadInputTokens = cached
		usage.ImageOutputTokens = v1Resp.Response.UsageMetadata.ImageOutputTokens()
	}

	responseID := v1Resp.ResponseID
	if responseID == "" {
		responseID = v1Resp.Response.ResponseID
	}
	if responseID == "" {
		responseID = p.runtime.MessageID()
	}

	message := map[string]any{
		"id":            responseID,
		"type":          "message",
		"role":          "assistant",
		"content":       []any{},
		"model":         p.originalModel,
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         usage,
	}

	event := map[string]any{
		"type":    "message_start",
		"message": message,
	}

	p.messageStartSent = true
	return p.formatSSE("message_start", event)
}

func (p *GeminiToAnthropicStreamProcessor) processPart(part *GeminiPart) []byte {
	var result bytes.Buffer
	signature := part.ThoughtSignature

	if part.FunctionCall != nil {
		// Emit a pending signature before the tool call.
		if p.trailingSignature != "" {
			_, _ = result.Write(p.endBlock())
			_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
			p.trailingSignature = ""
		}

		_, _ = result.Write(p.processFunctionCall(part.FunctionCall, signature))
		return result.Bytes()
	}

	if part.Text != "" || part.Thought {
		if part.Thought {
			_, _ = result.Write(p.processThinking(part.Text, signature))
		} else {
			_, _ = result.Write(p.processText(part.Text, signature))
		}
	}

	// Render inline images as Markdown data URLs.
	if part.InlineData != nil && part.InlineData.Data != "" {
		markdownImg := fmt.Sprintf("![image](data:%s;base64,%s)",
			part.InlineData.MimeType, part.InlineData.Data)
		_, _ = result.Write(p.processText(markdownImg, ""))
	}

	return result.Bytes()
}

func (p *GeminiToAnthropicStreamProcessor) captureGrounding(grounding *GeminiGroundingMetadata) {
	if grounding == nil {
		return
	}

	if len(grounding.WebSearchQueries) > 0 && len(p.webSearchQueries) == 0 {
		p.webSearchQueries = append([]string(nil), grounding.WebSearchQueries...)
	}

	if len(grounding.GroundingChunks) > 0 && len(p.groundingChunks) == 0 {
		p.groundingChunks = append([]GeminiGroundingChunk(nil), grounding.GroundingChunks...)
	}
}

// processThinking appends thinking deltas and defers the signature until the block closes.
func (p *GeminiToAnthropicStreamProcessor) processThinking(text, signature string) []byte {
	var result bytes.Buffer

	// Emit an unpaired signature before the next thinking block.
	if p.trailingSignature != "" {
		_, _ = result.Write(p.endBlock())
		_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
		p.trailingSignature = ""
	}

	if p.blockType != BlockTypeThinking {
		_, _ = result.Write(p.startBlock(BlockTypeThinking, map[string]any{
			"type":     "thinking",
			"thinking": "",
		}))
	}

	if text != "" {
		_, _ = result.Write(p.emitDelta("thinking_delta", map[string]any{
			"thinking": text,
		}))
	}

	if signature != "" {
		p.pendingSignature = signature
	}

	return result.Bytes()
}

// processText emits text deltas and represents signatures in separate thinking blocks.
func (p *GeminiToAnthropicStreamProcessor) processText(text, signature string) []byte {
	var result bytes.Buffer

	// Defer a signature that arrives with empty text.
	if text == "" {
		if signature != "" {
			p.trailingSignature = signature
		}
		return nil
	}

	// Emit a pending signature before the next text block.
	if p.trailingSignature != "" {
		_, _ = result.Write(p.endBlock())
		_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
		p.trailingSignature = ""
	}

	// Keep signed text and its signature in separate blocks.
	if signature != "" {
		_, _ = result.Write(p.startBlock(BlockTypeText, map[string]any{
			"type": "text",
			"text": "",
		}))
		_, _ = result.Write(p.emitDelta("text_delta", map[string]any{
			"text": text,
		}))
		_, _ = result.Write(p.endBlock())
		_, _ = result.Write(p.emitEmptyThinkingWithSignature(signature))
		return result.Bytes()
	}

	if p.blockType != BlockTypeText {
		_, _ = result.Write(p.startBlock(BlockTypeText, map[string]any{
			"type": "text",
			"text": "",
		}))
	}

	_, _ = result.Write(p.emitDelta("text_delta", map[string]any{
		"text": text,
	}))

	return result.Bytes()
}

// processFunctionCall emits a complete tool block, generating an ID when needed.
func (p *GeminiToAnthropicStreamProcessor) processFunctionCall(fc *GeminiFunctionCall, signature string) []byte {
	var result bytes.Buffer

	p.usedTool = true

	toolID := fc.ID
	if toolID == "" {
		toolID = fmt.Sprintf("%s-%s", fc.Name, p.runtime.RandomID())
	}

	toolUse := map[string]any{
		"type":  "tool_use",
		"id":    toolID,
		"name":  fc.Name,
		"input": map[string]any{},
	}

	if signature != "" {
		toolUse["signature"] = signature
	}

	_, _ = result.Write(p.startBlock(BlockTypeFunction, toolUse))

	if fc.Args != nil {
		argsJSON, _ := json.Marshal(fc.Args)
		_, _ = result.Write(p.emitDelta("input_json_delta", map[string]any{
			"partial_json": string(argsJSON),
		}))
	}

	_, _ = result.Write(p.endBlock())

	return result.Bytes()
}

// startBlock closes any open block before opening the requested type.
func (p *GeminiToAnthropicStreamProcessor) startBlock(blockType BlockType, contentBlock map[string]any) []byte {
	var result bytes.Buffer

	if p.blockType != BlockTypeNone {
		_, _ = result.Write(p.endBlock())
	}

	event := map[string]any{
		"type":          "content_block_start",
		"index":         p.blockIndex,
		"content_block": contentBlock,
	}

	_, _ = result.Write(p.formatSSE("content_block_start", event))
	p.blockType = blockType

	return result.Bytes()
}

// endBlock closes the current block and advances its index.
func (p *GeminiToAnthropicStreamProcessor) endBlock() []byte {
	if p.blockType == BlockTypeNone {
		return nil
	}

	var result bytes.Buffer

	// Emit the saved signature before closing a thinking block.
	if p.blockType == BlockTypeThinking && p.pendingSignature != "" {
		_, _ = result.Write(p.emitDelta("signature_delta", map[string]any{
			"signature": p.pendingSignature,
		}))
		p.pendingSignature = ""
	}

	event := map[string]any{
		"type":  "content_block_stop",
		"index": p.blockIndex,
	}

	_, _ = result.Write(p.formatSSE("content_block_stop", event))

	p.blockIndex++
	p.blockType = BlockTypeNone

	return result.Bytes()
}

func (p *GeminiToAnthropicStreamProcessor) emitDelta(deltaType string, deltaContent map[string]any) []byte {
	delta := map[string]any{
		"type": deltaType,
	}
	maps.Copy(delta, deltaContent)

	event := map[string]any{
		"type":  "content_block_delta",
		"index": p.blockIndex,
		"delta": delta,
	}

	return p.formatSSE("content_block_delta", event)
}

// emitEmptyThinkingWithSignature represents a standalone signature as a thinking block.
func (p *GeminiToAnthropicStreamProcessor) emitEmptyThinkingWithSignature(signature string) []byte {
	var result bytes.Buffer

	_, _ = result.Write(p.startBlock(BlockTypeThinking, map[string]any{
		"type":     "thinking",
		"thinking": "",
	}))
	_, _ = result.Write(p.emitDelta("thinking_delta", map[string]any{
		"thinking": "",
	}))
	_, _ = result.Write(p.emitDelta("signature_delta", map[string]any{
		"signature": signature,
	}))
	_, _ = result.Write(p.endBlock())

	return result.Bytes()
}

// emitFinish closes content blocks and emits usage and terminal events.
func (p *GeminiToAnthropicStreamProcessor) emitFinish(finishReason string) []byte {
	var result bytes.Buffer

	_, _ = result.Write(p.endBlock())

	// Emit any unpaired signature before finishing.
	if p.trailingSignature != "" {
		_, _ = result.Write(p.emitEmptyThinkingWithSignature(p.trailingSignature))
		p.trailingSignature = ""
	}

	if len(p.webSearchQueries) > 0 || len(p.groundingChunks) > 0 {
		groundingText := buildGroundingText(&GeminiGroundingMetadata{
			WebSearchQueries: p.webSearchQueries,
			GroundingChunks:  p.groundingChunks,
		})
		if groundingText != "" {
			_, _ = result.Write(p.startBlock(BlockTypeText, map[string]any{
				"type": "text",
				"text": "",
			}))
			_, _ = result.Write(p.emitDelta("text_delta", map[string]any{
				"text": groundingText,
			}))
			_, _ = result.Write(p.endBlock())
		}
	}

	stopReason := "end_turn"
	if p.usedTool {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}

	usage := ClaudeUsage{
		InputTokens:          p.inputTokens,
		OutputTokens:         p.outputTokens,
		CacheReadInputTokens: p.cacheReadTokens,
		ImageOutputTokens:    p.imageOutputTokens,
	}

	deltaEvent := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": usage,
	}

	_, _ = result.Write(p.formatSSE("message_delta", deltaEvent))

	if !p.messageStopSent {
		stopEvent := map[string]any{
			"type": "message_stop",
		}
		_, _ = result.Write(p.formatSSE("message_stop", stopEvent))
		p.messageStopSent = true
	}

	return result.Bytes()
}

func (p *GeminiToAnthropicStreamProcessor) formatSSE(eventType string, data any) []byte {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

// TakeDiagnostics returns and clears the buffered conversion diagnostics.
func (p *GeminiToAnthropicStreamProcessor) TakeDiagnostics() []string {
	out := p.diagnostics
	p.diagnostics = nil
	return out
}

func (p *GeminiToAnthropicStreamProcessor) addDiagnostic(format string, args ...any) {
	p.diagnostics = append(p.diagnostics, fmt.Sprintf(format, args...))
}
