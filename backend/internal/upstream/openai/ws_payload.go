package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const WSMaxPrevResponseIDDeletePasses = 8

type WSPreviousTurnStrictState struct {
	nonInputComparable []byte
}

func SetOpenAIWSTurnMetadata(payload map[string]any, turnMetadata string) {
	if len(payload) == 0 {
		return
	}
	metadata := strings.TrimSpace(turnMetadata)
	if metadata == "" {
		return
	}

	switch existing := payload["client_metadata"].(type) {
	case map[string]any:
		existing[WSTurnMetadataHeader] = metadata
		payload["client_metadata"] = existing
	case map[string]string:
		next := make(map[string]any, len(existing)+1)
		for k, v := range existing {
			next[k] = v
		}
		next[WSTurnMetadataHeader] = metadata
		payload["client_metadata"] = next
	default:
		payload["client_metadata"] = map[string]any{
			WSTurnMetadataHeader: metadata,
		}
	}
}

func ShouldForceNewConnOnStoreDisabled(mode, lastFailureReason string) bool {
	switch mode {
	case "off":
		return false
	case "adaptive":
		reason := strings.TrimPrefix(strings.TrimSpace(lastFailureReason), "prewarm_")
		switch reason {
		case "policy_violation", "message_too_big", "auth_failed", "write_request", "write":
			return true
		default:
			return false
		}
	default:
		return true
	}
}

func DropPreviousResponseIDFromRawPayload(payload []byte) ([]byte, bool, error) {
	return DropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, sjson.DeleteBytes)
}

func DropPreviousResponseIDFromRawPayloadWithDeleteFn(
	payload []byte,
	deleteFn func([]byte, string) ([]byte, error),
) ([]byte, bool, error) {
	if len(payload) == 0 {
		return payload, false, nil
	}
	if !gjson.GetBytes(payload, "previous_response_id").Exists() {
		return payload, false, nil
	}
	if deleteFn == nil {
		deleteFn = sjson.DeleteBytes
	}

	updated := payload
	for i := 0; i < WSMaxPrevResponseIDDeletePasses &&
		gjson.GetBytes(updated, "previous_response_id").Exists(); i++ {
		next, err := deleteFn(updated, "previous_response_id")
		if err != nil {
			return payload, false, err
		}
		updated = next
	}
	return updated, !gjson.GetBytes(updated, "previous_response_id").Exists(), nil
}

func SetPreviousResponseIDToRawPayload(payload []byte, previousResponseID string) ([]byte, error) {
	normalizedPrevID := strings.TrimSpace(previousResponseID)
	if len(payload) == 0 || normalizedPrevID == "" {
		return payload, nil
	}
	updated, err := sjson.SetBytes(payload, "previous_response_id", normalizedPrevID)
	if err == nil {
		return updated, nil
	}

	var reqBody map[string]any
	if unmarshalErr := json.Unmarshal(payload, &reqBody); unmarshalErr != nil {
		return nil, err
	}
	reqBody["previous_response_id"] = normalizedPrevID
	rebuilt, marshalErr := json.Marshal(reqBody)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return rebuilt, nil
}

func ShouldInferIngressFunctionCallOutputPreviousResponseID(
	storeDisabled bool,
	turn int,
	signals wire.ToolContinuationSignals,
	currentPreviousResponseID string,
	expectedPreviousResponseID string,
) bool {
	if !storeDisabled || turn <= 1 || !signals.HasFunctionCallOutput {
		return false
	}
	if strings.TrimSpace(currentPreviousResponseID) != "" {
		return false
	}
	if signals.HasFunctionCallOutputMissingCallID {
		return false
	}
	// A payload with the actual tool-call context is self-contained and needs no inferred anchor.
	// An item_reference alone still needs a valid previous response ID on the store=false
	// WebSocket path so upstream can resolve the referenced function_call.
	if signals.HasToolCallContext {
		return false
	}
	return strings.TrimSpace(expectedPreviousResponseID) != ""
}

