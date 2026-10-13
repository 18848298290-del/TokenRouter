package openai

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Fingerprint modes choose how many Codex identities are shared for a provider.
// Device mode replaces installation identity, session mode also shares a
// provider session while retaining distinct client threads, and full mode
// shares one thread across clients.
//
// One FingerprintIDs value supplies the headers, client_metadata, embedded turn
// metadata, and per-request turn timestamp. Stable session/thread derivation
// uses fixed hash domains, while the turn identifier comes from the supplied
// fresh-ID callback. Original body session identity is captured before
// rewriting so a default prompt_cache_key can follow the shared session without
// changing a caller-selected cache key. Raw-body rewriting splices the small
// metadata object and optional cache key, preserving large surrounding request
// fields.

// FingerprintIDs holds the attempt identity reused by outbound headers and
// request metadata.
type FingerprintIDs struct {
	// InstallationID, SessionID, ThreadID, TurnID, WindowID, and
	// TurnStartedAtUnixMs describe one attempt. Header and body writers reuse
	// this tuple so identity fields and embedded metadata stay consistent.
	ProviderID          int64  `json:"-"` // provider owning attempt identity
	Mode                string `json:"-"` // resolved fingerprint mode value
	InstallationID      string `json:"-"` // resolved device installation identifier
	SessionID           string `json:"-"` // resolved provider session identifier
	ThreadID            string `json:"-"` // resolved client thread identifier
	TurnID              string `json:"-"` // fresh per-attempt turn identifier
	WindowID            string `json:"-"` // thread-derived window identifier
	TurnStartedAtUnixMs int64  `json:"-"` // Unix milliseconds
	// OriginalBodySessionID retains the trimmed inbound body session; cache-key
	// matching reads it after fingerprint rewrites change client_metadata for the
	// attempt.
	OriginalBodySessionID string `json:"-"`
	// OriginalBodySessionIDCaptured records the first read even when the value is
	// missing, preventing a later rewritten session from becoming the original
	// identity.
	OriginalBodySessionIDCaptured bool `json:"-"`
}

// ResolveFingerprintIDs derives the identity selected by mode using the
// supplied clock and callbacks. Callbacks are required on the branches using
// them; empty installation identity or an unsupported mode returns nil. Device
// mode stops after installation identity.
//
// Session mode derives a provider-wide session and a client-specific thread,
// using the session as thread when the client-session derivation is empty. Full
// mode uses the provider session for its thread directly. Both modes request
// one fresh turn ID and derive window identity from the thread, so callers
// reuse the returned value across all representations and retries in that
// attempt.
func ResolveFingerprintIDs(providerID int64, seed, clientSessionID, mode string, installation func(string) string, now func() time.Time, newTurnID func() string) *FingerprintIDs {
	// Codex turn metadata represents this timestamp in Unix milliseconds.
	ids := &FingerprintIDs{ProviderID: providerID, Mode: mode, TurnStartedAtUnixMs: now().UnixMilli()}
	ids.InstallationID = installation(seed)
	if ids.InstallationID == "" {
		return nil
	}
	switch mode {
	case "device":
		return ids
	case "session":
		ids.SessionID = ResolveConvergedSessionID(seed)
		ids.ThreadID = ResolveConvergedThreadID(seed, clientSessionID)
		// Missing client-session evidence selects the shared provider session as its
		// thread, so enabled fingerprinting still supplies a usable thread identity.
		if ids.ThreadID == "" {
			ids.ThreadID = ids.SessionID
		}
		ids.TurnID = newTurnID()
		ids.WindowID = ids.ThreadID + ":0"
		return ids
	case "full":
		ids.SessionID = ResolveConvergedSessionID(seed)
		ids.ThreadID = ids.SessionID
		ids.TurnID = newTurnID()
		ids.WindowID = ids.ThreadID + ":0"
		return ids
	}
	return nil
}

// ResolveConvergedSessionID derives a deterministic provider session identity
// from seed. An empty seed returns empty text; the fixed derivation domain
// keeps callers using the same seed on the same session namespace.
func ResolveConvergedSessionID(seed string) string {
	if seed == "" {
		return ""
	}
	// The derivation domain is part of persisted session identity. Keeping its
	// literal value stable lets the same provider seed retain its upstream session.
	return DeriveStableUUIDv4("sub2api:codex-session-id:v2:" + seed)
}

