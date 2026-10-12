package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// Tool lowering can replace custom, namespace, and search declarations while
// response restoration needs the mapping associated with that attempt. The
// byte adapter decodes numbers as json.Number before invoking the map adapter,
// retaining valid numeric literals when a changed request is serialized.
//
// Decode reads the first JSON value and does not reject a trailing document.
// Callers establish complete single-object validity before using this adapter.
// Unchanged requests keep their supplied bytes with the returned mapping;
// decode, adaptation, or encoding failure returns the supplied bytes and an
// empty mapping. Inherited mappings and optional prior declarations participate
// according to the map adapter's tool-presence rules, so callers keep their
// ownership and turn association consistent across attempts.

// AdaptResponsesClientToolsJSON delegates byte-oriented tool adaptation with
// an empty inherited mapping. It returns repaired bytes and restoration metadata,
// or the supplied bytes with an empty mapping on error; the caller validates
// one complete request object beforehand.
func AdaptResponsesClientToolsJSON(body []byte, upstream string) ([]byte, ResponsesClientToolMapping, error) {
	return AdaptResponsesClientToolsJSONWithMapping(
		body,
		upstream,
		ResponsesClientToolMapping{},
	)
}

// AdaptResponsesClientToolsJSONWithMapping lowers tools and history using
// inherited restoration metadata when the request's tool-presence rules permit.
// The map adapter uses the first optional prior-declaration slice when needed;
// mapping collections can remain shared with inherited values.
//
// Unchanged adaptation returns the supplied bytes and mapping. Failed decoding,
// adaptation, or serialization returns the supplied body, empty mapping, and
// error. UseNumber retains valid numeric literals during a successful rebuild.
// The decoder reads only the first JSON value, so callers validate a single
// complete object rather than relying on this helper to reject trailing input.
func AdaptResponsesClientToolsJSONWithMapping(
	body []byte,
	upstream string,
	inherited ResponsesClientToolMapping,
	inheritedLoweredTools ...[]any,
) ([]byte, ResponsesClientToolMapping, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Tool adaptation rewrites a decoded object. Retain numeric literals here
	// so unrelated large identifiers do not round through float64 before serialization.
	decoder.UseNumber()
	var requestBody map[string]any
	if err := decoder.Decode(&requestBody); err != nil {
		return body, ResponsesClientToolMapping{}, fmt.Errorf("decode %s Responses client tools: %w", upstream, err)
	}

	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(requestBody, inherited, inheritedLoweredTools...)
	if err != nil {
		return body, ResponsesClientToolMapping{}, err
	}
	if !changed {
		return body, mapping, nil
	}
	rebuilt, err := wirejson.Marshal(requestBody)
	if err != nil {
		return body, ResponsesClientToolMapping{}, fmt.Errorf("encode %s Responses client tools: %w", upstream, err)
	}
	return rebuilt, mapping, nil
}
