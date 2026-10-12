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

// FingerprintIDs holds the identity shared by headers and body within one attempt.
// It also captures the inbound body session ID to recognize a default prompt_cache_key.
// Fields are excluded from JSON serialization.
type FingerprintIDs struct {
	ProviderID                    int64  `json:"-"`
	Mode                          string `json:"-"`
	InstallationID                string `json:"-"`
	SessionID                     string `json:"-"`
	ThreadID                      string `json:"-"`
	TurnID                        string `json:"-"`
	WindowID                      string `json:"-"`
	TurnStartedAtUnixMs           int64  `json:"-"`
	OriginalBodySessionID         string `json:"-"`
	OriginalBodySessionIDCaptured bool   `json:"-"`
}

// ResolveFingerprintIDs derives one attempt identity using the supplied clock and turn-ID source.
// It returns nil for an empty installation ID or unsupported mode.
func ResolveFingerprintIDs(providerID int64, seed, clientSessionID, mode string, installation func(string) string, now func() time.Time, newTurnID func() string) *FingerprintIDs {
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

// ResolveConvergedSessionID derives a stable provider session ID from seed.
func ResolveConvergedSessionID(seed string) string {
	if seed == "" {
		return ""
	}
	// Keep the hash domain stable across upgrades so session IDs do not change.
	return DeriveStableUUIDv4("sub2api:codex-session-id:v2:" + seed)
}

// ResolveConvergedThreadID derives a separate stable thread ID for each client session and seed.
func ResolveConvergedThreadID(seed, clientSessionID string) string {
	if seed == "" || clientSessionID == "" {
		return ""
	}
	// Keep the hash domain stable across upgrades so thread IDs do not change.
	return DeriveStableUUIDv4("sub2api:codex-thread-id:v2:" + seed + ":" + clientSessionID)
}

// ExtractClientSessionID prefers the Codex session-id header, then session_id.
// Call before rewriting headers to obtain the inbound client identity.
func ExtractClientSessionID(h http.Header) string {
	if v := strings.TrimSpace(h.Get("session-id")); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get("session_id"))
}

// ApplyCodexFingerprintHeaders writes precomputed IDs into outbound headers.
// Call after forwarding inbound headers and before enforcing the Codex identity.
func ApplyCodexFingerprintHeaders(h http.Header, ids *FingerprintIDs) {
	if h == nil || ids == nil {
		return
	}

	// Every enabled mode replaces the installation ID.
	h.Set("x-codex-installation-id", ids.InstallationID)

	if ids.Mode == "device" {
		RewriteCodexTurnMetadataFields(h, map[string]any{
			"installation_id": ids.InstallationID,
		})
		return
	}

	// Session and full modes also replace the session and thread headers.
	h.Set("x-codex-window-id", ids.WindowID)
	h.Set("x-client-request-id", ids.ThreadID)
	// Keep both session-header spellings consistent.
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

// RewriteCodexTurnMetadataFields updates fields in an existing turn-metadata header.
// Valid JSON objects retain other fields. Invalid nonempty values are rebuilt so
// embedded and top-level identities agree; a missing header is left absent.
func RewriteCodexTurnMetadataFields(h http.Header, fields map[string]any) {
	raw := strings.TrimSpace(h.Get("x-codex-turn-metadata"))
	if raw == "" {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	maps.Copy(metadata, fields)
	rebuilt, err := json.Marshal(metadata)
	if err != nil {
		return
	}
	h.Set("x-codex-turn-metadata", string(rebuilt))
}

// ApplyCodexFingerprintClientMetadata rewrites body identity and a session-derived prompt_cache_key.
// Use the same ids instance for headers so random turn fields agree.
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

// ApplyCodexFingerprintToClientMetadataMap rewrites fingerprint fields for decoded and raw-JSON callers.
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

	// Session and full modes also replace session, thread and turn fields.
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

func CaptureCodexFingerprintOriginalBodySessionIDRaw(ids *FingerprintIDs, value gjson.Result) {
	if ids == nil || ids.OriginalBodySessionIDCaptured {
		return
	}
	ids.OriginalBodySessionIDCaptured = true
	if value.Exists() && value.Type == gjson.String {
		ids.OriginalBodySessionID = strings.TrimSpace(value.String())
	}
}

func ShouldRewriteCodexFingerprintPromptCacheKey(ids *FingerprintIDs, promptCacheKey string) bool {
	if ids == nil || !ids.OriginalBodySessionIDCaptured || ids.OriginalBodySessionID == "" || ids.SessionID == "" {
		return false
	}
	if ids.Mode != "session" && ids.Mode != "full" {
		return false
	}
	return promptCacheKey == ids.OriginalBodySessionID
}

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

// ApplyCodexFingerprintClientMetadataRaw rewrites metadata in passthrough JSON.
// It decodes only client_metadata and splices it back to avoid decoding large bodies.
// Other bytes are retained except prompt_cache_key when it matches the captured body
// session ID. Non-object metadata is replaced with the fingerprint fields.
func ApplyCodexFingerprintClientMetadataRaw(body []byte, ids *FingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	// Writing fields into a non-object root could replace the entire body; leave it unchanged.
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

// RewriteClientMetadataEmbeddedTurnMetadata updates an existing nonempty metadata string.
// Invalid JSON and non-object values are rebuilt so embedded and top-level identities agree.
func RewriteClientMetadataEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any) {
	raw, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok || raw == "" {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	maps.Copy(metadata, fields)
	if rebuilt, err := json.Marshal(metadata); err == nil {
		clientMetadata["x-codex-turn-metadata"] = string(rebuilt)
	}
}
