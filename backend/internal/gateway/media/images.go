package media

import (
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	ImageCapabilityBasic  ImageCapability = "images-basic"
	ImageCapabilityNative ImageCapability = "images-native"
)

type ImageCapability = providercore.OpenAIImagesCapability

type ImageUpload = upstreamcore.ImageUpload

type ImageRequest struct {
	Endpoint           string
	ContentType        string
	Multipart          bool
	Model              string
	ExplicitModel      bool
	Prompt             string
	Stream             bool
	N                  int
	Size               string
	ExplicitSize       bool
	SizeTier           string
	ResponseFormat     string
	Quality            string
	Background         string
	OutputFormat       string
	Moderation         string
	InputFidelity      string
	Style              string
	OutputCompression  *int
	PartialImages      *int
	HasMask            bool
	HasNativeOptions   bool
	RequiredCapability ImageCapability
	InputImageURLs     []string
	MaskImageURL       string
	Uploads            []ImageUpload
	MaskUpload         *ImageUpload
	Body               []byte
	BodyHash           string `json:"-"`
}

// ParseImageRequest parses an image request and optionally validates its model.
func ParseImageRequest(endpoint, contentType string, body []byte, validateModel bool) (*ImageRequest, error) {
	value, err := upstreamcore.ParseImageRequest(endpoint, contentType, body)
	if err != nil {
		return nil, err
	}
	req := &ImageRequest{}
	ApplyNativeImageRequest(req, value)
	req.SizeTier = NormalizeImageSizeTier(req.Size)
	req.RequiredCapability = ClassifyImageCapability(req)
	if validateModel {
		if err := req.ValidateRoutingModel(req.Model); err != nil {
			return nil, err
		}
	}
	return req, nil
}

