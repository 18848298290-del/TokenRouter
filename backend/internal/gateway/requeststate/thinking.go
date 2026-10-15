package requeststate

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// thinking.go filters thinking history and normalizes effort fields for
// mapped-model replay policies. Caller-supplied flags select protocol filtering
// or preserve history, and display effort can be supplied for passback models
// that lack a native effort field.
//
// GLM normalization distinguishes built-in behavior from a configured
// allowed-level slice: nil uses the built-in mapping, while an empty configured
// slice removes the explicit OpenAI effort field. GLM 5.3 additionally retains
// low, and its Anthropic path writes enabled thinking while selecting or
// deleting output effort. The selection helper first honors an allowed explicit
// value, then the mapped value, then chooses a ranked available level. Declined
// or failed byte rewrites retain the supplied body for the caller.

// ThinkingRequestOptions contains resolved replay and model-family choices for
// request transformations. Callers keep Efforts stable while normalization
// reads it; family and filter flags are selected by the provider's mapped-model
// policy.
type ThinkingRequestOptions struct {
	// Efforts is read by GLM normalization. Nil uses built-in level mapping; a
	// supplied empty slice removes the OpenAI effort field, and caller-owned
	// entries stay unchanged while the functions read them.
	Efforts []string // configured allowed effort spellings
	// PreFilter gates initial history filtering, and RetryFilters gates both retry
	// downgrades. Disabled gates return the supplied bytes; mapped-model policy
	// chooses these separate transformation opportunities before forwarding.
	PreFilter, RetryFilters bool // initial history filtering and retry body filtering
	// PassbackRequired permits the display-effort default when
	// NativeReasoningEffort is false. Native support leaves the default absent so
	// upstream-owned effort choice is not reported as an invented explicit request
	// value.
	PassbackRequired, NativeReasoningEffort bool // reasoning passback requirement and native effort support
	// GLM selects OpenAI effort conversion, while GLM53 enables that family's
	// low-level exception and its Anthropic normalization. Callers resolve these
	// flags from the mapped model before applying transformations.
	GLM, GLM53     bool   // GLM model family and GLM 5.3 variant
	DummySignature string // placeholder signature rejected by filtering
}

// FilterThinkingBlocks delegates pre-forward history filtering when PreFilter
// is enabled. Otherwise it returns the supplied body slice; the protocol filter
// uses DummySignature and retains unchanged bytes when its heuristics or
// parsing decline a rewrite.
func FilterThinkingBlocks(body []byte, options ThinkingRequestOptions) []byte {
	if !options.PreFilter {
		return body
	}
	return FilterThinkingBlocksInternal(body, options.DummySignature)
}

// FilterThinkingBlocksForRetry delegates the protocol's retry downgrade when
// RetryFilters is enabled. Otherwise it retains the supplied slice; caller
// model policy selects this gate before potentially converting thinking history
// or empty content.
func FilterThinkingBlocksForRetry(body []byte, options ThinkingRequestOptions) []byte {
	if !options.RetryFilters {
		return body
	}
	return protocolanthropic.FilterThinkingBlocksForRetry(body)
}

// FilterSignatureSensitiveBlocksForRetry delegates removal/conversion of
// signature-sensitive thinking and tool history when RetryFilters is enabled.
// Disabled policy retains the supplied body; the protocol dependency defines
// changed-message encoding and recovery behavior.
func FilterSignatureSensitiveBlocksForRetry(body []byte, options ThinkingRequestOptions) []byte {
	if !options.RetryFilters {
		return body
	}
	return protocolanthropic.FilterSignatureSensitiveBlocksForRetry(body)
}

// FilterThinkingBlocksInternal delegates protocol filtering with the supplied
// rejected placeholder signature. The protocol dependency selects history
// changes and returns original bytes on decline or failure; changed generic-map
// encoding can round unrelated numbers.
func FilterThinkingBlocksInternal(body []byte, dummySignature string) []byte {
	return protocolanthropic.FilterThinkingBlocksInternal(body, dummySignature)
}

// DefaultEffortForThinkingEnabled returns a separate high display-effort
// pointer for passback-required models lacking native reasoning effort. Other
// option combinations return nil; this reporting default does not itself
// rewrite an outgoing request field.
func DefaultEffortForThinkingEnabled(options ThinkingRequestOptions) *string {
	if !options.PassbackRequired {
		return nil
	}
	if options.NativeReasoningEffort {
		// Native effort support leaves omission meaningful to the upstream model.
		// Keep the display default absent when the request supplied no explicit effort.
		return nil
	}
	effort := "high"
	return &effort
}