func AlignStoreDisabledPreviousResponseID(
	payload []byte,
	expectedPreviousResponseID string,
) ([]byte, bool, error) {
	if len(payload) == 0 {
		return payload, false, nil
	}
	expected := strings.TrimSpace(expectedPreviousResponseID)
	if expected == "" {
		return payload, false, nil
	}
	current := WSPayloadStringFromRaw(payload, "previous_response_id")
	if current == "" || current == expected {
		return payload, false, nil
	}

	withoutPrev, removed, dropErr := DropPreviousResponseIDFromRawPayload(payload)
	if dropErr != nil {
		return payload, false, dropErr
	}
	if !removed {
		return payload, false, nil
	}
	updated, setErr := SetPreviousResponseIDToRawPayload(withoutPrev, expected)
	if setErr != nil {
		return payload, false, setErr
	}
	return updated, true, nil
}

// CombineOpenAIWSReplayItems shares item bytes, allocating a slice when delta is nonempty; otherwise it returns history.
func CombineOpenAIWSReplayItems(history, delta []json.RawMessage) []json.RawMessage {
	if len(delta) == 0 {
		return history
	}
	combined := make([]json.RawMessage, 0, len(history)+len(delta))
	combined = append(combined, history...)
	return append(combined, delta...)
}

// OpenAIWSPayloadStringView returns a string sharing payload's backing array for gjson.Get.
// Using gjson.GetBytes would copy the result's Raw bytes, which can span most of input.
// The caller must keep payload immutable for the lifetime of the returned string
// and any replay items that share its bytes.
func OpenAIWSPayloadStringView(payload []byte) string {
	return unsafe.String(unsafe.SliceData(payload), len(payload))
}

// OpenAIWSRawMessageFromResult returns a matching subslice of parent when its index is valid, or a copy otherwise.
// The caller must keep parent immutable while the returned message shares its bytes.
func OpenAIWSRawMessageFromResult(parent []byte, value gjson.Result) json.RawMessage {
	idx := value.Index
	if idx > 0 && idx+len(value.Raw) <= len(parent) && string(parent[idx:idx+len(value.Raw)]) == value.Raw {
		return json.RawMessage(parent[idx : idx+len(value.Raw)])
	}
	return json.RawMessage(value.Raw)
}

func NormalizeOpenAIWSJSONForCompare(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("json is empty")
	}
	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

func NormalizeOpenAIWSJSONForCompareOrRaw(raw []byte) []byte {
	normalized, err := NormalizeOpenAIWSJSONForCompare(raw)
	if err != nil {
		return bytes.TrimSpace(raw)
	}
	return normalized
}

func NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("payload is empty")
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	delete(decoded, "input")
	delete(decoded, "previous_response_id")
	// Codex updates transport metadata per response.create; it does not change the referenced response context.
	delete(decoded, "client_metadata")
	delete(decoded, "stream_options")
	// Codex prewarms with generate=false and omits it on later requests. Ignore false for comparison, but retain true.
	if generate, ok := decoded["generate"].(bool); ok && !generate {
		delete(decoded, "generate")
	}
	return json.Marshal(decoded)
}

// OpenAIWSExtractNormalizedInputSequence extracts input items, sharing payload bytes when possible.
// The caller must keep payload immutable while the returned items remain in use.
func OpenAIWSExtractNormalizedInputSequence(payload []byte) ([]json.RawMessage, bool, error) {
	if len(payload) == 0 {
		return nil, false, nil
	}
	inputValue := gjson.Get(OpenAIWSPayloadStringView(payload), "input")
	if !inputValue.Exists() {
		return nil, false, nil
	}
	if inputValue.Type == gjson.JSON {
		if inputValue.IsArray() {
			// gjson accepts partial JSON. Validate the whole array before adding its items
			// to replay history so a truncated payload cannot become stored history.
			arrayRaw := OpenAIWSRawMessageFromResult(payload, inputValue)
			if !json.Valid(arrayRaw) {
				return nil, true, errors.New("input array json is invalid")
			}
			elems := inputValue.Array()
			items := make([]json.RawMessage, 0, len(elems))
			for _, elem := range elems {
				items = append(items, OpenAIWSRawMessageFromResult(payload, elem))
			}
			return items, true, nil
		}
		return []json.RawMessage{OpenAIWSRawMessageFromResult(payload, inputValue)}, true, nil
	}
	if inputValue.Type == gjson.String {
		encoded, _ := json.Marshal(inputValue.String())
		return []json.RawMessage{encoded}, true, nil
	}
	return []json.RawMessage{OpenAIWSRawMessageFromResult(payload, inputValue)}, true, nil
}

