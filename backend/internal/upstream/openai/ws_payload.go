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

// ws_payload.go constructs WebSocket replay payloads and checks continuation
// identity, request compatibility, and tool-call context.
// WebSocket continuation can carry delta input tied to a previous response or a
// self-contained replay for another provider. Replay assembly compares input
// prefixes, removes unanswered historical calls, and verifies that tool outputs
// have local call context before an anchor-free retry is eligible. Raw item
// slices share immutable request bytes to keep repeated full-history replay
// from copying each item.
//
// Continuation checks compare the expected response ID and normalized non-input
// context. Per-turn metadata and generate=false warmup flags are excluded,
// while semantic fields remain. A tool-output signal keeps its supplied anchor
// before those ordinary checks. JSON comparison uses ordinary float64 decoding,
// so large numbers can compare after rounding. Retry trimming removes optional
// include after the first attempt while keeping session/cache and semantic
// request fields.

// WSMaxPrevResponseIDDeletePasses bounds duplicate-anchor deletion work and
// permits callers to detect incomplete removal after repeated successful raw
// patches.
const WSMaxPrevResponseIDDeletePasses = 8

// WSPreviousTurnStrictState stores normalized non-input context for later
// ingress continuation checks.
type WSPreviousTurnStrictState struct {
	// nonInputComparable is private normalized context owned by this state; later
	// checks read it unchanged so one prior turn supplies consistent comparison
	// bytes.
	nonInputComparable []byte
}

// SetOpenAIWSTurnMetadata installs trimmed turn metadata on a nonempty decoded
// request. Blank metadata is ignored. Existing map[string]any metadata is
// mutated, map[string]string is copied to the generic representation, and other
// values are replaced; callers serialize access to the supplied maps.
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

// ShouldForceNewConnOnStoreDisabled applies the selected connection mode to the
// last failure reason. Off returns false; adaptive recognizes selected failure
// labels after removing prewarm_, and all other modes return true. Mode
// spelling is compared as supplied.
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

// DropPreviousResponseIDFromRawPayload removes up to the configured maximum
// duplicate previous_response_id fields using raw JSON patches. Missing fields
// return changed=false. Its boolean reports complete removal, so callers check
// it before treating the returned body as anchor-free.
func DropPreviousResponseIDFromRawPayload(payload []byte) ([]byte, bool, error) {
	return DropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, sjson.DeleteBytes)
}

// DropPreviousResponseIDFromRawPayloadWithDeleteFn removes repeated anchor
// fields through a supplied patch operation, using sjson.DeleteBytes when nil.
// Empty or initially missing anchors return unchanged, and a patch error
// returns the supplied payload.
//
// The pass limit bounds duplicate-key work or a delete function that makes no
// progress. When the limit is reached with another anchor present, the return
// can contain successful earlier deletions while removed=false and err=nil.
// Callers therefore use the removal boolean, rather than byte changes alone, to
// decide whether continuation has been detached.
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

	// Delete repeated raw keys up to the pass budget; publishing callers require
	// complete removal before installing a replacement anchor.
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

// SetPreviousResponseIDToRawPayload writes a trimmed nonblank anchor into a
// nonempty payload, first using a raw patch. Blank anchors or empty bodies
// return unchanged. If the patch fails, ordinary JSON decode/re-encode supplies
// an alternative.
//
// That alternative decodes numbers through float64 and can round unrelated
// numeric fields. A decode failure returns the initial patch error; a rebuild
// failure returns its own error. The caller publishes successful returned bytes
// and retains its request on error, keeping a failed representation change out
// of the next turn.
func SetPreviousResponseIDToRawPayload(payload []byte, previousResponseID string) ([]byte, error) {
	normalizedPrevID := strings.TrimSpace(previousResponseID)
	if len(payload) == 0 || normalizedPrevID == "" {
		return payload, nil
	}
	updated, err := sjson.SetBytes(payload, "previous_response_id", normalizedPrevID)
	if err == nil {
		return updated, nil
	}

	// Raw insertion failed; rebuild through ordinary float64 decoding as an
	// alternative, allowing unrelated large numeric values to change
	// representation.
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

// ShouldInferIngressFunctionCallOutputPreviousResponseID permits inference on
// later store-disabled turns containing tool outputs with usable call IDs, no
// supplied anchor, and a nonblank expected anchor. Any actual tool-call context
// prevents inference here; complete output coverage is checked separately
// during retry construction.
//
// An item_reference alone does not provide that context: upstream still needs
// the expected prior response to resolve the call. The caller's continuation
// signals distinguish those representations before this helper makes its
// decision. A missing output call ID prevents inference because an anchor
// cannot establish which call the result belongs to.
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
	// Presence of actual calls suppresses inference here; an item_reference still
	// needs the store-disabled connection's expected anchor to resolve its remote
	// call.
	if signals.HasToolCallContext {
		return false
	}
	return strings.TrimSpace(expectedPreviousResponseID) != ""
}

