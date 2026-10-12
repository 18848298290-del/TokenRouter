package requeststate

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ModelBodyReplacer replaces the model using the caller's body encoder.
type ModelBodyReplacer func([]byte, string) []byte

// GroupMappedModel returns the group-mapped model, or the trimmed requested model when mapping is empty.
func GroupMappedModel(requestedModel string, mapping routing.GroupMappingResult) string {
	routingModel := strings.TrimSpace(mapping.MappedModel)
	if routingModel == "" {
		return strings.TrimSpace(requestedModel)
	}
	return routingModel
}

// ModelMappedBody replaces the model when mapping is enabled and a replacer is supplied.
func ModelMappedBody(body []byte, mapped bool, mappedModel string, replace ModelBodyReplacer) []byte {
	if !mapped || replace == nil {
		return body
	}
	return replace(body, mappedModel)
}

// NewModelMappedBodyCache reuses replacement bytes by model across attempts; callers treat returned bytes as read-only.
func NewModelMappedBodyCache(body []byte, replace ModelBodyReplacer) func(bool, string) []byte {
	replacedBodies := make(map[string][]byte)
	return func(mapped bool, mappedModel string) []byte {
		if !mapped {
			return body
		}
		if cachedBody, ok := replacedBodies[mappedModel]; ok {
			return cachedBody
		}
		replacedBody := ModelMappedBody(body, true, mappedModel, replace)
		replacedBodies[mappedModel] = replacedBody
		return replacedBody
	}
}