// ImageExecutionPath returns true for OAuth or setup-token providers and false for API key providers.
func ImageExecutionPath(providerType string) (bool, error) {
	switch providerType {
	case "apikey":
		return false, nil
	case "oauth", "setup-token":
		return true, nil
	default:
		return false, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

func (r *ImageRequest) ModerationBody() []byte {
	return NativeImageRequest(r).ModerationBody()
}
func (r *ImageRequest) IsEdits() bool { return NativeImageRequest(r).IsEdits() }
func (r *ImageRequest) StickySessionSeed() string {
	return NativeImageRequest(r).StickySessionSeed()
}

// ValidateRoutingModel validates the group-mapped model and updates the capability used for selection.
func (r *ImageRequest) ValidateRoutingModel(routingModel string) error {
	if err := ValidateImageModel(routingModel); err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	routed := *r
	routed.Model = strings.TrimSpace(routingModel)
	r.RequiredCapability = ClassifyImageCapability(&routed)
	return nil
}

func IsImageGenerationModel(model string) bool {
	return IsGPTImageGenerationModel(model) || IsGrokImageGenerationModel(model)
}

func IsGPTImageGenerationModel(model string) bool {
	return upstreamcore.IsGPTImageGenerationModel(model)
}

func IsGrokImageGenerationModel(model string) bool {
	return upstreamcore.IsGrokImageGenerationModel(model)
}

func ValidateImageModel(model string) error {
	model = strings.TrimSpace(model)
	if IsImageGenerationModel(model) {
		return nil
	}
	if model == "" {
		return fmt.Errorf("images endpoint requires an image model")
	}
	return fmt.Errorf("images endpoint requires an image model, got %q", model)
}

func ClassifyImageCapability(req *ImageRequest) ImageCapability {
	if req == nil {
		return ImageCapabilityNative
	}
	if req.ExplicitModel || req.ExplicitSize {
		return ImageCapabilityNative
	}
	model := strings.ToLower(strings.TrimSpace(req.Model))
	if !strings.HasPrefix(model, "gpt-image-") {
		return ImageCapabilityNative
	}
	if req.Stream || req.N != 1 || req.HasMask || req.HasNativeOptions {
		return ImageCapabilityNative
	}
	if req.IsEdits() && !req.Multipart {
		return ImageCapabilityNative
	}
	if req.ResponseFormat != "" && req.ResponseFormat != "b64_json" {
		return ImageCapabilityNative
	}
	return ImageCapabilityBasic
}

func NormalizeImageSizeTier(size string) string {
	return pricing.NormalizeImageBillingTierOrDefault(size)
}

func NativeImageRequest(value *ImageRequest) *upstreamcore.ImageRequest {
	if value == nil {
		return nil
	}
	return &upstreamcore.ImageRequest{
		Endpoint:          value.Endpoint,
		ContentType:       value.ContentType,
		Multipart:         value.Multipart,
		Model:             value.Model,
		ExplicitModel:     value.ExplicitModel,
		Prompt:            value.Prompt,
		Stream:            value.Stream,
		N:                 value.N,
		Size:              value.Size,
		ExplicitSize:      value.ExplicitSize,
		SizeTier:          value.SizeTier,
		ResponseFormat:    value.ResponseFormat,
		Quality:           value.Quality,
		Background:        value.Background,
		OutputFormat:      value.OutputFormat,
		Moderation:        value.Moderation,
		InputFidelity:     value.InputFidelity,
		Style:             value.Style,
		OutputCompression: value.OutputCompression,
		PartialImages:     value.PartialImages,
		HasMask:           value.HasMask,
		HasNativeOptions:  value.HasNativeOptions,
		InputImageURLs:    value.InputImageURLs,
		MaskImageURL:      value.MaskImageURL,
		Uploads:           value.Uploads,
		MaskUpload:        value.MaskUpload,
		Body:              value.Body,
		BodyHash:          value.BodyHash,
	}
}

func ApplyNativeImageRequest(target *ImageRequest, value *upstreamcore.ImageRequest) {
	if target == nil || value == nil {
		return
	}
	target.Endpoint = value.Endpoint
	target.ContentType = value.ContentType
	target.Multipart = value.Multipart
	target.Model = value.Model
	target.ExplicitModel = value.ExplicitModel
	target.Prompt = value.Prompt
	target.Stream = value.Stream
	target.N = value.N
	target.Size = value.Size
	target.ExplicitSize = value.ExplicitSize
	target.SizeTier = value.SizeTier
	target.ResponseFormat = value.ResponseFormat
	target.Quality = value.Quality
	target.Background = value.Background
	target.OutputFormat = value.OutputFormat
	target.Moderation = value.Moderation
	target.InputFidelity = value.InputFidelity
	target.Style = value.Style
	target.OutputCompression = value.OutputCompression
	target.PartialImages = value.PartialImages
	target.HasMask = value.HasMask
	target.HasNativeOptions = value.HasNativeOptions
	target.InputImageURLs = value.InputImageURLs
	target.MaskImageURL = value.MaskImageURL
	target.Uploads = value.Uploads
	target.MaskUpload = value.MaskUpload
	target.Body = value.Body
	target.BodyHash = value.BodyHash
}

// ResolveImageModels validates the group-mapped model, then resolves and validates the provider model.
func ResolveImageModels(requested, groupMapped, fallback string, resolve func(string) string) (string, string, error) {
	model := strings.TrimSpace(requested)
	if mapped := strings.TrimSpace(groupMapped); mapped != "" {
		model = mapped
	}
	if model == "" {
		model = fallback
	}
	if err := ValidateImageModel(model); err != nil {
		return "", "", err
	}
	upstream := resolve(model)
	if err := ValidateImageModel(upstream); err != nil {
		return "", "", err
	}
	return model, upstream, nil
}

// ImageOutcome returns the image count and whether to retain the result after a failure.
// Successful OAuth or non-SSE responses can use the requested count; API key SSE requires observed output.
func ImageOutcome(stream, oauth, eventStream bool, requested, observed int, failure error) (int, bool) {
	if failure != nil && (!stream || observed <= 0) {
		return 0, false
	}
	count := observed
	if failure == nil && count <= 0 && (oauth || !stream || !eventStream) {
		count = requested
	}
	return count, true
}
