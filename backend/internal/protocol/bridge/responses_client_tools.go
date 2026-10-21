package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// responses_client_tools.go lowers Responses client tools and restores their
// identities in buffered and streamed replies.
// Function-only upstreams cannot directly express freeform client input,
// client-executed discovery, or a tool's namespace/name pair. Lowering
// converts declarations, history, and choice together and records the client
// identities needed when a response returns. Custom calls wrap one input
// string, while discovery becomes a proxy function with object-valued
// arguments restored on the client.
//
// Stream restoration keeps upstream item/call identities for matching
// fragments and emits another known prefix when client item kind changes. A
// wrapper such as {"input":"pwd"} can arrive across argument deltas, so custom
// text waits for a complete value before unwrapping. Suppressed and expanded
// events require one emitted sequence counter. Raw and typed paths have
// different field/number preservation limits, and mutable request/state
// objects can contain earlier changes when a later operation fails.

// responsesToolCallItemIDPrefixes records function fc_, custom ctc_, and
// discovery tsc_ identities whose suffixes can survive call-kind conversion on
// replay and restoration.
var responsesToolCallItemIDPrefixes = []string{"fc_", "ctc_", "tsc_"}

// ResponsesClientToolMapping records client kinds and namespace pairs for
// function-only execution. Returned mappings and restorer constructors can
// share these maps, so callers keep their contents stable while response
// conversion reads them.
type ResponsesClientToolMapping struct {
	// CustomTools, ToolSearch, and NamespaceTools describe the successful
	// lowering decision. A restorer copies the struct and shares its maps, so
	// their contents remain stable while that stream reads identities. Custom
	// membership wins over search classification; namespace restoration applies
	// to calls that remain ordinary functions after client-kind restoration.
	CustomTools    map[string]bool                   // custom client names enabled
	ToolSearch     bool                              // client discovery proxy enabled
	NamespaceTools map[string]ResponsesNamespaceName // flat-name namespace identity pairs
	// CallNames and FunctionTools describe the same accepted declarations as the
	// reverse map. Published maps stay immutable while later turns or response
	// readers retain them; history-only identities do not activate declarations.
	CallNames     map[NamespacedToolName]string // client identity upstream spelling
	FunctionTools map[string]bool               // ordinary function name membership
	// DeclaredNamespaces includes empty groups, allowing explicit group selectors
	// to be checked before a platform filters declarations without executable members.
	DeclaredNamespaces map[string]bool // supplied namespace group names
}

// ResponsesClientToolStreamRestorer owns one stream's argument buffers,
// identity lookups, and emitted sequence counter. Construct it with
// NewResponsesClientToolStreamRestorer, serialize access, and retain its
// shared mapping unchanged until that stream finishes.
type ResponsesClientToolStreamRestorer struct {
	// adapter retains the supplied mapping. Its custom and namespace maps are
	// read through shared storage during all event restoration operations.
	adapter ResponsesClientToolMapping // shared client tool mapping
	// nextSeq and seenSeq track one stream's emitted sequence state. The first
	// observed number seeds nextSeq; suppressed argument events consume no
	// emitted number, and every emitted event increments it.
	nextSeq int  // next emitted sequence number
	seenSeq bool // incoming sequence seed observed
	// calls and byOutput point to the same mutable call records. recordItem
	// publishes aliases from first-item identities/indexes, and typed item
	// completion removes those aliases before the stream continues.
	calls    map[string]*responsesClientToolStreamCall // upstream identity record aliases
	byOutput map[int]*responsesClientToolStreamCall    // upstream output-index record aliases
}

// responsesClientToolStreamCall retains one proxy call's upstream/client
// identities and accumulated argument text. Records are shared pointers in the
// restorer's lookup maps; their written Builder storage stays with that
// record.
type responsesClientToolStreamCall struct {
	// kind, name, callID, itemID, clientItemID, and outputIdx are fixed by the
	// first recorded item. Later full argument observations update arguments
	// while retaining those original lookup and emitted identity decisions.
	kind string // custom or search conversion kind
	name string // exact upstream proxy name
	// callID and itemID retain upstream spelling for event lookup, while
	// clientItemID can carry another known type prefix for emission. Retyping
	// leaves the known suffix and call/result correlation separate.
	callID       string // upstream call/result correlation ID
	itemID       string // upstream output item identity
	clientItemID string // retyped client output identity
	// outputIdx also indexes byOutput. A reused position can overwrite its
	// previous alias, so the caller keeps active item positions distinct.
	outputIdx int // first upstream output index
	// arguments accumulates suppressed fragments for this record. Nonempty
	// completed/item argument values reset it and replace the fragments before
	// client input is reconstructed.
	arguments strings.Builder // accumulated function argument text
}

// AdaptResponsesClientTools lowers decoded custom, search, and namespace
// declarations and their history/choice in place. Nil requests remain
// unchanged. Without active declarations, historical function identities can
// still be converted, while the returned future mapping stays empty.
// Declaration collisions and invalid identities return errors; failed
// candidates can contain earlier mutations and are discarded by the caller.
//
// Promotion registers effective discoveries before flattening. Custom input
// uses a fixed string wrapper, search uses a named function proxy, and
// returned maps identify response restoration. Unsupported entries can remain,
// custom outputs lower independently of custom-name membership, and no full
// history/schema validation is performed. Callers discard failed mutable
// request candidates and keep the mapping with the successful request.
func AdaptResponsesClientTools(req map[string]any) (ResponsesClientToolMapping, bool, error) {
	return adaptResponsesClientTools(req, ResponsesClientToolMapping{})
}

