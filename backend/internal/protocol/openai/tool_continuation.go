package openai

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// tool_continuation.go evaluates tool-result history and checks whether call or
// reference identities cover continuation outputs.
// A tool result can depend on a prior upstream response even when the request
// contains some tool-call context. Preliminary validation asks whether an
// output and any nonblank call context exist; that decision can accept
// unrelated context. Moving or rebuilding a continuation needs a stronger
// comparison in which every output ID has call or reference evidence.
//
// Decoded-map scans require actual string IDs, while raw byte scans use gjson
// string forms and borrowed request storage. The full raw coverage scan
// accepts an input array or one object and examines all outputs. Coarse scans
// can stop early, so their missing-ID and reference flags describe the items
// inspected under their individual rules; callers retain continuation state or
// reject recovery using the appropriate result.

// ToolContinuationSignals records decoded-array observations used by WebSocket
// continuation inference. FunctionCallOutput field names cover all four
// recognized Codex output types, and their flags have independent evidence
// rules.
type ToolContinuationSignals struct {
	// HasFunctionCallOutput records any recognized output, while
	// HasFunctionCallOutputMissingCallID marks an output with no nonblank string
	// call ID. Those observations remain independent of reference coverage for
	// other outputs.
	HasFunctionCallOutput              bool // recognized output item observed
	HasFunctionCallOutputMissingCallID bool // observed output lacks string ID
	// HasToolCallContext means some recognized call has a nonblank string
	// call_id; that call can be unrelated to the output IDs.
	HasToolCallContext bool // nonblank call context observed
	// HasItemReference records the literal item_reference type even when its id
	// is blank or has another decoded type.
	HasItemReference bool // literal reference item observed
	// HasItemReferenceForAllCallIDs covers the unique nonblank IDs in
	// FunctionCallOutputCallIDs. Missing output IDs are tracked separately and do
	// not force this flag false; the returned slice has unspecified order.
	HasItemReferenceForAllCallIDs bool     // nonblank outputs fully referenced
	FunctionCallOutputCallIDs     []string // unique trimmed output call IDs
}

// FunctionCallOutputValidation records coarse output/context evidence for
// preliminary HTTP-style gates. Its scanners can stop early and do not
// establish identity coverage for every output.
type FunctionCallOutputValidation struct {
	// HasFunctionCallOutput and HasToolCallContext can end preliminary scanning
	// once both are observed. Context means a recognized call's nonblank ID, so this
	// combination does not establish all-output matching.
	HasFunctionCallOutput bool // recognized output item observed
	HasToolCallContext    bool // nonblank call context observed
	// HasFunctionCallOutputMissingCallID records inspected missing IDs. The raw
	// scan can set it before finding context; map validation checks it only on
	// its no-context second pass.
	HasFunctionCallOutputMissingCallID bool // observed output lacks usable ID
	// HasItemReferenceForAllCallIDs checks collected nonblank output IDs when
	// call context is absent. Read the missing-ID flag separately before
	// accepting a reference-only request.
	HasItemReferenceForAllCallIDs bool // nonblank outputs fully referenced
}

// ToolCallOutputContextCoverage reports raw-input identity matching before
// callers discard an upstream continuation chain.
type ToolCallOutputContextCoverage struct {
	HasFunctionCallOutput bool // recognized output item observed
	// ContextCoversAllCallIDs requires an output and a nonblank call_id on every
	// output. Each distinct trimmed ID must match a call-context call_id or
	// literal item_reference id from the same input.
	ContextCoversAllCallIDs bool // every observed output ID matched
}

// IsCodexToolCallContextItemType recognizes the supported call-context labels
// after trimming surrounding whitespace.
func IsCodexToolCallContextItemType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "tool_call",
		"function_call",
		"local_shell_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call":
		return true
	default:
		return false
	}
}

// IsCodexToolCallOutputItemType recognizes the four supported output labels
// after trimming surrounding whitespace.
func IsCodexToolCallOutputItemType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "function_call_output",
		"tool_search_output",
		"custom_tool_call_output",
		"mcp_tool_call_output":
		return true
	default:
		return false
	}
}