// AlignStoreDisabledPreviousResponseID replaces a present mismatched anchor
// with the expected trimmed response ID. Empty payloads, blank expected IDs,
// missing anchors, and matching anchors return unchanged.
//
// Removal must eliminate every duplicate before the new value is inserted,
// preventing an earlier duplicate from winning subsequent raw lookups. A failed
// deletion, incomplete removal, or failed insertion returns the supplied
// payload with changed=false. The caller publishes the replacement only after
// both operations succeed; it does not retain a partially detached body.
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

	// Remove all duplicate anchor spellings before insertion; otherwise gjson
	// could continue reading a stale earlier field after alignment.
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

// CombineOpenAIWSReplayItems combines slice headers while sharing immutable
// item bytes. A nonempty delta allocates a result slice; an empty delta returns
// history directly. Callers copy item bytes before any mutation that could
// affect another replay owner.
func CombineOpenAIWSReplayItems(history, delta []json.RawMessage) []json.RawMessage {
	if len(delta) == 0 {
		return history
	}
	combined := make([]json.RawMessage, 0, len(history)+len(delta))
	combined = append(combined, history...)
	return append(combined, delta...)
}

// OpenAIWSPayloadStringView returns a string sharing payload storage for
// zero-copy gjson lookups. Keep payload immutable for the lifetime of this view
// and every replay item derived from it; a later byte mutation can change
// already retained JSON.
func OpenAIWSPayloadStringView(payload []byte) string {
	return unsafe.String(unsafe.SliceData(payload), len(payload))
}

// OpenAIWSRawMessageFromResult returns a parent subslice when a positive index,
// bounds, and raw-byte match identify that value. Other cases copy value.Raw.
// Callers keep parent immutable while any returned message sharing that storage
// remains in use.
func OpenAIWSRawMessageFromResult(parent []byte, value gjson.Result) json.RawMessage {
	idx := value.Index
	if idx > 0 && idx+len(value.Raw) <= len(parent) && string(parent[idx:idx+len(value.Raw)]) == value.Raw {
		return json.RawMessage(parent[idx : idx+len(value.Raw)])
	}
	return json.RawMessage(value.Raw)
}

// NormalizeOpenAIWSJSONForCompare trims and decodes JSON, then marshals it for
// structural comparison. Empty or invalid JSON returns an error. Numbers are
// decoded as float64, so distinct large integer spellings can converge after
// rounding.
//
// Map serialization removes key-order differences while array ordering remains
// significant. The output is a comparison representation, not a lossless
// replacement request. Callers requiring exact numeric identity use raw-byte
// equality first or select a number-preserving representation in their own
// policy.
func NormalizeOpenAIWSJSONForCompare(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("json is empty")
	}
	// Rebuild decoded objects for structural equality while retaining array order;
	// ordinary float64 decoding can merge distinct large integer values here.
	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

// NormalizeOpenAIWSJSONForCompareOrRaw returns normalized JSON when decoding
// succeeds and trimmed supplied bytes on failure. The latter can share its
// input storage. Callers keep comparison inputs immutable and decide whether
// malformed equal raw values are acceptable.
func NormalizeOpenAIWSJSONForCompareOrRaw(raw []byte) []byte {
	normalized, err := NormalizeOpenAIWSJSONForCompare(raw)
	if err != nil {
		// A normalization failure retains trimmed raw comparison bytes, allowing
		// malformed identical items to remain comparable without a reconstruction
		// error.
		return bytes.TrimSpace(raw)
	}
	return normalized
}

// NormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID builds comparison
// JSON after removing input, anchor, client_metadata, and stream_options. Empty
// or undecodable payloads return errors. Ordinary decoding uses float64
// numbers.
//
// Generate=false is also removed because a Codex warmup can precede a business
// request omitting generate; true remains a meaningful difference. Semantic
// fields such as model, tools, instructions, reasoning, and storage stay in the
// comparison. This result is used for continuation eligibility rather than as a
// replacement payload for upstream execution.
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
	// Transport metadata changes on each response.create, so remove it from
	// context comparison while retaining model, tools, instructions, and other
	// request fields.
	delete(decoded, "client_metadata")
	delete(decoded, "stream_options")
	// A prewarm with generate=false and a later omitted generate describe
	// equivalent context; generate=true remains a meaningful execution difference.
	if generate, ok := decoded["generate"].(bool); ok && !generate {
		delete(decoded, "generate")
	}
	return json.Marshal(decoded)
}

