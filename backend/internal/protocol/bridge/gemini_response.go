package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// gemini_response.go builds buffered Messages responses from Gemini content
// and replay signatures.
// Gemini thought signatures can accompany thinking, visible text, or function
// calls, while Messages carries replay signatures on thinking and tool blocks.
// Thinking accumulates text with the latest supplied signature; signed visible
// text contributes a text block followed by an empty signed thinking block,
// and calls keep their own signature.
//
// Unsigned visible text waits in a buffer until a tool, thinking transition,
// grounding footer, image, or final flush. Signed visible text appends
// directly and leaves any earlier unsigned text buffered for a later flush, so
// their resulting block order can differ from input part order. The processor
// converts one first-candidate answer, chooses wrapper/upstream/generated
// identity, and returns content sharing its retained storage and decoded call
// arguments.

// GeminiToAnthropicResponseProcessor holds buffered text, signatures, tool
// presence, and assembled blocks for one Gemini answer converted into
// Messages. Create a fresh instance for each answer.
type GeminiToAnthropicResponseProcessor struct {
	// runtime supplies missing response/call identities; the constructor retains
	// its callbacks and applies no defaults for absent generator functions.
	runtime GeminiConversionRuntime
	// diagnostics accumulates supplemental conversion messages until
	// TakeDiagnostics returns the current slice and clears this reference.
	diagnostics []string
	// contentBlocks is returned directly as ClaudeResponse.Content; subsequent
	// processor use can append to this storage, and call Inputs retain shared arguments.
	contentBlocks []ClaudeContentItem
	// textBuilder holds unsigned visible text until a flush; signed text appends
	// directly to contentBlocks and can precede this earlier buffered text.
	textBuilder string
	// thinkingBuilder accumulates adjacent thinking fragments; thinkingSignature
	// retains their latest nonempty signature. flushThinking emits a block when
	// either exists, then clears both before the next thinking sequence.
	thinkingBuilder   string // pending readable thinking text
	thinkingSignature string // pending thinking replay signature
	// trailingSignature belongs to the empty-visible-text branch, but processPart's
	// dispatch guard excludes bare signature-only parts from reaching that branch.
	trailingSignature string
	// hasToolCall records any processed invocation and makes tool_use win over
	// first-candidate length exhaustion when buildResponse selects the stop reason.
	hasToolCall bool
}

// GeminiConversionRuntime provides response and tool identity callbacks used
// when supplied Gemini identities are absent.
type GeminiConversionRuntime struct {
	// RandomID generates the suffix of a name-based tool ID when a call supplies
	// no ID; callers provide this function before processing such calls.
	RandomID func() string
	// MessageID generates response identity after supplied wrapper and upstream
	// IDs are empty; callers provide this function before that fallback is needed.
	MessageID func() string
}

// NewGeminiToAnthropicResponseProcessor creates fresh buffered state and
// copies the supplied runtime callbacks. Configure MessageID at construction
// for responses lacking both IDs, and RandomID for calls lacking an ID; use
// one processor per answer and serialize access while its returned content is
// in use.
func NewGeminiToAnthropicResponseProcessor(runtime GeminiConversionRuntime) *GeminiToAnthropicResponseProcessor {
	return &GeminiToAnthropicResponseProcessor{
		runtime:       runtime,
		contentBlocks: make([]ClaudeContentItem, 0),
	}
}

// Process converts the first candidate of a nonnil Gemini response into a
// Messages answer using the requested model. Use a fresh processor per answer
// because existing blocks, tool presence, and diagnostics are retained across
// calls.
//
// Thinking/text buffers flush around their respective transitions, tools,
// images, and grounding output. Signed visible text appends directly before a
// later flush of pending unsigned text, so that mixed sequence can reorder
// blocks. Bare nonthought signature-only parts are skipped by part dispatch.
// The returned Content slice shares processor storage, and tool Input values
// share decoded arguments; callers keep those references stable until response
// serialization finishes.
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

	// If deferred state exists, append its signature after the final content
	// flush; normal dispatch skips bare nonthought signature-only parts that
	// would populate this branch.
	if p.trailingSignature != "" {
		p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
			Type:      "thinking",
			Thinking:  "",
			Signature: p.trailingSignature,
		})
	}

	return p.buildResponse(geminiResp, responseID, originalModel)
}

