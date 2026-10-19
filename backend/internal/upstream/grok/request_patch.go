package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// request_patch.go prepares Grok request fields, tools, and replay history for
// the selected model.
// Grok Responses requests can contain client-tool declarations, replayed history
// and reasoning controls with shapes that differ from the selected upstream's
// accepted input. Preparation keeps the complete upstream model identity, adapts
// client tools with a mapping for response restoration, and applies capability
// lookup before filtering unsupported fields or tools.
//
// The order matters: tools carried by additional_tools are promoted before
// client-tool adaptation, Compact history becomes reasoning and summary input
// before model-input cleanup, and tool choice is checked against the
// declarations that survive. Recovery cleanup removes unusable encrypted state
// while retaining plaintext context and tool-output linkage when needed.
// Composer image conversion uses encounter order to pair description text with
// source images, and decoded-map helpers expose their mutation rules so shared
// request history is not reused accidentally.

// ComposerImageBridgeVisionModel and ComposerImageBridgeMaxOutputTokens choose
// the auxiliary vision target and bound each requested image description's
// output.
const (
	ComposerImageBridgeVisionModel     = "grok-build-0.1"
	ComposerImageBridgeMaxOutputTokens = 512

	// GrokSafeFunctionParameters supplies permissive object parameters when a
	// function's root union includes a branch without an explicit object type.
	GrokSafeFunctionParameters = `{"type":"object","properties":{},"additionalProperties":true}`
)

// These sets govern recursive removal of unsupported request keys and the exact
// tool types retained by Grok Responses filtering.
var (
	grokResponsesUnsupportedRecursiveFields = map[string]struct{}{
		"external_web_access": {},
	}

	grokResponsesSupportedToolTypes = map[string]struct{}{
		"code_execution": {},

		"code_interpreter": {},

		"collections_search": {},

		"file_search": {},

		"function": {},

		"mcp": {},

		"shell": {},

		"web_search": {},

		"x_search": {},
	}
)

// BodyCodec normalizes Grok request forms with injected IDs and model
// attributes.
type BodyCodec struct {
	// NewID is required when ConvertGrokResponseToOpenAICompact generates its
	// compaction identifier; regular request normalization can use the codec
	// without this callback.
	NewID func() string // compaction ID generator
	// Attributes receives the full upstream model identity. A nil callback returns
	// unknown attributes, retaining nonblank client effort values during
	// normalization.
	Attributes func(string) modelcatalog.Attributes // model attribute lookup
}

// GrokEncryptedContentStripRetriedKey identifies the request context's
// encrypted-state recovery marker.
type GrokEncryptedContentStripRetriedKey struct{}

// modelAttributes reads the injected attributes for the complete supplied model
// identity. A missing Attributes callback returns zero attributes, which
// reasoning normalization treats as unknown support and therefore retains
// nonblank client effort text.
func (m BodyCodec) modelAttributes(model string) modelcatalog.Attributes {
	if m.Attributes != nil {
		return m.Attributes(model)
	}
	return modelcatalog.Attributes{}
}

// IsGrokInvalidEncryptedContentResponse recognizes encrypted-state rejection in
// HTTP 400 or 422 bodies. It reads flat code first, then nested code, and
// selects explicit encrypted or compaction failure codes before message checks.
// Other known codes reject this recovery path.
//
// An invalid-argument code permits encrypted-content messages with decrypt or
// unmodified evidence. Missing code additionally requires decrypt wording, so an
// unrelated unmodified message cannot establish this failure. Selected flat,
// nested or plaintext message candidates support deployed envelope differences
// while keeping recovery tied to the request's encrypted state.
func (m BodyCodec) IsGrokInvalidEncryptedContentResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}

	// Flat and nested envelopes place the same recovery evidence in different
	// fields. Keep their selected code and message candidates available together.
	code := strings.TrimSpace(gjson.GetBytes(body, "code").String())
	errNode := gjson.GetBytes(body, "error")
	if code == "" && errNode.IsObject() {
		code = strings.TrimSpace(errNode.Get("code").String())
	}

	if strings.EqualFold(code, "invalid_encrypted_content") || strings.EqualFold(code, "invalid_compaction") || strings.EqualFold(code, "compaction_decode_error") {
		return true
	}
	// A populated unrelated code selects another failure category, so encrypted
	// message wording alone cannot override that code and start recovery.
	if !strings.EqualFold(code, "invalid-argument") && code != "" {
		return false
	}
	for _, candidate := range GrokStructuredErrorMessageCandidates(body) {
		normalizedMessage := strings.ToLower(candidate)
		// A missing code needs decrypt wording in the selected message; unmodified
		// text alone can describe a different request failure and cannot select
		// recovery.
		if code == "" && !strings.Contains(normalizedMessage, "decrypt") {
			continue
		}
		if strings.Contains(normalizedMessage, "encrypted_content") &&
			(strings.Contains(normalizedMessage, "decrypt") || strings.Contains(normalizedMessage, "unmodified")) {
			return true
		}
	}
	return false
}

// IsGrokCompactionReplayDecodeError recognizes HTTP 400 messages combining
// decode evidence with replay-context evidence. Empty bodies or other statuses
// return false. Callers use this narrower history-decoding signal when explicit
// encrypted-content rejection was not selected.
func (m BodyCodec) IsGrokCompactionReplayDecodeError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	for _, candidate := range GrokStructuredErrorMessageCandidates(body) {
		message := strings.ToLower(candidate)
		decodeSignal := strings.Contains(message, "decode") ||
			strings.Contains(message, "deserialize") ||
			strings.Contains(message, "decoder")
		replaySignal := strings.Contains(message, "compaction") ||
			strings.Contains(message, "summary") ||
			strings.Contains(message, "encrypted_content") ||
			strings.Contains(message, "response history")
		if decodeSignal && replaySignal {
			return true
		}
	}
	return false
}

