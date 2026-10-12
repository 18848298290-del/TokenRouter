package gemini

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type ModelGetOptions struct {
	Mode          CredentialMode
	BaseURL       func() string
	APIKey        func() string
	ValidateURL   func(string) (string, error)
	Token         func(context.Context) (string, error)
	Do            func(*http.Request) (*http.Response, error)
	FilterHeaders func(http.Header) http.Header
	Enter         func() (func(), error)
}

type HTTPResult struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Model describes a model and its supported generation methods.
type Model struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	Description                string   `json:"description,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
}

// ModelsListResponse is the model list response body.
type ModelsListResponse struct {
	Models []Model `json:"models"`
}

func ReadAIStudioModel(ctx context.Context, path string, options ModelGetOptions) (*HTTPResult, error) {
	if options.Enter != nil {
		done, err := options.Enter()
		if err != nil {
			return nil, err
		}
		defer done()
	}
	// Validate each path segment before appending the path to the upstream base URL.
	// See upstream.SanitizedUpstreamPathSuffix.
	sanitizedPath, ok := upstream.SanitizedUpstreamPathSuffix(path)
	if !ok || sanitizedPath == "" {
		return nil, errors.New("invalid path")
	}
	path = sanitizedPath

	baseURL := options.BaseURL()
	normalizedBaseURL, err := options.ValidateURL(baseURL)
	if err != nil {
		return nil, err
	}
	fullURL := strings.TrimRight(normalizedBaseURL, "/") + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}

	switch options.Mode {
	case APIKeyCredential:
		apiKey := strings.TrimSpace(options.APIKey())
		if apiKey == "" {
			return nil, errors.New("gemini api_key not configured")
		}
		req.Header.Set("x-goog-api-key", apiKey)
	case OAuthCredential:
		if options.Token == nil {
			return nil, errors.New("gemini token provider not configured")
		}
		accessToken, err := options.Token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", options.Mode)
	}

	resp, err := options.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	wwwAuthenticate := resp.Header.Get("Www-Authenticate")
	filteredHeaders := options.FilterHeaders(resp.Header)
	if wwwAuthenticate != "" {
		filteredHeaders.Set("Www-Authenticate", wwwAuthenticate)
	}
	return &HTTPResult{
		StatusCode: resp.StatusCode,
		Headers:    filteredHeaders,
		Body:       body,
	}, nil
}