// OpenAIWSExtractNormalizedInputSequence exposes input as raw replay items plus
// a field-presence flag. Missing input returns exists=false; present empty
// arrays remain distinguishable. Keep payload immutable while returned items
// share its bytes.
//
// Arrays receive whole-array JSON validation because gjson can inspect
// truncated data. Other JSON values become one raw item, and strings become one
// JSON string scalar rather than a user-message object. The helper validates
// the array representation it stores; it does not establish that every
// scalar/object or the complete enclosing request is protocol-valid.
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
		// A Go string has a supported JSON representation; encode it as one scalar
		// input item while retaining its decoded text content.
		encoded, _ := json.Marshal(inputValue.String())
		return []json.RawMessage{encoded}, true, nil
	}
	return []json.RawMessage{OpenAIWSRawMessageFromResult(payload, inputValue)}, true, nil
}

// OpenAIWSInputIsPrefixExtended compares the prior and current input sequences
// after extraction and JSON normalization. Extraction errors propagate. Both
// absent inputs match, and one absent input matches only an empty sequence on
// the other side.
//
// Current input must be at least as long and retain every prior item in order.
// Comparison ignores object key encoding and ordering differences through ordinary
// float64 decoding, falling back to trimmed bytes when normalization fails.
// This helper checks input growth; the separate ingress continuation check
// examines anchor and non-input context.
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

