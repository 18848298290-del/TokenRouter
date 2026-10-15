package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
	"github.com/TokenFlux/TokenRouter/internal/upstream/internal/googleauth"
)

// service_account.go parses Vertex service-account credentials and builds
// Gemini and Claude request URLs and bodies.
// Vertex endpoints encode project, location, and publisher model identity in
// their paths. Credential parsing pins token exchange to Google, while
// separate Gemini and Claude builders retain their different action names,
// date syntax, and body version requirements.

const (
	// DefaultLocation selects blank locations, DefaultTokenURL pins token
	// exchange, and AnthropicVersion chooses the Claude request body dialect.
	DefaultLocation  = "us-central1"
	DefaultTokenURL  = "https://oauth2.googleapis.com/token"
	AnthropicVersion = "vertex-2023-10-16"
)

var (
	// Vertex location and date patterns constrain endpoint construction and
	// recognize model suffixes before the Claude date separator changes.
	vertexLocationPattern                = regexp.MustCompile(`^[a-z0-9-]+$`)
	vertexAnthropicDatedModelIDPattern   = regexp.MustCompile(`^(.+)-([0-9]{8})$`)
	vertexAnthropicAlreadyDatedIDPattern = regexp.MustCompile(`^.+@[0-9]{8}$`)
)

// ParseVertexServiceAccountJSON decodes credentials and requires nonblank
// client_email, private_key, and project_id. It returns their original
// strings, replaces token_uri with DefaultTokenURL, and leaves RSA key parsing
// to the token exchange.
func ParseVertexServiceAccountJSON(raw []byte) (*google.ServiceAccountKey, error) {
	var key google.ServiceAccountKey
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("invalid service account json: %w", err)
	}
	if strings.TrimSpace(key.ClientEmail) == "" {
		return nil, errors.New("service account json missing client_email")
	}
	if strings.TrimSpace(key.PrivateKey) == "" {
		return nil, errors.New("service account json missing private_key")
	}
	if strings.TrimSpace(key.ProjectID) == "" {
		return nil, errors.New("service account json missing project_id")
	}
	// Use the fixed token endpoint for every parsed credential so a supplied
	// token_uri cannot redirect the signed assertion to another service.
	key.TokenURI = DefaultTokenURL
	return &key, nil
}

// ServiceAccountProjectID returns the stored project after service-account
// JSON validation. Missing credential fields or invalid JSON return the
// parsing error, and the project string retains its supplied whitespace.
func ServiceAccountProjectID(raw []byte) (string, error) {
	key, err := ParseVertexServiceAccountJSON(raw)
	if err != nil {
		return "", err
	}
	return key.ProjectID, nil
}

// BuildVertexGeminiURL returns an escaped publisher-model URL for generation
// or token counting. Blank location uses DefaultLocation; global selects the
// unprefixed host, and stream adds alt=sse independently of the supplied
// action.
func BuildVertexGeminiURL(projectID, location, model, action string, stream bool) (string, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	model = strings.TrimSpace(model)
	action = strings.TrimSpace(action)
	if projectID == "" {
		return "", errors.New("vertex project_id is required")
	}
	if location == "" {
		location = DefaultLocation
	}
	if !vertexLocationPattern.MatchString(location) {
		return "", fmt.Errorf("invalid vertex location: %s", location)
	}
	if model == "" {
		return "", errors.New("vertex model is required")
	}
	switch action {
	case "generateContent", "streamGenerateContent", "countTokens":
	default:
		return "", fmt.Errorf("unsupported vertex gemini action: %s", action)
	}
	// Global requests use the common API hostname while keeping global in the
	// resource path; regional requests use the location-prefixed hostname.
	host := fmt.Sprintf("%s-aiplatform.googleapis.com", location)
	if location == "global" {
		host = "aiplatform.googleapis.com"
	}
	u := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/google/models/%s:%s",
		host,
		url.PathEscape(projectID),
		url.PathEscape(location),
		url.PathEscape(model),
		action,
	)
	if stream {
		u += "?alt=sse"
	}
	return u, nil
}

// BuildVertexAnthropicURL returns the escaped Claude publisher URL with
// rawPredict or streamRawPredict. Blank location uses DefaultLocation, global
// selects the common API host, and an already normalized model keeps its date
// separator.
func BuildVertexAnthropicURL(projectID, location, model string, stream bool) (string, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	model = strings.TrimSpace(model)
	if projectID == "" {
		return "", errors.New("vertex project_id is required")
	}
	if location == "" {
		location = DefaultLocation
	}
	if !vertexLocationPattern.MatchString(location) {
		return "", fmt.Errorf("invalid vertex location: %s", location)
	}
	if model == "" {
		return "", errors.New("vertex model is required")
	}
	action := "rawPredict"
	if stream {
		action = "streamRawPredict"
	}
	// Global requests use the common API hostname while keeping global in the
	// resource path; regional requests use the location-prefixed hostname.
	host := fmt.Sprintf("%s-aiplatform.googleapis.com", location)
	if location == "global" {
		host = "aiplatform.googleapis.com"
	}
	// Restore the literal date separator after path escaping so a dated Claude
	// model uses the Vertex resource identity expected by rawPredict.
	escapedModel := strings.ReplaceAll(url.PathEscape(model), "%40", "@")
	return fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
		host,
		url.PathEscape(projectID),
		url.PathEscape(location),
		escapedModel,
		action,
	), nil
}

// NormalizeVertexAnthropicModelID trims a Claude model and converts a trailing
// eight-digit hyphen date to an at-sign date. Already dated identifiers and
// undated names pass through after trimming; it never invents a model date.
func NormalizeVertexAnthropicModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || vertexAnthropicAlreadyDatedIDPattern.MatchString(model) {
		return model
	}
	// Translate a supplied terminal date into Vertex syntax while retaining the
	// model prefix; authorization and forwarding callers resolve the actual model
	// separately.
	if m := vertexAnthropicDatedModelIDPattern.FindStringSubmatch(model); len(m) == 3 {
		return m[1] + "@" + m[2]
	}
	return model
}

// BuildVertexAnthropicRequestBody reencodes an Anthropic object for Vertex by
// removing model and setting AnthropicVersion. Callers supply a nonnull JSON
// object; ordinary map decoding gives numeric values float64 representation,
// and decoding or encoding errors return to the caller.
func BuildVertexAnthropicRequestBody(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse anthropic vertex request body: %w", err)
	}
	// The model is already carried by the Vertex resource URL. Remove the
	// duplicate body field and select its required Anthropic version together.
	delete(payload, "model")
	payload["anthropic_version"] = AnthropicVersion
	return json.Marshal(payload)
}

// ExchangeServiceAccountToken delegates RSA assertion signing and exchange to
// googleauth using a nonnil parsed key and the supplied proxy. Provider
// orchestration uses its cache duration and owns token caching and refresh
// locks.
// @project-doc docs/interfaces/gemini_upstream.md#vertex_service_account_execution
func ExchangeServiceAccountToken(ctx context.Context, key *google.ServiceAccountKey, proxyURL string) (string, time.Duration, error) {
	return googleauth.ExchangeServiceAccountToken(ctx, key, proxyURL)
}