// OpenAIBodyHasThinkingEnabled reports whether trimmed lowercase thinking.type
// is enabled or adaptive. Other spellings and missing fields return false
// through the lenient byte reader; complete JSON validity remains a caller
// responsibility.
func OpenAIBodyHasThinkingEnabled(body []byte) bool {
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	return thinkingType == "enabled" || thinkingType == "adaptive"
}

// ApplyThinkingEnabledFallback retains any supplied effort pointer, including
// one containing empty text. With no pointer, enabled/adaptive body evidence
// permits the option-selected display default; callers keep shared supplied
// pointer values stable while recording them.
func ApplyThinkingEnabledFallback(effort *string, body []byte, options ThinkingRequestOptions) *string {
	if effort != nil {
		return effort
	}
	if !OpenAIBodyHasThinkingEnabled(body) {
		return nil
	}
	return DefaultEffortForThinkingEnabled(options)
}

// NormalizeGLMOpenAIReasoningEffort normalizes the first nonblank nested or
// flat effort when GLM is selected. Declined changes and byte-edit failures
// return the supplied body with false; GLM53 alone does not enable this OpenAI
// path.
//
// Built-in low, medium and high map to high, and elevated aliases map to max,
// with the GLM53 low exception applied before configured levels are considered.
// A nonnil allowed slice can retain explicit or mapped choices or select
// another ranked level. An empty configured slice removes the chosen field
// while leaving the other spelling untouched. Callers supply mapped-model
// options and validated payloads before adopting successful bytes.
func NormalizeGLMOpenAIReasoningEffort(body []byte, options ThinkingRequestOptions) ([]byte, bool) {
	if !options.GLM {
		return body, false
	}

	path := "reasoning.effort"
	raw := strings.TrimSpace(gjson.GetBytes(body, path).String())
	// Blank nested Responses effort leaves the compatible Chat spelling
	// available; select that path before applying GLM family and allowed-level rules.
	if raw == "" {
		path = "reasoning_effort"
		raw = strings.TrimSpace(gjson.GetBytes(body, path).String())
	}
	if raw == "" {
		return body, false
	}

	mapped := NormalizeGLMEffortToken(raw)
	// GLM53 exposes low as a distinct supported choice; retain that explicit
	// request before the generic family mapping would collapse it into high.
	if options.GLM53 && mapped == "high" && NormalizeEffortToken(raw) == "low" {
		mapped = "low"
	}
	if options.Efforts != nil {
		mapped = configuredGLMEffort(raw, mapped, options.Efforts)
		// An explicitly empty allowed set means no outgoing effort field. Remove
		// the selected spelling so its unsupported explicit value cannot reach this model.
		if len(options.Efforts) == 0 {
			modified, err := sjson.DeleteBytes(body, path)
			if err != nil {
				// Field deletion failed, so retain the supplied bytes and report no
				// adopted normalization for the caller's outgoing request.
				return body, false
			}
			return modified, true
		}
	}
	if mapped == "" || mapped == raw {
		return body, false
	}

	modified, err := sjson.SetBytes(body, path, mapped)
	if err != nil {
		// The chosen GLM level was not encoded successfully. Keep the supplied
		// field and false result so this request adopts no failed effort replacement.
		return body, false
	}
	return modified, true
}

// NormalizeEffortToken trims, lowercases, and removes hyphens, underscores and
// spaces from effort text. Unknown words remain normalized text, letting
// callers apply their own supported-level lookup after the spelling conversion.
func NormalizeEffortToken(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
}

// NormalizeGLMEffortToken maps normalized low/medium/high to high and elevated
// accepted aliases to max. Empty or unrecognized spelling returns empty, and
// GLM53-specific low handling is applied separately by its caller.
func NormalizeGLMEffortToken(raw string) string {
	value := NormalizeEffortToken(raw)
	if value == "" {
		return ""
	}

	switch value {
	case "low", "medium", "high":
		return "high"
	case "xhigh", "extrahigh", "max", "ultracode":
		return "max"
	default:
		return ""
	}
}