// ResolveConvergedThreadID derives a deterministic thread identity from seed
// and client session. Either missing input returns empty text, allowing
// ResolveFingerprintIDs to use the provider session when no client thread can
// be derived.
func ResolveConvergedThreadID(seed, clientSessionID string) string {
	if seed == "" || clientSessionID == "" {
		return ""
	}
	// The domain separates thread derivation from session derivation. Keeping it
	// stable lets the same provider/client-session pair retain its upstream thread.
	return DeriveStableUUIDv4("sub2api:codex-thread-id:v2:" + seed + ":" + clientSessionID)
}

// ExtractClientSessionID reads the trimmed hyphenated session header before its
// underscore alias. Call before applying fingerprints so the result reflects
// client input; absent or blank values produce empty text.
func ExtractClientSessionID(h http.Header) string {
	if v := strings.TrimSpace(h.Get("session-id")); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get("session_id"))
}

// ApplyCodexFingerprintHeaders writes ids into the outbound header
// representations for one attempt. Nil headers or IDs are ignored; callers use
// a resolved identity and invoke this after inbound forwarding and before final
// Codex client pairing. Device mode updates installation fields alone.
//
// Session and full modes update both session spellings along with thread and
// window identity, then rewrite an existing turn-metadata header with the same
// values. Embedded metadata therefore describes the IDs sent beside it. This
// operation uses precomputed turn fields so retries and body rewrites can
// retain the same identity instead of independently generating new turn data.
func ApplyCodexFingerprintHeaders(h http.Header, ids *FingerprintIDs) {
	if h == nil || ids == nil {
		return
	}

	// Installation identity applies to every enabled mode; write it first so
	// device mode can return after updating the corresponding existing turn-metadata field.
	h.Set("x-codex-installation-id", ids.InstallationID)

	if ids.Mode == "device" {
		RewriteCodexTurnMetadataFields(h, map[string]any{
			"installation_id": ids.InstallationID,
		})
		return
	}

	// Session and full modes publish their precomputed session, thread, and window
	// identities together so top-level headers refer to one attempt before embedded metadata is rebuilt.
	h.Set("x-codex-window-id", ids.WindowID)
	h.Set("x-client-request-id", ids.ThreadID)
	// Different Codex clients read different session-header spellings; publish
	// the same resolved session through both so reconnecting clients keep one identity.
	h.Set("session-id", ids.SessionID)
	h.Set("session_id", ids.SessionID)
	h.Set("thread-id", ids.ThreadID)

	RewriteCodexTurnMetadataFields(h, map[string]any{
		"installation_id":         ids.InstallationID,
		"session_id":              ids.SessionID,
		"thread_id":               ids.ThreadID,
		"turn_id":                 ids.TurnID,
		"window_id":               ids.WindowID,
		"turn_started_at_unix_ms": ids.TurnStartedAtUnixMs,
	})
}

// RewriteCodexTurnMetadataFields overlays fields into an existing nonblank
// turn-metadata header. Headers must be writable when present, and fields must
// contain JSON-encodable values. Missing or blank headers remain absent.
//
// Valid object metadata keeps unrelated fields while the supplied identity
// values take precedence. Invalid JSON or a null object starts an empty map so
// its rebuilt identity agrees with the top-level headers. Encoding failure
// retains the previous header, allowing the caller to keep response
// construction moving when optional metadata rewriting cannot be published.
func RewriteCodexTurnMetadataFields(h http.Header, fields map[string]any) {
	raw := strings.TrimSpace(h.Get("x-codex-turn-metadata"))
	if raw == "" {
		return
	}
	var metadata map[string]any
	// Unusable existing metadata cannot carry matching identity fields. Rebuild
	// an object with the supplied tuple while normal JSON decoding handles other values.
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	maps.Copy(metadata, fields)
	rebuilt, err := json.Marshal(metadata)
	// Optional metadata rewriting leaves the existing header when the supplied
	// fields cannot be encoded, rather than publishing a partial identity document.
	if err != nil {
		return
	}
	h.Set("x-codex-turn-metadata", string(rebuilt))
}

