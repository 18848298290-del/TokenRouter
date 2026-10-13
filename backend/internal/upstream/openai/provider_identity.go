package openai

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Codex device and session values are scoped by API key and provider credential
// namespace before they reach an upstream account. Account/user identifiers
// take precedence over a stored seed and setup-token digest, so providers using
// the same credential identity can derive the same namespace.
//
// Identity kinds keep sessions, threads, turns, installations, and cache keys
// in separate hash domains. A prompt_cache_key equal to the original body
// session uses the session kind, retaining that intentional relationship after
// both values are rewritten. Decoded request maps are changed in place; raw
// requests decode and splice the small metadata object to retain surrounding
// large fields. Existing malformed embedded metadata is left untouched rather
// than treated as a usable identity object.

// Provider identity constants version the stable namespace derivation and name
// embedded turn metadata shared by HTTP and WebSocket request identity handling.
const (
	codexProviderIdentityNamespaceVersion = "v1"
	WSTurnMetadataHeader                  = "x-codex-turn-metadata"
)

// codexProviderIdentityFields maps known identity spellings to separate derivation
// kinds so equivalent session aliases share a hash domain while unrelated IDs remain distinct.
var codexProviderIdentityFields = []struct {
	name string // known identity field spelling
	kind string // identity hash derivation kind
}{
	{name: "installation_id", kind: "installation"},
	{name: "x-codex-installation-id", kind: "installation"},
	{name: "session_id", kind: "session"},
	{name: "session-id", kind: "session"},
	{name: "thread_id", kind: "thread"},
	{name: "thread-id", kind: "thread"},
	{name: "turn_id", kind: "turn"},
	{name: "turn-id", kind: "turn"},
	{name: "window_id", kind: "window"},
	{name: "x-codex-window-id", kind: "window"},
	{name: "x-client-request-id", kind: "request"},
}

// ProviderIdentityInput supplies credential identities used to choose a
// provider namespace.
type ProviderIdentityInput struct {
	// ChatGPTAccountID selects the account namespace, ChatGPTUserID refines it,
	// and Seed supplies the next choice when HasSeed records seed presence.
	ChatGPTAccountID, ChatGPTUserID, Seed string
	// HasSeed records seed presence independently of its string value; namespace
	// selection prefers that seed branch before the setup-token digest.
	HasSeed    bool
	SetupToken string `json:"-"` // setup-token credential digest input
}

// String returns a fixed namespace-input label that omits credentials.
func (ProviderIdentityInput) String() string { return "openai provider identity input" }

// GoString returns the redacted namespace-input label for Go-syntax formatting.
func (v ProviderIdentityInput) GoString() string { return v.String() }

// CodexProviderNamespace selects a stable namespace from the supplied
// credential evidence. Nonempty account ID takes precedence and includes user
// ID when present. Otherwise HasSeed selects Seed, even if Seed is empty,
// before a setup token can provide its domain-separated truncated digest.
//
// The order keeps a provider tied to its account identity when richer metadata
// is available, while older seed or setup-token credentials still have a
// reproducible namespace. All inputs are used as supplied here, and no evidence
// returns empty text for callers to skip credential scoping.
func CodexProviderNamespace(input ProviderIdentityInput) string {
	if input.ChatGPTAccountID != "" {
		if input.ChatGPTUserID != "" {
			return "chatgpt:" + input.ChatGPTAccountID + ":user:" + input.ChatGPTUserID
		}
		return "chatgpt:" + input.ChatGPTAccountID
	}
	if input.HasSeed {
		return "seed:" + input.Seed
	}
	if input.SetupToken != "" {
		sum := sha256.Sum256([]byte("openai-setup-token:" + input.SetupToken))
		return fmt.Sprintf("setup-token:%x", sum[:16])
	}
	return ""
}

// IsolateOpenAIUpstreamSessionID returns a trimmed session hashed with API-key
// ID and optional namespace. Blank sessions return empty text; absent namespace
// delegates legacy key isolation and configured namespaces use a
// sixteen-hex-character digest.
func IsolateOpenAIUpstreamSessionID(apiKeyID int64, namespace string, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if namespace == "" {
		return upstream.IsolateSessionID(apiKeyID, raw)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("u%d:a%s:%s", apiKeyID, namespace, raw)))
	return fmt.Sprintf("%x", sum[:8])
}

