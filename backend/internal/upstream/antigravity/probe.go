package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// Antigravity probes send minimal Claude or Gemini data through the same
// project envelope and provider-local retry adapter as forwarding. A nonblank
// first supplied prompt selects probe text, and response extraction joins
// first-candidate text from individual JSON/SSE lines.
//
// Optional lifecycle registration remains held through response closing, while
// the caller read limit retains a bounded prefix. Truncation can succeed
// without an oversize error, and extraction skips undecodable lines. Provider
// switching becomes a fixed test message; the probe owner decides health
// updates and model qualification from this technical result.

// TestConnectionResult holds extracted probe text and the caller-selected
// mapped model identity.
type TestConnectionResult struct {
	Text        string // extracted probe response text
	MappedModel string // model used by the probe
}

// BuildGeminiTestRequest wraps a one-output-token Gemini probe with its
// identity instruction. A blank or absent first prompt uses a dot, later
// prompts are ignored, and wrapping requires a trimmed project.
func BuildGeminiTestRequest(projectID, model string, prompts ...string) ([]byte, error) {
	prompt := "."
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		prompt = strings.TrimSpace(prompts[0])
	}
	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": prompt},
				},
			},
		},
		// Include the default platform identity before the minimal probe contents so
		// probe conversion uses the instruction selected by ordinary forwarding.
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": GetDefaultIdentityPatch()},
			},
		},
		"generationConfig": map[string]any{
			// One output token bounds generation requested by connection probes.
			"maxOutputTokens": 1,
		},
	}
	// Fixed maps and string/numeric values form this probe; encoding cannot
	// encounter unsupported values or a reference cycle before project wrapping.
	payloadBytes, _ := json.Marshal(payload)
	return WrapV1InternalRequest(projectID, model, payloadBytes)
}

// BuildClaudeTestRequest converts a one-token Claude probe using default
// transform options and mappedModel. It trims the first prompt or uses a dot;
// later prompt values do not participate.
func BuildClaudeTestRequest(projectID, mappedModel string, prompts ...string) ([]byte, error) {
	prompt := "."
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		prompt = strings.TrimSpace(prompts[0])
	}
	// The prompt is one string, so JSON encoding supplies escaping for the
	// Messages content without an unsupported-value or reference-cycle error.
	promptJSON, _ := json.Marshal(prompt)
	claudeReq := &protocolanthropic.ClaudeRequest{
		Model: mappedModel,
		Messages: []protocolanthropic.ClaudeMessage{
			{
				Role:    "user",
				Content: json.RawMessage(promptJSON),
			},
		},
		MaxTokens: 1,
		Stream:    false,
	}
	return TransformClaudeToGemini(claudeReq, projectID, mappedModel)
}

// ExtractTextFromSSEResponse joins first-candidate text from independently
// decoded response lines. It accepts optional data prefixes and object
// response wrappers; malformed, nonobject, and structurally incomplete lines
// are skipped.
//
// Only the first candidate contributes parts, in source line and part order.
// Thought text participates because extraction checks the text field without
// excluding the thought flag. A wrapper that is not an object leaves
// extraction on the direct object, and a body containing no selected text
// returns an empty string.
func ExtractTextFromSSEResponse(respBody []byte) string {
	var texts []string
	lines := bytes.SplitSeq(respBody, []byte("\n"))

	for line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		if after, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			line = after
			line = bytes.TrimSpace(line)
		}

		if len(line) == 0 || line[0] != '{' {
			continue
		}

		// Decode each line independently; a truncated or malformed line is skipped
		// while text already collected from earlier complete lines remains available.
		var data map[string]any
		if err := json.Unmarshal(line, &data); err != nil {
			continue
		}

		// Use an object response wrapper when present and otherwise inspect the
		// direct object, letting local fixtures and platform streams share
		// first-candidate extraction.
		response, ok := data["response"].(map[string]any)
		if !ok {
			response = data
		}

		candidates, ok := response["candidates"].([]any)
		if !ok || len(candidates) == 0 {
			continue
		}

		candidate, ok := candidates[0].(map[string]any)
		if !ok {
			continue
		}

		content, ok := candidate["content"].(map[string]any)
		if !ok {
			continue
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}

		for _, part := range parts {
			if partMap, ok := part.(map[string]any); ok {
				if text, ok := partMap["text"].(string); ok && text != "" {
					texts = append(texts, text)
				}
			}
		}
	}

	return strings.Join(texts, "")
}

// Probe runs provider-local retries using ctx and extracts text from a
// caller-bounded response prefix. Optional enter returns a nonnil release
// callback held through response closing; callers supply limit and applicable
// retry callbacks.
//
// Callers return a positive read limit to bound retained bytes; the reader
// accepts truncation without detecting oversize.
// Read and HTTP failures return errors, while provider switching becomes a
// fixed connection-test message. Successful text can be empty when lines are
// malformed or nontext. The caller model remains the requested probe identity
// beside those observations.
func Probe(ctx context.Context, input RetryInput, options RetryOptions, model string, limit func() int64, enter func() (func(), error)) (*TestConnectionResult, error) {
	if enter != nil {
		done, err := enter()
		if err != nil {
			return nil, err
		}
		defer done()
	}
	input.Ctx = ctx
	result, err := (&RetryAdapter{Options: options}).AntigravityRetryLoop(input)
	if err != nil {
		if switchErr, ok := errors.AsType[*AntigravityProviderSwitchError](err); ok {
			return nil, fmt.Errorf("provider model %s is rate limited; try again later", switchErr.RateLimitedModel)
		}
		return nil, err
	}
	if result == nil || result.Resp == nil {
		return nil, errors.New("upstream returned empty response")
	}
	defer func() { _ = result.Resp.Body.Close() }()
	// Read only the caller-selected prefix while lifecycle registration and
	// response ownership remain active; a truncated successful response can still
	// yield partial probe text.
	body, err := io.ReadAll(io.LimitReader(result.Resp.Body, limit()))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if result.Resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API returned %d: %s", result.Resp.StatusCode, string(body))
	}
	return &TestConnectionResult{Text: ExtractTextFromSSEResponse(body), MappedModel: model}, nil
}
