package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// responses_namespace.go flattens namespaced Responses tools and restores
// namespace identities in replies.
// A Responses namespace gives a tool two identity components, while function-
// only upstreams accept one name. Flattening joins the namespace and child
// name, checks ambiguous results before request mutation, and returns the
// pairs needed to restore response calls. Selected namespace groups can remain
// declared in their original form.
//
// Declarations, historical calls, and tool choice need corresponding
// identities for a round trip. Flattening copies each emitted child map but
// edits request history maps directly; restoration walks decoded response maps
// and arrays to reverse known function names. Reencoding a restored payload
// retains decoded fields while imposing encoding/json's number and string
// representation rules, so it cannot promise raw document identity.

// ResponsesNamespaceName aliases the bridge's namespace and child-name
// identity pair.
type ResponsesNamespaceName = NamespacedToolName

// FlattenResponsesNamespaces delegates request flattening with no preserved
// namespace set. The request map and its nested history may be mutated;
// callers retain the returned name map for later function-call restoration and
// handle declaration conflicts as errors.
func FlattenResponsesNamespaces(req map[string]any) (map[string]ResponsesNamespaceName, bool, error) {
	return FlattenResponsesNamespacesExcept(req, nil)
}

// FlattenResponsesNamespacesExcept replaces namespace function declarations
// with flat names, except for groups enabled in preserved. It accepts decoded
// map[string]any and []any shapes, mutates the request, and returns a newly
// collected restoration map. Without any eligible named function mapping it
// returns unchanged; unsupported children can be dropped when flattening
// occurs.
//
// Names are checked against top-level function/custom tools and other
// namespace identities before mutation, because one flat function name cannot
// restore two different calls. flattenNamespaceToolName shortens oversized
// joined names with a hash suffix, so restoration uses the returned identity
// pairs. Emitted children are shallow copies, while nested input calls and
// tool choice are rewritten in place. A removed namespace-group choice becomes
// auto; callers validate malformed declarations before relying on complete
// reverse mapping.
func FlattenResponsesNamespacesExcept(req map[string]any, preserved map[string]bool) (map[string]ResponsesNamespaceName, bool, error) {
	if req == nil {
		return nil, false, nil
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil, false, nil
	}

	topLevel := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.TrimSpace(stringValue(tool["type"]))
		name := strings.TrimSpace(stringValue(tool["name"]))
		if (typ == "function" || typ == "custom") && name != "" {
			topLevel[name] = true
		}
	}

	names := make(map[string]ResponsesNamespaceName)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(tool["type"])) != "namespace" {
			continue
		}
		namespace := strings.TrimSpace(stringValue(tool["name"]))
		if namespace == "" || preserved[namespace] {
			continue
		}
		for _, rawChild := range namespaceChildren(tool) {
			child, ok := rawChild.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(child["type"])) != "function" {
				continue
			}
			name := strings.TrimSpace(stringValue(child["name"]))
			if name == "" {
				continue
			}
			flat := flattenNamespaceToolName(namespace, name)
			entry := ResponsesNamespaceName{Namespace: namespace, Name: name}
			// Check conflicts before publishing new declarations or changing history.
			// One upstream name must map to one client namespace/name pair for restoration.
			if topLevel[flat] {
				return nil, false, fmt.Errorf("namespace tool %q/%q flattens to %q which conflicts with a top-level tool of the same name; this upstream cannot disambiguate them, rename one of the tools", namespace, name, flat)
			}
			if prev, exists := names[flat]; exists && prev != entry {
				return nil, false, fmt.Errorf("namespace tools %q/%q and %q/%q both flatten to %q; this upstream cannot disambiguate them, rename one of the tools", prev.Namespace, prev.Name, namespace, name, flat)
			}
			names[flat] = entry
		}
	}
	if len(names) == 0 {
		return nil, false, nil
	}

	flattened := make([]any, 0, len(tools)+len(names))
	seen := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(tool["type"])) != "namespace" {
			flattened = append(flattened, raw)
			continue
		}
		namespace := strings.TrimSpace(stringValue(tool["name"]))
		if preserved[namespace] {
			flattened = append(flattened, raw)
			continue
		}
		for _, rawChild := range namespaceChildren(tool) {
			child, ok := rawChild.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(child["type"])) != "function" {
				continue
			}
			name := strings.TrimSpace(stringValue(child["name"]))
			flat := flattenNamespaceToolName(namespace, name)
			if name == "" || seen[flat] {
				continue
			}
			seen[flat] = true
			// Copy the declaration map before renaming its child; schema and other
			// nested values remain shared because this pass changes only tool identity.
			flatChild := make(map[string]any, len(child))
			maps.Copy(flatChild, child)
			flatChild["name"] = flat
			flattened = append(flattened, flatChild)
		}
	}
	req["tools"] = flattened
	rewriteNamespaceQualifiedCalls(req["input"], names)
	if choice, ok := req["tool_choice"].(map[string]any); ok {
		choiceNamespace := strings.TrimSpace(stringValue(choice["name"]))
		if strings.TrimSpace(stringValue(choice["type"])) == "namespace" && !preserved[choiceNamespace] {
			// The selected group has become separate functions, so it cannot remain
			// a namespace choice; auto lets the upstream choose those flat functions.
			req["tool_choice"] = "auto"
		} else {
			rewriteNamespaceQualifiedCall(choice, names)
		}
	}
	return names, true, nil
}