// adaptResponsesClientTools prepares one effective declaration set while
// retaining verified plain-function aliases from an omitted-tool continuation.
// Explicit replacement declarations use an empty inherited mapping. Errors
// leave the mutable candidate disposable and prevent publication to readers.
//
// Effective declarations precede alias creation and history conversion, keeping
// executable names paired with their response identities. An inherited plain
// function can retain a discovery namespace only while that same bare function
// remains declared. Explicit tool replacement does not acquire those inherited
// aliases, so a reset or a new definition set starts its own conversion state.
func adaptResponsesClientTools(req map[string]any, inherited ResponsesClientToolMapping) (ResponsesClientToolMapping, bool, error) {
	if req == nil {
		return ResponsesClientToolMapping{}, false, nil
	}
	inputJSON, err := json.Marshal(req["input"])
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	additional, err := promoteResponsesAdditionalTools(req)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		history, err := responsesToolIdentityMapping(nil, inputJSON)
		if err != nil {
			return ResponsesClientToolMapping{}, false, err
		}
		// Resolve executed calls independently of future tools, including calls
		// whose declarations disappeared during compaction or replacement of the
		// active list.
		changed, err := rewriteResponsesFunctionCalls(req["input"], &history)
		if err != nil {
			return ResponsesClientToolMapping{}, false, err
		}
		choiceChanged, err := rewriteResponsesToolChoice(req, &history)
		return ResponsesClientToolMapping{}, additional || changed || choiceChanged, err
	}
	discovered, err := promoteResponsesToolSearchDiscoveries(req)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	if discovered {
		tools, _ = req["tools"].([]any)
	}
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	var identityTools []ResponsesTool
	if err := json.Unmarshal(toolsJSON, &identityTools); err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	identities, err := responsesToolIdentityMapping(identityTools, inputJSON)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	// Restored plain functions retain their discovery identity through omitted-tool
	// turns; an explicit replacement enters with no inherited aliases to revive.
	for upstream, identity := range inherited.NamespaceTools {
		if inherited.FunctionTools[upstream] && identities.FunctionTools[upstream] &&
			identity.Namespace == identity.Name && identity.Name == upstream {
			if current, exists := identities.CallNames[identity]; exists && current != upstream {
				return ResponsesClientToolMapping{}, false, fmt.Errorf("inherited discovered function conflicts with a current namespace tool")
			}
			identities.NamespaceTools[upstream] = identity
			identities.CallNames[identity] = upstream
		}
	}

	adapter := identities
	adapter.CustomTools = make(map[string]bool)
	functionNames := make(map[string]bool)
	customNames := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringValue(tool["name"]))
		switch strings.TrimSpace(stringValue(tool["type"])) {
		case "function":
			if name != "" {
				functionNames[name] = true
			}
		case "custom":
			if name != "" {
				customNames[name] = true
			}
		case "tool_search":
			adapter.ToolSearch = true
		}
	}
	for name := range customNames {
		if functionNames[name] {
			return ResponsesClientToolMapping{}, false, fmt.Errorf("custom tool %q conflicts with a function tool of the same name; this upstream cannot disambiguate them, rename one of the tools", name)
		}
	}
	if adapter.ToolSearch && (functionNames[toolSearchProxyName] || customNames[toolSearchProxyName]) {
		return ResponsesClientToolMapping{}, false, fmt.Errorf("built-in tool_search conflicts with a declared tool named %q; this upstream cannot disambiguate them, rename the tool", toolSearchProxyName)
	}
	// Validate the supplied selector before flattening can replace a namespace
	// group with auto, keeping an undeclared group from acquiring that downgrade.
	choiceChanged, err := rewriteResponsesToolChoice(req, &adapter)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}

	// Derive namespace names before client lowering so declarations, replayed
	// function calls, and choices use the same names for response restoration.
	names, flattened, err := FlattenResponsesNamespaces(req)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	if adapter.ToolSearch {
		if _, exists := names[toolSearchProxyName]; exists {
			return ResponsesClientToolMapping{}, false, fmt.Errorf("built-in tool_search conflicts with namespace tool flattened as %q; this upstream cannot disambiguate them, rename the tool", toolSearchProxyName)
		}
	}

	tools, _ = req["tools"].([]any)
	lowered := make([]any, 0, len(tools))
	changed := additional || discovered || flattened || choiceChanged
	seenSearch := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			lowered = append(lowered, raw)
			continue
		}
		typ := strings.TrimSpace(stringValue(tool["type"]))
		name := strings.TrimSpace(stringValue(tool["name"]))
		switch typ {
		case "custom":
			if name == "" {
				lowered = append(lowered, raw)
				continue
			}
			copy := copyClientTool(tool)
			copy["type"] = "function"
			// Function execution cannot carry the custom grammar in this shape.
			// Replace it with a single input string wrapper and remember the client kind.
			copy["parameters"] = json.RawMessage(customToolInputSchema)
			delete(copy, "format")
			adapter.CustomTools[name] = true
			lowered = append(lowered, copy)
			changed = true
		case "tool_search":
			// One proxy function represents client search. Repeated declarations
			// must not create indistinguishable upstream functions of the same name.
			if seenSearch {
				changed = true
				continue
			}
			seenSearch = true
			lowered = append(lowered, map[string]any{
				"type": "function", "name": toolSearchProxyName,
				"description": "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task.",
				"parameters":  json.RawMessage(toolSearchProxySchema),
			})
			changed = true
		default:
			lowered = append(lowered, raw)
		}
	}
	// Lowered search is an ordinary function, so upstream defer_loading markers
	// cannot rely on a remaining built-in search declaration to activate those tools.
	if stripResponsesDeferredToolFlags(lowered) {
		changed = true
	}
	if changed {
		req["tools"] = lowered
	}
	// Flattening handles explicit groups first. Resolve the remaining supplied
	// identities against discovery evidence before history conversion removes their
	// original representation or a response reader captures its reverse mapping.
	identityChanged, err := rewriteResponsesFunctionCalls(req["input"], &adapter)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	changed = changed || identityChanged
	historyChanged, err := rewriteClientToolHistory(req["input"], &adapter)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	if historyChanged {
		changed = true
	}
	if rewriteClientToolChoice(req, &adapter) {
		changed = true
	}
	if len(adapter.CustomTools) == 0 {
		adapter.CustomTools = nil
	}
	if len(adapter.NamespaceTools) == 0 {
		adapter.NamespaceTools = nil
	}
	return adapter, changed, nil
}

