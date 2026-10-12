package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GeminiToAnthropicResponseProcessor assembles a non-streaming Claude response from Gemini parts.
type GeminiToAnthropicResponseProcessor struct {
	runtime           GeminiConversionRuntime
	diagnostics       []string
	contentBlocks     []ClaudeContentItem
	textBuilder       string
	thinkingBuilder   string
	thinkingSignature string
	trailingSignature string
	hasToolCall       bool
}

// GeminiConversionRuntime supplies platform-owned ID generators.
type GeminiConversionRuntime struct {
	RandomID  func() string
	MessageID func() string
}

// NewGeminiToAnthropicResponseProcessor creates state for one response.
func NewGeminiToAnthropicResponseProcessor(runtime GeminiConversionRuntime) *GeminiToAnthropicResponseProcessor {
	return &GeminiToAnthropicResponseProcessor{
		runtime:       runtime,
		contentBlocks: make([]ClaudeContentItem, 0),
	}
}

// Process converts the first Gemini candidate to a Claude response.
func (p *GeminiToAnthropicResponseProcessor) Process(geminiResp *GeminiResponse, responseID, originalModel string) *ClaudeResponse {
	var parts []GeminiPart
	if len(geminiResp.Candidates) > 0 && geminiResp.Candidates[0].Content != nil {
		parts = geminiResp.Candidates[0].Content.Parts
	}

	for _, part := range parts {
		p.processPart(&part)
	}

	if len(geminiResp.Candidates) > 0 {
		if grounding := geminiResp.Candidates[0].GroundingMetadata; grounding != nil {
			p.processGrounding(grounding)
		}
	}

	p.flushThinking()
	p.flushText()

	// Emit an unpaired signature as an empty thinking block.
	if p.trailingSignature != "" {
		p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
			Type:      "thinking",
			Thinking:  "",
			Signature: p.trailingSignature,
		})
	}

	return p.buildResponse(geminiResp, responseID, originalModel)
}

func (p *GeminiToAnthropicResponseProcessor) processPart(part *GeminiPart) {
	signature := part.ThoughtSignature

	if part.FunctionCall != nil {
		p.flushThinking()
		p.flushText()

		// Keep a pending signature ahead of the tool call.
		if p.trailingSignature != "" {
			p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
				Type:      "thinking",
				Thinking:  "",
				Signature: p.trailingSignature,
			})
			p.trailingSignature = ""
		}

		p.hasToolCall = true

		toolID := part.FunctionCall.ID
		if toolID == "" {
			toolID = fmt.Sprintf("%s-%s", part.FunctionCall.Name, p.runtime.RandomID())
		}

		item := ClaudeContentItem{
			Type:  "tool_use",
			ID:    toolID,
			Name:  part.FunctionCall.Name,
			Input: part.FunctionCall.Args,
		}

		if signature != "" {
			item.Signature = signature
		}

		p.contentBlocks = append(p.contentBlocks, item)
		return
	}

	if part.Text != "" || part.Thought {
		if part.Thought {
			p.flushText()

			// Keep a pending signature separate from the next thinking block.
			if p.trailingSignature != "" {
				p.flushThinking()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: p.trailingSignature,
				})
				p.trailingSignature = ""
			}

			p.thinkingBuilder += part.Text
			if signature != "" {
				p.thinkingSignature = signature
			}
		} else {
			if part.Text == "" {
				// Defer a signature that arrives with empty text.
				if signature != "" {
					p.trailingSignature = signature
				}
				return
			}

			p.flushThinking()

			// Keep a pending signature ahead of the next text block.
			if p.trailingSignature != "" {
				p.flushText()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: p.trailingSignature,
				})
				p.trailingSignature = ""
			}

			// Represent signed text as text followed by an empty thinking block carrying the signature.
			if signature != "" {
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type: "text",
					Text: part.Text,
				})
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: signature,
				})
			} else {
				// Accumulate unsigned text until a block transition.
				p.textBuilder += part.Text
			}
		}
	}

	// Render inline images as Markdown data URLs.
	if part.InlineData != nil && part.InlineData.Data != "" {
		p.flushThinking()
		markdownImg := fmt.Sprintf("![image](data:%s;base64,%s)",
			part.InlineData.MimeType, part.InlineData.Data)
		p.textBuilder += markdownImg
		p.flushText()
	}
}