func OpenAIWSInputIsPrefixExtended(previousPayload, currentPayload []byte) (bool, error) {
	previousItems, previousExists, prevErr := OpenAIWSExtractNormalizedInputSequence(previousPayload)
	if prevErr != nil {
		return false, prevErr
	}
	currentItems, currentExists, currentErr := OpenAIWSExtractNormalizedInputSequence(currentPayload)
	if currentErr != nil {
		return false, currentErr
	}
	if !previousExists && !currentExists {
		return true, nil
	}
	if !previousExists {
		return len(currentItems) == 0, nil
	}
	if !currentExists {
		return len(previousItems) == 0, nil
	}
	if len(currentItems) < len(previousItems) {
		return false, nil
	}

	for idx := range previousItems {
		previousNormalized := NormalizeOpenAIWSJSONForCompareOrRaw(previousItems[idx])
		currentNormalized := NormalizeOpenAIWSJSONForCompareOrRaw(currentItems[idx])
		if !bytes.Equal(previousNormalized, currentNormalized) {
			return false, nil
		}
	}
	return true, nil
}

func OpenAIWSRawItemsHasPrefix(items []json.RawMessage, prefix []json.RawMessage) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(items) < len(prefix) {
		return false
	}
	for idx := range prefix {
		// Compare identical replays directly to avoid decoding and re-encoding the full history.
		if bytes.Equal(bytes.TrimSpace(prefix[idx]), bytes.TrimSpace(items[idx])) {
			continue
		}
		previousNormalized := NormalizeOpenAIWSJSONForCompareOrRaw(prefix[idx])
		currentNormalized := NormalizeOpenAIWSJSONForCompareOrRaw(items[idx])
		if !bytes.Equal(previousNormalized, currentNormalized) {
			return false
		}
	}
	return true
}

func OpenAIWSRawItemsHasFunctionCallOutput(items []json.RawMessage) bool {
	for _, item := range items {
		if wire.IsCodexToolCallOutputItemType(gjson.GetBytes(item, "type").String()) {
			return true
		}
	}
	return false
}

// OpenAIWSRawItemsHaveToolCallContextForOutputs reports whether every output has matching call context.
func OpenAIWSRawItemsHaveToolCallContextForOutputs(items []json.RawMessage) bool {
	if len(items) == 0 {
		return false
	}
	contextCallIDs := make(map[string]struct{})
	outputCallIDs := make(map[string]struct{})
	for _, item := range items {
		itemType := gjson.GetBytes(item, "type").String()
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		switch {
		case wire.IsCodexToolCallContextItemType(itemType):
			if callID != "" {
				contextCallIDs[callID] = struct{}{}
			}
		case wire.IsCodexToolCallOutputItemType(itemType):
			if callID == "" {
				return false
			}
			outputCallIDs[callID] = struct{}{}
		}
	}
	if len(outputCallIDs) == 0 || len(contextCallIDs) == 0 {
		return false
	}
	for callID := range outputCallIDs {
		if _, ok := contextCallIDs[callID]; !ok {
			return false
		}
	}
	return true
}

// SanitizeOpenAIWSHistoricalReplayToolCalls drops historical calls with no matching output
// in either sequence, so replay after removing previous_response_id has no unanswered calls.
// The returned slice shares item bytes with previousItems, which callers must keep immutable.
func SanitizeOpenAIWSHistoricalReplayToolCalls(
	previousItems []json.RawMessage,
	currentItems []json.RawMessage,
) []json.RawMessage {
	if len(previousItems) == 0 {
		return previousItems
	}
	outputCallIDs := make(map[string]struct{})
	collectOutputCallIDs := func(items []json.RawMessage) {
		for _, item := range items {
			if !wire.IsCodexToolCallOutputItemType(gjson.GetBytes(item, "type").String()) {
				continue
			}
			if callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String()); callID != "" {
				outputCallIDs[callID] = struct{}{}
			}
		}
	}
	collectOutputCallIDs(previousItems)
	collectOutputCallIDs(currentItems)

	sanitized := make([]json.RawMessage, 0, len(previousItems))
	for _, item := range previousItems {
		if wire.IsCodexToolCallContextItemType(gjson.GetBytes(item, "type").String()) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if _, paired := outputCallIDs[callID]; !paired {
				continue
			}
		}
		sanitized = append(sanitized, item)
	}
	return sanitized
}

