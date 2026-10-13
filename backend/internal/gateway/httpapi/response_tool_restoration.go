package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Tool lowering can rename reserved functions or flatten namespace declarations
// before upstream execution. Output restoration needs the mapping that belongs
// to that request or turn, while later WebSocket session updates can carry a
// different name set. Active and future-session names therefore occupy separate
// slots.
//
// The request state pointer is initialized through a shared mutex, and
// ResponseTools synchronizes publication of its individual slots. Name
// publication copies string maps; client and namespace mappings retain their
// internal maps, and getters return shared references. Consumers keep published
// storage immutable. SSE restoration can temporarily borrow its event-line type
// for missing payload metadata, removing that added field after a successful
// name rewrite.

// responseToolsContextKey names shared request restoration state read by HTTP
// and turn output adapters before restoring client-visible tool metadata.
const responseToolsContextKey = "gateway_response_tools"

// responseToolsInit serializes publication of per-request state pointers; each request owns its tool data.
var responseToolsInit sync.Mutex

// SetCodexToolNameReverse replaces active-turn names and clears future-session
// names for this request. Nil context is ignored. Each supplied string map is
// copied by ResponseTools, while the two slot publications occur separately and
// require stable input during copying.
func SetCodexToolNameReverse(c *gin.Context, reverse map[string]string) {
	if c == nil {
		return
	}
	responseTools(c, true).SetCodexNames(false, reverse)
	responseTools(c, true).SetCodexNames(true, nil)
}

// MergeCodexToolNameReverse merges supplied names over the current active-turn
// map. Nil context or empty input skips publication; matching keys prefer
// reverse. Retrieved mapping storage stays read-only, and the newly prepared
// map is copied when published.
func MergeCodexToolNameReverse(c *gin.Context, reverse map[string]string) {
	if c == nil || len(reverse) == 0 {
		return
	}
	merged := make(map[string]string, len(reverse)+len(CodexToolNameReverseFromContext(c)))
	maps.Copy(merged, CodexToolNameReverseFromContext(c))
	// Apply newly supplied names after the active snapshot so this update can
	// replace a prior alias while the published map remains read-only.
	maps.Copy(merged, reverse)
	responseTools(c, true).SetCodexNames(false, merged)
}

// CodexToolNameReverseFromContext returns the current active-turn name map or
// nil for missing state. The map is shared after retrieval; callers treat it as
// immutable and publish replacement mappings through setters.
func CodexToolNameReverseFromContext(c *gin.Context) map[string]string {
	return responseTools(c, false).CodexNames(false)
}

// UpdateCodexToolNameReverseForWSFrame updates future-session or active-turn
// restoration according to the root frame type. Nil context skips updates, and
// tools are expected in the normalized root/input forms examined by the
// explicit-declaration helper.
//
// A session.update with a present session.tools field replaces session names
// while leaving active output unchanged. A response.create or untyped frame
// replaces active names; absent explicit declarations inherit session names
// before the supplied reverse map is merged. Explicit tools, including present
// empty or null declarations, suppress that inheritance so client literal names
// do not acquire stale session aliases.
func UpdateCodexToolNameReverseForWSFrame(c *gin.Context, frame []byte, reverse map[string]string) {
	if c == nil {
		return
	}
	eventType := strings.TrimSpace(gjson.GetBytes(frame, "type").String())
	switch eventType {
	case "session.update":
		if gjson.GetBytes(frame, "session.tools").Exists() {
			responseTools(c, true).SetCodexNames(true, reverse)
		}
	case "response.create", "":
		active := reverse
		// Inherit session aliases only for undeclared tools; explicit client
		// declarations can intentionally use literal reserved-looking names and must
		// avoid stale alias restoration.
		if !OpenAIWSFrameHasExplicitToolDeclarations(frame) {
			active = openai.MergeCodexToolNameReverseMaps(
				responseTools(c, false).CodexNames(true),
				reverse,
			)
		}
		responseTools(c, true).SetCodexNames(false, active)
	}
}