func (p *GeminiToAnthropicResponseProcessor) processGrounding(grounding *GeminiGroundingMetadata) {
	groundingText := buildGroundingText(grounding)
	if groundingText == "" {
		return
	}

	p.flushThinking()
	p.flushText()
	p.textBuilder += groundingText
	p.flushText()
}

// flushText appends buffered text as a content block and clears the buffer.
func (p *GeminiToAnthropicResponseProcessor) flushText() {
	if p.textBuilder == "" {
		return
	}

	p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
		Type: "text",
		Text: p.textBuilder,
	})
	p.textBuilder = ""
}

// flushThinking appends buffered thinking and its signature, then clears both.
func (p *GeminiToAnthropicResponseProcessor) flushThinking() {
	if p.thinkingBuilder == "" && p.thinkingSignature == "" {
		return
	}

	p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
		Type:      "thinking",
		Thinking:  p.thinkingBuilder,
		Signature: p.thinkingSignature,
	})
	p.thinkingBuilder = ""
	p.thinkingSignature = ""
}

// buildResponse attaches usage, a response ID, and the stop reason to the collected blocks.
func (p *GeminiToAnthropicResponseProcessor) buildResponse(geminiResp *GeminiResponse, responseID, originalModel string) *ClaudeResponse {
	var finishReason string
	if len(geminiResp.Candidates) > 0 {
		finishReason = geminiResp.Candidates[0].FinishReason
		if finishReason == "MALFORMED_FUNCTION_CALL" {
			p.addDiagnostic("[Antigravity] MALFORMED_FUNCTION_CALL detected in response for model %s", originalModel)
			if geminiResp.Candidates[0].Content != nil {
				if b, err := json.Marshal(geminiResp.Candidates[0].Content); err == nil {
					p.addDiagnostic("[Antigravity] Malformed content: %s", string(b))
				}
			}
		}
	}

	stopReason := "end_turn"
	if p.hasToolCall {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}

	// Gemini promptTokenCount includes cached tokens; Claude input_tokens excludes them.
	usage := ClaudeUsage{}
	if geminiResp.UsageMetadata != nil {
		cached := geminiResp.UsageMetadata.CachedContentTokenCount
		usage.InputTokens = geminiResp.UsageMetadata.PromptTokenCount - cached
		usage.OutputTokens = geminiResp.UsageMetadata.CandidatesTokenCount + geminiResp.UsageMetadata.ThoughtsTokenCount
		usage.CacheReadInputTokens = cached
		usage.ImageOutputTokens = geminiResp.UsageMetadata.ImageOutputTokens()
	}

	// Prefer the supplied ID, then the upstream ID, then a generated ID.
	respID := responseID
	if respID == "" {
		respID = geminiResp.ResponseID
	}
	if respID == "" {
		respID = p.runtime.MessageID()
	}

	return &ClaudeResponse{
		ID:         respID,
		Type:       "message",
		Role:       "assistant",
		Model:      originalModel,
		Content:    p.contentBlocks,
		StopReason: stopReason,
		Usage:      usage,
	}
}

func buildGroundingText(grounding *GeminiGroundingMetadata) string {
	if grounding == nil {
		return ""
	}

	var builder strings.Builder

	if len(grounding.WebSearchQueries) > 0 {
		_, _ = builder.WriteString("\n\n---\nWeb search queries: ")
		_, _ = builder.WriteString(strings.Join(grounding.WebSearchQueries, ", "))
	}

	if len(grounding.GroundingChunks) > 0 {
		var links []string
		for i, chunk := range grounding.GroundingChunks {
			if chunk.Web == nil {
				continue
			}
			title := strings.TrimSpace(chunk.Web.Title)
			if title == "" {
				title = "Source"
			}
			uri := strings.TrimSpace(chunk.Web.URI)
			if uri == "" {
				uri = "#"
			}
			links = append(links, fmt.Sprintf("[%d] [%s](%s)", i+1, title, uri))
		}

		if len(links) > 0 {
			_, _ = builder.WriteString("\n\nSources:\n")
			_, _ = builder.WriteString(strings.Join(links, "\n"))
		}
	}

	return builder.String()
}

// TakeDiagnostics returns and clears diagnostics for the platform adapter to log.
func (p *GeminiToAnthropicResponseProcessor) TakeDiagnostics() []string {
	out := p.diagnostics
	p.diagnostics = nil
	return out
}

func (p *GeminiToAnthropicResponseProcessor) addDiagnostic(format string, args ...any) {
	p.diagnostics = append(p.diagnostics, fmt.Sprintf(format, args...))
}
