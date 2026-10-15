package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// scheduling_threshold_reason.go encodes scheduling threshold reasons and
// restores temporary pause state from stored reasons.
// Temporary scheduling blocks can store a structured threshold reason or a
// legacy plain message. Structured reasons retain platform, window,
// percentage, and reset details for administration, while their source marker
// lets health handling recognize a threshold pause separately from other
// temporary failures. Encoding failure falls back to the readable message.
//
// Restoring a stored reason combines its decoded deadline with the
// storage-provided fallback, keeping whichever is later so incomplete JSON
// cannot shorten the active block. A missing rule index starts at minus one
// for a system rule, and missing text selects either the threshold message or
// the raw reason according to its recognized source. These readers accept
// older text and partial JSON while preserving the deadline needed by runtime
// scheduling.

// Reason source and default messages distinguish threshold pauses from other
// temporary blocks when stored diagnostics are incomplete or cannot be
// decoded.
const (
	// ProviderSchedulingThresholdReasonSource: recognized threshold reason
	// source.
	ProviderSchedulingThresholdReasonSource = "provider_scheduling_threshold"

	defaultTempUnschedReasonErrorMessage           = "temporary scheduling block reason unavailable"
	defaultProviderSchedulingThresholdErrorMessage = "provider scheduling threshold reached"
)

// tempUnschedReasonPayload encodes the source, window observation, and
// readable explanation of a temporary scheduling pause.
type tempUnschedReasonPayload struct {
	Source   string `json:"source,omitempty"`   // reason classification source
	Platform string `json:"platform,omitempty"` // provider platform label
	Window   string `json:"window,omitempty"`   // observed usage-window label
	Scope    string `json:"scope,omitempty"`    // observed window scope label
	// ThresholdPercent and UsedPercent encode supplied values, omitting zero
	// values from JSON. providerHasSameSchedulingThresholdPause compares their
	// decoded numbers; readable text can default a nonpositive threshold and
	// round usage independently.
	ThresholdPercent int     `json:"threshold_percent,omitempty"` // percentage
	UsedPercent      float64 `json:"used_percent,omitempty"`      // percentage
	// UntilUnix and TriggeredAtUnix store reset and trigger instants in whole
	// Unix seconds; encoding omits either zero Unix value. Detailed construction
	// skips a zero Until input and samples Now when that input is zero.
	UntilUnix       int64  `json:"until_unix,omitempty"`        // Unix seconds
	TriggeredAtUnix int64  `json:"triggered_at_unix,omitempty"` // Unix seconds
	ErrorMessage    string `json:"error_message"`               // readable pause explanation
}

// ProviderSchedulingThresholdReasonInput supplies a selected usage window and
// times for an administrative pause reason.
type ProviderSchedulingThresholdReasonInput struct {
	Platform string // provider platform label
	// Window and Scope form the readable target together. A nonempty scope
	// follows the window with a slash, or supplies the target when the window is
	// blank.
	Window           string    // observed usage-window label
	Scope            string    // observed window scope label
	ThresholdPercent int       // percentage
	UsedPercent      float64   // percentage
	Until            time.Time // selected usage reset instant
	// Now supplies the trigger time for detailed reasons. A zero value samples
	// the builder's wall clock when the payload is created.
	Now time.Time // pause observation instant
}

// BuildTempUnschedReasonPayload encodes a trimmed source and normalized
// readable message as JSON. Blank text uses the temporary-block default;
// encoding failure returns the message directly so a stored reason remains
// readable.
func BuildTempUnschedReasonPayload(source string, errorMessage string) string {
	payload := tempUnschedReasonPayload{
		Source:       strings.TrimSpace(source),
		ErrorMessage: normalizeTempUnschedReasonErrorMessage(errorMessage, defaultTempUnschedReasonErrorMessage),
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		// Keep the readable explanation when structured encoding fails so
		// administrative state can still show why the provider is held.
		return payload.ErrorMessage
	}
	return string(raw)
}

// BuildProviderSchedulingThresholdReason builds a recognized threshold reason
// with normalized text and its source marker. Blank input uses the
// threshold-specific message before delegating JSON encoding and
// readable-error fallback.
func BuildProviderSchedulingThresholdReason(errorMessage string) string {
	return BuildTempUnschedReasonPayload(
		ProviderSchedulingThresholdReasonSource,
		normalizeTempUnschedReasonErrorMessage(errorMessage, defaultProviderSchedulingThresholdErrorMessage),
	)
}

// BuildDetailedProviderSchedulingThresholdReason encodes selected usage and
// deadline details with a readable threshold message. A zero Now samples UTC
// wall time, zero Until omits its Unix field, and other times use whole Unix
// seconds.
//
// The payload keeps supplied threshold and usage numbers, omitting zero
// values in JSON. Readable text uses one hundred for a nonpositive threshold
// and rounds usage to one decimal place. If JSON cannot encode the
// observation, such as NaN or infinity, the readable message is returned;
// its source and other structured fields are then unavailable to structured
// reason readers.
func BuildDetailedProviderSchedulingThresholdReason(input ProviderSchedulingThresholdReasonInput) string {
	triggeredAt := input.Now
	if triggeredAt.IsZero() {
		triggeredAt = time.Now().UTC()
	}
	payload := tempUnschedReasonPayload{
		Source:           ProviderSchedulingThresholdReasonSource,
		Platform:         strings.TrimSpace(input.Platform),
		Window:           strings.TrimSpace(input.Window),
		Scope:            strings.TrimSpace(input.Scope),
		ThresholdPercent: input.ThresholdPercent,
		UsedPercent:      input.UsedPercent,
		TriggeredAtUnix:  triggeredAt.Unix(),
		ErrorMessage:     buildProviderSchedulingThresholdErrorMessage(input),
	}
	if !input.Until.IsZero() {
		// Stored deadlines use seconds, matching TempUnschedState and cache expiry.
		payload.UntilUnix = input.Until.UTC().Unix()
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		// Keep the readable explanation when structured encoding fails so
		// administrative state can still show why the provider is held.
		return payload.ErrorMessage
	}
	return string(raw)
}