// NeedsToolContinuation reports a nonblank string previous ID, nonempty
// decoded tools/choice, or exact Codex call/output/reference labels in an
// input array. Unsupported input shapes contribute no item signal; this
// classification does not validate a continuation chain.
func NeedsToolContinuation(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	if HasNonEmptyString(reqBody["previous_response_id"]) {
		return true
	}
	if HasToolsSignal(reqBody) {
		return true
	}
	if HasToolChoiceSignal(reqBody) {
		return true
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if IsCodexToolCallItemType(itemType) || itemType == "item_reference" {
			return true
		}
	}
	return false
}

// AnalyzeToolContinuationSignals scans a decoded []any input array for output,
// context, and reference evidence. Unsupported request/item shapes produce no
// observation, and ID fields must be actual strings. Output IDs are trimmed,
// deduplicated, and returned in unspecified order.
//
// Any call with a nonblank ID establishes context presence, even if unrelated
// to an output. Reference coverage compares the unique nonblank output IDs
// with literal item_reference IDs; outputs lacking IDs set their own flag and
// do not force that coverage flag false. Callers combine these observations
// according to their continuation policy rather than treating context presence
// as a matched history.
func AnalyzeToolContinuationSignals(reqBody map[string]any) ToolContinuationSignals {
	signals := ToolContinuationSignals{}
	if reqBody == nil {
		return signals
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return signals
	}

	var callIDs map[string]struct{}
	var referenceIDs map[string]struct{}

	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		switch {
		case IsCodexToolCallContextItemType(itemType):
			callID, _ := itemMap["call_id"].(string)
			if strings.TrimSpace(callID) != "" {
				signals.HasToolCallContext = true
			}
		case IsCodexToolCallOutputItemType(itemType):
			signals.HasFunctionCallOutput = true
			callID, _ := itemMap["call_id"].(string)
			callID = strings.TrimSpace(callID)
			if callID == "" {
				signals.HasFunctionCallOutputMissingCallID = true
				continue
			}
			if callIDs == nil {
				callIDs = make(map[string]struct{})
			}
			callIDs[callID] = struct{}{}
		case itemType == "item_reference":
			signals.HasItemReference = true
			idValue, _ := itemMap["id"].(string)
			idValue = strings.TrimSpace(idValue)
			if idValue == "" {
				continue
			}
			if referenceIDs == nil {
				referenceIDs = make(map[string]struct{})
			}
			referenceIDs[idValue] = struct{}{}
		}
	}

	if len(callIDs) == 0 {
		return signals
	}
	signals.FunctionCallOutputCallIDs = make([]string, 0, len(callIDs))
	allReferenced := len(referenceIDs) > 0
	for callID := range callIDs {
		signals.FunctionCallOutputCallIDs = append(signals.FunctionCallOutputCallIDs, callID)
		if allReferenced {
			if _, ok := referenceIDs[callID]; !ok {
				allReferenced = false
			}
		}
	}
	signals.HasItemReferenceForAllCallIDs = allReferenced
	return signals
}

// ValidateFunctionCallOutputContextBytes collects coarse flags from a raw
// input array using gjson string forms. Empty or unsupported containers
// produce zero flags, and complete-document validation belongs to the caller.
// Keep body bytes unchanged during the synchronous borrowed-view scan.
//
// The scan can stop after observing an output and some nonblank call context.
// Missing output IDs encountered before that stop remain recorded; later items
// may be unexamined. Without context, nonblank output IDs are compared against
// reference IDs, while missing IDs remain a separate result. This preliminary
// gate does not establish all-output context coverage, and its coercion or
// early missing-ID observations can differ from the decoded-map helper.
func ValidateFunctionCallOutputContextBytes(body []byte) FunctionCallOutputValidation {
	result := FunctionCallOutputValidation{}
	if len(body) == 0 {
		return result
	}
	// ParseView borrows the body storage for this synchronous scan. The
	// returned flags contain no borrowed strings, but parsing requires stable input bytes.
	input := wirejson.ParseView(body).Get("input")
	if !input.IsArray() {
		return result
	}

	var callIDs map[string]struct{}
	var referenceIDs map[string]struct{}
	input.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			return true
		}
		itemType := item.Get("type").String()
		switch {
		case IsCodexToolCallOutputItemType(itemType):
			result.HasFunctionCallOutput = true
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				result.HasFunctionCallOutputMissingCallID = true
				return true
			}
			if callIDs == nil {
				callIDs = make(map[string]struct{})
			}
			callIDs[callID] = struct{}{}
		case IsCodexToolCallContextItemType(itemType):
			if strings.TrimSpace(item.Get("call_id").String()) != "" {
				result.HasToolCallContext = true
			}
		case itemType == "item_reference":
			idValue := strings.TrimSpace(item.Get("id").String())
			if idValue == "" {
				return true
			}
			if referenceIDs == nil {
				referenceIDs = make(map[string]struct{})
			}
			referenceIDs[idValue] = struct{}{}
		}
		// The HTTP gate accepts any call context before checking missing IDs or
		// references. Stop once both presence categories can decide that branch.
		return !result.HasFunctionCallOutput || !result.HasToolCallContext
	})
	if !result.HasFunctionCallOutput || result.HasToolCallContext || len(callIDs) == 0 || len(referenceIDs) == 0 {
		return result
	}
	allReferenced := true
	for callID := range callIDs {
		if _, ok := referenceIDs[callID]; !ok {
			allReferenced = false
			break
		}
	}
	result.HasItemReferenceForAllCallIDs = allReferenced
	return result
}