// stripResponsesDeferredToolFlags removes direct defer_loading keys from map
// declarations when no trimmed tool_search declaration remains. It mutates
// shared maps and reports any removal; a retained built-in search declaration
// leaves all supplied tools unchanged.
func stripResponsesDeferredToolFlags(tools []any) bool {
	if hasResponsesToolSearchDeclaration(tools) {
		return false
	}
	// When search is absent, deferred functions have no built-in discovery
	// declaration to enable them. Remove direct flags even from shared declarations.
	changed := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := tool["defer_loading"]; exists {
			delete(tool, "defer_loading")
			changed = true
		}
	}
	return changed
}

// AdaptResponsesClientToolsWithInheritedMapping adapts decoded follow-up
// history using inherited identities when tools is absent. Any present tools
// value selects fresh adaptation, including empty or invalid values. With no
// inherited identities, normal adaptation still resolves additional_tools
// and prepares qualified history. A nonempty first optional lowered slice
// restores effective client declarations; later optional slices are ignored.
//
// Without restored declarations, inherited maps remain shared while history
// and choice change in place. Namespace rewriting has no change result, so a
// present input key can conservatively set true even when no name matched.
// This flag selects serialization at byte wrappers. Errors can leave earlier
// request mutations, and callers discard failed candidates instead of relying
// on rollback.
func AdaptResponsesClientToolsWithInheritedMapping(
	req map[string]any,
	inherited ResponsesClientToolMapping,
	inheritedLoweredTools ...[]any,
) (ResponsesClientToolMapping, bool, error) {
	if req == nil {
		return ResponsesClientToolMapping{}, false, nil
	}
	if _, toolsPresent := req["tools"]; toolsPresent {
		tools, valid := req["tools"].([]any)
		if !valid || len(tools) == 0 {
			input, err := json.Marshal(req["input"])
			if err != nil {
				return ResponsesClientToolMapping{}, false, err
			}
			history, err := responsesToolIdentityMapping(nil, input)
			if err != nil {
				return ResponsesClientToolMapping{}, false, err
			}
			for identity, upstream := range inherited.CallNames {
				if current, exists := history.CallNames[identity]; exists && current != upstream {
					return ResponsesClientToolMapping{}, false, fmt.Errorf("historical tool identity conflicts with its inherited mapping")
				}
				history.CallNames[identity] = upstream
			}
			for upstream, identity := range inherited.NamespaceTools {
				if current, exists := history.NamespaceTools[upstream]; exists && current != identity {
					return ResponsesClientToolMapping{}, false, fmt.Errorf("historical upstream tool name conflicts with its inherited mapping")
				}
				history.NamespaceTools[upstream] = identity
			}
			// A reset removes future declarations, while executed historical calls
			// still need their known names. Return empty state after that rewrite so
			// subsequent turns cannot reactivate a cleared tool through inheritance.
			changed, err := rewriteResponsesFunctionCalls(req["input"], &history)
			return ResponsesClientToolMapping{}, changed, err
		}
		return AdaptResponsesClientTools(req)
	}
	if len(inherited.CustomTools) == 0 && !inherited.ToolSearch && len(inherited.NamespaceTools) == 0 {
		return AdaptResponsesClientTools(req)
	}
	if len(inheritedLoweredTools) > 0 && len(inheritedLoweredTools[0]) > 0 {
		req["tools"] = restoreInheritedResponsesClientToolDeclarations(inheritedLoweredTools[0], inherited)
		return adaptResponsesClientTools(req, inherited)
	}

	changed, err := rewriteClientToolHistory(req["input"], &inherited)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	if len(inherited.NamespaceTools) > 0 {
		before := changed
		rewriteNamespaceQualifiedCalls(req["input"], inherited.NamespaceTools)
		// The namespace walker reports no change flag. A present input key
		// therefore selects conservative rebuilding even if no qualified call matched.
		if _, inputPresent := req["input"]; inputPresent && !before {
			changed = true
		}
	}
	identityChanged, err := rewriteResponsesFunctionCalls(req["input"], &inherited)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	changed = changed || identityChanged
	choiceChanged, err := rewriteResponsesToolChoice(req, &inherited)
	if err != nil {
		return ResponsesClientToolMapping{}, false, err
	}
	changed = changed || choiceChanged
	if rewriteClientToolChoice(req, &inherited) {
		changed = true
	}
	return inherited, changed, nil
}

// copyClientTool allocates a declaration map and copies its entries shallowly.
// Nested maps/slices and their mutable values remain shared, including when
// the caller later renames only the top-level type or name.
func copyClientTool(tool map[string]any) map[string]any {
	copy := make(map[string]any, len(tool))
	maps.Copy(copy, tool)
	return copy
}

