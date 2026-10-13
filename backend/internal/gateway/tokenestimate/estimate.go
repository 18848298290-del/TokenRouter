package tokenestimate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiktoken-go/tokenizer"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// Local input estimates count serialized text with a selected tiktoken
// encoding when an upstream count endpoint is unavailable. Responses messages
// add fixed item and part overheads, while tools and unfamiliar JSON shapes
// contribute their compact serialized representation. Anthropic requests first
// use the protocol bridge to obtain those Responses inputs.
//
// These totals are estimates of request content, including metadata such as
// roles and call identifiers. Image URLs contribute text; a data URL
// contributes its header before the comma instead of its encoded payload.
// Instructions and structured item text are trimmed, while a plain string
// input is counted as decoded. Model-prefix rules choose between two
// encodings, with unfamiliar names using o200k_base; Anthropic additionally
// requires a positive minimum.

// These local overheads approximate Responses item and content framing; the
// Anthropic counting path applies a positive minimum after summing estimated
// content.
const (
	openAIResponsesInputItemTokenOverhead = 3 // token overhead per input item
	openAIResponsesContentPartOverhead    = 1 // token overhead per content part
	openAIInputTokensFallbackMinimum      = 1 // minimum Anthropic input token estimate
)

// Request contains the Responses input portions counted by the local
// estimator.
type Request struct {
	Model        string                         `json:"model"`                  // model name selecting tokenizer encoding
	Instructions string                         `json:"instructions,omitempty"` // instructions counted after trimming whitespace
	Input        json.RawMessage                `json:"input,omitempty"`        // JSON input string or items
	Tools        []protocolopenai.ResponsesTool `json:"tools,omitempty"`        // serialized tool definition inputs
	ToolChoice   json.RawMessage                `json:"tool_choice,omitempty"`  // JSON tool selection input
}

// Anthropic estimates request input by converting valid Anthropic JSON into
// Responses fields and invoking the local tokenizer estimate. Missing models
// and conversion failures return an error; a completed estimate below one
// becomes one.
func Anthropic(body []byte) (int, error) {
	var anthropicReq protocolanthropic.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return 0, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}
	if strings.TrimSpace(anthropicReq.Model) == "" {
		return 0, fmt.Errorf("parse anthropic count_tokens request: model is required")
	}

	responsesReq, err := bridge.AnthropicToResponses(&anthropicReq, bridge.RequestOptions{SupportsMaxEffort: true})
	if err != nil {
		return 0, fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	estimated, err := Responses(Request{
		Model:        anthropicReq.Model,
		Instructions: responsesReq.Instructions,
		Input:        responsesReq.Input,
		Tools:        responsesReq.Tools,
		ToolChoice:   responsesReq.ToolChoice,
	})
	if err != nil {
		return 0, fmt.Errorf("estimate input tokens: %w", err)
	}
	// Represent an accepted empty conversation with a positive estimate.
	if estimated < openAIInputTokensFallbackMinimum {
		estimated = openAIInputTokensFallbackMinimum
	}
	return estimated, nil
}

// Responses sums estimated tokens for instructions, input, tool definitions,
// and tool choice using the model-selected codec. Tools use their wire JSON,
// nonempty tool choice must be valid JSON, and tokenizer or encoding errors
// stop the estimate.
//
// Structured input adds fixed framing costs and counts its recognized text and
// identity fields, while other valid JSON is compacted before counting.
// Instructions and serialized additions are trimmed before tokenization. The
// result is a local approximation rather than an upstream usage observation; a
// negative final accumulator returns zero, with no broader overflow guarantee.
func Responses(req Request) (int, error) {
	codec, err := openAIInputTokensCodecForModel(req.Model)
	if err != nil {
		return 0, err
	}

	total := 0
	addCount := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	if err := addCount(req.Instructions); err != nil {
		return 0, err
	}
	inputTokens, err := estimateOpenAIInputTokensForInput(codec, req.Input)
	if err != nil {
		return 0, err
	}
	total += inputTokens

	for _, tool := range req.Tools {
		raw, err := wirejson.Marshal(tool)
		if err != nil {
			return 0, err
		}
		if err := addCount(string(raw)); err != nil {
			return 0, err
		}
	}
	if len(req.ToolChoice) > 0 {
		compacted, err := compactOpenAIInputTokensJSON(req.ToolChoice)
		if err != nil {
			return 0, err
		}
		if err := addCount(compacted); err != nil {
			return 0, err
		}
	}

	// Suppress a negative final accumulator as zero so callers do not report
	// a negative estimate; earlier positive overflow cases have no separate check here.
	if total < 0 {
		return 0, nil
	}
	return total, nil
}

