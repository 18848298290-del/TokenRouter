package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

// images.go builds and sends Gemini image generation requests and selects a
// decoded image result.
// Creative Gemini image requests embed reference bytes in generation parts and
// select API-key, OAuth, or service-account endpoints from caller options.
// Project-bearing OAuth adds the Code Assist envelope, while service accounts
// obtain their already resolved model URL from the caller and other modes use
// a validated REST base.
//
// The request is serialized before authentication, and provider header
// handling runs after credential headers are applied. Response reads retain
// at most sixty-four MiB through the shared limiter, which returns a prefix
// successfully when that limit is reached. Image parsing walks the first
// candidate and keeps the last decodable inline part as one zero-indexed
// result; earlier decoded images and other candidates are omitted by this
// creative-result selection rule.

// ImageRequestInput supplies prompt text, generation controls, and decoded
// reference-image bytes.
type ImageRequestInput struct {
	// Prompt supplies user text; ImageSize selects size; AspectRatio selects
	// shape; ThinkingLevel selects reasoning.
	Prompt, ImageSize, AspectRatio, ThinkingLevel string
	// decoded inline reference images
	Sources []upstream.DecodedImage
}

// ImageOptions supplies image endpoint selection, credentials, transport, and
// failure callbacks.
type ImageOptions struct {
	Mode CredentialMode // selected image credential family
	// Model identifies the upstream model; ProjectID identifies its Code Assist
	// project.
	Model, ProjectID string
	// APIKey is required for API-key mode and is read during endpoint validation
	// and again during header authentication; callers supply a consistent
	// credential across those reads.
	APIKey func() string // API-key credential source callback
	// bearer access-token source callback
	// Token supplies bearer credentials for every mode except API-key mode. A
	// missing callback is an input error; acquisition failures pass through
	// HTTPError.
	Token func(context.Context) (string, error)
	// BaseURL and ValidateGeminiBaseURL resolve REST destinations; ValidateURL
	// validates Code Assist destinations; VertexURL builds service-account
	// destinations. Callers supply the callbacks used by the selected route.
	BaseURL func() string // configured image REST API base
	// ValidateURL checks the Code Assist base; ValidateGeminiBaseURL checks the
	// REST base.
	ValidateURL, ValidateGeminiBaseURL func(string) (string, error)
	VertexURL                          func() (string, error) // service-account image endpoint builder
	// ApplyHeaders and Do are required after successful construction; HTTPError
	// and Invalid map applicable failures. Endpoint validation/build callbacks
	// are required for the branch selected by Mode and ProjectID.
	// post-authentication header mutation callback
	ApplyHeaders func(http.Header)
	// Do returns a usable response and body on success. GenerateImages closes
	// successful exchanges; the callback cleans up any response returned with an
	// error.
	Do           func(*http.Request) (*http.Response, error)
	HTTPError    func(int, string) error    // image HTTP error mapping
	Invalid      func(string, ...any) error // image input error mapping
	ErrorMessage func([]byte) string        // upstream error text extraction
	// Enter optionally registers the image attempt and returns a nonnil release
	// callback. Its release runs after the response closes or a later preparation
	// failure.
	Enter func() (func(), error) // optional image attempt registration
}

// BuildImageRequest returns image generation parts with trimmed prompt/control
// text and base64 reference data. Blank source MIME uses image/png, output
// requests TEXT and IMAGE, and a nonblank thinking level adds controls with
// thoughts excluded.
func BuildImageRequest(input ImageRequestInput) wire.ImageGenerateRequest {
	parts := make([]wire.BatchPart, 0, len(input.Sources)+1)
	if prompt := strings.TrimSpace(input.Prompt); prompt != "" {
		parts = append(parts, wire.BatchPart{Text: prompt})
	}
	for _, source := range input.Sources {
		mime := strings.TrimSpace(source.Mime)
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, wire.BatchPart{InlineData: &wire.BatchInlineData{
			MimeType: mime,
			Data:     base64.StdEncoding.EncodeToString(source.Bytes),
		}})
	}
	imageConfig := &wire.ImageConfig{
		ImageSize:   strings.TrimSpace(input.ImageSize),
		AspectRatio: strings.TrimSpace(input.AspectRatio),
	}
	config := wire.ImageGenerationConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
		ImageConfig:        imageConfig,
	}
	if thinkingLevel := strings.TrimSpace(input.ThinkingLevel); thinkingLevel != "" {
		config.ThinkingConfig = &wire.ImageThinkingConfig{
			ThinkingLevel:   thinkingLevel,
			IncludeThoughts: false,
		}
	}
	return wire.ImageGenerateRequest{
		Contents:         []wire.BatchContent{{Parts: parts}},
		GenerationConfig: config,
	}
}

