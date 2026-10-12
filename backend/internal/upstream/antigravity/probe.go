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

// TestConnectionResult contains probe text and the model used.
type TestConnectionResult struct {
	Text        string // response text
	MappedModel string // model used by the probe
}

// BuildGeminiTestRequest builds a one-output-token Gemini probe.
// The first prompt is used when nonblank; an omitted or blank first prompt becomes ".".
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
		// Include the identity instruction required by the Antigravity request format.
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": GetDefaultIdentityPatch()},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 1,
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	return WrapV1InternalRequest(projectID, model, payloadBytes)
}

// BuildClaudeTestRequest converts a one-token Claude probe to Gemini format.
// The first prompt is used when nonblank; otherwise it defaults to ".".
func BuildClaudeTestRequest(projectID, mappedModel string, prompts ...string) ([]byte, error) {
	prompt := "."
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		prompt = strings.TrimSpace(prompts[0])
	}
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

// ExtractTextFromSSEResponse concatenates text from the first candidate in each JSON line.
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

		var data map[string]any
		if err := json.Unmarshal(line, &data); err != nil {
			continue
		}

		// Accept both v1internal envelopes and direct Gemini payloads.
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

// Probe runs provider-local retries and extracts a bounded response for connection testing.
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
	body, err := io.ReadAll(io.LimitReader(result.Resp.Body, limit()))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if result.Resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API returned %d: %s", result.Resp.StatusCode, string(body))
	}
	return &TestConnectionResult{Text: ExtractTextFromSSEResponse(body), MappedModel: model}, nil
}
