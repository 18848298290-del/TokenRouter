package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// Codex custom and tool-search shapes can need conversion before a function-
// only Responses upstream receives them. A recursive type screen chooses
// whether to decode, while inherited mappings let subsequent turns reuse the
// client identities recorded earlier. Namespace-only requests pass through
// this screen unchanged.
//
// The decoding path requires a single JSON value and uses json.Number to
// retain numeric spelling during rebuilding. Adaptation itself belongs to the
// shared mapping implementation; this wrapper establishes trailing-input
// validation and wire encoding. The delegate's change flag selects rebuilding;
// a false flag returns the supplied bytes. Callers handle errors and mapping
// lifetime before forwarding the result.

// AdaptOpenAIResponsesClientTools adapts screened Codex client-tool shapes for
// a function-only upstream. When screening finds no supported shape, it
// returns the supplied bytes and an empty mapping without complete JSON
// validation; otherwise the mapping adapter performs decoding.
func AdaptOpenAIResponsesClientTools(body []byte) ([]byte, ResponsesClientToolMapping, error) {
	if !NeedsOpenAIResponsesClientToolAdaptation(body) {
		return body, ResponsesClientToolMapping{}, nil
	}
	return AdaptOpenAIResponsesClientToolsWithMapping(body, ResponsesClientToolMapping{})
}

// AdaptOpenAIResponsesClientToolsWithMapping delegates tool adaptation with
// identities inherited from an earlier turn. It requires one complete JSON
// object or null value, preserves numeric literals through UseNumber and
// wirejson, and rejects trailing garbage or another document.
//
// The returned mapping can retain inherited shared collections. The delegate's
// change flag controls serialization: false returns source bytes, while true
// reencodes the parsed request. An inherited namespace map with present input
// can select reencoding even when no qualified name matched.
//
// Decode and encode failures return the source body and an empty mapping,
// while delegated adaptation failures return that delegate's mapping. Callers
// keep inherited identities within one conversation and handle errors before
// forwarding.
func AdaptOpenAIResponsesClientToolsWithMapping(
	body []byte,
	inherited ResponsesClientToolMapping,
) ([]byte, ResponsesClientToolMapping, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Tool schemas and discovery data can contain integers beyond float64
	// precision. Retain json.Number values until wirejson encodes an adapted request.
	decoder.UseNumber()
	var requestBody map[string]any
	if err := decoder.Decode(&requestBody); err != nil {
		return body, ResponsesClientToolMapping{}, fmt.Errorf("decode OpenAI Responses client tools: %w", err)
	}
	// Ingress decoding can leave trailing bytes unread. Demand EOF after the
	// first object so adaptation cannot discard an extra value during rebuilding.
	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return body, ResponsesClientToolMapping{}, fmt.Errorf("decode OpenAI Responses client tools trailing data: %w", err)
	}
	mapping, changed, err := AdaptResponsesClientToolsWithInheritedMapping(requestBody, inherited)
	if err != nil || !changed {
		return body, mapping, err
	}
	rebuilt, err := wirejson.Marshal(requestBody)
	if err != nil {
		return body, ResponsesClientToolMapping{}, fmt.Errorf("encode OpenAI Responses client tools: %w", err)
	}
	return rebuilt, mapping, nil
}

// NeedsOpenAIResponsesClientToolAdaptation reports any recognized custom or
// tool-search type in a parsed JSON tree. It recursively screens all objects
// and arrays, trims type spelling, and does not establish complete-document
// validity or declaration eligibility.
func NeedsOpenAIResponsesClientToolAdaptation(body []byte) bool {
	needsAdaptation := false
	var visit func(gjson.Result) bool
	visit = func(value gjson.Result) bool {
		if value.IsObject() {
			switch strings.TrimSpace(value.Get("type").String()) {
			case "custom", "custom_tool_call", "custom_tool_call_output",
				"tool_search", "tool_search_call", "tool_search_output":
				needsAdaptation = true
				return false
			}
		}
		if value.IsObject() || value.IsArray() {
			value.ForEach(func(_, child gjson.Result) bool {
				return visit(child)
			})
		}
		return !needsAdaptation
	}
	visit(gjson.ParseBytes(body))
	return needsAdaptation
}

// IsOpenAIResponsesToolCallItemType checks the configured namespace-bearing
// call spellings after trimming whitespace and folding case. Unknown item
// types return false, allowing direct input cleanup to remove their namespace
// fields.
func IsOpenAIResponsesToolCallItemType(itemType string) bool {
	return openAIResponsesToolCallItemTypes[strings.ToLower(strings.TrimSpace(itemType))]
}
