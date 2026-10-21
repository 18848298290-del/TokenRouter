package bridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responses_tool_identity.go resolves client tool identities for Responses
// protocol conversion and records the reverse names used by output readers.
// Native Responses requests carry their supplied identities directly. A
// function-only protocol instead needs one executable name for each original
// namespace and child pair, including tools introduced by discovery or an
// additional_tools history item. Default functions share the bare-name space
// with ordinary declarations, while discovered plain functions can carry
// their own declared name as a client namespace.
//
// Declaration evidence establishes known aliases before history or choices
// are rewritten. Missing historical declarations use responses_tool_history.go
// for separate replay names. Reverse-name collisions fail before an upstream
// request can execute the wrong tool. The resulting maps belong to one
// preparation and remain immutable after publication to response readers or
// a later WebSocket turn. Historical declarations supply identity evidence;
// collecting them here does not add tools to the active declaration list.

// responsesDefaultToolNamespace identifies Codex's default function scope,
// whose tools retain bare upstream names and omit namespace in restored output.
const responsesDefaultToolNamespace = "functions"

// responsesToolIdentityMapping builds forward aliases and response identities
// from active declarations and supported historical declaration items. Missing
// input is allowed; malformed declaration arrays and conflicting executable
// identities return an error before the caller forwards a converted request.
//
// Ordinary and default-scoped declarations share one canonical identity. A
// discovered plain function also accepts its declared name as a namespace,
// while explicit groups retain their own pair and bounded flat name. History
// declarations establish how prior calls are interpreted without activating
// those declarations for the current request. Callers publish the returned
// maps only after request conversion succeeds and keep them read-only.
func responsesToolIdentityMapping(tools []ResponsesTool, input json.RawMessage) (ResponsesClientToolMapping, error) {
	mapping := ResponsesClientToolMapping{
		CallNames:          make(map[NamespacedToolName]string),
		NamespaceTools:     make(map[string]ResponsesNamespaceName),
		FunctionTools:      make(map[string]bool),
		DeclaredNamespaces: make(map[string]bool),
	}
	owners := make(map[string]string)
	directFunctions := make(map[string]bool)
	discoveredFunctions := make(map[string]bool)
	for _, tool := range tools {
		if err := mapping.registerToolIdentity(tool, owners, directFunctions); err != nil {
			return ResponsesClientToolMapping{}, err
		}
	}
	var declarationErr error
	gjson.ParseBytes(input).ForEach(func(_, item gjson.Result) bool {
		kind := item.Get("type").String()
		if kind != "additional_tools" && kind != "tool_search_output" {
			return true
		}
		if kind == "tool_search_output" {
			status := item.Get("status")
			if status.Exists() && (status.Type != gjson.String || strings.TrimSpace(status.String()) != "completed") {
				return true
			}
		}
		declared := item.Get("tools")
		if !declared.Exists() || declared.Type == gjson.Null {
			return true
		}
		var historyTools []ResponsesTool
		if !declared.IsArray() {
			declarationErr = fmt.Errorf("%s tools must be an array", kind)
			return false
		}
		if declarationErr = json.Unmarshal([]byte(declared.Raw), &historyTools); declarationErr != nil {
			return false
		}
		for _, tool := range historyTools {
			if declarationErr = mapping.registerToolIdentity(tool, owners, directFunctions); declarationErr != nil {
				return false
			}
			if tool.Type == "function" && tool.Name != "" {
				discoveredFunctions[tool.Name] = true
			}
		}
		return true
	})
	if declarationErr != nil {
		return ResponsesClientToolMapping{}, declarationErr
	}
	// Discovery output establishes these aliases; a top-level function alone
	// cannot authorize another namespace for its bare executable name.
	for name := range discoveredFunctions {
		if !directFunctions[name] || name == responsesDefaultToolNamespace {
			continue
		}
		identity := NamespacedToolName{Namespace: name, Name: name}
		if previous, exists := mapping.CallNames[identity]; exists && previous != name {
			return ResponsesClientToolMapping{}, fmt.Errorf("discovered function %q conflicts with a namespace tool of the same identity", name)
		}
		mapping.CallNames[identity] = name
		mapping.NamespaceTools[name] = identity
	}
	return mapping, nil
}

