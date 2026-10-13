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

// AI Studio model reads use a caller-validated base and a bounded, validated
// resource suffix. API-key and OAuth modes select different headers, while the
// returned HTTP result retains upstream status and a challenge header so the
// caller can handle successful and failed resource reads.

// ModelGetOptions supplies credential and transport callbacks for one AI
// Studio model read.
type ModelGetOptions struct {
	Mode CredentialMode // selected upstream authentication mode
	// BaseURL, ValidateURL, Do, and FilterHeaders are required callbacks. APIKey
	// is required in API-key mode; OAuth mode requires Token and reports an error
	// when it is absent.
	BaseURL func() string // configured model API base
	// APIKey provides API-key credentials when Mode selects that authentication
	// path; callers supply it for that mode because the callback is invoked
	// directly.
	APIKey      func() string                // API-key credential source
	ValidateURL func(string) (string, error) // base URL policy validation
	// Token supplies OAuth credentials for OAuthCredential mode. A missing
	// callback returns a configuration error before the request can be sent.
	Token func(context.Context) (string, error) // OAuth access-token acquisition callback
	// Do supplies the exchange. ReadAIStudioModel closes successful bodies; the
	// callback cleans up any response it returns together with an error.
	Do func(*http.Request) (*http.Response, error)
	// FilterHeaders selects returned headers. Its result must be writable when
	// upstream sends a challenge, because ReadAIStudioModel restores that header.
	FilterHeaders func(http.Header) http.Header
	// Enter optionally registers the read with caller lifecycle tracking. A
	// successful entry returns a nonnil release callback, invoked after response
	// reading or any later failure.
	Enter func() (func(), error) // optional attempt registration callback
}

// HTTPResult carries a bounded upstream body, status, and selected response
// headers.
type HTTPResult struct {
	StatusCode int         // upstream HTTP status code
	Headers    http.Header // selected upstream response headers
	Body       []byte      // collected response body prefix
}

// Model represents a Gemini model resource and its supported generation
// actions.
type Model struct {
	Name        string `json:"name"`                  // model resource name
	DisplayName string `json:"displayName,omitempty"` // model display name
	Description string `json:"description,omitempty"` // model descriptive text
	// supported model action names
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
}

// ModelsListResponse wraps the ordered model resources returned by a Gemini
// catalogue.
type ModelsListResponse struct {
	Models []Model `json:"models"` // ordered model resource collection
}

// ReadAIStudioModel reads a validated model resource with the selected API-key
// or OAuth credential. Callers supply the callbacks required by
// ModelGetOptions; successful entry registration is released after response
// processing.
//
// It returns the upstream HTTP status with at most eight MiB of body,
// including non-success responses. Body-read errors retain the collected
// prefix, and Www-Authenticate is restored after header filtering; that filter
// must return a writable header map when a challenge exists.
func ReadAIStudioModel(ctx context.Context, path string, options ModelGetOptions) (*HTTPResult, error) {
	if options.Enter != nil {
		done, err := options.Enter()
		if err != nil {
			return nil, err
		}
		defer done()
	}
	// Validate every resource suffix segment before joining the selected base; an
	// invalid or empty suffix must fail rather than silently target the API root.
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

	// Collect at most eight MiB while retaining partial bytes after read failure.
	// The caller receives the status and this prefix to interpret the model
	// response.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	wwwAuthenticate := resp.Header.Get("Www-Authenticate")
	filteredHeaders := options.FilterHeaders(resp.Header)
	// Restore the authentication challenge after general header filtering so a
	// failed OAuth resource read retains the scope information needed by its
	// caller.
	if wwwAuthenticate != "" {
		filteredHeaders.Set("Www-Authenticate", wwwAuthenticate)
	}
	return &HTTPResult{
		StatusCode: resp.StatusCode,
		Headers:    filteredHeaders,
		Body:       body,
	}, nil
}