// OpenAIWSFrameHasExplicitToolDeclarations reports present root tools or input
// additional_tools declarations. Presence includes empty or null tools fields,
// and additional_tools labels are trimmed and compared case-insensitively;
// callers normalize any nested response envelope before this check.
func OpenAIWSFrameHasExplicitToolDeclarations(frame []byte) bool {
	if gjson.GetBytes(frame, "tools").Exists() {
		return true
	}
	for _, item := range gjson.GetBytes(frame, "input").Array() {
		if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "additional_tools") && item.Get("tools").Exists() {
			return true
		}
	}
	return false
}

// RestoreCodexToolNamesFromContext restores supported response tool nodes using
// current request state. Session-created or session-updated payload types
// select future-session names; other payloads select active-turn names. The
// shared JSON restorer decides whether bytes are reused and retains unrelated
// fields.
func RestoreCodexToolNamesFromContext(c *gin.Context, data []byte) []byte {
	reverse := CodexToolNameReverseFromContext(c)
	switch strings.TrimSpace(gjson.GetBytes(data, "type").String()) {
	case "session.created", "session.updated":
		reverse = responseTools(c, false).CodexNames(true)
	}
	return openai.RestoreCodexToolNamesInJSON(data, reverse)
}

// RestoreCodexToolNamesFromSSEContext restores names when an SSE event line
// supplies a missing payload type. A nonblank payload type or blank event type
// delegates directly, keeping payload classification authoritative.
//
// Otherwise a temporary type guides the shared tool-node restorer. If
// restoration made no change, original bytes return. Changed payloads have that
// synthetic field removed before output, retaining the caller's type-free
// shape; deletion failure returns the restored bytes with the temporary type
// still present. This exception keeps successful restoration available while
// leaving actual removal failure visible in the resulting representation.
func RestoreCodexToolNamesFromSSEContext(c *gin.Context, data []byte, eventType string) []byte {
	if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "" || strings.TrimSpace(eventType) == "" {
		return RestoreCodexToolNamesFromContext(c, data)
	}
	// Borrow event-line classification while rewriting recognized tool nodes, then
	// remove the synthetic field so a type-free payload retains its original
	// protocol shape.
	compat := []byte(openaicore.OpenAICompatPayloadWithEventType(string(data), eventType))
	restored := RestoreCodexToolNamesFromContext(c, compat)
	if string(restored) == string(compat) {
		return data
	}
	withoutSyntheticType, err := sjson.DeleteBytes(restored, "type")
	// Keep the restored payload if synthetic-type removal fails, retaining the
	// completed name rewrite even though the temporary classification field
	// remains in output.
	if err != nil {
		return restored
	}
	return withoutSyntheticType
}

// HasOpenAIResponsesClientToolMapping reports custom, search or namespace
// restoration state presence.
func HasOpenAIResponsesClientToolMapping(mapping protocolbridge.ResponsesClientToolMapping) bool {
	return len(mapping.CustomTools) > 0 || mapping.ToolSearch || len(mapping.NamespaceTools) > 0
}

// RestoreOpenAIResponsesClientToolPayload restores lowered client tool
// representations using the stored OpenAI mapping. Missing mappings or invalid
// JSON return supplied bytes with no error; a mapped valid payload receives the
// shared bridge result and its restoration error.
func RestoreOpenAIResponsesClientToolPayload(c *gin.Context, payload []byte) ([]byte, error) {
	mapping, ok := OpenAIResponsesClientToolMapping(c)
	if !ok || !json.Valid(payload) {
		return payload, nil
	}
	restored, _, err := protocolbridge.RestoreResponsesClientToolPayload(payload, mapping)
	return restored, err
}

// RestoreGrokResponsesClientToolPayload restores lowered tool representations
// using the Grok mapping. Missing mapping, absent raw function_call marker or
// invalid JSON returns supplied bytes with no error; otherwise the shared
// bridge supplies restored bytes and any error.
func RestoreGrokResponsesClientToolPayload(c *gin.Context, payload []byte) ([]byte, error) {
	mapping, ok := GrokResponsesClientToolMapping(c)
	if !ok || !bytes.Contains(payload, []byte(`"function_call"`)) || !json.Valid(payload) {
		return payload, nil
	}
	restored, _, err := protocolbridge.RestoreResponsesClientToolPayload(payload, mapping)
	return restored, err
}