// RestoreResponsesNamespaceCalls restores mapped function-call names
// throughout a JSON value and adds their namespaces. Empty payloads or
// mappings and documents with no matched calls return the supplied bytes
// unchanged. Decode or encode errors also return those bytes with false and an
// error.
//
// Changed payloads are reencoded with HTML escaping disabled. The generic
// decoder uses float64 numbers, so conversion can round large integers or
// reject numbers outside its range; raw numeric spelling is not retained. Only
// function_call objects whose trimmed names match the map are renamed,
// including nested lifecycle items. Other fields survive in their decoded
// forms.
func RestoreResponsesNamespaceCalls(payload []byte, names map[string]ResponsesNamespaceName) ([]byte, bool, error) {
	if len(payload) == 0 || len(names) == 0 {
		return payload, false, nil
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return payload, false, err
	}
	changed := restoreResponsesNamespaceValue(value, names)
	if !changed {
		return payload, false, nil
	}
	var rebuilt bytes.Buffer
	encoder := json.NewEncoder(&rebuilt)
	// Rebuilding generic decoded values can change numeric representation. Keep
	// literal HTML characters in text while restoring the protocol identity fields.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return payload, false, err
	}
	return bytes.TrimSuffix(rebuilt.Bytes(), []byte("\n")), true, nil
}

// namespaceChildren returns a nonempty decoded tools array when available.
// Otherwise it reads the children alias, including an empty array; the
// returned collection shares storage with the supplied declaration map.
func namespaceChildren(tool map[string]any) []any {
	if children, ok := tool["tools"].([]any); ok && len(children) > 0 {
		return children
	}
	children, _ := tool["children"].([]any)
	return children
}

// rewriteNamespaceQualifiedCalls recursively rewrites known function_call
// objects in decoded maps and arrays. It changes shared input maps in place;
// callers supply an acyclic JSON-shaped value and the mapping from the same
// flattening operation.
func rewriteNamespaceQualifiedCalls(value any, names map[string]ResponsesNamespaceName) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			rewriteNamespaceQualifiedCalls(item, names)
		}
	case map[string]any:
		if strings.TrimSpace(stringValue(typed["type"])) == "function_call" {
			rewriteNamespaceQualifiedCall(typed, names)
		}
		for _, child := range typed {
			rewriteNamespaceQualifiedCalls(child, names)
		}
	}
}

// rewriteNamespaceQualifiedCall replaces a known namespace/name pair with its
// joined function identity and removes the namespace field. Both supplied
// values are trimmed, and a blank component leaves the map unchanged. The
// mapping entry must match both trimmed components, so an unrelated pair that
// happens to produce the same joined spelling is rejected.
//
// The operation mutates the supplied map in place and returns true for a
// matched rewrite. Callers retain the mapping for response restoration; this
// helper validates that pair alone, rather than the complete call or tool
// declaration.
func rewriteNamespaceQualifiedCall(item map[string]any, names map[string]ResponsesNamespaceName) bool {
	namespace := strings.TrimSpace(stringValue(item["namespace"]))
	name := strings.TrimSpace(stringValue(item["name"]))
	if namespace == "" || name == "" {
		return false
	}
	flat := flattenNamespaceToolName(namespace, name)
	entry, ok := names[flat]
	if !ok || entry.Namespace != namespace || entry.Name != name {
		return false
	}
	item["name"] = flat
	delete(item, "namespace")
	return true
}

// restoreResponsesNamespaceValue walks decoded maps and arrays and restores
// mapped function_call identities. It trims the current function name,
// replaces a matching flat name with the recorded child name, and writes the
// recorded namespace. Other object types keep their identity fields while
// their children are still visited.
//
// The operation mutates shared maps and reports whether any call matched,
// including a match whose values already equal the mapping. Its caller
// provides an acyclic JSON-shaped value and serializes a changed result; byte
// and numeric representation are determined by that caller's encoder.
func restoreResponsesNamespaceValue(value any, names map[string]ResponsesNamespaceName) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			changed = restoreResponsesNamespaceValue(item, names) || changed
		}
	case map[string]any:
		if strings.TrimSpace(stringValue(typed["type"])) == "function_call" {
			if entry, ok := names[strings.TrimSpace(stringValue(typed["name"]))]; ok {
				typed["name"] = entry.Name
				typed["namespace"] = entry.Namespace
				changed = true
			}
		}
		for _, child := range typed {
			changed = restoreResponsesNamespaceValue(child, names) || changed
		}
	}
	return changed
}

// stringValue reads a decoded string and returns empty for other values.
// Namespace matching uses this exact type check before trimming, so numeric or
// null identities cannot become string names.
func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