// estimateOpenAIInputTokensForInput selects the counting representation for
// one raw input value. Blank input contributes zero, a decoded string is
// counted directly, and a decoded input-item array uses the structured
// estimator.
//
// When neither recognized shape decodes, compact valid JSON contributes its
// textual token count. Invalid JSON returns the compaction error. The
// plain-string path retains its decoded whitespace, whereas item text is
// trimmed by its own counting helper; the representation therefore determines
// both framing overhead and the whitespace counted.
func estimateOpenAIInputTokensForInput(codec tokenizer.Codec, raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}

	var plainText string
	if err := json.Unmarshal(raw, &plainText); err == nil {
		return codec.Count(plainText)
	}

	var items []protocolopenai.ResponsesInputItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return estimateOpenAIInputTokensForInputItems(codec, items)
	}

	// Count unfamiliar valid JSON in its compact textual form so accepted input
	// shapes still contribute content while malformed JSON remains a reported error.
	compacted, err := compactOpenAIInputTokensJSON(raw)
	if err != nil {
		return 0, err
	}
	return codec.Count(compacted)
}

// estimateOpenAIInputTokensForInputItems sums fixed item overhead with
// recognized metadata and content text. Roles, non-message types, names,
// arguments, outputs, and call or item identifiers contribute trimmed text
// under the supplied codec.
//
// String content adds text directly; decoded part arrays additionally add each
// part's overhead and choose text, image URL header, or an unfamiliar type
// label. Other valid content JSON is compacted and counted. These
// representation choices retain a deterministic local approximation for tool
// and multimodal inputs, while tokenizer or JSON errors end the estimate.
func estimateOpenAIInputTokensForInputItems(codec tokenizer.Codec, items []protocolopenai.ResponsesInputItem) (int, error) {
	total := 0
	countText := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	for _, item := range items {
		// Add framing cost even when an input item has no countable text.
		total += openAIResponsesInputItemTokenOverhead
		if err := countText(item.Role); err != nil {
			return 0, err
		}
		if item.Type != "" && item.Type != "message" {
			if err := countText(item.Type); err != nil {
				return 0, err
			}
		}
		if err := countText(item.Name); err != nil {
			return 0, err
		}
		if err := countText(item.Arguments); err != nil {
			return 0, err
		}
		if err := countText(item.Output); err != nil {
			return 0, err
		}
		if err := countText(item.CallID); err != nil {
			return 0, err
		}
		if err := countText(item.ID); err != nil {
			return 0, err
		}

		if len(bytes.TrimSpace(item.Content)) == 0 {
			continue
		}

		var contentText string
		if err := json.Unmarshal(item.Content, &contentText); err == nil {
			if err := countText(contentText); err != nil {
				return 0, err
			}
			continue
		}

		var parts []protocolopenai.ResponsesContentPart
		if err := json.Unmarshal(item.Content, &parts); err == nil {
			for _, part := range parts {
				// Count each part's framing before its recognized textual contribution.
				total += openAIResponsesContentPartOverhead
				switch part.Type {
				case "input_text", "output_text", "text":
					if err := countText(part.Text); err != nil {
						return 0, err
					}
				case "input_image":
					if err := countText(estimateOpenAIInputImageText(part.ImageURL)); err != nil {
						return 0, err
					}
				default:
					if err := countText(part.Type); err != nil {
						return 0, err
					}
				}
			}
			continue
		}

		// Retain unfamiliar valid content as compact JSON text; dropping the whole
		// value would omit its contribution merely because it uses another shape.
		compacted, err := compactOpenAIInputTokensJSON(item.Content)
		if err != nil {
			return 0, err
		}
		if err := countText(compacted); err != nil {
			return 0, err
		}
	}

	return total, nil
}

// estimateOpenAIInputImageText returns trimmed URL text for image estimation.
// A data URL with a comma contributes its header alone; ordinary URLs and
// comma-free data values retain their trimmed text.
func estimateOpenAIInputImageText(imageURL string) string {
	trimmed := strings.TrimSpace(imageURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		// Exclude the encoded payload from this text approximation.
		if comma := strings.Index(trimmed, ","); comma > 0 {
			return trimmed[:comma]
		}
	}
	return trimmed
}

// compactOpenAIInputTokensJSON returns valid JSON with insignificant
// whitespace removed for textual counting. Blank input returns an empty string
// successfully; nonblank invalid JSON returns the decoder's compaction error.
func compactOpenAIInputTokensJSON(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// openAIInputTokensCodecForModel loads the codec chosen by the estimator's
// model-prefix rules. EncodingForModel supplies one of its two supported
// encodings, and tokenizer construction errors propagate to the caller.
func openAIInputTokensCodecForModel(model string) (tokenizer.Codec, error) {
	switch EncodingForModel(model) {
	case tokenizer.Cl100kBase:
		return tokenizer.Get(tokenizer.Cl100kBase)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

// EncodingForModel chooses cl100k_base for GPT-3.5, older GPT-4 names, and
// text embeddings after trimming and lowercasing. GPT-4o, GPT-4.1, and every
// other prefix use o200k_base, including unknown model names.
func EncodingForModel(model string) tokenizer.Encoding {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gpt-3.5"),
		strings.HasPrefix(normalized, "gpt-4") &&
			!strings.HasPrefix(normalized, "gpt-4o") &&
			!strings.HasPrefix(normalized, "gpt-4.1"),
		strings.HasPrefix(normalized, "text-embedding-"):
		return tokenizer.Cl100kBase
	default:
		return tokenizer.O200kBase
	}
}
