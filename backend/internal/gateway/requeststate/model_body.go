package requeststate

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// model_body.go selects group-mapped models and caches request body
// replacements by target model. Serial retries reuse encoded bytes for the same
// target, while an unmapped attempt continues using the supplied body. Callers
// keep the source and returned slices stable throughout the cache lifetime.

// ModelBodyReplacer encodes a supplied model into a caller-owned request body.
type ModelBodyReplacer func([]byte, string) []byte

// GroupMappedModel returns the trimmed group-mapped model when present,
// otherwise the trimmed requested model. The mapping result supplies the
// choice; this helper performs no additional mapping or model validation.
func GroupMappedModel(requestedModel string, mapping routing.GroupMappingResult) string {
	routingModel := strings.TrimSpace(mapping.MappedModel)
	if routingModel == "" {
		return strings.TrimSpace(requestedModel)
	}
	return routingModel
}

// ModelMappedBody invokes replace when mapping is enabled and a replacer
// exists. Otherwise it returns the supplied body slice, and the callback
// defines the ownership and encoding of replacement bytes.
func ModelMappedBody(body []byte, mapped bool, mappedModel string, replace ModelBodyReplacer) []byte {
	if !mapped || replace == nil {
		return body
	}
	return replace(body, mappedModel)
}

// NewModelMappedBodyCache returns a serial request-local lookup for model
// replacement bytes. Unmapped calls return the supplied body, while mapped
// calls reuse the result associated with that target model.
//
// The closure retains source and cached slices, including an unchanged source
// returned by a nil replacer. Callers treat returned bytes as read-only and
// keep the source stable throughout these attempts. It performs no invalidation
// or locking, so a changed source requires a new cache and concurrent calls
// require caller synchronization.
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