// ScopeCodexProviderIdentityValue derives a deterministic UUID-shaped identity
// from namespace, key ID, kind, and trimmed raw value. Missing raw or namespace
// returns trimmed raw, allowing callers to keep unscoped identity when
// credential evidence is absent.
func ScopeCodexProviderIdentityValue(namespace string, apiKeyID int64, kind, raw string) string {
	raw = strings.TrimSpace(raw)

	if raw == "" || namespace == "" {
		return raw
	}
	// This domain string defines the stable identity format. Retain its literal
	// spelling so matching key, credential namespace, kind, and value continue deriving the same ID.
	return DeriveStableUUIDv4(fmt.Sprintf(
		"sub2api:codex-account-identity:%s:user:%d:account:%s:kind:%s:value:%s",
		codexProviderIdentityNamespaceVersion,
		apiKeyID,
		namespace,
		kind,
		raw,
	))
}

// ApplyCodexProviderIdentityFields scopes known nonblank string fields in the
// caller-owned map. Nil maps or empty namespace return false; matching identity
// kinds share derivation rules and the result reports whether any field value
// changed.
func ApplyCodexProviderIdentityFields(values map[string]any, namespace string, apiKeyID int64) bool {
	if values == nil || namespace == "" {
		return false
	}
	changed := false
	for _, field := range codexProviderIdentityFields {
		raw, ok := values[field.name].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		next := ScopeCodexProviderIdentityValue(namespace, apiKeyID, field.kind, raw)
		if next != raw {
			values[field.name] = next
			changed = true
		}
	}
	return changed
}

// ApplyCodexProviderIdentityEmbeddedMetadata scopes a nonblank embedded
// metadata string when it decodes to an object. Invalid JSON, null objects,
// unchanged fields, and encoding failures return false and retain the supplied
// string.
func ApplyCodexProviderIdentityEmbeddedMetadata(values map[string]any, namespace string, apiKeyID int64) bool {
	raw, ok := values[WSTurnMetadataHeader].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return false
	}
	metadata := map[string]any{}
	// Embedded metadata is optional identity evidence. Keep an unusable string
	// intact because it cannot be safely scoped through the recognized object fields.
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return false
	}
	if !ApplyCodexProviderIdentityFields(metadata, namespace, apiKeyID) {
		return false
	}
	rebuilt, err := json.Marshal(metadata)
	// Publish a complete encoded metadata string after all scoped fields succeed;
	// an encoding failure leaves the existing value for the owning request builder.
	if err != nil {
		return false
	}
	values[WSTurnMetadataHeader] = string(rebuilt)
	return true
}

// ApplyCodexProviderIdentityClientMetadataMap scopes body identity and cache
// keys in place. Callers own the decoded maps and serialize mutation. Nil
// bodies or empty namespaces return false; only existing object client_metadata
// is visited.
//
// The body session is captured before its fields and embedded metadata are
// rewritten. A nonblank prompt_cache_key exactly equal to that original string
// uses the session kind, while independently selected keys use prompt-cache.
// Both are scoped under the same key and credential namespace, retaining the
// intended session relationship without merging unrelated cache keys into the
// upstream session.
func ApplyCodexProviderIdentityClientMetadataMap(requestBody map[string]any, namespace string, apiKeyID int64) bool {
	if requestBody == nil || namespace == "" {
		return false
	}
	changed := false
	clientMetadata, _ := requestBody["client_metadata"].(map[string]any)
	originalBodySessionID := ""
	if clientMetadata != nil {
		originalBodySessionID, _ = clientMetadata["session_id"].(string)
		if ApplyCodexProviderIdentityFields(clientMetadata, namespace, apiKeyID) {
			changed = true
		}
		if ApplyCodexProviderIdentityEmbeddedMetadata(clientMetadata, namespace, apiKeyID) {
			changed = true
		}
	}
	if raw, ok := requestBody["prompt_cache_key"].(string); ok && strings.TrimSpace(raw) != "" {
		// Capture and compare the supplied session before mutation so a default
		// cache key follows the session domain while caller-selected cache keys remain separate.
		kind := "prompt-cache"
		if strings.TrimSpace(originalBodySessionID) != "" && raw == originalBodySessionID {
			kind = "session"
		}
		next := ScopeCodexProviderIdentityValue(namespace, apiKeyID, kind, raw)
		if next != raw {
			requestBody["prompt_cache_key"] = next
			changed = true
		}
	}
	return changed
}

