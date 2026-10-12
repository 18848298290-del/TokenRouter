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

const (
	codexProviderIdentityNamespaceVersion = "v1"
	WSTurnMetadataHeader                  = "x-codex-turn-metadata"
)

var codexProviderIdentityFields = []struct {
	name string
	kind string
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

// ProviderIdentityInput supplies credential namespace inputs.
// Do not serialize or expand SetupToken in logs.
type ProviderIdentityInput struct {
	ChatGPTAccountID, ChatGPTUserID, Seed string
	HasSeed                               bool
	SetupToken                            string `json:"-"`
}

func (ProviderIdentityInput) String() string { return "openai provider identity input" }

func (v ProviderIdentityInput) GoString() string { return v.String() }

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

// IsolateOpenAIUpstreamSessionID hashes a session with the API-key ID and optional OAuth namespace.
// Changing credential namespaces during failover produces a different upstream identity.
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

func ScopeCodexProviderIdentityValue(namespace string, apiKeyID int64, kind, raw string) string {
	raw = strings.TrimSpace(raw)

	if raw == "" || namespace == "" {
		return raw
	}
	// Keep the hash domain stable so provider and user identities survive upgrades.
	return DeriveStableUUIDv4(fmt.Sprintf(
		"sub2api:codex-account-identity:%s:user:%d:account:%s:kind:%s:value:%s",
		codexProviderIdentityNamespaceVersion,
		apiKeyID,
		namespace,
		kind,
		raw,
	))
}

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

func ApplyCodexProviderIdentityEmbeddedMetadata(values map[string]any, namespace string, apiKeyID int64) bool {
	raw, ok := values[WSTurnMetadataHeader].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return false
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return false
	}
	if !ApplyCodexProviderIdentityFields(metadata, namespace, apiKeyID) {
		return false
	}
	rebuilt, err := json.Marshal(metadata)
	if err != nil {
		return false
	}
	values[WSTurnMetadataHeader] = string(rebuilt)
	return true
}

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

// ApplyCodexProviderIdentityClientMetadataRaw scopes only the small identity
// subobjects with gjson/sjson. The passthrough hot path never unmarshals the
// potentially multi-megabyte request body.
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

func ApplyCodexProviderIdentityHeaders(headers http.Header, namespace string, apiKeyID int64) {
	if headers == nil || namespace == "" {
		return
	}
	for _, field := range codexProviderIdentityFields {
		// Underscore session/conversation headers are rebuilt separately from the
		// prompt cache key by each request builder.
		if field.name == "session_id" {
			continue
		}
		raw := strings.TrimSpace(headers.Get(field.name))
		if raw != "" {
			headers.Set(field.name, ScopeCodexProviderIdentityValue(namespace, apiKeyID, field.kind, raw))
		}
	}
	if raw := strings.TrimSpace(headers.Get(WSTurnMetadataHeader)); raw != "" {
		metadata := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &metadata); err == nil && metadata != nil && ApplyCodexProviderIdentityFields(metadata, namespace, apiKeyID) {
			if rebuilt, err := json.Marshal(metadata); err == nil {
				headers.Set(WSTurnMetadataHeader, string(rebuilt))
			}
		}
	}
}

// DeriveStableUUIDv4 derives a deterministic UUID-shaped value with version 4 and RFC variant bits.
// The same seed produces the same value; the result is not randomly generated.
func DeriveStableUUIDv4(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// SetChatGPTAccountHeaders sets a nonempty account ID and sets or removes the FedRAMP flag.
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