// registerToolIdentity records supported declaration names and canonical
// owners in the supplied request-local maps. Namespace groups register their
// function declarations; other child kinds follow the existing converter's
// supported function-child selection rather than acquiring executable aliases.
//
// A default functions group shares bare names with direct functions. Other
// groups use bounded derived names, and each derived name must retain exactly
// one canonical client identity and tool kind. Aliases are registered before
// historical calls are converted so input, choices, and response restoration
// cannot independently choose incompatible names for one declaration.
func (m *ResponsesClientToolMapping) registerToolIdentity(tool ResponsesTool, owners map[string]string, directFunctions map[string]bool) error {
	if tool.Type == "namespace" {
		if tool.Name == "" {
			return nil
		}
		m.DeclaredNamespaces[tool.Name] = true
		children := tool.Tools
		if len(children) == 0 {
			children = tool.Children
		}
		for _, child := range children {
			if child.Type != "function" || child.Name == "" {
				continue
			}
			identity := NamespacedToolName{Namespace: tool.Name, Name: child.Name}
			upstream := flattenNamespaceToolName(tool.Name, child.Name)
			canonical := identity
			if canonical.Namespace == responsesDefaultToolNamespace {
				// Default-scoped and ordinary functions share one executable owner,
				// allowing output restoration to omit the redundant default namespace.
				canonical.Namespace = ""
			}
			owner := fmt.Sprintf("function:%q:%q", canonical.Namespace, canonical.Name)
			if previous, exists := owners[upstream]; exists && previous != owner {
				return fmt.Errorf("namespace tool %q/%q conflicts with upstream function name %q", tool.Name, child.Name, upstream)
			}
			owners[upstream] = owner
			m.CallNames[identity] = upstream
			m.NamespaceTools[upstream] = canonical
			if canonical.Namespace == "" {
				m.CallNames[canonical] = upstream
				m.FunctionTools[upstream] = true
			}
		}
		return nil
	}
	if (tool.Type != "function" && tool.Type != "custom") || tool.Name == "" {
		return nil
	}
	identity := NamespacedToolName{Name: tool.Name}
	owner := fmt.Sprintf("%s:%q:%q", tool.Type, "", tool.Name)
	if previous, exists := owners[tool.Name]; exists && previous != owner {
		return fmt.Errorf("tool %q conflicts with an existing converted tool identity; cannot disambiguate the upstream name", tool.Name)
	}
	owners[tool.Name] = owner
	m.CallNames[identity] = tool.Name
	m.CallNames[NamespacedToolName{Namespace: responsesDefaultToolNamespace, Name: tool.Name}] = tool.Name
	if tool.Type == "function" {
		m.FunctionTools[tool.Name] = true
		directFunctions[tool.Name] = true
	}
	return nil
}

// resolveCallName looks up the upstream spelling of an evidenced identity.
// Bare and default-scoped names remain usable without current
// declarations; another supplied namespace requires declaration or inherited
// mapping evidence and returns an error when that evidence is absent.
//
// Both supplied components identify the client tool. A matching reverse entry
// supports inherited mappings created before forward aliases were recorded.
// Qualified names cannot be derived by concatenation alone, because discovery
// can assign a namespace while the executable declaration still uses a bare
// name. The mapping therefore supplies the conversion's declared aliases.
// Historical replay allocates missing identities separately through
// responsesToolHistoryNames, leaving this lookup strict for qualified choices.
func (m *ResponsesClientToolMapping) resolveCallName(namespace, name string) (string, error) {
	if upstream, ok := m.CallNames[NamespacedToolName{Namespace: namespace, Name: name}]; ok {
		return upstream, nil
	}
	for upstream, original := range m.NamespaceTools {
		if original.Namespace == namespace && original.Name == name {
			return upstream, nil
		}
	}
	if namespace == "" || namespace == responsesDefaultToolNamespace {
		return name, nil
	}
	return "", fmt.Errorf("cannot convert tool call %q in namespace %q without its declaration or inherited mapping", name, namespace)
}

// rewriteResponsesFunctionCalls resolves direct historical function calls
// before a function-only request is sent. It mutates accepted input maps and
// reports changes; nested schemas and tool-result data remain untouched.
// A failure can follow earlier mutations, so callers discard failed candidates.
//
// Calls are visited only at direct input positions. Argument strings, result
// payloads, and nested schemas are not recursively interpreted as executable
// requests, so JSON data that resembles a call remains data. Call IDs and
// argument values stay with their original items while prepared names encode
// the removed namespace for function-only execution. Missing declarations
// receive request-local names independent of callable mappings and selectors.
func rewriteResponsesFunctionCalls(input any, mapping *ResponsesClientToolMapping) (bool, error) {
	items, ok := input.([]any)
	if !ok {
		return false, nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return false, err
	}
	history, err := newResponsesToolHistoryNames(encoded, *mapping)
	if err != nil {
		return false, err
	}
	changed := false
	// Move direct calls to their prepared function-only identities together;
	// call correlation and arguments remain attached while the encoded namespace is removed.
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || stringValue(item["type"]) != "function_call" {
			continue
		}
		name := stringValue(item["name"])
		resolved, err := history.resolve(stringValue(item["namespace"]), name)
		if err != nil {
			return false, err
		}
		if resolved != name {
			item["name"] = resolved
			changed = true
		}
		if _, present := item["namespace"]; present {
			delete(item, "namespace")
			changed = true
		}
	}
	return changed, nil
}

