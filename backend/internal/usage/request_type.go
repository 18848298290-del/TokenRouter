package usage

import (
	"fmt"
	"strings"
)

// Usage records carry a request category alongside older stream and WebSocket
// flags. An explicit transport category determines those flags; older records
// can still recover a category from their booleans, with WebSocket taking
// precedence when both flags suggest it. Query parsing accepts stable lowercase
// labels and storage conversion keeps unrecognized integers in the unknown
// category.
//
// Safety-refusal and Live categories describe execution outcomes beyond the
// three transport shapes. ApplyLegacyRequestFields retains their supplied
// booleans so selecting a special category does not discard recorded transport
// information. Normalization and display labels remain separate from that
// legacy resolution.

// Billing and request-type values encode stored funding labels and execution
// categories consumed by usage filters and response formatting.
const (
	BillingTypeBalance      int8 = 0 // Wallet balance
	BillingTypeSubscription int8 = 1 // Subscription plan

	RequestTypeUnknown      RequestType = 0 // unspecified recorded request type
	RequestTypeSync         RequestType = 1 // synchronous response request type
	RequestTypeStream       RequestType = 2 // streamed response request type
	RequestTypeWSV2         RequestType = 3 // OpenAI WebSocket turn type
	RequestTypeCyberBlocked RequestType = 4 // Upstream safety refusal forwarded after a cyber_policy match
	// RequestTypeLive identifies ChatGPT Frameless Live requests recorded by
	// session duration.
	RequestTypeLive RequestType = 5
)

// RequestType is the persisted usage category with legacy transport
// compatibility.
type RequestType int16

// IsValid reports whether the code names a supported usage request category.
func (t RequestType) IsValid() bool {
	switch t {
	case RequestTypeUnknown, RequestTypeSync, RequestTypeStream, RequestTypeWSV2, RequestTypeCyberBlocked, RequestTypeLive:
		return true
	default:
		return false
	}
}

// Normalize returns the supported request type or the unknown code for invalid
// values. Callers can retain unknown before resolving older records from their
// stream and WebSocket flags.
func (t RequestType) Normalize() RequestType {
	if t.IsValid() {
		return t
	}
	// Invalid codes retain the unknown marker so record readers can consult legacy
	// transport flags before deciding which effective request category to display.
	return RequestTypeUnknown
}

// String returns the lowercase query and response label for this request type.
// Invalid codes normalize to unknown, keeping display and filtering labels
// within the supported category set.
func (t RequestType) String() string {
	switch t.Normalize() {
	case RequestTypeSync:
		return "sync"
	case RequestTypeStream:
		return "stream"
	case RequestTypeWSV2:
		return "ws_v2"
	case RequestTypeCyberBlocked:
		return "cyber"
	case RequestTypeLive:
		return "live"
	default:
		return "unknown"
	}
}

// RequestTypeFromInt16 converts a stored integer to a supported request
// category. Values outside the recognized codes return unknown so historical
// transport flags can supply an effective type.
func RequestTypeFromInt16(v int16) RequestType {
	return RequestType(v).Normalize()
}

// ParseUsageRequestType parses a case-insensitive query label after trimming
// whitespace. Empty and unrecognized strings return an error alongside unknown;
// callers reject that error before applying a filter.
func ParseUsageRequestType(value string) (RequestType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unknown":
		return RequestTypeUnknown, nil
	case "sync":
		return RequestTypeSync, nil
	case "stream":
		return RequestTypeStream, nil
	case "ws_v2":
		return RequestTypeWSV2, nil
	case "cyber":
		return RequestTypeCyberBlocked, nil
	case "live":
		return RequestTypeLive, nil
	default:
		return RequestTypeUnknown, fmt.Errorf("invalid request_type, allowed values: unknown, sync, stream, ws_v2, cyber, live")
	}
}

// RequestTypeFromLegacy derives the transport category represented by older
// boolean fields. WebSocket mode takes precedence over stream because a
// WebSocket row can carry either stream value. When WebSocket mode is false,
// the stream flag distinguishes streamed and synchronous requests.
//
// This mapping lets UsageLog.EffectiveRequestType recover records whose request
// category is unknown. Callers needing special safety-refusal or Live
// categories retain those explicit codes before consulting legacy flags.
func RequestTypeFromLegacy(stream bool, openAIWSMode bool) RequestType {
	// Both older flags can be true for a WebSocket row. Resolving WebSocket before
	// stream prevents those records from being classified as ordinary HTTP streaming.
	if openAIWSMode {
		return RequestTypeWSV2
	}
	if stream {
		return RequestTypeStream
	}
	return RequestTypeSync
}

// ApplyLegacyRequestFields returns booleans corresponding to a supported
// transport category. Sync clears both flags, stream sets only stream, and WS
// v2 sets both so older consumers receive the corresponding transport shape.
//
// Safety-refusal, Live, unknown, and invalid categories retain fallbackStream
// and fallbackOpenAIWSMode. Those categories do not supply a complete transport
// representation, so rebuilding their flags from the category alone would erase
// information still recorded by the caller.
func ApplyLegacyRequestFields(requestType RequestType, fallbackStream bool, fallbackOpenAIWSMode bool) (stream bool, openAIWSMode bool) {
	// Explicit transport categories replace stale booleans during PostgreSQL
	// hydration and HTTP formatting; otherwise a synchronous record could still
	// be displayed as WebSocket.
	switch requestType.Normalize() {
	case RequestTypeSync:
		return false, false
	case RequestTypeStream:
		return true, false
	case RequestTypeWSV2:
		return true, true
	default:
		// Safety refusals, Live sessions, and unknown categories carry separate
		// execution meaning, so retain their recorded transport flags for consumers.
		return fallbackStream, fallbackOpenAIWSMode
	}
}
