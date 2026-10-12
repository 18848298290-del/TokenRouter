package gemini

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// geminiAIStudioActions restricts the operations embedded in model URLs.
// It covers generation and token counting for native and compatibility requests.
var geminiAIStudioActions = map[string]struct{}{
	"generateContent":       {},
	"streamGenerateContent": {},
	"countTokens":           {},
}

// BuildGeminiAIStudioModelActionURL builds an AI Studio
// /v1beta/models/{model}:{action} URL.
//
// Model names can originate in client paths or bodies and pass through group mapping.
// Validate them with upstream.ValidateUpstreamPathSegment before constructing the URL.
// Use this builder for AI Studio model-action endpoints.
func BuildGeminiAIStudioModelActionURL(baseURL, model, action string, stream bool) (string, error) {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBase == "" {
		return "", errors.New("gemini base url is required")
	}
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return "", errors.New("gemini model is required")
	}
	if err := upstream.ValidateUpstreamPathSegment("gemini model", trimmedModel); err != nil {
		return "", err
	}
	trimmedAction := strings.TrimSpace(action)
	if _, ok := geminiAIStudioActions[trimmedAction]; !ok {
		return "", fmt.Errorf("unsupported gemini action: %s", trimmedAction)
	}

	fullURL := fmt.Sprintf("%s/v1beta/models/%s:%s", trimmedBase, trimmedModel, trimmedAction)
	if stream {
		fullURL += "?alt=sse"
	}
	return fullURL, nil
}

// IsSafeGeminiModelPathSegment reports whether a parsed model name is safe for a URL path.
// Handlers can reject unsafe names before forwarding.
func IsSafeGeminiModelPathSegment(model string) bool {
	return upstream.IsSafeUpstreamPathSegment(strings.TrimSpace(model))
}