// FlattenOpenAIResponsesNamespaces lowers supported namespace declarations
// while exempting image_gen and stores their reverse names after successful
// encoding. Bodies with no raw namespace marker or no conversion return
// supplied bytes.
//
// Decoding or transformation failure returns the body with an error, and
// namespace state is published after replacement bytes exist. Ordinary map
// decoding represents JSON numbers as float64; re-encoding a changed body can
// round large integer literals through that representation. Callers retain
// source bytes separately when another attempt needs its original namespace
// declarations.
func FlattenOpenAIResponsesNamespaces(c *gin.Context, body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return body, nil
	}
	var requestBody map[string]any
	if err := json.Unmarshal(body, &requestBody); err != nil {
		return body, fmt.Errorf("decode OpenAI namespace body: %w", err)
	}
	names, changed, err := protocolbridge.FlattenResponsesNamespacesExcept(requestBody, map[string]bool{"image_gen": true})
	if err != nil {
		return body, err
	}
	if !changed {
		return body, nil
	}
	rebuilt, err := wirejson.Marshal(requestBody)
	if err != nil {
		return body, fmt.Errorf("encode OpenAI namespace body: %w", err)
	}
	// Publish reverse names after encoding succeeds, so a failed replacement
	// cannot install restoration state for bytes the caller never received.
	SetOpenAIResponsesNamespaceNames(c, names)
	return rebuilt, nil
}

// RestoreOpenAIResponsesNamespacePayload restores flattened call names from
// shared namespace state. Missing names or invalid JSON keeps supplied bytes,
// and a restoration error returns those bytes with the cause; only a changed
// successful payload replaces them.
func RestoreOpenAIResponsesNamespacePayload(c *gin.Context, payload []byte) ([]byte, error) {
	names := OpenAIResponsesNamespaceNames(c)
	if len(names) == 0 || !json.Valid(payload) {
		return payload, nil
	}
	restored, changed, err := protocolbridge.RestoreResponsesNamespaceCalls(payload, names)
	if err != nil {
		return payload, err
	}
	if changed {
		return restored, nil
	}
	return payload, nil
}

// responseTools reads the request restoration state and optionally publishes a
// new instance. Nil contexts return nil, reads without create do not allocate,
// and a stored typed nil remains nil.
//
// Creation takes the shared initialization mutex and rechecks the binding
// before publishing, so callers using this helper can reuse an already
// initialized pointer. The mutex protects pointer creation, while ResponseTools
// owns slot publication. Retrieved maps and bytes remain shared, requiring
// immutable consumer access and setter-based replacement; another owner
// replacing the Gin binding is outside this helper's initialization
// coordination.
func responseTools(c *gin.Context, create bool) *requeststate.ResponseTools {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(responseToolsContextKey); ok {
		if state, ok := value.(*requeststate.ResponseTools); ok {
			return state
		}
	}
	if !create {
		return nil
	}
	// Recheck under the shared initialization lock before publishing to the Gin
	// slot, so concurrent helper creation cannot replace another helper's newly
	// initialized request state.
	responseToolsInit.Lock()
	defer responseToolsInit.Unlock()
	if value, ok := c.Get(responseToolsContextKey); ok {
		if state, ok := value.(*requeststate.ResponseTools); ok {
			return state
		}
	}
	state := &requeststate.ResponseTools{}
	c.Set(responseToolsContextKey, state)
	return state
}

// SetOpenAIResponsesClientToolMapping publishes a nonempty OpenAI client
// mapping or clears existing state when its presence test fails. Internal maps
// remain shared after publication, so callers keep them immutable while
// response restoration consumes them.
func SetOpenAIResponsesClientToolMapping(c *gin.Context, mapping protocolbridge.ResponsesClientToolMapping) {
	if !HasOpenAIResponsesClientToolMapping(mapping) {
		ClearOpenAIResponsesClientToolMapping(c)
		return
	}
	responseTools(c, true).SetClientMapping(false, mapping)
}