// OpenAIWSRawPayloadHasToolCallOutput reports whether a response.create input includes tool output.
func OpenAIWSRawPayloadHasToolCallOutput(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	input := gjson.Get(OpenAIWSPayloadStringView(payload), "input")
	if !input.Exists() {
		return false
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			if wire.IsCodexToolCallOutputItemType(item.Get("type").String()) {
				return true
			}
		}
		return false
	}
	if input.Type == gjson.JSON {
		return wire.IsCodexToolCallOutputItemType(input.Get("type").String())
	}
	return false
}

// BuildOpenAIWSReplayInputSequenceFromItems combines parsed current input with replay history.
// It shares item bytes with both inputs and may return an input slice directly.
// Callers must keep the shared bytes immutable while the result is in use.
func BuildOpenAIWSReplayInputSequenceFromItems(
	previousFullInput []json.RawMessage,
	previousFullInputExists bool,
	currentItems []json.RawMessage,
	currentExists bool,
	hasPreviousResponseID bool,
) ([]json.RawMessage, bool) {
	if !hasPreviousResponseID || !previousFullInputExists {
		return currentItems, currentExists
	}
	previousFullInput = SanitizeOpenAIWSHistoricalReplayToolCalls(previousFullInput, currentItems)
	if !currentExists || len(currentItems) == 0 {
		return previousFullInput, true
	}
	if OpenAIWSRawItemsHasPrefix(currentItems, previousFullInput) {
		return currentItems, true
	}
	merged := make([]json.RawMessage, 0, len(previousFullInput)+len(currentItems))
	merged = append(merged, previousFullInput...)
	merged = append(merged, currentItems...)
	return merged, true
}

func BuildOpenAIWSReplayInputSequence(
	previousFullInput []json.RawMessage,
	previousFullInputExists bool,
	currentPayload []byte,
	hasPreviousResponseID bool,
) ([]json.RawMessage, bool, error) {
	currentItems, currentExists, currentErr := OpenAIWSExtractNormalizedInputSequence(currentPayload)
	if currentErr != nil {
		return nil, false, currentErr
	}
	items, exists := BuildOpenAIWSReplayInputSequenceFromItems(
		previousFullInput,
		previousFullInputExists,
		currentItems,
		currentExists,
		hasPreviousResponseID,
	)
	return items, exists, nil
}

func SetOpenAIWSPayloadInputSequence(
	payload []byte,
	fullInput []json.RawMessage,
	fullInputExists bool,
) ([]byte, error) {
	if !fullInputExists {
		return payload, nil
	}
	// Encode present but empty input as an array, not null.
	inputForMarshal := fullInput
	if inputForMarshal == nil {
		inputForMarshal = []json.RawMessage{}
	}
	inputRaw, marshalErr := json.Marshal(inputForMarshal)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return sjson.SetRawBytes(payload, "input", inputRaw)
}

// BuildOpenAIWSCurrentTurnRetryPayload builds a self-contained request for another provider.
// It returns an eligible payload only when replay input covers every tool output's call context.
func BuildOpenAIWSCurrentTurnRetryPayload(
	payload []byte,
	fullInput []json.RawMessage,
	fullInputExists bool,
	originalModel string,
) ([]byte, bool, error) {
	if !fullInputExists {
		return nil, false, nil
	}
	retryPayload, err := SetOpenAIWSPayloadInputSequence(payload, fullInput, true)
	if err != nil {
		return nil, false, err
	}
	retryPayload = wire.RemovePreviousResponseIDFromBody(retryPayload)
	if model := strings.TrimSpace(originalModel); model != "" {
		retryPayload, err = sjson.SetBytes(retryPayload, "model", model)
		if err != nil {
			return nil, false, err
		}
	}
	coverage := wire.AnalyzeToolCallOutputContextCoverageBytes(retryPayload)
	if coverage.HasFunctionCallOutput && !coverage.ContextCoversAllCallIDs {
		return nil, false, nil
	}
	return retryPayload, true, nil
}

