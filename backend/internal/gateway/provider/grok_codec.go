package provider

import (
	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// GrokBodyCodec binds random ID generation for request encoding.
func GrokBodyCodec() grok.BodyCodec { return grok.BodyCodec{NewID: uuid.NewString} }

// GrokMediaCodec binds media encoding to billing/pricing normalization
// and the upstream upload-size limit.
func GrokMediaCodec() grok.MediaCodec {
	return grok.MediaCodec{Options: grok.MediaNormalization{
		MaxUploadPartSize:                             upstream.OpenAIImageMaxUploadPartSize,
		ImageTier1K:                                   pricing.ImageBillingSize1K,
		MarshalJSON:                                   wirejson.Marshal,
		NormalizeImageBillingTierOrDefault:            pricing.NormalizeImageBillingTierOrDefault,
		NormalizeVideoBillingResolutionOrDefault:      pricing.NormalizeVideoBillingResolutionOrDefault,
		NormalizeVideoBillingDurationSecondsOrDefault: pricing.NormalizeVideoBillingDurationSecondsOrDefault,
		ClassifyImageBillingTier:                      pricing.ClassifyImageBillingTier,
		ParseImageDimensions:                          pricing.ParseImageBillingDimensions,
	}}
}