// processPart appends or buffers a Gemini part under the buffered Messages
// signature rules. Function calls take priority over the same part's text or
// inline data, flush current text/thinking, and retain supplied call IDs or
// generate a name-based ID.
//
// Thinking text accumulates with its latest nonempty signature, while unsigned
// visible text stays buffered. Signed visible text appends a text/signature
// pair directly and leaves any earlier unsigned buffer pending. Bare
// signature-only nonthought parts never enter text dispatch. Inline data
// becomes an unvalidated Markdown data URL and flushes text after thinking;
// nested function arguments remain shared with the input part.
func (p *GeminiToAnthropicResponseProcessor) processPart(part *GeminiPart) {
	signature := part.ThoughtSignature

	// Calls consume their part before text/image processing; flush prior buffers
	// so the invocation follows already buffered content and carries its own
	// supplied signature.
	if part.FunctionCall != nil {
		p.flushThinking()
		p.flushText()

		// Drain any deferred signature into a distinct empty thinking block before
		// the tool, so that state does not become the tool's own signature.
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
		// Keep a supplied invocation ID for replay correlation; an absent ID uses
		// the call name and injected random suffix to identify the generated tool
		// block.
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

			// Flush existing thinking before appending a deferred standalone signature,
			// keeping that metadata separate from the next thinking fragment and its
			// later signature.
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
				// The outer dispatch requires nonempty text for nonthought parts, so bare
				// signature-only parts skip this branch and do not create deferred
				// signature state.
				if signature != "" {
					p.trailingSignature = signature
				}
				return
			}

			p.flushThinking()

			// Flush pending visible text before inserting deferred signature state, so
			// that state occupies its own thinking block ahead of the next visible
			// fragment.
			if p.trailingSignature != "" {
				p.flushText()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: p.trailingSignature,
				})
				p.trailingSignature = ""
			}

			// Signed visible text appends a text block and empty signed thinking block
			// directly; earlier unsigned text remains buffered and can be emitted after
			// this pair.
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
				// Unsigned fragments concatenate in their current text buffer until a
				// flush trigger; unlike signed text, they do not immediately occupy
				// contentBlocks positions.
				p.textBuilder += part.Text
			}
		}
	}

	// Inline data becomes Markdown text using its supplied MIME/base64 strings,
	// with no image validation here; flush it with any pending unsigned text
	// after closing thinking.
	if part.InlineData != nil && part.InlineData.Data != "" {
		p.flushThinking()
		markdownImg := fmt.Sprintf("![image](data:%s;base64,%s)",
			part.InlineData.MimeType, part.InlineData.Data)
		p.textBuilder += markdownImg
		p.flushText()
	}
}

// processGrounding appends the formatted search/source footer as a separate
// text block after flushing content buffers. Missing or unusable grounding
// text leaves state unchanged; formatting and source numbering are owned by
// buildGroundingText.
func (p *GeminiToAnthropicResponseProcessor) processGrounding(grounding *GeminiGroundingMetadata) {
	groundingText := buildGroundingText(grounding)
	if groundingText == "" {
		return
	}

	// Grounding appears as a separate text footer, so finish pending thinking and
	// visible text before appending and flushing its formatted queries and links.
	p.flushThinking()
	p.flushText()
	p.textBuilder += groundingText
	p.flushText()
}

// flushText appends the pending visible text as one Messages block and clears
// its string buffer. An empty buffer emits no block, and existing signed text
// blocks retain their already appended positions.
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

// flushThinking appends pending thinking text and its retained signature as
// one Messages block, then clears both fields. A nonempty signature emits a
// block even when thinking text is empty.
//
// processPart can receive signature-bearing empty thought parts, so testing
// text alone would discard replay metadata at a transition or the final flush.
// The most recent nonempty signature belongs to the accumulated thinking
// buffer. When both fields are empty there is no pending block, and the
// content list remains unchanged.
func (p *GeminiToAnthropicResponseProcessor) flushThinking() {
	if p.thinkingBuilder == "" && p.thinkingSignature == "" {
		return
	}

	// A signature-bearing empty thought still needs a Messages thinking block;
	// append both pending fields before clearing them so that replay metadata
	// survives the flush.
	p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
		Type:      "thinking",
		Thinking:  p.thinkingBuilder,
		Signature: p.thinkingSignature,
	})
	p.thinkingBuilder = ""
	p.thinkingSignature = ""
}