// rewriteClientToolHistory recursively lowers recognized calls/results in
// every decoded map and array under value. Callers supply an acyclic value and
// nonnil adapter, and mutations are visible through shared maps. Search-output
// lowering requires a nonblank string call ID and can fail after earlier items
// changed.
//
// Mapped custom calls wrap input, while custom outputs lower regardless of
// custom-name mapping. Search calls/results require enabled search mapping;
// item IDs are normalized separately from call_id pairing. Result
// normalization retains recognized content arrays, stringifies other values,
// and conditionally drops discovery metadata. On error the returned change
// flag is false even when prior mutations remain, so the caller rejects that
// candidate.
func rewriteClientToolHistory(value any, adapter *ResponsesClientToolMapping) (bool, error) {
	changed := false
	var visit func(any) error
	visit = func(value any) error {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				if err := visit(item); err != nil {
					return err
				}
			}
		case map[string]any:
			typ := strings.TrimSpace(stringValue(typed["type"]))
			switch typ {
			case "custom_tool_call":
				// The function declaration expects a string wrapper, so replayed custom
				// input needs the same encoding before its client kind is removed.
				if adapter.CustomTools[strings.TrimSpace(stringValue(typed["name"]))] {
					typed["type"] = "function_call"
					typed["arguments"] = customToolCallArguments(stringValue(typed["input"]))
					delete(typed, "input")
					normalizeLoweredFunctionItemID(typed)
					changed = true
				}
			case "custom_tool_call_output":
				// Outputs lack a tool name for custom-map lookup. Lower this known
				// result kind independently and keep call_id as its pairing identity.
				typed["type"] = "function_call_output"
				normalizeLoweredFunctionItemID(typed)
				normalizeClientToolOutput(typed)
				changed = true
			case "tool_search_call":
				// Search executes through the reserved function proxy on this upstream.
				// Keep argument text and call correlation while removing client execution metadata.
				if adapter.ToolSearch {
					typed["type"] = "function_call"
					typed["name"] = toolSearchProxyName
					typed["arguments"] = rawObjectString(typed["arguments"])
					delete(typed, "execution")
					normalizeLoweredFunctionItemID(typed)
					changed = true
				}
			case "tool_search_output":
				if adapter.ToolSearch {
					callID := strings.TrimSpace(stringValue(typed["call_id"]))
					if callID == "" {
						return fmt.Errorf("tool_search_output requires a non-empty string call_id before it can be lowered to function_call_output")
					}
					typed["type"] = "function_call_output"
					normalizeLoweredFunctionItemID(typed)
					normalizeToolSearchOutput(typed)
					changed = true
				}
			}
			for _, child := range typed {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(value); err != nil {
		return false, err
	}
	return changed, nil
}

// normalizeLoweredFunctionItemID changes known custom/search string IDs back
// to function prefixes and removes other nonblank incompatible string IDs.
// Blank, nonstring, and already-function values stay as supplied; call_id
// remains the independent call/result correlation key.
func normalizeLoweredFunctionItemID(item map[string]any) {
	id := strings.TrimSpace(stringValue(item["id"]))
	if id == "" || strings.HasPrefix(id, "fc_") {
		return
	}
	// A restored custom/search item can be replayed to function execution.
	// Recover its known function prefix so its suffix identifies that upstream item.
	if recovered := retypedResponsesToolCallItemID(id, "function_call"); recovered != id {
		item["id"] = recovered
		return
	}
	// An unfamiliar nonblank item ID cannot be assigned a corresponding
	// function prefix. Remove it while leaving call_id available to pair the result.
	delete(item, "id")
}

// responsesToolCallItemIDPrefix returns the exact function/custom/discovery
// prefix for a supported item label. Other labels return empty text, which
// tells retyping to leave the supplied ID unchanged.
func responsesToolCallItemIDPrefix(itemType string) string {
	switch itemType {
	case "custom_tool_call":
		return "ctc_"
	case "tool_search_call":
		return "tsc_"
	case "function_call":
		return "fc_"
	default:
		return ""
	}
}

// retypedResponsesToolCallItemID replaces a known family prefix with the
// target prefix and retains its suffix. Empty IDs, unsupported target types,
// correct prefixes, and unknown vendor prefixes pass through. Neither ID nor
// type is trimmed here.
//
// The prefix describes output-item kind rather than call_id pairing.
// Preserving the suffix lets restored client IDs return to the corresponding
// function ID on replay, while unfamiliar ID formats remain available instead
// of being assigned an invented identity. Only the three known prefixes
// participate in this reversible spelling change.
func retypedResponsesToolCallItemID(id, itemType string) string {
	want := responsesToolCallItemIDPrefix(itemType)
	if want == "" || id == "" || strings.HasPrefix(id, want) {
		return id
	}
	// Restrict conversion to this known family: retaining a recognized suffix
	// supports replay, while guessing a vendor prefix would invent an unrelated item identity.
	for _, known := range responsesToolCallItemIDPrefixes {
		if known != want && strings.HasPrefix(id, known) {
			return want + strings.TrimPrefix(id, known)
		}
	}
	return id
}

// retypeResponsesToolCallItemID conditionally writes a known target prefix
// using the item's trimmed string ID. If retyping produces the same trimmed
// value, the original field remains unchanged, including its whitespace or
// nonstring value.
func retypeResponsesToolCallItemID(item map[string]any, itemType string) {
	id := strings.TrimSpace(stringValue(item["id"]))
	if retyped := retypedResponsesToolCallItemID(id, itemType); retyped != id {
		item["id"] = retyped
	}
}

// normalizeClientToolOutput retains an existing string or a nonempty decoded
// content array whose every block has an exact supported input type. Missing
// output is untouched; literal nil becomes empty text. Other values are
// encoded as a JSON string payload, and an encoding failure also selects empty
// text.
//
// Keeping supported content arrays lets later request converters inspect
// image/file parts rather than treating them as ordinary text. The block check
// validates type labels only, so callers validate required content fields
// separately. Empty, mixed, typed-other, or unsupported arrays use
// stringification; the normalized field is changed in place and may share
// retained content storage.
func normalizeClientToolOutput(item map[string]any) {
	output, exists := item["output"]
	if !exists {
		return
	}
	if _, ok := output.(string); ok {
		return
	}
	// Recognized decoded content arrays retain their block shapes for later
	// media conversion. Other arrays follow ordinary JSON-text output normalization.
	if isResponsesToolOutputContent(output) {
		return
	}
	if output == nil {
		item["output"] = ""
		return
	}
	// Unsupported or cyclic programmatic output cannot become JSON text.
	// An encoding failure selects the same empty text used for literal nil output.
	encoded, err := json.Marshal(output)
	if err != nil {
		item["output"] = ""
		return
	}
	item["output"] = string(encoded)
}

// isResponsesToolOutputContent checks a nonempty []any of map blocks with
// exact input_text/input_image/input_file type strings. It validates those
// labels only; other fields, media usability, and independently typed Go slice
// forms are outside this predicate.
func isResponsesToolOutputContent(output any) bool {
	parts, ok := output.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		typed, ok := part.(map[string]any)
		if !ok {
			return false
		}
		switch stringValue(typed["type"]) {
		case "input_text", "input_image", "input_file":
		default:
			return false
		}
	}
	return true
}

// normalizeToolSearchOutput prefers a present output key, otherwise it copies
// the tools value into output. With neither key it returns before removing
// metadata, leaving status/execution available. Existing output can be null
// and still takes precedence.
//
// When a source value exists, normalization retains supported content or
// serializes other values, then removes tools/status/execution regardless of
// discovery status. The enclosing history pass has already lowered the item
// type. Promotion's completed-status rule controls declarations only; this
// operation prepares the function-output representation and performs no
// matching-call or outcome validation.
func normalizeToolSearchOutput(item map[string]any) {
	if _, exists := item["output"]; !exists {
		if tools, hasTools := item["tools"]; hasTools {
			item["output"] = tools
		} else {
			return
		}
	}
	// The result now uses function-output fields. Drop discovery metadata after
	// selecting content so an explicit output still outranks the discovered tools list.
	normalizeClientToolOutput(item)
	delete(item, "tools")
	delete(item, "status")
	delete(item, "execution")
}