// AnalyzeToolCallOutputContextCoverageBytes checks all recognized output IDs
// against call or reference IDs in a raw input array or single object. At
// least one output is required, every output needs a nonblank gjson
// string-form call ID, and each distinct ID must occur in the combined context
// set.
//
// The full scan ignores item order and accepts either call_id on a recognized
// call or id on a literal item_reference. Missing IDs or one unmatched output
// leave coverage false. Keep body bytes stable during borrowed parsing and
// validate the complete request separately; matching these strings does not
// verify that an upstream referenced object exists.
func AnalyzeToolCallOutputContextCoverageBytes(body []byte) ToolCallOutputContextCoverage {
	coverage := ToolCallOutputContextCoverage{}
	if len(body) == 0 {
		return coverage
	}
	input := wirejson.ParseView(body).Get("input")
	if !input.IsArray() && !input.IsObject() {
		return coverage
	}

	missingCallID := false
	var outputCallIDs map[string]struct{}
	var contextIDs map[string]struct{}
	analyzeItem := func(item gjson.Result) {
		if !item.IsObject() {
			return
		}
		itemType := item.Get("type").String()
		switch {
		case IsCodexToolCallOutputItemType(itemType):
			coverage.HasFunctionCallOutput = true
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				missingCallID = true
				return
			}
			if outputCallIDs == nil {
				outputCallIDs = make(map[string]struct{})
			}
			outputCallIDs[callID] = struct{}{}
		case IsCodexToolCallContextItemType(itemType):
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				return
			}
			if contextIDs == nil {
				contextIDs = make(map[string]struct{})
			}
			contextIDs[callID] = struct{}{}
		case itemType == "item_reference":
			idValue := strings.TrimSpace(item.Get("id").String())
			if idValue == "" {
				return
			}
			if contextIDs == nil {
				contextIDs = make(map[string]struct{})
			}
			contextIDs[idValue] = struct{}{}
		}
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			analyzeItem(item)
			return true
		})
	} else {
		analyzeItem(input)
	}

	if !coverage.HasFunctionCallOutput || missingCallID {
		return coverage
	}
	for callID := range outputCallIDs {
		if _, ok := contextIDs[callID]; !ok {
			return coverage
		}
	}
	coverage.ContextCoversAllCallIDs = true
	return coverage
}

