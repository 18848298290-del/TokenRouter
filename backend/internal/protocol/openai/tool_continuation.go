package openai

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// ToolContinuationSignals holds input signals collected in one scan for tool continuation checks.
type ToolContinuationSignals struct {
	HasFunctionCallOutput              bool
	HasFunctionCallOutputMissingCallID bool
	HasToolCallContext                 bool
	HasItemReference                   bool
	HasItemReferenceForAllCallIDs      bool
	FunctionCallOutputCallIDs          []string
}

// FunctionCallOutputValidation holds preliminary context checks for Codex tool outputs.
type FunctionCallOutputValidation struct {
	HasFunctionCallOutput              bool
	HasToolCallContext                 bool
	HasFunctionCallOutputMissingCallID bool
	HasItemReferenceForAllCallIDs      bool
}

// ToolCallOutputContextCoverage describes whether input contains the context needed for each tool output
// when a caller considers removing previous_response_id.
type ToolCallOutputContextCoverage struct {
	HasFunctionCallOutput bool
	// ContextCoversAllCallIDs requires at least one tool output and a nonempty call_id on every output.
	// Each call_id must match a tool call context call_id or an item_reference id in input.
	// Missing coverage means removing previous_response_id can leave an output unresolved,
	// causing upstream rejection with "No tool call found for function call output".
	ContextCoversAllCallIDs bool
}

// IsCodexToolCallContextItemType recognizes item types that carry tool call context.
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

// IsCodexToolCallOutputItemType recognizes Codex tool output item types.
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

// NeedsToolContinuation reports whether a request contains continuation signals.
// Signals include previous_response_id, tool calls or outputs, item_reference,
// and nonempty tools or tool_choice declarations.
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

// AnalyzeToolContinuationSignals scans input once for tool outputs, call context, and item references.
// Fields named FunctionCallOutput cover every Codex output type:
// function_call_output, tool_search_output, custom_tool_call_output, and mcp_tool_call_output.
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

// ValidateFunctionCallOutputContextBytes scans raw JSON for preliminary handler validation.
func ValidateFunctionCallOutputContextBytes(body []byte) FunctionCallOutputValidation {
	result := FunctionCallOutputValidation{}
	if len(body) == 0 {
		return result
	}
	// The synchronous scan can reference the request bytes directly.
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

// AnalyzeToolCallOutputContextCoverageBytes matches every output call_id against input context.
// ValidateFunctionCallOutputContextBytes.HasToolCallContext only establishes that some
// context exists; partial coverage can still cause upstream rejection.
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

// ValidateFunctionCallOutputContext performs preliminary handler validation.
// It returns after the first scan when no outputs exist or some call context exists.
// Only requests with outputs and no call context need call_id and item_reference sets.
// Use AnalyzeToolCallOutputContextCoverageBytes when every output must be matched.
// Fields named FunctionCallOutput cover all Codex tool output types.
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

// HasFunctionCallOutput reports whether input contains a Codex tool output requiring continuation checks.
// It recognizes function_call_output and the other Codex output types.
func HasFunctionCallOutput(reqBody map[string]any) bool {
	return AnalyzeToolContinuationSignals(reqBody).HasFunctionCallOutput
}

// HasItemReferenceForCallIDs reports whether references cover every supplied call ID.
// It supports continuation checks for requests relying on reference items.
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

// HasNonEmptyString reports whether value is a string with non-whitespace content.
func HasNonEmptyString(value any) bool {
	stringValue, ok := value.(string)
	return ok && strings.TrimSpace(stringValue) != ""
}

// HasToolsSignal reports whether tools is a nonempty []any.
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

// HasToolChoiceSignal reports whether tool_choice is a nonempty string or map.
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