// SanitizeGrokCompactionReplayBody prepares a changed payload for
// history-decoding recovery. Compact entries are converted before encrypted
// state and empty reasoning are removed. It returns the supplied body with false
// when the combined cleanup makes no change.
//
// A nonblank previous_response_id is dropped only when the decoded request has
// no function-call output, keeping outputs linked to their upstream
// continuation. That removal adds store=false only when store was absent.
// Re-encoding uses Number-valued decoded input, and failures stop recovery
// before a retry body is published.
func (m BodyCodec) SanitizeGrokCompactionReplayBody(body []byte) ([]byte, bool, error) {
	converted, err := m.ConvertOpenAICompactInputsForGrok(body)
	if err != nil {
		return nil, false, fmt.Errorf("convert Grok compaction replay: %w", err)
	}
	var requestBody map[string]any
	decoder := json.NewDecoder(bytes.NewReader(converted))
	decoder.UseNumber()
	if err := decoder.Decode(&requestBody); err != nil {
		return nil, false, err
	}

	changed := !bytes.Equal(converted, body)
	if protocolopenai.TrimEncryptedReasoningItems(requestBody) {
		changed = true
	}
	if m.DropEmptyGrokReplayReasoning(requestBody) {
		changed = true
	}
	// Function outputs can depend on previous response linkage. Drop that
	// identifier only for self-contained replay, adding store=false when storage
	// was unspecified.
	if previousID, _ := requestBody["previous_response_id"].(string); strings.TrimSpace(previousID) != "" && !protocolopenai.HasFunctionCallOutput(requestBody) {
		delete(requestBody, "previous_response_id")
		if _, exists := requestBody["store"]; !exists {
			requestBody["store"] = false
		}
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	retryBody, err := wirejson.Marshal(requestBody)
	if err != nil {
		return nil, false, err
	}
	return retryBody, true, nil
}

// DropEmptyGrokReplayReasoning removes empty reasoning entries from a decoded
// input array and reports a change. Filtering reuses its backing storage, so
// callers own the mutable map and slice. Any encrypted key, nonempty summary or
// nonnil content keeps an entry.
func (m BodyCodec) DropEmptyGrokReplayReasoning(requestBody map[string]any) bool {
	items, ok := requestBody["input"].([]any)
	if !ok {
		return false
	}
	// Reuse the decoded input's storage while removing empty reasoning; callers
	// must isolate aliases before this recovery filter rewrites positions.
	filtered := items[:0]
	changed := false
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(m.GrokStringValue(item["type"])) != "reasoning" {
			filtered = append(filtered, rawItem)
			continue
		}
		summary, _ := item["summary"].([]any)
		content, hasContent := item["content"]
		_, hasEncrypted := item["encrypted_content"]
		if hasEncrypted || len(summary) > 0 || (hasContent && content != nil) {
			filtered = append(filtered, rawItem)
			continue
		}
		changed = true
	}
	if changed {
		requestBody["input"] = filtered
	}
	return changed
}

// RequestHasGrokEncryptedReasoning reports a nonnull, nonblank encrypted_content
// value on reasoning input. Input may be an object or array. Compaction items
// are outside this predicate, although the retry-body trimmer separately
// recognizes their encrypted keys.
func (m BodyCodec) RequestHasGrokEncryptedReasoning(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	items := input.Array()
	if input.IsObject() {
		items = []gjson.Result{input}
	}
	for _, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		enc := item.Get("encrypted_content")
		if enc.Exists() && enc.Type != gjson.Null && strings.TrimSpace(enc.String()) != "" {
			return true
		}
	}
	return false
}

// MarkGrokEncryptedContentStripRetried returns a child context carrying the
// recovery-attempt marker. Callers propagate that context to later retry checks;
// inherited cancellation and values remain provided by the parent context passed
// to context.WithValue.
func (m BodyCodec) MarkGrokEncryptedContentStripRetried(ctx context.Context) context.Context {
	return context.WithValue(ctx, GrokEncryptedContentStripRetriedKey{}, true)
}

// GrokEncryptedContentStripRetried reads the bool recovery marker from ctx.
// Missing or differently typed values return false, allowing caller policy to
// distinguish an unmarked request from one that already attempted
// encrypted-state stripping.
func (m BodyCodec) GrokEncryptedContentStripRetried(ctx context.Context) bool {
	v, _ := ctx.Value(GrokEncryptedContentStripRetriedKey{}).(bool)
	return v
}

// TrimGrokInvalidEncryptedContentRetryBody removes encrypted reasoning or
// compaction state from decoded input. A matching encrypted key is needed before
// decoding; unchanged cleanup returns body with false. The shared trimmer can
// discard encrypted compaction objects and retain remaining plaintext context.
func (m BodyCodec) TrimGrokInvalidEncryptedContentRetryBody(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	items := input.Array()
	if input.IsObject() {
		items = []gjson.Result{input}
	}

	hasEncryptedReasoning := false
	for _, item := range items {
		if (strings.TrimSpace(item.Get("type").String()) == "reasoning" && item.Get("encrypted_content").Exists()) ||
			(protocolopenai.IsCompactionItemType(strings.TrimSpace(item.Get("type").String())) && item.Get("encrypted_content").Exists()) {
			hasEncryptedReasoning = true
			break
		}
	}
	if !hasEncryptedReasoning {
		return body, false, nil
	}

	var requestBody map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&requestBody); err != nil {
		return nil, false, err
	}
	if !protocolopenai.TrimEncryptedReasoningItems(requestBody) {
		return body, false, nil
	}

	retryBody, err := wirejson.Marshal(requestBody)
	if err != nil {
		return nil, false, err
	}
	return retryBody, true, nil
}

// PatchGrokResponsesBody applies the base model and compatibility patch sequence
// to a complete JSON body. Callers needing client-tool restoration use
// PatchGrokResponsesBodyWithClientTools, which also returns the mapping required
// by the response reader.
func (m BodyCodec) PatchGrokResponsesBody(body []byte, upstreamModel string) ([]byte, error) {
	return m.PatchGrokResponsesBodyBase(body, upstreamModel)
}