// buildResponse returns assembled Messages content with identity, model, stop
// reason, and observed usage from a nonnil Gemini response. A supplied
// responseID wins over the upstream ID, with MessageID generating the final
// default.
//
// Any observed tool call selects tool_use; otherwise exact MAX_TOKENS selects
// max_tokens and other reasons select end_turn. Prompt tokens lose cached
// input, candidate/thought counts sum to output, and image count remains an
// overlapping output subset. Counts receive no nonnegative clamp. Content
// shares processor storage and decoded call arguments. Malformed-function
// reasons add diagnostics, with serialized candidate details included only
// when encoding succeeds.
func (p *GeminiToAnthropicResponseProcessor) buildResponse(geminiResp *GeminiResponse, responseID, originalModel string) *ClaudeResponse {
	var finishReason string
	if len(geminiResp.Candidates) > 0 {
		finishReason = geminiResp.Candidates[0].FinishReason
		if finishReason == "MALFORMED_FUNCTION_CALL" {
			p.addDiagnostic("[Antigravity] MALFORMED_FUNCTION_CALL detected in response for model %s", originalModel)
			if geminiResp.Candidates[0].Content != nil {
				// Candidate serialization is supplemental diagnostic detail; encoding
				// failure skips that detail while the MALFORMED_FUNCTION_CALL diagnostic
				// and converted answer remain available.
				if b, err := json.Marshal(geminiResp.Candidates[0].Content); err == nil {
					p.addDiagnostic("[Antigravity] Malformed content: %s", string(b))
				}
			}
		}
	}

	// Tool presence takes precedence over length exhaustion because the assembled
	// answer contains an invocation that needs the client to provide a result.
	stopReason := "end_turn"
	if p.hasToolCall {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}

	// Subtract cached prompt tokens from Messages input and include thinking in
	// output; image tokens remain a candidate subset used separately for image
	// accounting.
	usage := ClaudeUsage{}
	if geminiResp.UsageMetadata != nil {
		cached := geminiResp.UsageMetadata.CachedContentTokenCount
		usage.InputTokens = geminiResp.UsageMetadata.PromptTokenCount - cached
		usage.OutputTokens = geminiResp.UsageMetadata.CandidatesTokenCount + geminiResp.UsageMetadata.ThoughtsTokenCount
		usage.CacheReadInputTokens = cached
		usage.ImageOutputTokens = geminiResp.UsageMetadata.ImageOutputTokens()
	}

	// Wrapper identity supplied by the adapter wins, then direct response
	// identity, then generated Messages identity, allowing both envelope and
	// direct formats to use this converter.
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

// buildGroundingText returns a Markdown footer containing search queries and
// web-source links, or empty text when no usable metadata exists. Query labels
// are joined with commas, and chunks lacking Web data contribute no link.
//
// Links retain original chunk indexes converted to one-based labels: a non-web
// first chunk followed by a web chunk yields [2]. Titles and URIs are trimmed,
// with blank results becoming Source and # respectively. Query strings retain
// their supplied spelling. This formatting carries source positions into the
// displayed footer and performs no Markdown escaping or URL validation.
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
			// Keep each web chunk in the displayed source list even when its title or
			// URI is blank, using printable label/link defaults after trimming.
			title := strings.TrimSpace(chunk.Web.Title)
			if title == "" {
				title = "Source"
			}
			uri := strings.TrimSpace(chunk.Web.URI)
			if uri == "" {
				uri = "#"
			}
			// Number by original chunk position, so skipped non-web chunks leave gaps
			// instead of changing the displayed identity of later web sources.
			links = append(links, fmt.Sprintf("[%d] [%s](%s)", i+1, title, uri))
		}

		if len(links) > 0 {
			_, _ = builder.WriteString("\n\nSources:\n")
			_, _ = builder.WriteString(strings.Join(links, "\n"))
		}
	}

	return builder.String()
}

// TakeDiagnostics returns the pending diagnostic slice and clears the
// processor's reference for subsequent collection. The returned slice is
// transferred directly without copying, and an empty buffer returns nil.
func (p *GeminiToAnthropicResponseProcessor) TakeDiagnostics() []string {
	out := p.diagnostics
	p.diagnostics = nil
	return out
}

// addDiagnostic appends a formatted conversion message to the pending
// diagnostic slice.
func (p *GeminiToAnthropicResponseProcessor) addDiagnostic(format string, args ...any) {
	p.diagnostics = append(p.diagnostics, fmt.Sprintf(format, args...))
}