// ParseImageOutputs returns the last decodable inline part from the first
// response candidate as one zero-indexed image. Callers supply a nonnil
// httpError mapper for absent content or image results.
//
// Existing inlineData takes precedence over inline_data, while a blank MIME
// spelling permits its alternate spelling or decoded MIME. Invalid or empty
// base64 sources are skipped before selecting the final retained image.
// Selection follows part order and does not inspect a thought flag, so the
// returned creative result represents that ordered selection rather than all
// inline image observations.
func ParseImageOutputs(body []byte, httpError func(int, string) error) ([]upstream.ImageOutput, error) {
	parts := gjson.GetBytes(body, "candidates.0.content.parts")
	if !parts.IsArray() || len(parts.Array()) == 0 {
		return nil, httpError(http.StatusBadGateway, "gemini upstream returned no content parts")
	}
	outputs := make([]upstream.ImageOutput, 0, len(parts.Array()))
	for _, part := range parts.Array() {
		inline := part.Get("inlineData")
		if !inline.Exists() {
			inline = part.Get("inline_data")
		}
		if !inline.Exists() {
			continue
		}
		raw := strings.TrimSpace(inline.Get("data").String())
		if raw == "" {
			continue
		}
		// Skip an undecodable or empty inline source so a malformed later part
		// cannot displace the last usable image already accepted from earlier parts.
		decoded, err := upstream.DecodeBase64Image(raw)
		if err != nil || len(decoded.Bytes) == 0 {
			continue
		}
		mime := strings.TrimSpace(inline.Get("mimeType").String())
		if mime == "" {
			mime = strings.TrimSpace(inline.Get("mime_type").String())
		}
		if mime == "" {
			mime = decoded.Mime
		}
		outputs = append(outputs, upstream.ImageOutput{Index: len(outputs), Bytes: decoded.Bytes, Mime: mime})
	}
	if len(outputs) == 0 {
		return nil, httpError(http.StatusBadGateway, "gemini upstream returned no image output")
	}
	// Select one creative result from the decoded part sequence and reset its
	// index to zero; this choice uses ordering and does not examine thought
	// metadata.
	outputs = outputs[len(outputs)-1:]
	for index := range outputs {
		outputs[index].Index = index
	}
	return outputs, nil
}