// PatchGrokResponsesBodyWithClientTools promotes carried tools, adapts client
// protocol shapes, and applies base Grok patching. It rejects invalid or
// trailing JSON documents and returns a mapping paired with the resulting body.
// Callers retain that mapping through response restoration; failed preparation
// returns no usable mapping.
func (m BodyCodec) PatchGrokResponsesBodyWithClientTools(body []byte, upstreamModel string) ([]byte, protocolbridge.ResponsesClientToolMapping, error) {
	if !json.Valid(body) {
		return nil, protocolbridge.ResponsesClientToolMapping{}, fmt.Errorf("invalid json request body")
	}
	promoted, err := m.SanitizeGrokResponsesInput(body)
	if err != nil {
		return nil, protocolbridge.ResponsesClientToolMapping{}, err
	}
	adapted, mapping, err := protocolbridge.AdaptResponsesClientToolsJSON(promoted, "Grok")
	if err != nil {
		return nil, protocolbridge.ResponsesClientToolMapping{}, err
	}
	patched, err := m.PatchGrokResponsesBodyBase(adapted, upstreamModel)
	if err != nil {
		return nil, protocolbridge.ResponsesClientToolMapping{}, err
	}
	return patched, mapping, nil
}

// PatchGrokResponsesBodyBase validates one complete JSON document and applies
// ordered model and compatibility normalization. Reasoning and model
// capabilities precede unsupported-field removal, Compact conversion, carried
// tools, model-input cleanup, redundant image tools, null cleanup and tool
// filtering. Callers configure any required codec callbacks before those paths
// execute.
func (m BodyCodec) PatchGrokResponsesBodyBase(body []byte, upstreamModel string) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid json request body")
	}
	// Start the mapped request from copied bytes so this preparation has its own
	// request storage while callers retain body for billing and replay decisions.
	out, err := sjson.SetBytes(append([]byte(nil), body...), "model", upstreamModel)
	if err != nil {
		return nil, err
	}
	out, err = m.NormalizeGrokResponsesReasoningEffort(out, upstreamModel)
	if err != nil {
		return nil, err
	}
	out, err = m.SanitizeGrokResponsesModelCapabilities(out, upstreamModel)
	if err != nil {
		return nil, err
	}
	// These request metadata controls are omitted for Grok forwarding, while the
	// selected model and later cache routing use their own effective request
	// fields.
	for _, unsupportedField := range []string{"prompt_cache_retention", "safety_identifier", "metadata"} {
		if gjson.GetBytes(out, unsupportedField).Exists() {
			out, err = sjson.DeleteBytes(out, unsupportedField)
			if err != nil {
				return nil, err
			}
		}
	}
	// Apply the 4.5 parameter exclusions after capability normalization so the
	// selected deployment receives neither penalty spelling nor its unsupported
	// stop control.
	if strings.EqualFold(upstreamModel, "grok-4.5") {
		for _, unsupportedField := range []string{"presence_penalty", "presencePenalty", "frequency_penalty", "frequencyPenalty", "stop"} {
			if gjson.GetBytes(out, unsupportedField).Exists() {
				out, err = sjson.DeleteBytes(out, unsupportedField)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	// The recognized 4.20 family drops both logprob controls together; keeping one
	// would still leave the unsupported request feature populated.
	if m.GrokModelRejectsLogprobs(upstreamModel) {
		for _, unsupportedField := range []string{"logprobs", "top_logprobs"} {
			if gjson.GetBytes(out, unsupportedField).Exists() {
				out, err = sjson.DeleteBytes(out, unsupportedField)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	out, err = m.SanitizeGrokResponsesUnsupportedFields(out)
	if err != nil {
		return nil, err
	}
	out, err = m.ConvertOpenAICompactInputsForGrok(out)
	if err != nil {
		return nil, err
	}
	out, err = m.SanitizeGrokResponsesInput(out)
	if err != nil {
		return nil, err
	}
	out, err = m.SanitizeGrokResponsesModelInput(out)
	if err != nil {
		return nil, err
	}
	out, err = m.StripRedundantGrokViewImageTool(out)
	if err != nil {
		return nil, err
	}
	out, err = m.SanitizeGrokReasoningNullContent(out)
	if err != nil {
		return nil, err
	}
	out, err = m.SanitizeGrokResponsesTools(out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GrokModelRejectsLogprobs recognizes the 4.20 prefix after the final model
// qualifier.
func (m BodyCodec) GrokModelRejectsLogprobs(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = strings.TrimSpace(model[slash+1:])
	}
	return strings.HasPrefix(model, "grok-4.20")
}

// SanitizeGrokResponsesModelCapabilities removes reasoning controls when the
// final attributes explicitly disable them. Unknown declarations retain client
// controls; configured effort choices therefore survive Composer platform defaults.
func (m BodyCodec) SanitizeGrokResponsesModelCapabilities(body []byte, upstreamModel string) ([]byte, error) {
	attributes := m.modelAttributes(upstreamModel)
	disabled := attributes.Reasoning != nil && !*attributes.Reasoning
	if !disabled && (attributes.ReasoningOptions == nil || len(*attributes.ReasoningOptions) != 0) {
		return body, nil
	}
	out := body
	for _, field := range []string{"reasoning", "reasoning_effort", "reasoningEffort"} {
		if !gjson.GetBytes(out, field).Exists() {
			continue
		}
		var err error
		out, err = sjson.DeleteBytes(out, field)
		if err != nil {
			return nil, fmt.Errorf("remove unsupported reasoning control %s: %w", field, err)
		}
	}
	return out, nil
}

// NormalizeGrokResponsesReasoningEffort applies model attributes to nested and
// flat effort controls. Missing fields remain absent, unsupported values are
// removed, and retained values use NormalizeGrokReasoningEffortValue. The
// complete upstream identity is passed to capability lookup.
//
// Camel-case reasoningEffort is removed and can populate reasoning_effort when
// that field is absent after its own processing. Nested effort remains
// independent of the flat spelling. An emptied reasoning object is removed, so
// known models without an accepted effort do not retain an empty control object.
// JSON-edit errors terminate the ordered normalization.
func (m BodyCodec) NormalizeGrokResponsesReasoningEffort(body []byte, upstreamModel string) ([]byte, error) {
	out := body
	var err error
	for _, field := range []string{"reasoning.effort", "reasoning_effort"} {
		value := gjson.GetBytes(out, field)
		if !value.Exists() {
			continue
		}
		normalized, keep := m.NormalizeGrokReasoningEffortValue(value.String(), upstreamModel)
		if !keep {
			out, err = sjson.DeleteBytes(out, field)
		} else {
			out, err = sjson.SetBytes(out, field, normalized)
		}
		if err != nil {
			return nil, fmt.Errorf("normalize Grok reasoning field %s: %w", field, err)
		}
	}
	// Remove the legacy spelling after processing flat effort; it can supply that
	// flat field only when normalization left it absent.
	if camel := gjson.GetBytes(out, "reasoningEffort"); camel.Exists() {
		normalized, keep := m.NormalizeGrokReasoningEffortValue(camel.String(), upstreamModel)
		out, err = sjson.DeleteBytes(out, "reasoningEffort")
		if err != nil {
			return nil, fmt.Errorf("remove Grok reasoningEffort: %w", err)
		}
		if keep && !gjson.GetBytes(out, "reasoning_effort").Exists() {
			out, err = sjson.SetBytes(out, "reasoning_effort", normalized)
			if err != nil {
				return nil, fmt.Errorf("set Grok reasoning_effort: %w", err)
			}
		}
	}
	if reasoning := gjson.GetBytes(out, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
		out, err = sjson.DeleteBytes(out, "reasoning")
		if err != nil {
			return nil, fmt.Errorf("remove empty Grok reasoning: %w", err)
		}
	}
	return out, nil
}

// NormalizeGrokChatReasoningEffort chooses nonblank reasoning_effort before the
// camel-case spelling and applies model capability normalization. A blank flat
// value allows reasoningEffort to supply the selection; the legacy key is then
// removed from the result.
//
// Source selection precedes validation, so an unsupported nonblank flat value
// suppresses an otherwise usable legacy value. Known capabilities can remove
// that selected effort, while unknown attributes retain its trimmed text. The
// result carries one flat spelling and reports JSON-edit errors to the caller.
func (m BodyCodec) NormalizeGrokChatReasoningEffort(body []byte, upstreamModel string) ([]byte, error) {
	var cleanupErr error
	body, cleanupErr = m.SanitizeGrokResponsesModelCapabilities(body, upstreamModel)
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	// Select the source before deleting the legacy key; unsupported nonblank flat
	// text still outranks legacy text, so validation cannot revive displaced input.
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoningEffort").String())
	}
	normalized, keep := m.NormalizeGrokReasoningEffortValue(raw, upstreamModel)
	out := body
	var err error
	if gjson.GetBytes(out, "reasoningEffort").Exists() {
		out, err = sjson.DeleteBytes(out, "reasoningEffort")
		if err != nil {
			return nil, err
		}
	}
	if !keep {
		if gjson.GetBytes(out, "reasoning_effort").Exists() {
			out, err = sjson.DeleteBytes(out, "reasoning_effort")
		}
		return out, err
	}
	out, err = sjson.SetBytes(out, "reasoning_effort", normalized)
	return out, err
}

// NormalizeGrokReasoningEffortValue returns the selected effort and whether the
// caller should keep it. Unknown attributes with no disabled reasoning retain
// trimmed nonblank client text. Known capability state normalizes aliases and
// rejects unsupported selections.
//
// With known capabilities, minimal maps to low; max and ultra map to high.
// Extra-high aliases select xhigh, then high when the resolver rejects xhigh;
// high support is checked afterward. This separates absent capability knowledge
// from an explicit empty or disabled option list, so unknown qualified identities
// do not silently lose client controls.
func (m BodyCodec) NormalizeGrokReasoningEffortValue(raw, model string) (string, bool) {
	value := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(raw)))
	attributes := m.modelAttributes(model)
	// Absent options with no reasoning disable retain the caller's spelling;
	// applying known-model aliases here would change controls whose support is
	// still unknown.
	if attributes.ReasoningOptions == nil && (attributes.Reasoning == nil || *attributes.Reasoning) {
		return strings.TrimSpace(raw), strings.TrimSpace(raw) != ""
	}
	if supported := attributes.EffortSupport(value); supported != nil && *supported {
		return value, true
	}
	switch value {
	case "none", "low", "medium", "high":
	case "minimal":
		value = "low"
	case "xhigh", "extrahigh":
		// Extra-high aliases first select xhigh, then fall to high when known
		// attributes reject it; the resulting effort still needs support checking.
		value = "xhigh"
		if supported := attributes.EffortSupport(value); supported != nil && !*supported {
			value = "high"
		}
	case "max", "ultra":
		value = "high"
	default:
		return "", false
	}
	supported := attributes.EffortSupport(value)
	return value, supported == nil || *supported
}

// GrokSupportsXHighReasoningEffort reports confirmed xhigh support from the
// injected model attributes. Missing knowledge returns false here, even though
// NormalizeGrokReasoningEffortValue retains an explicitly supplied effort when
// model capability knowledge is absent.
func (m BodyCodec) GrokSupportsXHighReasoningEffort(model string) bool {
	supported := m.modelAttributes(model).EffortSupport("xhigh")
	return supported != nil && *supported
}

// GrokSupportsReasoningEffort reports whether model attributes list an effort
// control. A missing option list returns false. This is option-presence
// reporting; individual effort support and disabled reasoning are evaluated
// through the attributes' EffortSupport method.
func (m BodyCodec) GrokSupportsReasoningEffort(model string) bool {
	return m.modelAttributes(model).HasReasoningOption("effort")
}

// SanitizeGrokResponsesUnsupportedFields recursively deletes the selected
// unsupported keys from decoded JSON. Bodies without the marker return supplied
// bytes, and unchanged decoded content also returns body. Modified content is
// re-encoded with Number-valued numeric fields.
func (m BodyCodec) SanitizeGrokResponsesUnsupportedFields(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"external_web_access"`)) {
		return body, nil
	}

	var payload any
	if err := wirejson.DecodeUseNumber(body, &payload); err != nil {
		return nil, err
	}
	if !m.DeleteJSONFields(payload, grokResponsesUnsupportedRecursiveFields) {
		return body, nil
	}
	return wirejson.Marshal(payload)
}

// DeleteJSONFields removes named keys recursively from decoded maps and arrays
// and reports whether anything changed. It edits caller-owned mutable values in
// place; aliases of those maps or slices observe deletions in their shared
// children.
func (m BodyCodec) DeleteJSONFields(value any, fields map[string]struct{}) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for field := range fields {
			if _, ok := typed[field]; ok {
				delete(typed, field)
				changed = true
			}
		}
		for _, child := range typed {
			if m.DeleteJSONFields(child, fields) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if m.DeleteJSONFields(child, fields) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

// SanitizeGrokResponsesInput promotes declarations carried by additional_tools
// input items. Only array input participates, and those carrier items are
// removed after examination. Existing top-level tools retain their order and any
// duplicates already present.
//
// New carrier declarations follow top-level declarations and skip identities
// already seen by GrokResponsesToolDedupKey. Thus earlier definitions win while
// unrelated input entries keep their order. Tools is rewritten only when
// promotion added something, and input is still rewritten when carriers contain
// no new declarations. Later client adaptation and filtering decide the accepted
// vendor representation.
func (m BodyCodec) SanitizeGrokResponsesInput(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"additional_tools"`)) {
		return body, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, nil
	}

	rawItems := input.Array()
	filtered := make([]json.RawMessage, 0, len(rawItems))
	topLevelTools := gjson.GetBytes(body, "tools")
	mergedTools := make([]json.RawMessage, 0)
	seenTools := make(map[string]struct{})
	appendTool := func(tool gjson.Result) bool {
		key := m.GrokResponsesToolDedupKey(tool)
		if _, exists := seenTools[key]; exists {
			return false
		}
		seenTools[key] = struct{}{}
		mergedTools = append(mergedTools, json.RawMessage(tool.Raw))
		return true
	}
	if topLevelTools.IsArray() {
		for _, tool := range topLevelTools.Array() {
			seenTools[m.GrokResponsesToolDedupKey(tool)] = struct{}{}
			mergedTools = append(mergedTools, json.RawMessage(tool.Raw))
		}
	}

	// Keep top-level definitions ahead of carried declarations, deduplicating
	// additions by tool identity so replay carriers cannot replace earlier
	// definitions.
	promoted := false
	for _, item := range rawItems {
		if strings.TrimSpace(item.Get("type").String()) == "additional_tools" {
			tools := item.Get("tools")
			if tools.IsArray() {
				for _, tool := range tools.Array() {
					if appendTool(tool) {
						promoted = true
					}
				}
			}
			continue
		}
		filtered = append(filtered, json.RawMessage(item.Raw))
	}
	if len(filtered) == len(rawItems) {
		return body, nil
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetRawBytes(body, "input", encoded)
	if err != nil || !promoted {
		return body, err
	}
	encodedTools, err := json.Marshal(mergedTools)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encodedTools)
}

// StripRedundantGrokViewImageTool removes flat automatic view_image declarations
// for a final user input containing images. A named view_image selection keeps
// the supplied request. Required choice also keeps it when filtering would
// remove every remaining tool.
//
// Other declarations retain order. A populated remainder replaces tools, while
// an empty remainder removes tools and parallel_tool_calls. tool_choice is left
// for the later SanitizeGrokResponsesTools step, which checks it against the
// surviving declaration set; callers using this helper independently retain that
// cleanup responsibility.
func (m BodyCodec) StripRedundantGrokViewImageTool(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	items := input.Array()
	if len(items) == 0 {
		return body, nil
	}
	current := items[len(items)-1]
	if strings.TrimSpace(current.Get("role").String()) != "user" ||
		!protocolopenai.JSONValueMayContainImageInput(current) {
		return body, nil
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.IsObject() && strings.TrimSpace(toolChoice.Get("type").String()) == "function" {
		choiceName := strings.TrimSpace(toolChoice.Get("name").String())
		if choiceName == "" {
			choiceName = strings.TrimSpace(toolChoice.Get("function.name").String())
		}
		if choiceName == "view_image" {
			return body, nil
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	filtered := make([]json.RawMessage, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "function" &&
			strings.TrimSpace(tool.Get("name").String()) == "view_image" {
			changed = true
			continue
		}
		filtered = append(filtered, json.RawMessage(tool.Raw))
	}
	if !changed {
		return body, nil
	}
	// Required choice needs a declaration after filtering; keep the original tool
	// set when removing view_image would make that choice unusable.
	if len(filtered) == 0 && strings.TrimSpace(toolChoice.String()) == "required" {
		return body, nil
	}

	if len(filtered) == 0 {
		out, err := sjson.DeleteBytes(body, "tools")
		if err != nil {
			return nil, err
		}
		return sjson.DeleteBytes(out, "parallel_tool_calls")
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
}

// GrokResponsesToolDedupKey selects identity by type/name, then MCP server
// label, then normalized JSON. Named tools therefore compare by their declared
// identity even when their descriptions or schemas differ. A NUL separator
// marks the type/name components in that key.
//
// Unnamed tools compare their normalized JSON representation. That helper
// decodes numbers through float64, so large integer values or numeric spellings
// can collapse to one key; this is a comparison seed rather than a lossless
// representation. SanitizeGrokResponsesInput uses the selected key to suppress
// later carried definitions while keeping the existing top-level list.
func (m BodyCodec) GrokResponsesToolDedupKey(tool gjson.Result) string {
	toolType := strings.TrimSpace(tool.Get("type").String())
	if toolType != "" {
		if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
			return "type:" + toolType + "\x00name:" + name
		}
		if toolType == "mcp" {
			if label := strings.TrimSpace(tool.Get("server_label").String()); label != "" {
				return "type:mcp\x00server_label:" + label
			}
		}
	}
	return "json:" + protocolopenai.NormalizeCompatSeedJSON(json.RawMessage(tool.Raw))
}

// SanitizeGrokReasoningNullContent removes null object fields from decoded
// Responses input before re-encoding. Unsupported input shapes and decode
// failures return supplied bytes. The input-item dispatcher skips compaction
// objects and keeps array positions, including null elements, while mutable
// object cleanup reports actual deletions.
func (m BodyCodec) SanitizeGrokReasoningNullContent(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || (!input.IsArray() && !input.IsObject()) {
		return body, nil
	}
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	// Null cleanup is best effort for this helper. A decode failure returns the
	// supplied request bytes for the surrounding validator's decision.
	if err := decoder.Decode(&decoded); err != nil {
		return body, nil
	}
	rawInput, ok := decoded["input"]
	if !ok {
		return body, nil
	}
	cleaned, changed := m.StripExplicitNullsFromGrokInput(rawInput)
	if !changed {
		return body, nil
	}
	decoded["input"] = cleaned
	return wirejson.Marshal(decoded)
}

// StripExplicitNullsFromGrokInput mutates supported input maps and arrays while
// skipping compaction input items. It removes null object fields through
// StripExplicitNullsFromGrokObject; scalar and null array elements remain.
// Callers own the decoded containers because map and slice aliases observe
// changes.
func (m BodyCodec) StripExplicitNullsFromGrokInput(value any) (any, bool) {
	switch node := value.(type) {
	case []any:
		changed := false
		for i, item := range node {
			itemMap, ok := item.(map[string]any)
			if !ok {
				next, childChanged := m.StripExplicitNullsFromGrokInput(item)
				if childChanged {
					node[i] = next
					changed = true
				}
				continue
			}
			// Compaction input items carry replay state whose fields need their own
			// conversion; leave those objects intact at this input dispatcher.
			if protocolopenai.IsCompactionItemType(m.GrokCompactStringValue(itemMap["type"])) {
				continue
			}
			next, childChanged := m.StripExplicitNullsFromGrokObject(itemMap)
			if childChanged {
				node[i] = next
				changed = true
			}
		}
		return node, changed
	case map[string]any:
		// The object-form input can itself be compaction state, so skip generic
		// null-field deletion until Compact conversion owns its representation.
		if protocolopenai.IsCompactionItemType(m.GrokCompactStringValue(node["type"])) {
			return node, false
		}
		return m.StripExplicitNullsFromGrokObject(node)
	default:
		return value, false
	}
}

// StripExplicitNullsFromGrokObject deletes null fields from node and recursively
// edits map or array children. Nil maps return unchanged. Callers own mutable
// storage; compaction skipping is supplied by the input dispatcher and does not
// apply to every direct nested-map recursion.
func (m BodyCodec) StripExplicitNullsFromGrokObject(node map[string]any) (map[string]any, bool) {
	if node == nil {
		return node, false
	}
	changed := false
	for key, child := range node {
		if child == nil {
			delete(node, key)
			changed = true
			continue
		}
		switch typed := child.(type) {
		case map[string]any:
			next, childChanged := m.StripExplicitNullsFromGrokObject(typed)
			if childChanged {
				node[key] = next
				changed = true
			}
		case []any:
			next, childChanged := m.StripExplicitNullsFromGrokInput(typed)
			if childChanged {
				node[key] = next
				changed = true
			}
		}
	}
	return node, changed
}

// SanitizeGrokResponsesTools retains supported vendor tool types and checks
// controls against the result. Missing or non-array tools clears orphan controls
// while retaining any supplied tools field. An already-empty array remains
// present; a nonempty set filtered to empty is removed.
//
// Function schemas receive an object default when absent/null, or a permissive
// object replacement for invalid root unions with strict disabled when true.
// Raw tool_search is excluded from the retained type set; client-tool
// adaptation lowers it to a function before this sanitizer. Deferred loading
// flags consequently disappear from all retained declarations. Named function
// choice is then checked against remaining declarations, so a removed tool
// cannot remain selected in the forwarded request.
func (m BodyCodec) SanitizeGrokResponsesTools(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() {
		return m.DeleteGrokOrphanToolControls(body)
	}
	if !tools.IsArray() {
		return m.DeleteGrokOrphanToolControls(body)
	}

	rawTools := tools.Array()
	filteredTools := make([]json.RawMessage, 0, len(rawTools))
	toolsChanged := false
	for _, tool := range rawTools {
		toolType := strings.TrimSpace(tool.Get("type").String())
		if _, ok := grokResponsesSupportedToolTypes[toolType]; ok {
			raw := json.RawMessage(tool.Raw)
			// Absent or null parameters need an object schema for function decoding;
			// invalid union roots instead receive the permissive schema below.
			if toolType == "function" && (!tool.Get("parameters").Exists() || tool.Get("parameters").Type == gjson.Null) {
				var payload map[string]any
				if err := wirejson.DecodeUseNumber(raw, &payload); err != nil {
					return nil, err
				}
				payload["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
				encoded, err := wirejson.Marshal(payload)
				if err != nil {
					return nil, err
				}
				raw = encoded
				toolsChanged = true
			} else if toolType == "function" && m.GrokFunctionParametersHaveInvalidUnionRoot(tool.Get("parameters")) {
				var err error
				raw, err = sjson.SetRawBytes(raw, "parameters", []byte(GrokSafeFunctionParameters))
				if err != nil {
					return nil, err
				}
				if strict := tool.Get("strict"); strict.Exists() && strict.Bool() {
					raw, err = sjson.SetBytes(raw, "strict", false)
					if err != nil {
						return nil, err
					}
				}
				toolsChanged = true
			}
			filteredTools = append(filteredTools, raw)
		}
	}
	// Deferred flags depend on a surviving tool_search type. Remove those flags
	// after filtering so declarations cannot retain unsupported deferred-loading
	// controls.
	if !m.GrokRawToolsContainType(filteredTools, "tool_search") {
		for index, raw := range filteredTools {
			if !gjson.GetBytes(raw, "defer_loading").Exists() {
				continue
			}
			cleaned, deleteErr := sjson.DeleteBytes(raw, "defer_loading")
			if deleteErr != nil {
				return nil, deleteErr
			}
			filteredTools[index] = cleaned
			toolsChanged = true
		}
	}

	var err error
	if len(filteredTools) != len(rawTools) || toolsChanged {
		if len(filteredTools) == 0 {
			body, err = sjson.DeleteBytes(body, "tools")
		} else {
			var encoded []byte
			encoded, err = json.Marshal(filteredTools)
			if err != nil {
				return nil, err
			}
			body, err = sjson.SetRawBytes(body, "tools", encoded)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(filteredTools) == 0 {
		return m.DeleteGrokOrphanToolControls(body)
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if !toolChoice.Exists() {
		return body, nil
	}
	if m.ShouldDropGrokToolChoice(toolChoice, filteredTools) {
		body, err = sjson.DeleteBytes(body, "tool_choice")
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// GrokFunctionParametersHaveInvalidUnionRoot recognizes a nonempty anyOf or
// oneOf with a branch not explicitly typed object. Other parameter shapes return
// false. Reference-only or null branches therefore select schema simplification
// in the tool sanitizer.
func (m BodyCodec) GrokFunctionParametersHaveInvalidUnionRoot(parameters gjson.Result) bool {
	if !parameters.Exists() || !parameters.IsObject() {
		return false
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches := parameters.Get(keyword)
		if !branches.IsArray() {
			continue
		}
		values := branches.Array()
		if len(values) == 0 {
			continue
		}
		for _, branch := range values {
			if !strings.EqualFold(strings.TrimSpace(branch.Get("type").String()), "object") {
				return true
			}
		}
	}
	return false
}

// GrokRawToolsContainType finds an exact type label in decoded raw tool entries.
func (m BodyCodec) GrokRawToolsContainType(tools []json.RawMessage, want string) bool {
	for _, tool := range tools {
		if strings.TrimSpace(gjson.GetBytes(tool, "type").String()) == want {
			return true
		}
	}
	return false
}

// DeleteGrokOrphanToolControls removes tool_choice and parallel_tool_calls from
// supplied request bytes. Callers invoke it after determining that tools cannot
// support those controls; missing fields leave the body unchanged and JSON-edit
// failures propagate.
func (m BodyCodec) DeleteGrokOrphanToolControls(body []byte) ([]byte, error) {
	var err error
	for _, field := range []string{"tool_choice", "parallel_tool_calls"} {
		if !gjson.GetBytes(body, field).Exists() {
			continue
		}
		body, err = sjson.DeleteBytes(body, field)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// ShouldDropGrokToolChoice checks object choices against supported types and
// named function declarations. An empty tool list drops every choice. Scalar
// choices and unnamed function choices remain when tools exist; supported
// non-function types are kept without checking for a matching declaration.
func (m BodyCodec) ShouldDropGrokToolChoice(toolChoice gjson.Result, tools []json.RawMessage) bool {
	if len(tools) == 0 {
		return true
	}
	if !toolChoice.IsObject() {
		return false
	}
	choiceType := strings.TrimSpace(toolChoice.Get("type").String())
	if choiceType == "" {
		return false
	}
	if _, ok := grokResponsesSupportedToolTypes[choiceType]; !ok {
		return true
	}
	if choiceType == "function" {
		choiceName := strings.TrimSpace(toolChoice.Get("name").String())
		if choiceName == "" {
			choiceName = strings.TrimSpace(toolChoice.Get("function.name").String())
		}
		if choiceName == "" {
			return false
		}
		for _, tool := range tools {
			var item struct {
				// Type, Name and Function identify one candidate declaration for choice
				// matching. Name supplies a flat function identity, while Function supplies
				// its nested name when the flat value is blank.
				Type     string   `json:"type"` // declared tool type
				Name     string   `json:"name"` // function name text
				Function struct { // nested function name container
					Name string `json:"name"` // function name text
				} `json:"function"`
			}
			// Unreadable declarations cannot establish a matching function choice. Skip
			// that candidate and continue searching the remaining accepted tool list.
			if err := json.Unmarshal(tool, &item); err != nil {
				continue
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = strings.TrimSpace(item.Function.Name)
			}
			if strings.TrimSpace(item.Type) == "function" && name == choiceName {
				return false
			}
		}
		return true
	}
	return false
}

// ShouldBridgeGrokComposerImageInputs recognizes Composer Chat messages that may
// contain image input.
func (m BodyCodec) ShouldBridgeGrokComposerImageInputs(body []byte) bool {
	if len(body) == 0 || !m.IsGrokComposerModel(gjson.GetBytes(body, "model").String()) {
		return false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() {
		return false
	}
	return protocolopenai.JSONValueMayContainImageInput(messages)
}

// IsGrokComposerModel identifies registered deployments using the Composer
// image-to-text adapter. This encoding choice is independent of configurable
// reasoning controls and does not establish the model's declared modalities.
func (m BodyCodec) IsGrokComposerModel(model string) bool {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "xai/")
	return model == "grok-composer" || model == "grok-composer-2.5-fast" || model == "composer-2.5"
}

// CollectGrokComposerImageURLs collects usable image_url parts in message and
// content encounter order. It accepts decoded map/slice shapes and skips
// unsupported or empty image values. The returned order pairs with one-based
// auxiliary description positions.
func (m BodyCodec) CollectGrokComposerImageURLs(reqBody map[string]any) []string {
	messages, ok := reqBody["messages"].([]any)
	if !ok {
		return nil
	}

	var imageURLs []string
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			if imageURL := m.GrokComposerImageURLFromPart(part); imageURL != "" {
				imageURLs = append(imageURLs, imageURL)
			}
		}
	}
	return imageURLs
}

// GrokComposerImageURLFromPart extracts a usable image_url string or nested url
// from a decoded part. Other types return empty. Empty base64 data URIs are
// rejected through normalization before the description bridge selects an image.
func (m BodyCodec) GrokComposerImageURLFromPart(part any) string {
	partMap, ok := part.(map[string]any)
	if !ok {
		return ""
	}
	if strings.TrimSpace(strings.ToLower(fmt.Sprint(partMap["type"]))) != "image_url" {
		return ""
	}
	switch imageURL := partMap["image_url"].(type) {
	case string:
		return m.NormalizeGrokComposerImageURL(imageURL)
	case map[string]any:
		raw, _ := imageURL["url"].(string)
		return m.NormalizeGrokComposerImageURL(raw)
	default:
		return ""
	}
}

// NormalizeGrokComposerImageURL trims an image candidate and rejects blank or
// empty-base64 data URIs. Other text passes through unchanged; the description
// request carries the remaining candidate to the upstream. Callers needing
// additional URL or content validation apply it before building that request.
func (m BodyCodec) NormalizeGrokComposerImageURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || protocolopenai.IsEmptyBase64DataURI(trimmed) {
		return ""
	}
	return trimmed
}

// BuildGrokComposerImageDescriptionBody builds a non-streaming auxiliary vision
// request identifying imageURL by index. It selects the fixed vision model and
// output cap with storage disabled. Callers provide the collection's one-based
// index. imageURL is forwarded as supplied, so any additional image-candidate
// validation belongs before construction.
func (m BodyCodec) BuildGrokComposerImageDescriptionBody(imageURL string, index int) ([]byte, error) {
	prompt := fmt.Sprintf("Describe image %d in concise, factual text for a downstream coding/composer model. Include visible text, UI elements, diagrams, errors, and spatial relationships. Do not mention that you are an image analysis bridge.", index)
	req := map[string]any{
		"model": ComposerImageBridgeVisionModel,

		"stream": false,

		"store": false,

		"max_output_tokens": ComposerImageBridgeMaxOutputTokens,

		"input": []any{
			map[string]any{
				"type": "message",

				"role": "user",

				"content": []any{
					map[string]any{"type": "input_text", "text": prompt},
					map[string]any{"type": "input_image", "image_url": imageURL},
				},
			},
		},
	}
	return wirejson.Marshal(req)
}

// GrokResponsesOutputText joins nonblank recognized text content with two
// newlines in output order. Nil responses and outputs with no usable text return
// empty; content tags output_text, text and input_text participate regardless of
// the enclosing output item's type.
func (m BodyCodec) GrokResponsesOutputText(resp *protocolopenai.ResponsesResponse) string {
	if resp == nil {
		return ""
	}
	var parts []string
	for _, output := range resp.Output {
		for _, content := range output.Content {
			if content.Type == "output_text" || content.Type == "text" || content.Type == "input_text" {
				if text := strings.TrimSpace(content.Text); text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// RewriteGrokComposerImagesAsText replaces usable image parts with ordered
// descriptions and mutates request messages. Callers pass descriptions in
// CollectGrokComposerImageURLs order. Messages with no usable image remain
// unchanged, while affected messages become joined text content.
//
// A single index advances across the conversation, pairing each usable image
// with its one-based description label. Too few descriptions still removes later
// images, so callers complete description collection before this step.
// Recognized text parts are retained and trimmed; other part shapes are omitted
// from affected messages. The boolean reports whether any message changed.
func (m BodyCodec) RewriteGrokComposerImagesAsText(reqBody map[string]any, descriptions []string) bool {
	messages, ok := reqBody["messages"].([]any)
	if !ok {
		return false
	}

	imageIndex := 0
	changed := false
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		var textParts []string
		messageChanged := false
		for _, part := range parts {
			if imageURL := m.GrokComposerImageURLFromPart(part); imageURL != "" {
				if imageIndex < len(descriptions) {
					textParts = append(textParts, fmt.Sprintf("Image %d description: %s", imageIndex+1, strings.TrimSpace(descriptions[imageIndex])))
				}
				imageIndex++
				messageChanged = true
				continue
			}
			if text := m.GrokComposerTextFromPart(part); text != "" {
				textParts = append(textParts, text)
			}
		}
		// Collapse only messages with a usable image into joined text; descriptions
		// and recognized text keep encounter order across the rewritten conversation.
		if messageChanged {
			msgMap["content"] = strings.Join(textParts, "\n\n")
			changed = true
		}
	}
	return changed
}

// GrokComposerTextFromPart extracts trimmed text from decoded text or input_text
// parts. Other part tags, non-map values and non-string text return empty,
// allowing Composer rewriting to omit unsupported content from affected
// messages.
func (m BodyCodec) GrokComposerTextFromPart(part any) string {
	partMap, ok := part.(map[string]any)
	if !ok {
		return ""
	}
	partType := strings.TrimSpace(strings.ToLower(fmt.Sprint(partMap["type"])))
	switch partType {
	case "text", "input_text":
		text, _ := partMap["text"].(string)
		return strings.TrimSpace(text)
	default:
		return ""
	}
}