// OpenAIWSRawItemsHasPrefix reports whether items begins with every supplied
// prefix item in order. An empty prefix matches, and a shorter items slice
// fails. Identical trimmed bytes bypass JSON normalization.
//
// That direct path keeps repeated full-history input comparisons from
// rebuilding unchanged items. Differing encodings are compared using ordinary
// JSON normalization, or trimmed raw bytes when normalization fails. Large
// numbers can therefore match after float64 rounding on the normalized path,
// and malformed but identical raw values can also match.
func OpenAIWSRawItemsHasPrefix(items []json.RawMessage, prefix []json.RawMessage) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(items) < len(prefix) {
		return false
	}
	for idx := range prefix {
		// Identical retained history bytes need no JSON rebuild, keeping growing
		// replay comparisons proportional to byte comparison on their common path.
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

// OpenAIWSRawItemsHasFunctionCallOutput recognizes protocol-defined tool-output
// item types in raw replay items.
func OpenAIWSRawItemsHasFunctionCallOutput(items []json.RawMessage) bool {
	for _, item := range items {
		if wire.IsCodexToolCallOutputItemType(gjson.GetBytes(item, "type").String()) {
			return true
		}
	}
	return false
}

// OpenAIWSRawItemsHaveToolCallContextForOutputs requires at least one tool
// output and call context, with every output's nonblank call ID represented by
// context. Missing IDs fail immediately. Input items remain unchanged.
//
// Independent call-context and output observations are collected before
// coverage is checked, allowing their order to vary in replay history. Extra
// context entries are accepted, while an output without its paired context
// makes the anchor-free sequence ineligible. The shared protocol type
// predicates decide which item kinds participate in that identity relationship.
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

// SanitizeOpenAIWSHistoricalReplayToolCalls removes historical call-context
// items with no matching output in either history or current input. Empty
// history returns directly. Surviving raw item bytes remain shared and need
// immutable owners.
//
// Both sequences contribute output IDs before historical calls are filtered, so
// a current tool reply can retain its earlier call. Missing call IDs do not
// acquire a pair and are removed. Current items themselves are not filtered
// here; the retry builder later verifies whether their outputs have sufficient
// retained context after continuation is removed.
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

// OpenAIWSRawPayloadHasToolCallOutput recognizes protocol-defined tool outputs
// in array or object input. Empty payloads and other input representations
// return false. This presence probe does not validate call IDs or establish
// full replay context coverage.
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

// BuildOpenAIWSReplayInputSequenceFromItems chooses current input or joins it
// to retained history for an anchored continuation. History participates only
// when both the anchor and previous-input presence flag are set. Returned
// slices share immutable item bytes.
//
// Historical unanswered calls are removed before prefix comparison. Missing or
// empty current input uses the cleaned history; already complete current
// history returns directly. Otherwise a new slice concatenates cleaned history
// and the delta, keeping the presence flag true even when the resulting
// sequence is empty. Callers retain all shared request storage for the result's
// lifetime.
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
	// Remove historical calls lacking answers across both sequences before prefix
	// matching, so anchor-free replay does not send those unfinished calls.
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

// BuildOpenAIWSReplayInputSequence extracts current raw input, then delegates
// history selection and merging to the item-based builder. Extraction errors
// propagate. Returned item bytes can share currentPayload and previous history;
// keep both immutable while replay storage uses them.
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

// SetOpenAIWSPayloadInputSequence replaces raw input when fullInputExists is
// true, encoding a nil slice as an empty array. An absent full-input
// observation leaves the payload unchanged. Invalid raw items or patch failures
// return an error for caller recovery.
func SetOpenAIWSPayloadInputSequence(
	payload []byte,
	fullInput []json.RawMessage,
	fullInputExists bool,
) ([]byte, error) {
	if !fullInputExists {
		return payload, nil
	}
	// A present input observation remains an array even when its slice is nil,
	// distinguishing empty history from JSON null upstream.
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

// BuildOpenAIWSCurrentTurnRetryPayload constructs an anchor-free request for
// another provider from known replay input and an optional original model.
// Missing full input returns eligible=false. Encoding and patch errors prevent
// an eligible replacement.
//
// After installing full input and removing continuation, the builder restores
// the original model before examining tool-output coverage. Every recognized
// output needs its call context in the resulting request; otherwise the
// function returns no eligible payload. This prevents another provider from
// receiving delta results that depend on an inaccessible connection-local
// response.
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
	// The other provider cannot resolve the old connection's response; detach its
	// anchor after full local input has been installed.
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

// ShouldKeepIngressPreviousResponseID checks whether ordinary ingress
// continuation matches the last response ID and previous non-input context. It
// returns a diagnostic reason, with normalization errors reported separately.
// Tool-output presence returns keep=true before those checks.
//
// For ordinary input, model, tools, instructions, and other retained context
// must compare equal after transport metadata and warmup fields are removed.
// Input-prefix growth is not tested here, so delta input can keep its anchor.
// Callers use the tool-output fast path according to their independently
// derived signals and handle replay coverage elsewhere.
func ShouldKeepIngressPreviousResponseID(
	previousPayload []byte,
	currentPayload []byte,
	lastTurnResponseID string,
	hasFunctionCallOutput bool,
) (bool, string, error) {
	// Tool-output continuation signals retain their supplied anchor before
	// ordinary identity checks; replay-context eligibility is evaluated by
	// separate recovery code.
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

// BuildOpenAIWSIngressPreviousTurnStrictState normalizes a prior payload once
// for repeated continuation checks. Empty payloads return a nil state, and
// normalization errors propagate. The resulting comparison bytes are stored
// privately and treated as immutable by later checks.
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

// ShouldKeepIngressPreviousResponseIDWithStrictState compares a current payload
// against cached non-input context and the expected prior response ID. A nil
// state means prior context is missing; normalization failures return the
// specific reason and error.
//
// Tool-output presence retains the supplied anchor before identity and context
// checks, matching the uncached entry point. Ordinary input can be a delta
// because only non-input fields are compared. Reusing the cached bytes avoids
// decoding prior context on every check while applying the same
// warmup/transport exclusions and float64 numeric comparison.
func ShouldKeepIngressPreviousResponseIDWithStrictState(
	previousState *WSPreviousTurnStrictState,
	currentPayload []byte,
	lastTurnResponseID string,
	hasFunctionCallOutput bool,
) (bool, string, error) {
	// Tool-output continuation signals retain their supplied anchor before
	// ordinary identity checks; replay-context eligibility is evaluated by
	// separate recovery code.
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

// WSPayloadStringFromRaw returns a trimmed gjson string value or empty text for
// blank payload/key. The key is a gjson path, and non-string JSON values follow
// gjson's String conversion rather than a type assertion.
func WSPayloadStringFromRaw(payload []byte, key string) string {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, key).String())
}

// dropWSRetryPayloadKey deletes a present key and appends its name to supplied
// removal storage. Blank requests or keys are ignored. Pass a nonnil removal
// pointer when a key can be present, and serialize access to the request map.
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

// ApplyWSRetryPayloadStrategy removes include after the first WebSocket attempt
// and reports the strategy plus sorted removed names. Empty requests use empty;
// first attempts and requests with nothing removed use full.
//
// Trimming leaves instructions, tools, reasoning, text, and prompt_cache_key
// available because they retain request and session identity across retries.
// The supplied map is mutated in place, and removed names describe actual
// deletions rather than the attempt number alone. Callers keep this
// optional-field strategy separate from encrypted-history recovery and
// cross-provider replay eligibility.
func ApplyWSRetryPayloadStrategy(payload map[string]any, attempt int) (strategy string, removedKeys []string) {
	if len(payload) == 0 {
		return "empty", nil
	}
	if attempt <= 1 {
		return "full", nil
	}

	removed := make([]string, 0, 2)
	// Reduce optional replay content after the initial attempt while retaining
	// cache/session identity and the request fields determining generated output.
	if attempt >= 2 {
		dropWSRetryPayloadKey(payload, "include", &removed)
	}

	if len(removed) == 0 {
		return "full", nil
	}
	sort.Strings(removed)
	return "trim_optional_fields", removed
}