// rewriteClientToolChoice lowers a mapped custom choice in place or replaces
// enabled search choice with its function proxy object. Unsupported choice
// shapes/kinds remain, and true reports a selected rewrite; namespace choice
// has its separate flattening rules.
func rewriteClientToolChoice(req map[string]any, adapter *ResponsesClientToolMapping) bool {
	choice, ok := req["tool_choice"].(map[string]any)
	if !ok {
		return false
	}
	typ := strings.TrimSpace(stringValue(choice["type"]))
	name := strings.TrimSpace(stringValue(choice["name"]))
	// Choice needs the lowered declaration kind as well as its name. Custom
	// keeps the supplied map; search replaces it with the reserved proxy selection.
	if typ == "custom" && adapter.CustomTools[name] {
		choice["type"] = "function"
		return true
	}
	if typ == "tool_search" && adapter.ToolSearch {
		req["tool_choice"] = map[string]any{"type": "function", "name": toolSearchProxyName}
		return true
	}
	return false
}

// customToolCallArguments encodes freeform input in the function proxy's
// single string field. The returned text is valid JSON from a string-valued
// map, including empty input; it does not enforce a removed custom grammar.
func customToolCallArguments(input string) string {
	// The fresh string-valued map has no unsupported values or cycles. Its
	// encoder can therefore produce the wrapper while the discarded error remains nil.
	encoded, _ := json.Marshal(map[string]string{"input": input})
	return string(encoded)
}

// rawObjectString retains string values verbatim and JSON-encodes any other
// supplied value. Nil encodes as null, and unsupported/cyclic values return
// the literal empty-object text; the result need not represent an object or
// valid supplied string JSON.
func rawObjectString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	// A failed generic encoding supplies no usable argument JSON. Use the
	// empty-object text expected by proxy conversion while preserving string inputs verbatim.
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// RestoreResponsesClientToolPayload restores mapped custom/search calls and
// then namespace identities throughout decoded JSON. Empty payloads return
// unchanged bytes; other payloads decode even when mapping is empty.
// Decode/encode/namespace errors return the source bytes with false and an
// error.
//
// Changed documents reencode generic float64 values with HTML escaping
// disabled, so raw numeric spelling or large integer precision can change. No
// client-kind change can still delegate namespace restoration. Custom lookup
// precedes search lookup, known item prefixes retain suffixes, and unknown
// decoded fields remain through generic reencoding. The mapping is read, while
// mutations occur on newly decoded payload storage.
func RestoreResponsesClientToolPayload(payload []byte, mapping ResponsesClientToolMapping) ([]byte, bool, error) {
	if len(payload) == 0 {
		return payload, false, nil
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return payload, false, err
	}
	changed := restoreClientToolValue(value, &mapping)
	if !changed {
		if len(mapping.NamespaceTools) == 0 {
			return payload, false, nil
		}
		return RestoreResponsesNamespaceCalls(payload, mapping.NamespaceTools)
	}
	var rebuilt bytes.Buffer
	encoder := json.NewEncoder(&rebuilt)
	// Rebuilding uses decoded numeric values, which can change unrelated number
	// spelling. Keep literal HTML text while publishing the restored client identity fields.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return payload, false, err
	}
	rebuiltPayload := bytes.TrimSuffix(rebuilt.Bytes(), []byte("\n"))
	if len(mapping.NamespaceTools) == 0 {
		return rebuiltPayload, true, nil
	}
	restored, _, err := RestoreResponsesNamespaceCalls(rebuiltPayload, mapping.NamespaceTools)
	if err != nil {
		return payload, false, err
	}
	return restored, true, nil
}

// restoreClientToolValue recursively restores mapped function calls in decoded
// maps/arrays and reports any recognized client-kind change. Callers supply an
// acyclic value and nonnil adapter; it mutates shared decoded objects, trims
// lookup names, and prefers custom mapping over enabled search mapping.
//
// Custom arguments become freeform input with their argument/namespace fields
// removed. Search arguments become client-executed JSON values and lose proxy
// name/namespace. Known ID prefixes change independently of call_id; unknown
// prefixes remain. Unrelated nested objects are still visited, so caller
// validation determines whether these recognized shapes belong to actual
// response output.
func restoreClientToolValue(value any, adapter *ResponsesClientToolMapping) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			changed = restoreClientToolValue(item, adapter) || changed
		}
	case map[string]any:
		if strings.TrimSpace(stringValue(typed["type"])) == "function_call" {
			name := strings.TrimSpace(stringValue(typed["name"]))
			// Client kind determines argument representation. Resolve custom before
			// search so one function name cannot acquire two incompatible client input shapes.
			if adapter.CustomTools[name] {
				typed["type"] = "custom_tool_call"
				retypeResponsesToolCallItemID(typed, "custom_tool_call")
				typed["input"] = extractCustomToolCallInput(rawObjectString(typed["arguments"]))
				delete(typed, "arguments")
				delete(typed, "namespace")
				changed = true
			} else if adapter.ToolSearch && name == toolSearchProxyName {
				typed["type"] = "tool_search_call"
				retypeResponsesToolCallItemID(typed, "tool_search_call")
				typed["execution"] = "client"
				typed["arguments"] = toolSearchCallArgumentsJSON(rawObjectString(typed["arguments"]))
				delete(typed, "name")
				delete(typed, "namespace")
				changed = true
			}
		}
		for _, child := range typed {
			changed = restoreClientToolValue(child, adapter) || changed
		}
	}
	return changed
}