// responsesMappedToolChoice resolves an explicitly qualified tool selector
// against the request's declaration mapping. Bare choices retain their bytes;
// a known namespace-group choice becomes auto after lowering its members.
// Missing qualified identities return an error before upstream execution.
//
// Selectors use evidenced aliases, preventing a synthesized historical name
// from authorizing an undeclared tool after discovery. Raw byte patches keep
// other selector fields intact and support escaped JSON keys. A declared group
// authorizes automatic selection even when platform filtering later removes
// all executable members from that group.
func responsesMappedToolChoice(raw json.RawMessage, mapping *ResponsesClientToolMapping) (json.RawMessage, error) {
	choice := gjson.ParseBytes(raw)
	if !choice.IsObject() {
		return raw, nil
	}
	if choice.Get("type").String() == "namespace" {
		// Function-only selectors cannot select an entire group. A known group
		// declaration authorizes the existing automatic-selection downgrade before
		// platform capability filtering considers its members.
		if mapping.DeclaredNamespaces[choice.Get("name").String()] {
			return json.RawMessage(`"auto"`), nil
		}
		for identity := range mapping.CallNames {
			if identity.Namespace == choice.Get("name").String() {
				return json.RawMessage(`"auto"`), nil
			}
		}
		for _, identity := range mapping.NamespaceTools {
			if identity.Namespace == choice.Get("name").String() {
				return json.RawMessage(`"auto"`), nil
			}
		}
		return nil, fmt.Errorf("cannot convert namespace choice %q without a declared group", choice.Get("name").String())
	}
	namespace := choice.Get("namespace").String()
	if namespace == "" {
		return raw, nil
	}
	namePath := "name"
	name := choice.Get(namePath).String()
	if name == "" {
		namePath = "function.name"
		name = choice.Get(namePath).String()
	}
	resolved, err := mapping.resolveCallName(namespace, name)
	if err != nil {
		return nil, err
	}
	// Encode the resolved identity in the executable name, then remove the
	// namespace because function-only selectors dispatch through that name alone.
	updated, err := sjson.SetBytes(raw, namePath, resolved)
	if err != nil {
		return nil, err
	}
	return sjson.DeleteBytes(updated, "namespace")
}

// rewriteResponsesToolChoice adopts a mapped selector in a mutable request.
// Errors leave the supplied selector unchanged; unchanged representations keep
// their current value. Callers apply tool-kind lowering after identity mapping.
func rewriteResponsesToolChoice(req map[string]any, mapping *ResponsesClientToolMapping) (bool, error) {
	choice, present := req["tool_choice"]
	if !present {
		return false, nil
	}
	encoded, err := json.Marshal(choice)
	if err != nil {
		return false, err
	}
	mapped, err := responsesMappedToolChoice(encoded, mapping)
	if err != nil {
		return false, err
	}
	if string(mapped) == string(encoded) {
		return false, nil
	}
	if err := json.Unmarshal(mapped, &choice); err != nil {
		return false, err
	}
	req["tool_choice"] = choice
	return true, nil
}

// promoteResponsesAdditionalTools moves current additional_tools declarations
// into a function-only request's active list. Explicit empty or null top-level
// tools retain reset behavior; invalid additional arrays return an error.
// Native Responses does not call this conversion-specific preparation.
//
// Native declarations establish visibility at a particular input position.
// A function-only target uses a separate declaration list, so this adaptation
// publishes those definitions there and consumes their declaration items.
// Explicit resets remain authoritative. Other input items retain their relative
// order and map contents, and the adopted declaration slice continues sharing
// each supplied tool map with the mutable request candidate.
func promoteResponsesAdditionalTools(req map[string]any) (bool, error) {
	tools, present := req["tools"]
	declared, valid := tools.([]any)
	if present && (!valid || len(declared) == 0) {
		return false, nil
	}
	input, ok := req["input"].([]any)
	if !ok {
		return false, nil
	}
	filtered := make([]any, 0, len(input))
	changed := false
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || stringValue(item["type"]) != "additional_tools" {
			filtered = append(filtered, raw)
			continue
		}
		additional, ok := item["tools"].([]any)
		if !ok {
			return false, fmt.Errorf("additional_tools tools must be an array")
		}
		declared = append(declared, additional...)
		changed = true
	}
	if changed {
		// Publish the matched declaration and filtered-history slices together so
		// the converted request cannot retain only one side of the visibility change.
		req["tools"] = declared
		req["input"] = filtered
	}
	return changed, nil
}
