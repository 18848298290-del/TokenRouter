package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// AntigravityGemini31ProAgentModel is the upstream route for Gemini 3.1 Pro High.
const AntigravityGemini31ProAgentModel = "gemini-pro-agent"

var (
	// AntigravityPassthroughErrorMessages lists lowercase messages safe to expose to clients.
	// Matching uses substring containment.
	AntigravityPassthroughErrorMessages = []string{
		"prompt is too long",
	}

	ErrProjectIDRequired = errors.New("this standard-tier Antigravity provider requires project_id")

	// DefaultAntigravityModelMapping maps public model names to internal routes.
	DefaultAntigravityModelMapping = map[string]string{
		"gemini-3.1-pro-high": AntigravityGemini31ProAgentModel,
	}
)

// PromptTooLongError carries an upstream prompt-length rejection.
type PromptTooLongError struct {
	StatusCode int
	RequestID  string
	Body       []byte
}

func (e *PromptTooLongError) Error() string {
	return fmt.Sprintf("prompt too long: status=%d", e.StatusCode)
}

// ApplyThinkingModelSuffix selects the thinking variant of Claude Sonnet 4.5 when enabled.
// Other models retain their mapped name.
func ApplyThinkingModelSuffix(mappedModel string, thinkingEnabled bool) string {
	if !thinkingEnabled {
		return mappedModel
	}
	if mappedModel == "claude-sonnet-4-5" {
		return "claude-sonnet-4-5-thinking"
	}
	return mappedModel
}

// InjectIdentityPatchToGeminiRequest prepends the default identity instruction.
// A systemInstruction containing "You are Antigravity" leaves the body unchanged.
func InjectIdentityPatchToGeminiRequest(body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("parse Gemini request: %w", err)
	}

	if sysInst, ok := request["systemInstruction"].(map[string]any); ok {
		if parts, ok := sysInst["parts"].([]any); ok {
			for _, part := range parts {
				if partMap, ok := part.(map[string]any); ok {
					if text, ok := partMap["text"].(string); ok {
						if strings.Contains(text, "You are Antigravity") {
							return body, nil
						}
					}
				}
			}
		}
	}

	identityPatch := GetDefaultIdentityPatch()

	newPart := map[string]any{"text": identityPatch}

	if existing, ok := request["systemInstruction"].(map[string]any); ok {
		if parts, ok := existing["parts"].([]any); ok {
			existing["parts"] = append([]any{newPart}, parts...)
		} else {
			existing["parts"] = []any{newPart}
		}
	} else {
		request["systemInstruction"] = map[string]any{
			"parts": []any{newPart},
		}
	}

	return json.Marshal(request)
}

// WrapV1InternalRequest adds routing and identity fields; projectID must be nonempty.
func WrapV1InternalRequest(projectID, model string, originalBody []byte) ([]byte, error) {
	var request any
	if err := json.Unmarshal(originalBody, &request); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, ErrProjectIDRequired
	}

	wrapped := map[string]any{
		"project":     projectID,
		"requestId":   "agent-" + uuid.New().String(),
		"userAgent":   "antigravity", // fixed protocol identity
		"requestType": "agent",
		"model":       model,
		"request":     request,
	}

	return json.Marshal(wrapped)
}

func IsSignatureRelatedError(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody)))
	if msg == "" {
		// Inspect the raw payload when the structured message is absent.
		msg = strings.ToLower(string(respBody))
	}

	// Accept both signature and thought_signature wording.
	if strings.Contains(msg, "thought_signature") || strings.Contains(msg, "signature") {
		return true
	}

	// Thinking block shape errors also require signature recovery:
	// "Expected `thinking` or `redacted_thinking`, but found `text`"
	if strings.Contains(msg, "expected") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		return true
	}

	return false
}

// IsPromptTooLongError detects length-limit and max_tokens error wording.
func IsPromptTooLongError(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody)))
	if msg == "" {
		msg = strings.ToLower(string(respBody))
	}
	return strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "request is too long") ||
		strings.Contains(msg, "context length exceeded") ||
		strings.Contains(msg, "max_tokens")
}

// IsPassthroughErrorMessage reports whether a message contains an allowed pattern.
func IsPassthroughErrorMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, pattern := range AntigravityPassthroughErrorMessages {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// GetPassthroughOrDefault returns an allowed upstream message or the supplied default.
func GetPassthroughOrDefault(upstreamMsg, defaultMsg string) string {
	if IsPassthroughErrorMessage(upstreamMsg) {
		return upstreamMsg
	}
	return defaultMsg
}

// CleanGeminiRequest cleans function parameter schemas in a Gemini request.
func CleanGeminiRequest(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	modified := false

	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		for _, t := range tools {
			toolMap, ok := t.(map[string]any)
			if !ok {
				continue
			}

			// Accept both JSON naming conventions for function declarations.
			var funcs []any
			if f, ok := toolMap["functionDeclarations"].([]any); ok {
				funcs = f
			} else if f, ok := toolMap["function_declarations"].([]any); ok {
				funcs = f
			}

			if len(funcs) == 0 {
				continue
			}

			for _, f := range funcs {
				funcMap, ok := f.(map[string]any)
				if !ok {
					continue
				}

				if params, ok := funcMap["parameters"].(map[string]any); ok {
					DeepCleanUndefined(params)
					cleaned := CleanJSONSchema(params)
					funcMap["parameters"] = cleaned
					modified = true
				}
			}
		}
	}

	if !modified {
		return body, nil
	}

	return json.Marshal(payload)
}

func PreserveChatCompletionTokenLimit(request *protocolopenai.ChatCompletionsRequest, claudeRequest *protocolanthropic.AnthropicRequest) {
	if request == nil || claudeRequest == nil {
		return
	}
	limit := request.MaxTokens
	if request.MaxCompletionTokens != nil {
		limit = request.MaxCompletionTokens
	}
	if limit != nil && *limit > 0 {
		claudeRequest.MaxTokens = *limit
	}
}

func EnableMixedGeminiToolInvocations(body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}

	var hasGoogleSearch, hasFunctionDeclarations bool
	if tools, ok := request["tools"].([]any); ok {
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			_, hasSearch := tool["googleSearch"]
			declarations, hasFunctions := tool["functionDeclarations"].([]any)
			hasGoogleSearch = hasGoogleSearch || hasSearch
			hasFunctionDeclarations = hasFunctionDeclarations || hasFunctions && len(declarations) > 0
		}
	}
	if !hasGoogleSearch || !hasFunctionDeclarations {
		return body, nil
	}

	toolConfig, _ := request["toolConfig"].(map[string]any)
	if toolConfig == nil {
		toolConfig = make(map[string]any)
		request["toolConfig"] = toolConfig
	}
	toolConfig["includeServerSideToolInvocations"] = true
	return json.Marshal(request)
}

// CleanJSONSchema converts a schema to the Gemini-supported form.
func CleanJSONSchema(schema map[string]any) map[string]any { return bridge.CleanJSONSchema(schema) }

// DeepCleanUndefined recursively removes object fields whose value is "[undefined]".
func DeepCleanUndefined(value any) { bridge.DeepCleanUndefined(value) }