// NewResponsesClientToolStreamRestorer allocates lookup maps and zero stream
// state around mapping. Mapping collections remain shared and are read during
// conversion; callers keep them stable and use a new restorer for each
// independent upstream stream.
func NewResponsesClientToolStreamRestorer(mapping ResponsesClientToolMapping) *ResponsesClientToolStreamRestorer {
	return &ResponsesClientToolStreamRestorer{adapter: mapping, calls: make(map[string]*responsesClientToolStreamCall), byOutput: make(map[int]*responsesClientToolStreamCall)}
}

// Restore converts one typed event into zero or more client events and updates
// stream state. Nil receivers return the supplied event unchanged. Constructed
// receivers resequence emitted events from the first incoming number; nested
// Item pointers and Response.Output storage can be mutated through the copied
// event.
//
// Proxy argument deltas are buffered and suppressed. Custom argument
// completion unwraps one complete input and can emit delta plus done, while
// search waits for output_item.done. A nonempty completed argument string
// replaces prior fragments. Item completion removes recorded lookups, and
// namespace identities are restored before emission. Callers provide
// consistent upstream identities/indexes, serialize calls, and discard the
// restorer after an error or stream end.
func (r *ResponsesClientToolStreamRestorer) Restore(event ResponsesStreamEvent) []ResponsesStreamEvent {
	if r == nil {
		return []ResponsesStreamEvent{event}
	}
	if !r.seenSeq {
		r.nextSeq = event.SequenceNumber
		r.seenSeq = true
	}
	var out []ResponsesStreamEvent
	emit := func(event ResponsesStreamEvent) {
		event.SequenceNumber = r.nextSeq
		r.nextSeq++
		out = append(out, event)
	}

	switch event.Type {
	case "response.output_item.added":
		// Opening items expose client kinds before complete arguments are available.
		// Retain upstream lookup identities in the record while emitting the client representation.
		if call := r.recordItem(event); call != nil {
			if call.kind == "custom" {
				event.Item.Type = "custom_tool_call"
				event.Item.Input = ""
				event.Item.Arguments = ""
				event.Item.Namespace = ""
			} else {
				event.Item.Type = "tool_search_call"
				event.Item.Name = ""
				event.Item.Arguments = "{}"
				event.Item.Namespace = ""
			}
			if call.clientItemID != "" {
				event.Item.ID = call.clientItemID
			}
		}
		emit(r.restoreNamespaceEvent(event))
	case "response.function_call_arguments.delta":
		// A custom wrapper can be split inside its JSON string. Buffer fragments
		// until completion; Builder writes return nil errors and no client number is emitted here.
		if call := r.callFor(event); call != nil {
			_, _ = call.arguments.WriteString(event.Delta)
			return nil
		}
		emit(r.restoreNamespaceEvent(event))
	case "response.function_call_arguments.done":
		if call := r.callFor(event); call != nil {
			// A nonempty done value is the complete argument representation and
			// replaces earlier fragments. Empty done values retain available buffered text;
			// Builder.WriteString returns nil error, so discarding its result loses no failure.
			if event.Arguments != "" {
				call.arguments.Reset()
				_, _ = call.arguments.WriteString(event.Arguments)
			}
			// A completed wrapper yields one whole client input. Expand nonempty text
			// into delta and done events; search carries its object on item completion.
			if call.kind == "custom" {
				input := extractCustomToolCallInput(call.arguments.String())
				if input != "" {
					emit(ResponsesStreamEvent{Type: "response.custom_tool_call_input.delta", OutputIndex: call.outputIdx, ItemID: call.clientItemID, Delta: input})
				}
				emit(ResponsesStreamEvent{Type: "response.custom_tool_call_input.done", OutputIndex: call.outputIdx, ItemID: call.clientItemID, CallID: call.callID, Name: call.name, Input: input})
			}
			return out
		}
		emit(r.restoreNamespaceEvent(event))
	case "response.output_item.done":
		// Closing items carry the final accumulated representation even when no
		// argument-done event arrived. Restore it before removing the record lookup aliases.
		if call := r.recordItem(event); call != nil {
			if call.kind == "custom" {
				event.Item.Type = "custom_tool_call"
				event.Item.Input = extractCustomToolCallInput(call.arguments.String())
				event.Item.Arguments = ""
				event.Item.Namespace = ""
			} else {
				event.Item.Type = "tool_search_call"
				event.Item.Name = ""
				event.Item.Arguments = call.arguments.String()
				// An empty completed search buffer supplies no JSON value to its codec.
				// Select an empty object so the emitted discovery arguments remain encodable.
				if strings.TrimSpace(event.Item.Arguments) == "" {
					event.Item.Arguments = "{}"
				}
				event.Item.Namespace = ""
			}
			if call.clientItemID != "" {
				event.Item.ID = call.clientItemID
			}
			// Remove all published aliases once this item closes, so later
			// argument events cannot keep appending through an obsolete output slot.
			delete(r.calls, call.itemID)
			delete(r.calls, call.callID)
			delete(r.byOutput, call.outputIdx)
		}
		emit(r.restoreNamespaceEvent(event))
	default:
		// Response snapshots carry buffered output representations. Restore that
		// shared slice before emitting the enclosing event with the stream's next number.
		if event.Response != nil {
			restoreResponsesOutputClientTools(event.Response.Output, &r.adapter)
		}
		emit(r.restoreNamespaceEvent(event))
	}
	return out
}

