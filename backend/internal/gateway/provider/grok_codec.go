package provider

import (
	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_codec.go constructs Grok body and media codecs with gateway encoding
// and billing rules.
// Grok body encoding needs generated request IDs, and media normalization
// needs the same size rules used for billing. These constructors bind UUID
// generation, upload limits, and image/video normalization to the upstream
// codecs before gateway execution builds a request.

// GrokBodyCodec returns a Grok request codec using UUID string generation.
func GrokBodyCodec() grok.BodyCodec { return grok.BodyCodec{NewID: uuid.NewString} }

// GrokMediaCodec returns media normalization using gateway upload limits and
// billing size rules.
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
