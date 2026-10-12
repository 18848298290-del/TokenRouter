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

type ImageRequestInput struct {
	Prompt, ImageSize, AspectRatio, ThinkingLevel string
	Sources                                       []upstream.DecodedImage
}
type ImageOptions struct {
	Mode                               CredentialMode
	Model, ProjectID                   string
	APIKey                             func() string
	Token                              func(context.Context) (string, error)
	BaseURL                            func() string
	ValidateURL, ValidateGeminiBaseURL func(string) (string, error)
	VertexURL                          func() (string, error)
	ApplyHeaders                       func(http.Header)
	Do                                 func(*http.Request) (*http.Response, error)
	HTTPError                          func(int, string) error
	Invalid                            func(string, ...any) error
	ErrorMessage                       func([]byte) string
	Enter                              func() (func(), error)
}

// BuildImageRequest builds generateContent parts from the prompt and inline reference images.
// Creative requests do not send a separate mask to Gemini.
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

// ParseImageOutputs returns the last decodable inline image from generateContent.
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
	// Use the last image because Vertex high-resolution output can place thought images before the final image.
	outputs = outputs[len(outputs)-1:]
	for index := range outputs {
		outputs[index].Index = index
	}
	return outputs, nil
}

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
		// Vertex service accounts use a project-scoped model URL and bearer authentication.
		targetURL, err = options.VertexURL()
		if err != nil {
			return nil, options.Invalid("build vertex gemini url: %s", err.Error())
		}
	case options.Mode == OAuthCredential && projectID != "":
		// Code Assist wraps the request for the v1internal endpoint.
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
		// OAuth without a project uses AI Studio bearer authentication.
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
	// Provider header overrides take precedence over built-in defaults.
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
	// Code Assist wraps its payload in the response field.
	if options.Mode == OAuthCredential && projectID != "" {
		respBody, err = UnwrapGeminiResponse(respBody)
		if err != nil {
			return nil, options.HTTPError(http.StatusBadGateway, err.Error())
		}
	}
	return ParseImageOutputs(respBody, options.HTTPError)
}

// applyImageAuth uses x-goog-api-key for API keys and bearer authentication for other modes.
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