// ApplyCodexProviderIdentityClientMetadataRaw scopes selected identity
// subobjects in raw JSON and returns change status. Empty input, empty
// namespace, or a nonobject root retains the supplied bytes. Only existing
// object client_metadata is fully decoded, using ordinary encoding/json number
// handling.
//
// The original body session is read before metadata scoping so an equal
// prompt_cache_key derives under the same session kind. Other cache keys use
// their own kind. Rebuilt metadata and the cache key are spliced into request
// bytes after each operation succeeds; an error returns the original body with
// false, preventing partially rewritten bytes from being published as a
// successful result.
func ApplyCodexProviderIdentityClientMetadataRaw(body []byte, namespace string, apiKeyID int64) ([]byte, bool, error) {
	if len(body) == 0 || namespace == "" {
		return body, false, nil
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false, nil
	}

	next := body
	changed := false
	originalBodySessionID := ""
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		clientMetadata := map[string]any{}
		if err := json.Unmarshal([]byte(cm.Raw), &clientMetadata); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for provider identity: %w", err)
		}
		originalBodySessionID, _ = clientMetadata["session_id"].(string)
		metadataChanged := ApplyCodexProviderIdentityFields(clientMetadata, namespace, apiKeyID)
		if ApplyCodexProviderIdentityEmbeddedMetadata(clientMetadata, namespace, apiKeyID) {
			metadataChanged = true
		}
		if metadataChanged {
			raw, err := json.Marshal(clientMetadata)
			if err != nil {
				return body, false, fmt.Errorf("encode provider-scoped client_metadata: %w", err)
			}
			var setErr error
			next, setErr = sjson.SetRawBytes(next, "client_metadata", raw)
			if setErr != nil {
				return body, false, fmt.Errorf("splice provider-scoped client_metadata: %w", setErr)
			}
			changed = true
		}
	}
	if promptCacheKey := gjson.GetBytes(body, "prompt_cache_key"); promptCacheKey.Type == gjson.String && strings.TrimSpace(promptCacheKey.String()) != "" {
		raw := promptCacheKey.String()
		// Capture and compare the supplied session before mutation so a default
		// cache key follows the session domain while caller-selected cache keys remain separate.
		kind := "prompt-cache"
		if strings.TrimSpace(originalBodySessionID) != "" && raw == originalBodySessionID {
			kind = "session"
		}
		scoped := ScopeCodexProviderIdentityValue(namespace, apiKeyID, kind, raw)
		if scoped != raw {
			rewritten, err := sjson.SetBytes(next, "prompt_cache_key", scoped)
			if err != nil {
				return body, false, fmt.Errorf("splice provider-scoped prompt_cache_key: %w", err)
			}
			next = rewritten
			changed = true
		}
	}
	return next, changed, nil
}

// ApplyCodexProviderIdentityHeaders scopes existing known identity headers and
// valid embedded metadata in place. Nil headers or empty namespace are ignored;
// the underscore session header remains for request builders to derive from
// their selected session or cache key.
func ApplyCodexProviderIdentityHeaders(headers http.Header, namespace string, apiKeyID int64) {
	if headers == nil || namespace == "" {
		return
	}
	for _, field := range codexProviderIdentityFields {
		// Request builders rebuild underscore session/conversation headers from their
		// selected cache or session value; skip this alias to avoid independently hashing it twice.
		if field.name == "session_id" {
			continue
		}
		raw := strings.TrimSpace(headers.Get(field.name))
		if raw != "" {
			headers.Set(field.name, ScopeCodexProviderIdentityValue(namespace, apiKeyID, field.kind, raw))
		}
	}
	// Embedded metadata is optional. Decode or encode failure keeps its original
	// header value while already scoped top-level identity headers remain applied.
	if raw := strings.TrimSpace(headers.Get(WSTurnMetadataHeader)); raw != "" {
		metadata := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &metadata); err == nil && metadata != nil && ApplyCodexProviderIdentityFields(metadata, namespace, apiKeyID) {
			if rebuilt, err := json.Marshal(metadata); err == nil {
				headers.Set(WSTurnMetadataHeader, string(rebuilt))
			}
		}
	}
}

// DeriveStableUUIDv4 returns a deterministic SHA-256-derived UUID-shaped value
// with version-four and RFC variant bits. Matching seeds reproduce the same
// value; callers treat it as stable identity rather than random identifier
// generation.
func DeriveStableUUIDv4(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	// Set version and variant bits on the hash prefix so UUID consumers accept
	// its format while repeated seeds retain deterministic identity.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// SetChatGPTAccountHeaders writes a nonempty account ID and sets or removes the
// FedRAMP flag. Nil headers are ignored, and an empty id leaves any existing
// account header unchanged while FedRAMP still follows the boolean.
func SetChatGPTAccountHeaders(headers http.Header, id string, fedRAMP bool) {
	if headers == nil {
		return
	}
	if id != "" {
		headers.Set("chatgpt-account-id", id)
	}
	if fedRAMP {
		headers.Set("x-openai-fedramp", "true")
	} else {
		headers.Del("x-openai-fedramp")
	}
}