func ShouldKeepIngressPreviousResponseID(
	previousPayload []byte,
	currentPayload []byte,
	lastTurnResponseID string,
	hasFunctionCallOutput bool,
) (bool, string, error) {
	if hasFunctionCallOutput {
		return true, "has_function_call_output", nil
	}
	currentPreviousResponseID := strings.TrimSpace(WSPayloadStringFromRaw(currentPayload, "previous_response_id"))
	if currentPreviousResponseID == "" {
		return false, "missing_previous_response_id", nil
	}
	expectedPreviousResponseID := strings.TrimSpace(lastTurnResponseID)
	if expectedPreviousResponseID == "" {
		return false, "missing_last_turn_response_id", nil
	}
	if currentPreviousResponseID != expectedPreviousResponseID {
		return false, "previous_response_id_mismatch", nil
	}
	if len(previousPayload) == 0 {
		return false, "missing_previous_turn_payload", nil
	}

	previousComparable, previousComparableErr := NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(previousPayload)
	if previousComparableErr != nil {
		return false, "non_input_compare_error", previousComparableErr
	}
	currentComparable, currentComparableErr := NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(currentPayload)
	if currentComparableErr != nil {
		return false, "non_input_compare_error", currentComparableErr
	}
	if !bytes.Equal(previousComparable, currentComparable) {
		return false, "non_input_changed", nil
	}
	return true, "strict_incremental_ok", nil
}

func BuildOpenAIWSIngressPreviousTurnStrictState(payload []byte) (*WSPreviousTurnStrictState, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	nonInputComparable, nonInputErr := NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
	if nonInputErr != nil {
		return nil, nonInputErr
	}
	return &WSPreviousTurnStrictState{
		nonInputComparable: nonInputComparable,
	}, nil
}

func ShouldKeepIngressPreviousResponseIDWithStrictState(
	previousState *WSPreviousTurnStrictState,
	currentPayload []byte,
	lastTurnResponseID string,
	hasFunctionCallOutput bool,
) (bool, string, error) {
	if hasFunctionCallOutput {
		return true, "has_function_call_output", nil
	}
	currentPreviousResponseID := strings.TrimSpace(WSPayloadStringFromRaw(currentPayload, "previous_response_id"))
	if currentPreviousResponseID == "" {
		return false, "missing_previous_response_id", nil
	}
	expectedPreviousResponseID := strings.TrimSpace(lastTurnResponseID)
	if expectedPreviousResponseID == "" {
		return false, "missing_last_turn_response_id", nil
	}
	if currentPreviousResponseID != expectedPreviousResponseID {
		return false, "previous_response_id_mismatch", nil
	}
	if previousState == nil {
		return false, "missing_previous_turn_payload", nil
	}

	currentComparable, currentComparableErr := NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(currentPayload)
	if currentComparableErr != nil {
		return false, "non_input_compare_error", currentComparableErr
	}
	if !bytes.Equal(previousState.nonInputComparable, currentComparable) {
		return false, "non_input_changed", nil
	}
	return true, "strict_incremental_ok", nil
}

func WSPayloadStringFromRaw(payload []byte, key string) string {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, key).String())
}

// dropWSRetryPayloadKey deletes a field and records its name when present.
func dropWSRetryPayloadKey(payload map[string]any, key string, removed *[]string) {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return
	}
	if _, exists := payload[key]; !exists {
		return
	}
	delete(payload, key)
	*removed = append(*removed, key)
}

// ApplyWSRetryPayloadStrategy removes optional include fields on attempts after the first.
// It keeps prompt_cache_key because the key identifies the session.
func ApplyWSRetryPayloadStrategy(payload map[string]any, attempt int) (strategy string, removedKeys []string) {
	if len(payload) == 0 {
		return "empty", nil
	}
	if attempt <= 1 {
		return "full", nil
	}

	removed := make([]string, 0, 2)
	if attempt >= 2 {
		dropWSRetryPayloadKey(payload, "include", &removed)
	}

	if len(removed) == 0 {
		return "full", nil
	}
	sort.Strings(removed)
	return "trim_optional_fields", removed
}