// ClearOpenAIResponsesClientToolMapping clears the OpenAI mapping in existing
// request state. Missing state is ignored through its nil-safe setter, so
// clearing does not allocate an otherwise absent restoration instance.
func ClearOpenAIResponsesClientToolMapping(c *gin.Context) {
	responseTools(c, false).SetClientMapping(false, protocolbridge.ResponsesClientToolMapping{})
}

// OpenAIResponsesClientToolMapping returns the stored OpenAI mapping and its
// meaningful-presence flag. Missing state yields empty and false; internal
// mapping maps are shared and stay read-only for response consumers.
func OpenAIResponsesClientToolMapping(c *gin.Context) (protocolbridge.ResponsesClientToolMapping, bool) {
	mapping := responseTools(c, false).ClientMapping(false)
	return mapping, HasOpenAIResponsesClientToolMapping(mapping)
}

// SetGrokResponsesClientToolMapping publishes a nonempty Grok client mapping or
// clears it when its presence test fails. Internal maps are retained, so
// callers leave their published contents immutable while output restoration
// reads them.
func SetGrokResponsesClientToolMapping(c *gin.Context, mapping protocolbridge.ResponsesClientToolMapping) {
	if !HasOpenAIResponsesClientToolMapping(mapping) {
		ClearGrokResponsesClientToolMapping(c)
		return
	}
	responseTools(c, true).SetClientMapping(true, mapping)
}

// ClearGrokResponsesClientToolMapping clears Grok mapping from existing
// restoration state. Missing state remains absent because the nil-safe setter
// performs no allocation; callers use this before another attempt must discard
// earlier lowered tool evidence.
func ClearGrokResponsesClientToolMapping(c *gin.Context) {
	responseTools(c, false).SetClientMapping(true, protocolbridge.ResponsesClientToolMapping{})
}

// GrokResponsesClientToolMapping returns the stored Grok mapping and its
// presence flag. An absent instance yields empty and false, while present
// mapping internals remain shared and require immutable consumer access.
func GrokResponsesClientToolMapping(c *gin.Context) (protocolbridge.ResponsesClientToolMapping, bool) {
	mapping := responseTools(c, false).ClientMapping(true)
	return mapping, HasOpenAIResponsesClientToolMapping(mapping)
}

// SetOpenAIResponsesNamespaceNames publishes a nonempty namespace reverse map
// for the request. Empty input retains existing names; callers use
// ClearOpenAIResponsesNamespaceNames to remove them. Published map storage is
// shared and remains immutable while restoration reads it.
func SetOpenAIResponsesNamespaceNames(c *gin.Context, names map[string]protocolbridge.ResponsesNamespaceName) {
	if len(names) > 0 {
		responseTools(c, true).SetNamespaces(names)
	}
}

// ClearOpenAIResponsesNamespaceNames clears existing namespace reverse-name
// storage without creating request state. The nil-safe setter leaves absent
// state unchanged, while output consumers of an earlier retrieved map keep
// their captured reference.
func ClearOpenAIResponsesNamespaceNames(c *gin.Context) { responseTools(c, false).SetNamespaces(nil) }

// OpenAIResponsesNamespaceNames returns the shared namespace reverse-name map
// from request state. Missing state yields nil; callers read the map immutably
// and use setters to publish a replacement for later restoration.
func OpenAIResponsesNamespaceNames(c *gin.Context) map[string]protocolbridge.ResponsesNamespaceName {
	return responseTools(c, false).Namespaces()
}

// OpenAIWSHTTPBridgeToolStateFromContext returns retained bridge declarations
// and their presence flag. Missing state is absent; retrieved bytes and mapping
// internals are shared, so the consuming turn keeps that storage immutable.
func OpenAIWSHTTPBridgeToolStateFromContext(c *gin.Context) (requeststate.WSBridgeTools, bool) {
	return responseTools(c, false).Bridge()
}

// SetOpenAIWSHTTPBridgeToolState publishes bridge declarations and marks the
// state present, including an empty value. Lowered declaration bytes are
// copied, while internal client mapping maps remain shared and caller-immutable
// for the next bridge turn.
func SetOpenAIWSHTTPBridgeToolState(c *gin.Context, state requeststate.WSBridgeTools) {
	responseTools(c, true).SetBridge(state)
}