// ApplyCodexFingerprintClientMetadata rewrites decoded body metadata and
// reports whether a field was assigned. It captures the original session before
// mutation and reuses the supplied attempt IDs; callers own the request maps
// and serialize access. Nil bodies or IDs return false.
//
// An absent or nonobject client_metadata value is replaced when fingerprint
// application produces fields. The original body session remains in ids across
// later calls, so prompt_cache_key is rewritten only when it matches that
// captured value in session or full mode. Caller-selected cache keys retain
// their meaning while headers and metadata use the shared identity.
func ApplyCodexFingerprintClientMetadata(reqBody map[string]any, ids *FingerprintIDs) bool {
	if reqBody == nil || ids == nil {
		return false
	}

	CaptureCodexFingerprintOriginalBodySessionID(ids, reqBody["client_metadata"])
	existing, _ := reqBody["client_metadata"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	modified := false
	if ApplyCodexFingerprintToClientMetadataMap(existing, ids) {
		reqBody["client_metadata"] = existing
		modified = true
	}
	if ApplyCodexFingerprintPromptCacheKey(reqBody, ids) {
		modified = true
	}
	return modified
}

// ApplyCodexFingerprintToClientMetadataMap writes a resolved fingerprint into a
// caller-owned metadata map. Nil inputs return false, device mode limits
// changes to installation metadata, and other resolved modes assign session,
// thread, turn, and window values.
func ApplyCodexFingerprintToClientMetadataMap(existing map[string]any, ids *FingerprintIDs) bool {
	if existing == nil || ids == nil {
		return false
	}

	modified := false

	if ids.InstallationID != "" {
		existing["x-codex-installation-id"] = ids.InstallationID
		modified = true
	}

	if ids.Mode == "device" {
		RewriteClientMetadataEmbeddedTurnMetadata(existing, map[string]any{
			"installation_id": ids.InstallationID,
		})
		return modified
	}

	// Session and full modes publish the same precomputed turn tuple used by
	// headers, keeping body-level and embedded metadata on one identity for this attempt.
	existing["session_id"] = ids.SessionID
	existing["thread_id"] = ids.ThreadID
	existing["turn_id"] = ids.TurnID
	existing["x-codex-window-id"] = ids.WindowID

	RewriteClientMetadataEmbeddedTurnMetadata(existing, map[string]any{
		"installation_id":         ids.InstallationID,
		"session_id":              ids.SessionID,
		"thread_id":               ids.ThreadID,
		"turn_id":                 ids.TurnID,
		"window_id":               ids.WindowID,
		"turn_started_at_unix_ms": ids.TurnStartedAtUnixMs,
	})
	return true
}

// CaptureCodexFingerprintOriginalBodySessionID captures the trimmed inbound
// session once from a supported metadata map. It records capture even for
// absent or unsupported values; later rewrites therefore cannot replace the
// original-session evidence.
func CaptureCodexFingerprintOriginalBodySessionID(ids *FingerprintIDs, clientMetadata any) {
	if ids == nil || ids.OriginalBodySessionIDCaptured {
		return
	}
	ids.OriginalBodySessionIDCaptured = true
	if clientMetadata == nil {
		return
	}
	switch metadata := clientMetadata.(type) {
	case map[string]any:
		if sessionID, ok := metadata["session_id"].(string); ok {
			ids.OriginalBodySessionID = strings.TrimSpace(sessionID)
		}
	case map[string]string:
		ids.OriginalBodySessionID = strings.TrimSpace(metadata["session_id"])
	}
}

// CaptureCodexFingerprintOriginalBodySessionIDRaw captures a trimmed string
// session from parsed raw metadata once. Missing or nonstring values still
// finish capture with an empty value, preventing later rewritten metadata from
// becoming original-session evidence.
func CaptureCodexFingerprintOriginalBodySessionIDRaw(ids *FingerprintIDs, value gjson.Result) {
	if ids == nil || ids.OriginalBodySessionIDCaptured {
		return
	}
	ids.OriginalBodySessionIDCaptured = true
	if value.Exists() && value.Type == gjson.String {
		ids.OriginalBodySessionID = strings.TrimSpace(value.String())
	}
}

// ShouldRewriteCodexFingerprintPromptCacheKey reports whether a cache key
// equals the captured nonempty body session. Capture, resolved session
// identity, and session/full mode are required; exact comparison leaves
// independently selected cache keys unchanged.
func ShouldRewriteCodexFingerprintPromptCacheKey(ids *FingerprintIDs, promptCacheKey string) bool {
	if ids == nil || !ids.OriginalBodySessionIDCaptured || ids.OriginalBodySessionID == "" || ids.SessionID == "" {
		return false
	}
	if ids.Mode != "session" && ids.Mode != "full" {
		return false
	}
	return promptCacheKey == ids.OriginalBodySessionID
}

// ApplyCodexFingerprintPromptCacheKey replaces an eligible default cache key
// with ids.SessionID. Missing, blank, nonstring, independently selected, or
// already-converged values produce false; the request map is updated in place
// when the replacement differs.
func ApplyCodexFingerprintPromptCacheKey(reqBody map[string]any, ids *FingerprintIDs) bool {
	if reqBody == nil {
		return false
	}
	promptCacheKey, ok := reqBody["prompt_cache_key"].(string)
	if !ok || strings.TrimSpace(promptCacheKey) == "" || !ShouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey) {
		return false
	}
	if promptCacheKey == ids.SessionID {
		return false
	}
	reqBody["prompt_cache_key"] = ids.SessionID
	return true
}