// ValidateFunctionCallOutputContext collects preliminary flags from a decoded
// input array. Its first pass can return after finding any output plus any
// nonblank call context, or after proving one category absent. Unsupported
// shapes contribute no evidence.
//
// Only output-bearing requests with no call context receive the second pass
// for missing IDs and references. That pass requires string IDs and compares
// the unique nonblank output set with trimmed reference IDs; missing IDs
// remain independently flagged. Context presence is deliberately coarse, so
// callers use AnalyzeToolCallOutputContextCoverageBytes when every output must
// match before a continuation chain is removed.
func ValidateFunctionCallOutputContext(reqBody map[string]any) FunctionCallOutputValidation {
	result := FunctionCallOutputValidation{}
	if reqBody == nil {
		return result
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return result
	}

	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		switch {
		case IsCodexToolCallOutputItemType(itemType):
			result.HasFunctionCallOutput = true
		case IsCodexToolCallContextItemType(itemType):
			callID, _ := itemMap["call_id"].(string)
			if strings.TrimSpace(callID) != "" {
				result.HasToolCallContext = true
			}
		}
		// Context presence decides the coarse gate, so later malformed outputs
		// need not be scanned on this path. Full identity coverage uses its separate helper.
		if result.HasFunctionCallOutput && result.HasToolCallContext {
			return result
		}
	}

	if !result.HasFunctionCallOutput || result.HasToolCallContext {
		return result
	}

	callIDs := make(map[string]struct{})
	referenceIDs := make(map[string]struct{})
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		switch {
		case IsCodexToolCallOutputItemType(itemType):
			callID, _ := itemMap["call_id"].(string)
			callID = strings.TrimSpace(callID)
			if callID == "" {
				result.HasFunctionCallOutputMissingCallID = true
				continue
			}
			callIDs[callID] = struct{}{}
		case itemType == "item_reference":
			idValue, _ := itemMap["id"].(string)
			idValue = strings.TrimSpace(idValue)
			if idValue == "" {
				continue
			}
			referenceIDs[idValue] = struct{}{}
		}
	}

	if len(callIDs) == 0 || len(referenceIDs) == 0 {
		return result
	}
	allReferenced := true
	for callID := range callIDs {
		if _, ok := referenceIDs[callID]; !ok {
			allReferenced = false
			break
		}
	}
	result.HasItemReferenceForAllCallIDs = allReferenced
	return result
}

// HasFunctionCallOutput delegates decoded-array signal collection and reports
// any recognized Codex output type. Nil requests and unsupported input
// containers return false; the result is presence evidence, independent of IDs
// or matching context.
func HasFunctionCallOutput(reqBody map[string]any) bool {
	return AnalyzeToolContinuationSignals(reqBody).HasFunctionCallOutput
}

// HasItemReferenceForCallIDs matches every supplied trimmed call ID against
// trimmed literal item_reference IDs in a decoded input array. Nil requests,
// empty ID lists, unsupported input, or no nonblank references return false.
// Repeated requested IDs can share one reference entry.
//
// The helper compares the provided ID list rather than deriving output IDs or
// checking their call contexts. A blank requested ID fails because blank
// references are excluded. Matching establishes string-set coverage only; the
// caller determines whether those references belong to valid upstream objects
// and whether other continuation requirements are satisfied.
func HasItemReferenceForCallIDs(reqBody map[string]any, callIDs []string) bool {
	if reqBody == nil || len(callIDs) == 0 {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	referenceIDs := make(map[string]struct{})
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType != "item_reference" {
			continue
		}
		idValue, _ := itemMap["id"].(string)
		idValue = strings.TrimSpace(idValue)
		if idValue == "" {
			continue
		}
		referenceIDs[idValue] = struct{}{}
	}
	if len(referenceIDs) == 0 {
		return false
	}
	for _, callID := range callIDs {
		if _, ok := referenceIDs[strings.TrimSpace(callID)]; !ok {
			return false
		}
	}
	return true
}

// HasNonEmptyString reports whether the value is a string containing
// non-whitespace text.
func HasNonEmptyString(value any) bool {
	stringValue, ok := value.(string)
	return ok && strings.TrimSpace(stringValue) != ""
}

// HasToolsSignal reports a nonempty []any value under tools in the decoded
// request map. Missing, nil, empty, and differently typed collections return
// false; the signal does not validate individual tool declarations.
func HasToolsSignal(reqBody map[string]any) bool {
	raw, exists := reqBody["tools"]
	if !exists || raw == nil {
		return false
	}
	if tools, ok := raw.([]any); ok {
		return len(tools) > 0
	}
	return false
}

// HasToolChoiceSignal reports a nonblank string or nonempty decoded map under
// tool_choice. Any such string, including none, supplies this signal; absent,
// nil, empty, and other typed values return false.
func HasToolChoiceSignal(reqBody map[string]any) bool {
	raw, exists := reqBody["tool_choice"]
	if !exists || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case map[string]any:
		return len(value) > 0
	default:
		return false
	}
}
