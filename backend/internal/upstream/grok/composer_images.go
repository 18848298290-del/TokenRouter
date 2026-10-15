package grok

import (
	"encoding/json"
	"fmt"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// composer_images.go replaces Composer image inputs with ordered descriptions
// and returns the auxiliary usage observations.
// Composer requests can contain inline images that need textual descriptions
// before the selected Composer model receives its conversation. Image URLs are
// collected in encounter order, and each auxiliary description uses that same
// one-based position so replacement text still identifies its source image.
//
// Description calls run sequentially and accumulate usage from successful
// callbacks. A later failure returns that accumulated usage with the supplied
// request body because replacement has not happened. After every description
// succeeds, the conversation is rewritten and serialized for the main request.
// DecodeComposerDescription returns usage alongside an empty-text error, but
// BridgeComposerImages does not add a failed callback's usage to its
// accumulation.

// BridgeComposerImages replaces usable Composer image inputs with descriptions
// from describe. Non-target requests return body with zero usage and false;
// qualifying requests need a describe callback. Image positions are one-based
// and follow collection order.
//
// Auxiliary calls accumulate usage sequentially before any rewrite is
// serialized. A failing call returns the supplied body, previously accumulated
// usage and false; its own failed-call usage is not added. After successful
// descriptions, rewrite or serialization can still return false or an error with
// accumulated usage, so callers assess usage separately from whether a bridged
// body was produced.
func BridgeComposerImages(body []byte, describe func(string, int) (string, protocolopenai.ForwardUsage, error)) ([]byte, protocolopenai.ForwardUsage, bool, error) {
	codec := BodyCodec{}

	if !codec.ShouldBridgeGrokComposerImageInputs(body) {
		return body, protocolopenai.ForwardUsage{}, false, nil
	}

	var reqBody map[string]any
	if err := wirejson.DecodeUseNumber(body, &reqBody); err != nil {
		return body, protocolopenai.ForwardUsage{}, false, fmt.Errorf("parse grok composer image bridge request: %w", err)
	}

	imageURLs := codec.CollectGrokComposerImageURLs(reqBody)
	if len(imageURLs) == 0 {
		return body, protocolopenai.ForwardUsage{}, false, nil
	}

	descriptions := make([]string, 0, len(imageURLs))
	var bridgeUsage protocolopenai.ForwardUsage
	// Keep description positions aligned with image encounter order, accumulating
	// completed-call usage before a later failure can stop the bridge.
	for index, imageURL := range imageURLs {
		description, usage, err := describe(imageURL, index+1)
		if err != nil {
			return body, bridgeUsage, false, err
		}
		descriptions = append(descriptions, description)
		protocolopenai.AddForwardUsage(&bridgeUsage, usage)
	}

	// Delay conversation mutation until all descriptions succeed; earlier errors
	// return the supplied body together with usage from completed auxiliary calls.
	if !codec.RewriteGrokComposerImagesAsText(reqBody, descriptions) {
		return body, bridgeUsage, false, nil
	}
	bridgedBody, err := wirejson.Marshal(reqBody)
	if err != nil {
		return body, bridgeUsage, false, fmt.Errorf("serialize grok composer image bridge request: %w", err)
	}
	return bridgedBody, bridgeUsage, true, nil
}

// DecodeComposerDescription extracts joined textual output and usage from an
// auxiliary Responses body. Malformed JSON returns a parse error. Empty trimmed
// text returns parsed usage with an error, so callers can observe usage even
// when the description is unusable.
func DecodeComposerDescription(body []byte) (string, protocolopenai.ForwardUsage, error) {
	var parsed protocolopenai.ResponsesResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", protocolopenai.ForwardUsage{}, fmt.Errorf("parse grok composer image bridge response: %w", err)
	}
	description := strings.TrimSpace((BodyCodec{}).GrokResponsesOutputText(&parsed))
	// A response can include usage while yielding no description. Return both that
	// usage and an error for the caller's completion policy.
	if description == "" {
		return "", protocolopenai.CopyForwardUsage(parsed.Usage), fmt.Errorf("grok composer image bridge returned empty description")
	}
	return description, protocolopenai.CopyForwardUsage(parsed.Usage), nil
}