// ApplyCodexFingerprintClientMetadataRaw returns a body with fingerprint
// metadata spliced into selected fields. Nil IDs, empty input, and nonobject
// roots retain the input bytes; calls mutate the original-session capture state
// in ids and require serialized access. Only client_metadata is fully decoded.
//
// The original session is captured before metadata replacement, allowing an
// exact matching default prompt_cache_key to follow ids.SessionID. Existing
// object metadata retains decoded members using encoding/json number handling;
// nonobject metadata is replaced by the
// resolved fingerprint fields. A decode, encode, or splice error returns the
// supplied body and false, so partially rewritten bytes are not published as a
// successful result.
func ApplyCodexFingerprintClientMetadataRaw(body []byte, ids *FingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	// Field splicing into a nonobject root could replace the complete request.
	// Finish original-session capture but retain those bytes for the owning request validator.
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		CaptureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
		return body, false, nil
	}

	existing := map[string]any{}
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		CaptureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.GetBytes(body, "client_metadata.session_id"))
		if err := json.Unmarshal([]byte(cm.Raw), &existing); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for fingerprint: %w", err)
		}
	} else {
		CaptureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
	}

	next := body
	modified := false
	if ApplyCodexFingerprintToClientMetadataMap(existing, ids) {
		raw, err := json.Marshal(existing)
		if err != nil {
			return body, false, fmt.Errorf("encode converged client_metadata: %w", err)
		}
		var setErr error
		next, setErr = sjson.SetRawBytes(body, "client_metadata", raw)
		if setErr != nil {
			return body, false, fmt.Errorf("splice converged client_metadata: %w", setErr)
		}
		modified = true
	}
	promptCacheKey := gjson.GetBytes(body, "prompt_cache_key")
	if promptCacheKey.Exists() && promptCacheKey.Type == gjson.String && strings.TrimSpace(promptCacheKey.String()) != "" && ShouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey.String()) {
		rewritten, err := sjson.SetBytes(next, "prompt_cache_key", ids.SessionID)
		if err != nil {
			return body, false, fmt.Errorf("splice converged prompt_cache_key: %w", err)
		}
		next = rewritten
		modified = true
	}
	return next, modified, nil
}

// RewriteClientMetadataEmbeddedTurnMetadata updates an existing nonempty string
// with supplied identity fields. Invalid or null object JSON is rebuilt,
// unrelated decoded members remain, and encoding failure leaves the existing
// string in the caller-owned metadata map.
func RewriteClientMetadataEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any) {
	raw, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok || raw == "" {
		return
	}
	var metadata map[string]any
	// Unusable existing metadata cannot carry matching identity fields. Rebuild
	// an object with the supplied tuple while normal JSON decoding handles other values.
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	maps.Copy(metadata, fields)
	// Publish complete embedded JSON only after encoding succeeds, so a failed
	// optional rewrite leaves the existing metadata string available to the request builder.
	if rebuilt, err := json.Marshal(metadata); err == nil {
		clientMetadata["x-codex-turn-metadata"] = string(rebuilt)
	}
}