// NormalizeGLM53AnthropicThinking normalizes output_config.effort or, when
// blank, thinking.type for GLM53. Unsupported evidence or a failed byte edit
// returns the supplied body with false; a successful selected transformation
// returns true even if values already match.
//
// Disabled/none/minimal/low select low, enabled/adaptive/medium/high select
// high, and elevated labels select max before configured levels are considered.
// The outgoing thinking.type becomes enabled; an empty selected level removes
// output effort while that enabled field remains. Both edits are temporary
// until they succeed together, so a failed second edit returns the original
// body rather than an enabled-only intermediate result.
func NormalizeGLM53AnthropicThinking(body []byte, options ThinkingRequestOptions) ([]byte, bool) {
	if !options.GLM53 {
		return body, false
	}

	raw := gjson.GetBytes(body, "output_config.effort").String()
	if strings.TrimSpace(raw) == "" {
		raw = gjson.GetBytes(body, "thinking.type").String()
	}

	var effort string
	switch NormalizeEffortToken(raw) {
	case "disabled", "off", "none", "minimal", "low":
		effort = "low"
	case "enabled", "adaptive", "medium", "high":
		effort = "high"
	case "xhigh", "max", "ultra":
		effort = "max"
	default:
		return body, false
	}
	if options.Efforts != nil {
		effort = configuredGLMEffort(raw, effort, options.Efforts)
	}

	// GLM53 carries the selected level in output_config while using enabled
	// thinking mode; keep both edits temporary until their combined normalization succeeds.
	modified, err := sjson.SetBytes(body, "thinking.type", "enabled")
	if err != nil {
		// The required enabled mode was not encoded. Retain the supplied request
		// before attempting its associated GLM53 output-level update or deletion.
		return body, false
	}
	if effort == "" {
		modified, err = sjson.DeleteBytes(modified, "output_config.effort")
	} else {
		modified, err = sjson.SetBytes(modified, "output_config.effort", effort)
	}
	if err != nil {
		// Output-level encoding failed after enabled mode was prepared. Return the
		// supplied body so that temporary mode change cannot escape as a partial conversion.
		return body, false
	}
	return modified, true
}

// configuredGLMEffort selects an allowed effort using explicit spelling,
// built-in mapping, then ranked available levels. Callers supply canonical
// allowed spellings, and the slice is read without mutation.
//
// An allowed normalized explicit value wins, followed by an allowed fallback.
// Otherwise a recognized fallback selects the first available ranked level at
// or above it, or the highest available lower level. If no ranked selection
// exists, an allowed default can be returned; an unrecognized fallback exits
// empty before that final default check. This ordering keeps configured choices
// separate from the built-in family mapping.
func configuredGLMEffort(raw, fallback string, allowed []string) string {
	value := NormalizeEffortToken(raw)
	if slices.Contains(allowed, value) {
		return value
	}
	if slices.Contains(allowed, fallback) {
		return fallback
	}
	// Canonical level order governs this selection even when configured entries
	// appear in another order; choose by rank rather than the caller's slice positions.
	levels := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	rank := slices.Index(levels, fallback)
	if rank < 0 {
		return ""
	}
	selected := ""
	for index, level := range levels {
		if !slices.Contains(allowed, level) {
			continue
		}
		selected = level
		if index >= rank {
			break
		}
	}
	// The configured default entry supplies a value after every ranked choice
	// was unavailable, retaining this final option for an otherwise unresolved selection.
	if selected == "" && slices.Contains(allowed, "default") {
		return "default"
	}
	return selected
}

// NormalizeChineseLLMThinking changes exact enabled thinking.type to adaptive
// when minimaxAdaptive is selected. Other spellings, disabled selection and
// failed byte edits retain the supplied body with false; successful conversion
// returns replacement bytes.
func NormalizeChineseLLMThinking(body []byte, minimaxAdaptive bool) ([]byte, bool) {
	if !minimaxAdaptive {
		return body, false
	}
	if gjson.GetBytes(body, "thinking.type").String() != "enabled" {
		return body, false
	}
	// MiniMax's selected request policy uses adaptive for an SDK-style enabled
	// request. Change the mode while keeping the remaining request fields in place.
	modified, err := sjson.SetBytes(body, "thinking.type", "adaptive")
	if err != nil {
		// Adaptive mode was not encoded successfully. Retain the supplied enabled
		// request and false result for the caller's unchanged MiniMax preparation.
		return body, false
	}
	return modified, true
}
