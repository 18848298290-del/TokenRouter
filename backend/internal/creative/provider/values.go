package provider

import (
	"fmt"
	"io"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

func ReadCreativeUpstreamBody(body io.Reader, limit int64) ([]byte, error) {
	return upstream.ReadLimitedBody(body, limit)
}

// CreativeOpenAIImageSize maps creative size tiers and aspect ratios to OpenAI pixel dimensions.
// The 4K mapping uses a maximum edge of 3840 pixels and about 8.3 million pixels.
func CreativeOpenAIImageSize(imageSize, aspectRatio string) string {
	tier := pricing.NormalizeImageBillingTierOrDefault(imageSize)
	ratio := strings.TrimSpace(aspectRatio)
	if tier == pricing.ImageBillingSize4K {
		switch ratio {
		case "16:9":
			return "3840x2160"
		case "9:16":
			return "2160x3840"
		case "4:3":
			return "3264x2448"
		case "3:4":
			return "2448x3264"
		default:
			return "2880x2880"
		}
	}
	base := 1024
	if tier != pricing.ImageBillingSize1K {
		base = 1536
	}
	switch ratio {
	case "16:9", "3:2", "4:3", "2:1", "19.5:9", "20:9":
		return fmt.Sprintf("%dx%d", base+base/2, base)
	case "9:16", "3:4", "2:3", "1:2", "9:19.5", "9:20":
		return fmt.Sprintf("%dx%d", base, base+base/2)
	default:
		return fmt.Sprintf("%dx%d", base, base)
	}
}

// CreativeGrokImageResolution maps size tiers to the Grok resolution values 1k and 2k.
func CreativeGrokImageResolution(imageSize string) string {
	if pricing.NormalizeImageBillingTierOrDefault(imageSize) == pricing.ImageBillingSize1K {
		return "1k"
	}
	return "2k"
}

func CreativeGrokAspectRatio(aspectRatio string) string {
	return grok.NormalizeImagineAspectRatio(aspectRatio)
}

func DecodeBase64Image(raw string) (upstream.DecodedImage, error) {
	return upstream.DecodeBase64Image(raw)
}

// CreativeFileExtension selects an extension for multipart filenames, defaulting to png.
func CreativeFileExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
}