// RestoreEvent restores one JSON event object, preserving raw paths when typed
// tool conversion is unnecessary. It requires a constructed nonnil receiver
// for nonempty successfully decoded input. Empty input returns no payload;
// decoding or encoding errors return no payload result with an error.
//
// Terminal objects use generic client-tool restoration then raw resequencing.
// Other unmatched events can retain exact bytes if their sequence already
// agrees; matched lifecycle events use the typed codec and may lose unmodeled
// fields. Suppressed events return an empty list with true. State can already
// advance before a later encoding failure, so callers discard this restorer on
// error; null/nonobject input is outside the required event shape.
func (r *ResponsesClientToolStreamRestorer) RestoreEvent(payload []byte) ([][]byte, bool, error) {
	if len(payload) == 0 {
		return nil, false, nil
	}
	var wire struct {
		// Type selects terminal generic restoration before lifecycle typed decoding.
		// Terminal classification trims whitespace; the four typed lifecycle labels compare exactly.
		Type string `json:"type"` // incoming event type label
		// Sequence seeds emission when state is unseen. A missing number decodes
		// as zero and can stay absent when the first raw payload passes through.
		Sequence int `json:"sequence_number"` // incoming event sequence number
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return nil, false, err
	}
	// Terminal snapshots contain complete output arrays and extension fields.
	// Restore them through generic JSON before assigning the emitted sequence number.
	if isResponsesClientToolTerminalEvent(wire.Type) {
		restored, changed, err := RestoreResponsesClientToolPayload(payload, r.adapter)
		if err != nil {
			return nil, false, err
		}
		return r.resequenceRaw(restored, wire.Sequence, changed)
	}
	if !clientToolLifecycleEvent(wire.Type) {
		return r.resequenceRaw(payload, wire.Sequence, false)
	}
	if !r.clientToolEventPayload(payload) {
		return r.resequenceRaw(payload, wire.Sequence, false)
	}
	// Selected tool lifecycle events need buffering or expansion. Their typed
	// codec models emitted tool fields, so extension preservation depends on taking the raw path.
	var event ResponsesStreamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, false, err
	}
	events := r.Restore(event)
	if len(events) == 1 {
		unchanged, err := json.Marshal(events[0])
		// Equality permits retaining the source bytes after a one-event conversion.
		// A failed comparison encoding continues to the normal result encoder, which reports errors.
		if err == nil && bytes.Equal(bytes.TrimSpace(unchanged), bytes.TrimSpace(payload)) {
			return [][]byte{payload}, false, nil
		}
	}
	result := make([][]byte, 0, len(events))
	for _, restored := range events {
		encoded, err := json.Marshal(restored)
		if err != nil {
			return nil, false, err
		}
		result = append(result, encoded)
	}
	return result, true, nil
}