// IsProviderSchedulingThresholdReason reports whether decoded JSON carries
// the trimmed threshold source marker. Plain text, blank reasons, and
// unparseable payloads return false; the message's wording does not determine
// recognition.
func IsProviderSchedulingThresholdReason(rawReason string) bool {
	payload, ok := parseTempUnschedReasonPayload(rawReason)
	if !ok {
		return false
	}
	return payload.Source == ProviderSchedulingThresholdReasonSource
}

// parseTempUnschedReasonPayload decodes a trimmed reason and trims its source
// and message fields. Blank or invalid JSON returns false; successfully
// decoded zero-valued payloads remain valid for the caller's source
// comparison.
func parseTempUnschedReasonPayload(rawReason string) (tempUnschedReasonPayload, bool) {
	rawReason = strings.TrimSpace(rawReason)
	if rawReason == "" {
		return tempUnschedReasonPayload{}, false
	}

	var payload tempUnschedReasonPayload
	// Malformed reason JSON supplies no recognized source. Consumers then retain
	// their ordinary temporary-block classification rather than treating
	// unreadable text as a threshold marker.
	if err := json.Unmarshal([]byte(rawReason), &payload); err != nil {
		return tempUnschedReasonPayload{}, false
	}
	payload.Source = strings.TrimSpace(payload.Source)
	payload.ErrorMessage = strings.TrimSpace(payload.ErrorMessage)
	return payload, true
}

// normalizeTempUnschedReasonErrorMessage selects trimmed nonempty input, then
// trimmed fallback, then the built-in temporary-block message. Callers can
// pass blank values at either level and still receive usable readable text.
func normalizeTempUnschedReasonErrorMessage(errorMessage string, fallback string) string {
	errorMessage = strings.TrimSpace(errorMessage)
	if errorMessage != "" {
		return errorMessage
	}

	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		return fallback
	}
	return defaultTempUnschedReasonErrorMessage
}

// buildProviderSchedulingThresholdErrorMessage formats platform, window or
// scope, usage, threshold, and reset text for administration. Blank identity
// fields receive generic labels, nonpositive thresholds display one hundred
// percent, and a zero deadline uses the window-reset phrase.
func buildProviderSchedulingThresholdErrorMessage(input ProviderSchedulingThresholdReasonInput) string {
	platform := strings.TrimSpace(input.Platform)
	if platform == "" {
		platform = "provider"
	}

	target := strings.TrimSpace(input.Window)
	if scope := strings.TrimSpace(input.Scope); scope != "" {
		if target == "" {
			target = scope
		} else {
			target = target + "/" + scope
		}
	}
	if target == "" {
		target = "usage window"
	}

	threshold := input.ThresholdPercent
	if threshold <= 0 {
		threshold = 100
	}

	untilText := "the window reset"
	if !input.Until.IsZero() {
		untilText = input.Until.UTC().Format(time.RFC3339)
	}

	// Administration shows one decimal place while detailed JSON retains the
	// original percentage, so display rounding does not replace stored usage.
	return fmt.Sprintf(
		"%s scheduling threshold reached for %s: %.1f%% used >= %d%%; paused until %s",
		platform,
		target,
		input.UsedPercent,
		threshold,
		untilText,
	)
}

// tempUnschedStateFromStoredReason restores runtime pause state from
// structured JSON or legacy text. The supplied fallback deadline remains the
// minimum returned Unix deadline, and a missing rule index starts at minus
// one for a system rule.
//
// A blank reason supplies the temporary-block message. Valid JSON keeps
// recognized TempUnschedState fields, raises a shorter deadline, and supplies
// missing text from the recognized threshold default or trimmed reason JSON.
// Failed decoding returns the trimmed reason text with the fallback deadline,
// retaining that hold when structured diagnostics are unavailable.
func tempUnschedStateFromStoredReason(rawReason string, fallbackUntilUnix int64) *TempUnschedState {
	state := &TempUnschedState{
		UntilUnix: fallbackUntilUnix,
		RuleIndex: -1,
	}

	rawReason = strings.TrimSpace(rawReason)
	if rawReason == "" {
		state.ErrorMessage = defaultTempUnschedReasonErrorMessage
		return state
	}

	// Initialize the system-rule sentinel before decoding older JSON that may
	// omit rule_index; zero would incorrectly identify the first configured
	// rule.
	parsed := TempUnschedState{RuleIndex: -1}
	if err := json.Unmarshal([]byte(rawReason), &parsed); err == nil {
		// Storage already supplies an active deadline. Keep its later value when an
		// incomplete or older reason would shorten the runtime block.
		if fallbackUntilUnix > parsed.UntilUnix {
			parsed.UntilUnix = fallbackUntilUnix
		}
		// Structured reasons can omit their readable text. Recognition selects a
		// threshold-specific message; other JSON stays available as the raw
		// explanation rather than becoming an empty administration field.
		if strings.TrimSpace(parsed.ErrorMessage) == "" {
			if IsProviderSchedulingThresholdReason(rawReason) {
				parsed.ErrorMessage = defaultProviderSchedulingThresholdErrorMessage
			} else {
				parsed.ErrorMessage = rawReason
			}
		}
		return &parsed
	}

	// A legacy text reason or malformed JSON remains readable with the storage
	// deadline, so decoding failure does not discard the scheduling hold.
	state.ErrorMessage = rawReason
	return state
}
