package completion

import "strings"

// Financial deduplication keeps historical prefixes and stable operation keys.
// A local diagnostic request and an upstream video or audio operation can use
// different identifiers, so repeated task completion reuses its financial key
// while analysis rows retain the request used for diagnostic correlation.
// PayloadFingerprint independently chooses evidence for detecting conflicting
// reuse of the selected settlement key.
//
// The generic identity input accepts separate compatibility and local seeds.
// Recognized operation keys take priority, and SettlementKey then applies
// WebSocket-turn and video-task overrides. Source adapters decide which seeds
// are available; gateway/provider's production capture reads the compatibility
// and local values from one aliased internal request-ID context key.

// RequestIdentity contains correlation identifiers captured before the HTTP request ends.
type RequestIdentity struct {
	// Client and Local can be supplied independently. gateway/provider.RequestIdentity
	// reads them from telemetry.ClientRequestID and RequestID, which share one
	// compatibility context key, so production capture supplies the same internal ID.
	// Upstream and PayloadHash retain separate operation and fingerprint evidence.
	// Client: compatibility request seed. Local: local diagnostic identifier.
	// Upstream: upstream or operation identifier. PayloadHash: semantic payload digest.
	Client, Local, Upstream, PayloadHash string
}

// ForcedRequestID reports whether a trimmed identifier has a recognized operation
// prefix. Matching is case sensitive and covers web search, Grok video, audio,
// and Realtime keys used by billing capture.
func ForcedRequestID(id string) bool {
	id = strings.TrimSpace(id)
	return strings.HasPrefix(id, "web_search:") || strings.HasPrefix(id, "grok-video:") || strings.HasPrefix(id, "grok_audio:") || strings.HasPrefix(id, "grok_realtime:")
}

// ResolveBillingKey chooses the financial deduplication identity from captured
// request evidence. A recognized upstream operation key wins over caller and
// local seeds; other nonblank upstream IDs are used after those prefixed choices.
//
// Every candidate is trimmed before selection. Compatibility and local prefixes keep
// those namespaces distinct, and the generator runs only when every captured ID
// is blank. Callers provide a nonnil generator for that case and keep the resulting
// key separate from the diagnostic request ID.
// @project-doc docs/operations/request_lookup.md#billing_identity
func ResolveBillingKey(in RequestIdentity, generate func() string) string {
	upstream := strings.TrimSpace(in.Upstream)
	if upstream != "" && ForcedRequestID(upstream) {
		return upstream
	}
	if client := strings.TrimSpace(in.Client); client != "" {
		return "client:" + client
	}
	if local := strings.TrimSpace(in.Local); local != "" {
		return "local:" + local
	}
	if upstream != "" {
		return upstream
	}
	// Generate once when capture has no usable identity, so the submitted snapshot
	// can reuse one financial key throughout its later settlement and log writes.
	return "generated:" + generate()
}

// PayloadFingerprint chooses the supplied semantic digest or an identity-based
// substitute for the settlement command. Nonblank payload evidence wins, followed
// by prefixed compatibility and local seeds; every selected value is trimmed.
//
// An upstream operation ID can identify a settlement while this function returns
// empty, because upstream aliases alone provide no payload substitute here.
// Billing includes the returned evidence in its broader command fingerprint, so
// callers retain both the settlement key and this value when capturing completion.
func PayloadFingerprint(in RequestIdentity) string {
	if hash := strings.TrimSpace(in.PayloadHash); hash != "" {
		return hash
	}
	if client := strings.TrimSpace(in.Client); client != "" {
		return "client:" + client
	}
	if local := strings.TrimSpace(in.Local); local != "" {
		return "local:" + local
	}
	return ""
}

// StableAudioRequestID returns a Grok audio settlement key from a trimmed ID.
// An existing audio prefix is retained; blank IDs invoke the required generator
// before the prefix is added.
func StableAudioRequestID(id string, generate func() string) string {
	return stableRequestID("grok_audio:", id, generate)
}

// StableRealtimeRequestID returns a Grok Realtime key from a trimmed session ID.
// An existing Realtime prefix is retained; blank IDs invoke the required generator
// before the prefix is added.
func StableRealtimeRequestID(id string, generate func() string) string {
	return stableRequestID("grok_realtime:", id, generate)
}

// stableRequestID applies one operation prefix after trimming the supplied ID.
// Already prefixed IDs are returned directly. A blank ID calls generate, which
// must be nonnil when that path is possible.
func stableRequestID(prefix, id string, generate func() string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, prefix) {
		return id
	}
	// A missing operation ID needs one generated seed; callers retain the returned
	// prefixed value so later completion uses the same financial identity.
	if id == "" {
		id = generate()
	}
	return prefix + id
}

// SettlementKey returns the financial identity after turn and media overrides.
// The input must be nonnil; an absent result uses BillingKey, or RequestID when
// BillingKey is empty.
//
// A nonblank WebSocket result ID replaces that initial key. Positive VideoCount
// then selects a stable video key from result RequestID, ResponseID, or the
// current key, in that order, stripping the video prefix where appropriate before
// reapplying it. These choices leave Input.RequestID available for diagnostic
// correlation even when the financial operation has a task-specific identity.
func (in *Input) SettlementKey() string {
	key := in.BillingKey
	if key == "" {
		key = in.RequestID
	}
	if in.Result == nil {
		return key
	}
	result := in.Result
	// Each WebSocket turn settles independently; the result carries the stable
	// turn identity even when capture began from the connection request context.
	if result.OpenAIWSMode && strings.TrimSpace(result.RequestID) != "" {
		key = strings.TrimSpace(result.RequestID)
	}
	// Repeated video polling reuses the task key, preferring its creation request
	// identifier and then response identity before the already selected key.
	if result.VideoCount > 0 {
		if stable := stableVideoRequestID(firstNonEmpty(strings.TrimPrefix(strings.TrimSpace(result.RequestID), "grok-video:"), strings.TrimSpace(result.ResponseID), strings.TrimPrefix(strings.TrimSpace(key), "grok-video:"))); stable != "" {
			key = stable
		}
	}
	return key
}
