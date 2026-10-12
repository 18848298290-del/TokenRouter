package batchimage

import (
	"context"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

func (s *Public) validateModelAttributes(ctx context.Context, session *routing.ModelAttributeSession, groupID *int64, provider *Candidate, model string, request BatchImageSubmitRequest) error {
	target := modelcatalog.Target{Platform: "gemini", Model: model, ClientProtocol: "image_batches"}
	if provider.AttributeTarget != nil {
		target = provider.AttributeTarget(model)
		target.ClientProtocol = "image_batches"
	}
	var group int64
	if groupID != nil {
		group = *groupID
	}
	result, err := session.Resolve(ctx, group, target)
	if err != nil {
		return err
	}
	media := result.Attributes.Media
	if media == nil {
		return nil
	}
	if media.ImageSizes != nil && !slices.Contains(media.ImageSizes, request.ImageSize) {
		return ErrBatchImageInvalidItems
	}
	if request.AspectRatio != "" && media.AspectRatios != nil && !slices.Contains(media.AspectRatios, request.AspectRatio) {
		return ErrBatchImageInvalidItems
	}
	for _, item := range request.Items {
		if media.MaxReferenceImages != nil && len(item.ReferenceImages) > *media.MaxReferenceImages {
			return ErrBatchImageTooManyReferenceImages
		}
	}
	return nil
}
