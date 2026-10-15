package gemini

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// url.go builds validated AI Studio model-action URLs for generation,
// streaming, and token counting.
// AI Studio model actions share the v1beta resource path while selecting
// generation, streaming generation, or token counting. Model names can
// originate in client input or mapping, so the builder validates their trimmed
// path segment before appending it to the caller-validated base URL.

// geminiAIStudioActions is the operation allowlist used before model URL
// construction, covering both generation actions and token counting.
var geminiAIStudioActions = map[string]struct{}{
	"generateContent":       {},
	"streamGenerateContent": {},
	"countTokens":           {},
}

// BuildGeminiAIStudioModelActionURL constructs a validated model-action path
// under the supplied base URL. It trims base, model, and action; callers
// validate base URL policy, while this builder rejects unsafe model segments
// and unsupported actions. stream adds alt=sse independently of action.
func BuildGeminiAIStudioModelActionURL(baseURL, model, action string, stream bool) (string, error) {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBase == "" {
		return "", errors.New("gemini base url is required")
	}
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return "", errors.New("gemini model is required")
	}
	// Mapped or client-supplied model text becomes one URL segment. Validate it
	// before concatenation so navigation and URL delimiters cannot select a
	// different upstream resource.
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

// IsSafeGeminiModelPathSegment reports whether the trimmed model passes the
// shared upstream segment allowlist. Empty, oversized, dot-only, or
// delimiter-bearing values fail, allowing handlers to reject those names
// before constructing a request.
func IsSafeGeminiModelPathSegment(model string) bool {
	return upstream.IsSafeUpstreamPathSegment(strings.TrimSpace(model))
}