// GenerateImages selects an endpoint, authenticates the image request, and
// returns its selected creative image result. Callers supply callbacks for the
// selected ImageOptions route, and optional lifecycle registration remains
// held through response closing.
//
// Project-bearing OAuth wraps the serialized request for Code Assist, service
// accounts use VertexURL, and remaining routes use validated REST
// construction. API-key acquisition occurs again when setting authentication,
// and ApplyHeaders can override prepared headers before Do. The bounded
// response read precedes error-status mapping and optional OAuth unwrapping;
// decoding then returns the final decodable image of the first candidate.
func GenerateImages(ctx context.Context, request wire.ImageGenerateRequest, options ImageOptions) ([]upstream.ImageOutput, error) {
	if options.Enter != nil {
		done, err := options.Enter()
		if err != nil {
			return nil, err
		}
		defer done()
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	var targetURL string
	projectID := strings.TrimSpace(options.ProjectID)
	switch {
	case options.Mode == ServiceAccountCredential:
		// Resolve the service-account model resource through the supplied Vertex
		// builder before authentication so project and location stay with the caller
		// selected endpoint policy.
		targetURL, err = options.VertexURL()
		if err != nil {
			return nil, options.Invalid("build vertex gemini url: %s", err.Error())
		}
	case options.Mode == OAuthCredential && projectID != "":
		// Include the caller project and model alongside generation data for the
		// internal endpoint, keeping its routing envelope distinct from the REST
		// request shape.
		baseURL, validateErr := options.ValidateURL(codeassist.GeminiCliBaseURL)
		if validateErr != nil {
			return nil, options.HTTPError(0, validateErr.Error())
		}
		targetURL = strings.TrimRight(baseURL, "/") + "/v1internal:generateContent"
		wrapped := map[string]any{"model": options.Model, "project": projectID}
		var inner any
		if err := json.Unmarshal(body, &inner); err != nil {
			return nil, err
		}
		wrapped["request"] = inner
		body, err = json.Marshal(wrapped)
		if err != nil {
			return nil, err
		}
	case options.Mode == APIKeyCredential:
		apiKey := strings.TrimSpace(options.APIKey())
		if apiKey == "" {
			return nil, options.Invalid("gemini api_key not configured")
		}
		baseURL, validateErr := options.ValidateGeminiBaseURL(options.BaseURL())
		if validateErr != nil {
			return nil, options.Invalid("gemini base url invalid: %s", validateErr.Error())
		}
		targetURL, err = BuildGeminiAIStudioModelActionURL(baseURL, options.Model, "generateContent", false)
		if err != nil {
			return nil, options.Invalid("build gemini url: %s", err.Error())
		}
	default:
		// A remaining mode uses the REST endpoint builder and bearer authentication;
		// callers validate supported credential modes before passing image options
		// to this technical operation.
		baseURL, validateErr := options.ValidateGeminiBaseURL(options.BaseURL())
		if validateErr != nil {
			return nil, options.Invalid("gemini base url invalid: %s", validateErr.Error())
		}
		targetURL, err = BuildGeminiAIStudioModelActionURL(baseURL, options.Model, "generateContent", false)
		if err != nil {
			return nil, options.Invalid("build gemini url: %s", err.Error())
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := applyImageAuth(ctx, req, options); err != nil {
		return nil, err
	}
	// Apply caller header handling after authentication so its allowed overrides
	// can replace built-in values before the selected transport sends this
	// prepared request.
	options.ApplyHeaders(req.Header)

	resp, err := options.Do(req)
	if err != nil {
		return nil, options.HTTPError(0, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := upstream.ReadLimitedBody(resp.Body, 64<<20)
	if err != nil {
		return nil, options.HTTPError(0, err.Error())
	}
	if resp.StatusCode >= 400 {
		return nil, options.HTTPError(resp.StatusCode, options.ErrorMessage(respBody))
	}
	// Unwrap only the project-bearing OAuth response because that route used the
	// internal envelope; REST and service-account responses already carry the
	// selected generation payload.
	if options.Mode == OAuthCredential && projectID != "" {
		respBody, err = UnwrapGeminiResponse(respBody)
		if err != nil {
			return nil, options.HTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return ParseImageOutputs(respBody, options.HTTPError)
}

// applyImageAuth sets an API-key header or acquires a bearer token from the
// selected callback. Blank API keys and absent bearer callbacks return input
// errors; OAuth mode also selects the Gemini CLI User-Agent.
func applyImageAuth(ctx context.Context, req *http.Request, options ImageOptions) error {
	if options.Mode == APIKeyCredential {
		apiKey := strings.TrimSpace(options.APIKey())
		if apiKey == "" {
			return options.Invalid("gemini api_key not configured")
		}
		req.Header.Set("x-goog-api-key", apiKey)
		return nil
	}
	if options.Token == nil {
		return options.Invalid("gemini token provider is not configured")
	}
	token, err := options.Token(ctx)
	if err != nil {
		return options.HTTPError(0, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if options.Mode == OAuthCredential {
		req.Header.Set("User-Agent", codeassist.GeminiCLIUserAgent)
	}
	return nil
}