// isResponsesClientToolTerminalEvent recognizes the six
// completion/failure/cancellation lifecycle labels after trimming surrounding
// whitespace.
func isResponsesClientToolTerminalEvent(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

// clientToolEventPayload decides whether a decoded lifecycle payload needs
// typed conversion using names, recorded IDs, or output index. Decode failure
// returns false. Callers provide a constructed receiver; an absent index
// decodes to zero and can match a record at that position.
//
// An item must have exact function_call type and can qualify through
// custom/search/namespace mapping or recorded identities. Item-free payloads
// also qualify through a known namespace name or lookup record. These are
// selection signals, not proof of unique association: field names are compared
// exactly and a matched output slot can identify a call despite absent or
// conflicting IDs.
func (r *ResponsesClientToolStreamRestorer) clientToolEventPayload(payload []byte) bool {
	var raw struct {
		// ItemID, CallID, Name, and OutputIndex select item-free argument events.
		// IDs/index can match recorded calls, while Name alone selects a declared
		// namespace; an absent index decodes as output zero.
		ItemID      string `json:"item_id"`      // incoming upstream item identity
		CallID      string `json:"call_id"`      // incoming call correlation identity
		Name        string `json:"name"`         // incoming exact function name
		OutputIndex int    `json:"output_index"` // incoming output position index
		// Item selects an output lifecycle record. With it present, only
		// function_call shapes qualify and item identities supply the lookup evidence.
		Item *struct {
			Type   string `json:"type"`    // incoming item kind label
			ID     string `json:"id"`      // incoming item identity string
			CallID string `json:"call_id"` // incoming call correlation identity
			Name   string `json:"name"`    // incoming exact function name
		} `json:"item"`
	}
	// A failed selector decode supplies no trusted typed-tool signal. The
	// raw path remains available for ordinary resequencing and its own error reporting.
	if err := json.Unmarshal(payload, &raw); err != nil {
		return false
	}
	if raw.Item != nil {
		if raw.Item.Type != "function_call" {
			return false
		}
		_, namespaceTool := r.adapter.NamespaceTools[raw.Item.Name]
		return r.adapter.CustomTools[raw.Item.Name] || (r.adapter.ToolSearch && raw.Item.Name == toolSearchProxyName) || namespaceTool || r.calls[raw.Item.ID] != nil || r.calls[raw.Item.CallID] != nil
	}
	if _, namespaceTool := r.adapter.NamespaceTools[raw.Name]; namespaceTool {
		return true
	}
	if r.calls[raw.ItemID] != nil || r.calls[raw.CallID] != nil || r.byOutput[raw.OutputIndex] != nil {
		return true
	}
	return false
}

// clientToolLifecycleEvent recognizes four exact item/argument lifecycle
// labels handled by typed client-tool restoration.
func clientToolLifecycleEvent(typ string) bool {
	switch typ {
	case "response.output_item.added", "response.output_item.done", "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return true
	default:
		return false
	}
}

// resequenceRaw seeds sequence state from the first observation and keeps
// bytes when the expected number matches and changed is false. Otherwise it
// requires an object decoded into a writable map, installs nextSeq, and
// advances before encoding. Failures return no payload and can leave sequence
// state advanced.
//
// Generic reencoding retains unknown fields in decoded form but uses float64
// numbers and ordinary HTML escaping, so lexical and numeric representation
// can change. Missing wire sequence values arrive as zero from the caller's
// header decode; an unchanged first payload can therefore remain without an
// explicit key. Callers serialize access and discard the stream converter
// after an error.
func (r *ResponsesClientToolStreamRestorer) resequenceRaw(payload []byte, sequence int, changed bool) ([][]byte, bool, error) {
	if !r.seenSeq {
		r.nextSeq, r.seenSeq = sequence, true
	}
	if r.nextSeq == sequence && !changed {
		r.nextSeq++
		return [][]byte{payload}, false, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, false, err
	}
	// Suppression or expansion can break incoming numbering. Publish the next
	// emitted number through a rebuilt object; unrelated numeric text now follows float64 encoding.
	raw["sequence_number"] = r.nextSeq
	r.nextSeq++
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	return [][]byte{encoded}, true, nil
}

// responsesClientToolItemType returns custom_tool_call for the literal custom
// kind. Every other kind selects tool_search_call; recordItem passes only the
// recognized custom or search kinds on this conversion path.
func responsesClientToolItemType(kind string) string {
	if kind == "custom" {
		return "custom_tool_call"
	}
	return "tool_search_call"
}

// recordItem registers a mapped exact function_call item and returns its
// shared record, otherwise nil. It compares names exactly and uses item ID
// before call ID as its primary key. Constructed lookup maps are required, and
// both absent identities can use the empty key.
//
// New records fix client kind, upstream/client IDs, name, and output index
// from that first item, then publish shared aliases by nonempty call ID and
// output index. Reused records retain this metadata. A nonempty item argument
// string replaces accumulated fragments, while an empty string leaves earlier
// content. Stable distinct caller identities and indexes are required; this
// helper does not reject alias collisions.
func (r *ResponsesClientToolStreamRestorer) recordItem(event ResponsesStreamEvent) *responsesClientToolStreamCall {
	if event.Item == nil || event.Item.Type != "function_call" {
		return nil
	}
	name := event.Item.Name
	kind := ""
	if r.adapter.CustomTools[name] {
		kind = "custom"
	} else if r.adapter.ToolSearch && name == toolSearchProxyName {
		kind = "tool_search"
	}
	if kind == "" {
		return nil
	}
	// Some items supply only call correlation. Use it as the primary lookup key
	// when item identity is absent, then retain any output-position alias separately.
	key := event.Item.ID
	if key == "" {
		key = event.Item.CallID
	}
	call := r.calls[key]
	if call == nil {
		call = &responsesClientToolStreamCall{
			kind:         kind,
			name:         name,
			callID:       event.Item.CallID,
			itemID:       event.Item.ID,
			clientItemID: retypedResponsesToolCallItemID(event.Item.ID, responsesClientToolItemType(kind)),
			outputIdx:    event.OutputIndex,
		}
		r.calls[key] = call
		if call.callID != "" {
			r.calls[call.callID] = call
		}
		r.byOutput[call.outputIdx] = call
	}
	// Full item arguments can arrive after delta collection. Their nonempty
	// value replaces the Builder contents, whose WriteString operation cannot return an error.
	if event.Item.Arguments != "" {
		call.arguments.Reset()
		_, _ = call.arguments.WriteString(event.Item.Arguments)
	}
	return call
}

// callFor selects an existing call by exact item-ID lookup, then output index,
// then a map scan. The scan accepts matching nonempty call ID or, with absent
// item ID, matching nonempty name. Nil result means no record matched; the
// returned pointer is shared stream state.
//
// Index fallback precedes call-ID/name resolution, and absent indexes can
// select zero. The final scan uses map iteration order, so ambiguous name
// matches or conflicting criteria have no deterministic identity guarantee.
// Callers provide consistent upstream item IDs and positions to keep argument
// fragments associated with their intended call.
func (r *ResponsesClientToolStreamRestorer) callFor(event ResponsesStreamEvent) *responsesClientToolStreamCall {
	if call := r.calls[event.ItemID]; call != nil {
		return call
	}
	// Argument frames can omit item identity. The recorded output position
	// resolves those fragments before the weaker call-ID/name scan is attempted.
	if call := r.byOutput[event.OutputIndex]; call != nil {
		return call
	}
	// If direct identities and positions miss, accept call correlation or an
	// item-free named fragment. Map order makes ambiguous matches depend on caller consistency.
	for _, call := range r.calls {
		if (event.CallID != "" && call.callID == event.CallID) || (event.ItemID == "" && event.Name != "" && call.name == event.Name) {
			return call
		}
	}
	return nil
}

// restoreNamespaceEvent restores mapped exact function item names/namespaces
// and mapped argument-event names. It returns a copied event whose nested Item
// pointer can still change in place; an empty namespace map leaves the
// supplied fields as they are.
func (r *ResponsesClientToolStreamRestorer) restoreNamespaceEvent(event ResponsesStreamEvent) ResponsesStreamEvent {
	if len(r.adapter.NamespaceTools) == 0 {
		return event
	}
	// Items hold namespace and child name; argument frames expose only a name.
	// Restore each modeled location from the same flat-name pair recorded at adaptation.
	if event.Item != nil && event.Item.Type == "function_call" {
		if name, ok := r.adapter.NamespaceTools[event.Item.Name]; ok {
			event.Item.Name, event.Item.Namespace = name.Name, name.Namespace
		}
	}
	if event.Type == "response.function_call_arguments.delta" || event.Type == "response.function_call_arguments.done" {
		if name, ok := r.adapter.NamespaceTools[event.Name]; ok {
			event.Name = name.Name
		}
	}
	return event
}

// restoreResponsesOutputClientTools restores supported function outputs in the
// supplied slice in place. Callers pass a nonnil adapter and retain ownership
// of shared output storage; exact custom names take precedence over enabled
// search, then ordinary function names can regain namespaces.
//
// Known item prefixes change with client kind while call IDs remain. Custom
// arguments become freeform input and lose argument/namespace fields; search
// keeps accumulated argument text for its specialized codec and removes proxy
// name/namespace. Unsupported outputs remain, and this helper neither
// reconstructs missing history nor validates that a name corresponds to an
// available client tool.
func restoreResponsesOutputClientTools(outputs []ResponsesOutput, adapter *ResponsesClientToolMapping) {
	for index := range outputs {
		output := &outputs[index]
		if output.Type != "function_call" {
			continue
		}
		// Change client kind before namespace lookup. Custom and search proxies use
		// their own input/argument shapes, while ordinary functions retain the recorded namespace pair.
		if adapter.CustomTools[output.Name] {
			output.Type = "custom_tool_call"
			output.ID = retypedResponsesToolCallItemID(output.ID, output.Type)
			output.Input = extractCustomToolCallInput(output.Arguments)
			output.Arguments = ""
			output.Namespace = ""
		} else if adapter.ToolSearch && output.Name == toolSearchProxyName {
			output.Type = "tool_search_call"
			output.ID = retypedResponsesToolCallItemID(output.ID, output.Type)
			output.Name = ""
			output.Namespace = ""
		}
		if name, ok := adapter.NamespaceTools[output.Name]; ok && output.Type == "function_call" {
			output.Name, output.Namespace = name.Name, name.Namespace
		}
	}
}
