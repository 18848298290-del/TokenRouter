package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// responses_tool_search_discoveries.go promotes discovered tool declarations
// from Responses history while checking identity conflicts.
// A tool-search result can advertise declarations that a function-only
// upstream must receive on the next request. Promotion keeps static
// declarations before history discoveries and admits only named
// function/custom tools or named namespace function children. Completed or
// status-free results can supply these declarations; discovery data remains in
// history for a separate output-normalization pass.
//
// One flat upstream name must retain its original client identity. Static
// records therefore register kind, namespace/name, and encoded normalized
// shape before discoveries are admitted. Repeated equal identities are
// skipped, conflicting keys fail, and namespace promotion retains only newly
// admitted children. Shallow copies keep nested schema sharing, while custom
// comparison uses the lowered wrapper shape because that is the declaration
// sent to function execution.

// responsesDiscoveredToolIdentity records the normalized client identity and
// encoded comparison shape of a declaration.
type responsesDiscoveredToolIdentity struct {
	// typ, name, namespace, and encoded describe one normalized identity.
	// Equality checks all four, while an admission key can join namespace/name
	// into a single upstream label and therefore needs this stronger comparison.
	typ       string // normalized client tool kind
	name      string // trimmed unqualified tool name
	namespace string // trimmed namespace or empty direct identity
	encoded   string // normalized comparison JSON bytes
	// ambiguous marks differing static identities at the same admission key.
	// Later discovery admission rejects that key even when one identity matches.
	ambiguous bool // known key has conflicting declarations
}

// responsesNamespaceToolCandidate pairs a flattened lookup name with a copied
// child declaration and its client identity.
type responsesNamespaceToolCandidate struct {
	// flat indexes identity's namespace/name pair, while child carries its
	// copied unqualified function declaration. Promotion retains only admitted
	// children, and child's nested schema values still share source storage.
	flat     string                          // bounded flat admission key
	child    map[string]any                  // copied child declaration map
	identity responsesDiscoveredToolIdentity // original client identity dimensions
}

// promoteResponsesToolSearchDiscoveries appends accepted history discoveries
// after static declarations in req. It requires decoded nonempty tools/input
// arrays and a tool-search declaration; otherwise it leaves the request
// unchanged. Admission errors return before assigning the combined tools
// slice, while returned promoted maps can share nested values.
func promoteResponsesToolSearchDiscoveries(req map[string]any) (bool, error) {
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 || !hasResponsesToolSearchDeclaration(tools) {
		return false, nil
	}
	input, ok := req["input"].([]any)
	if !ok || len(input) == 0 {
		return false, nil
	}
	promoted, err := promotedResponsesToolSearchDiscoveries(tools, input)
	if err != nil {
		return false, err
	}
	if len(promoted) == 0 {
		return false, nil
	}
	req["tools"] = append(tools, promoted...)
	return true, nil
}

// promotedResponsesToolSearchDiscoveries collects eligible declarations from
// tool_search_output history while registering static identities first. Absent
// status or a trimmed completed string permits promotion; unsupported
// item/tool shapes and unencodable discovery arrays are skipped. Nil or
// unqualified inputs return no declarations.
//
// Direct function/custom names and flattened namespace child names share
// admission keys. Equal normalized identities are omitted, while conflicting
// or ambiguous keys return an error and discard the collected result. Static
// declarations retain their order, and discoveries follow history order.
// Namespace copies receive only newly admitted children; shallow child/parent
// copies keep their nested schema values shared.
func promotedResponsesToolSearchDiscoveries(tools, input []any) ([]any, error) {
	if len(tools) == 0 || !hasResponsesToolSearchDeclaration(tools) || len(input) == 0 {
		return nil, nil
	}

	known := make(map[string]responsesDiscoveredToolIdentity)
	for _, raw := range tools {
		registerExistingResponsesToolIdentity(known, raw)
	}

	promoted := make([]any, 0)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(item["type"])) != "tool_search_output" || !usableResponsesToolSearchOutput(item) {
			continue
		}
		discoveries, ok := item["tools"].([]any)
		if !ok || len(discoveries) == 0 {
			continue
		}
		// An unencodable discovery array cannot be forwarded as tool-search output.
		// Skip its entire promotion contribution so declarations are not taken from
		// data whose matching history representation cannot be serialized.
		if _, err := json.Marshal(discoveries); err != nil {
			continue
		}
		for _, rawDiscovery := range discoveries {
			discovery, ok := rawDiscovery.(map[string]any)
			if !ok {
				continue
			}
			typ := strings.TrimSpace(stringValue(discovery["type"]))
			switch typ {
			case "function", "custom":
				copy, identity, ok := responsesDirectToolDiscovery(discovery, typ)
				if !ok {
					continue
				}
				appendTool, err := admitResponsesDiscoveredTool(known, identity.name, identity)
				if err != nil {
					return nil, err
				}
				if appendTool {
					promoted = append(promoted, copy)
				}
			case "namespace":
				copy, identities, ok := responsesNamespaceToolDiscovery(discovery)
				if !ok {
					continue
				}
				children := make([]any, 0, len(identities))
				for _, candidate := range identities {
					appendTool, err := admitResponsesDiscoveredTool(known, candidate.flat, candidate.identity)
					if err != nil {
						return nil, err
					}
					if appendTool {
						children = append(children, candidate.child)
					}
				}
				if len(children) > 0 {
					// Publish only newly admitted children under the primary tools key.
					// Remove the alternate key so a later flattening pass sees one child list.
					copy["tools"] = children
					delete(copy, "children")
					promoted = append(promoted, copy)
				}
			}
		}
	}
	return promoted, nil
}

// hasResponsesToolSearchDeclaration reports a decoded map declaration with
// trimmed tool_search type spelling.
func hasResponsesToolSearchDeclaration(tools []any) bool {
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if ok && strings.TrimSpace(stringValue(tool["type"])) == "tool_search" {
			return true
		}
	}
	return false
}

// usableResponsesToolSearchOutput accepts an absent status or a string whose
// trimmed value is completed. Present null, other types, and other status text
// reject promotion; the function does not inspect call IDs or discovery
// contents.
func usableResponsesToolSearchOutput(item map[string]any) bool {
	status, present := item["status"]
	if !present {
		return true
	}
	text, ok := status.(string)
	return ok && strings.TrimSpace(text) == "completed"
}

// registerExistingResponsesToolIdentity delegates normalized
// function/custom/namespace identity collection into known. Unsupported
// shapes, blank names, and encoding failures contribute nothing; parameter
// schemas are not validated. Namespace children register by flat names.
// The supplied map is mutated and must be initialized for any accepted
// declaration.
func registerExistingResponsesToolIdentity(known map[string]responsesDiscoveredToolIdentity, raw any) {
	tool, ok := raw.(map[string]any)
	if !ok {
		return
	}
	typ := strings.TrimSpace(stringValue(tool["type"]))
	switch typ {
	case "function", "custom":
		_, identity, ok := responsesDirectToolDiscovery(tool, typ)
		if ok {
			registerExistingResponsesIdentity(known, identity.name, identity)
		}
	case "namespace":
		_, identities, ok := responsesNamespaceToolDiscovery(tool)
		if !ok {
			return
		}
		for _, candidate := range identities {
			registerExistingResponsesIdentity(known, candidate.flat, candidate.identity)
		}
	}
}

// registerExistingResponsesIdentity stores the first identity at key and marks
// it ambiguous when a later identity differs. Callers supply an initialized
// map. Equivalent identities leave the stored value unchanged, including an
// ambiguity flag already set.
//
// The first declaration remains the comparison record rather than being
// replaced by the conflicting declaration. Discovery admission then rejects
// every use of that ambiguous key, even one matching the first record. This
// prevents a discovered function name from choosing between different client
// kinds, namespace pairs, or encoded declaration shapes.
func registerExistingResponsesIdentity(known map[string]responsesDiscoveredToolIdentity, key string, identity responsesDiscoveredToolIdentity) {
	previous, exists := known[key]
	if !exists {
		known[key] = identity
		return
	}
	if !sameResponsesDiscoveredTool(previous, identity) {
		previous.ambiguous = true
		known[key] = previous
	}
}

// admitResponsesDiscoveredTool admits a new identity at key, skips an equal
// unambiguous identity, and rejects conflicting or already-ambiguous keys.
// Callers provide an initialized known map; true reports a newly stored
// identity that should become a promoted declaration.
//
// The lookup key is a direct name or a derived flat namespace name, while
// equality also checks kind, original child name, namespace, and encoded
// shape. Keeping those dimensions separate rejects joined-name collisions that
// could otherwise restore a response call to the wrong client tool. The map
// can retain earlier admissions when a later candidate returns an error.
func admitResponsesDiscoveredTool(known map[string]responsesDiscoveredToolIdentity, key string, identity responsesDiscoveredToolIdentity) (bool, error) {
	previous, exists := known[key]
	if !exists {
		known[key] = identity
		return true, nil
	}
	if !previous.ambiguous && sameResponsesDiscoveredTool(previous, identity) {
		return false, nil
	}
	return false, fmt.Errorf("discovered tool %q conflicts with an existing declaration; this upstream cannot safely disambiguate different names, namespaces, or schemas", key)
}

// sameResponsesDiscoveredTool compares kind, name, namespace, and encoded
// declaration bytes for exact equality. The ambiguity flag is excluded because
// it records conflicts around that identity; admission checks the stored flag
// separately.
func sameResponsesDiscoveredTool(left, right responsesDiscoveredToolIdentity) bool {
	return left.typ == right.typ && left.name == right.name && left.namespace == right.namespace && left.encoded == right.encoded
}

// responsesDirectToolDiscovery shallow-copies a named declaration, trims its
// name, and overwrites type with typ. Blank names or encoding failure return
// no copy/identity. Other nested values remain shared, and the caller selects
// the supported kind.
//
// Custom declarations are compared using their function wrapper schema after
// format removal, while the returned copy keeps the client-facing declaration
// fields. The identity also retains the custom kind, preventing equivalence
// with an ordinary function that has the same lowered shape. Encoded bytes
// compare the entire normalized shape, including retained extension fields,
// rather than only its parameter schema.
func responsesDirectToolDiscovery(tool map[string]any, typ string) (map[string]any, responsesDiscoveredToolIdentity, bool) {
	name := strings.TrimSpace(stringValue(tool["name"]))
	if name == "" {
		return nil, responsesDiscoveredToolIdentity{}, false
	}
	copy := copyClientTool(tool)
	copy["type"] = typ
	copy["name"] = name
	identityCopy := copy
	if typ == "custom" {
		// Function-only execution represents freeform input with one wrapper field.
		// Compare that lowered schema while keeping the custom client kind distinct.
		identityCopy = copyClientTool(copy)
		identityCopy["type"] = "function"
		identityCopy["parameters"] = json.RawMessage(customToolInputSchema)
		delete(identityCopy, "format")
	}
	// Encoding failure supplies no comparable declaration bytes. Decline this
	// candidate and let promotion keep other independently valid discoveries.
	encoded, err := json.Marshal(identityCopy)
	if err != nil {
		return nil, responsesDiscoveredToolIdentity{}, false
	}
	return copy, responsesDiscoveredToolIdentity{typ: typ, name: name, encoded: string(encoded)}, true
}

// restoreInheritedResponsesClientToolDeclarations rebuilds client declaration
// identities from an earlier lowered slice and mapping. Search-name
// restoration wins before custom and namespace lookup; unmapped map entries
// are shallow-copied, while nonmap entries remain shared. The returned slice
// is new.
//
// Custom entries regain their type, and flat namespace entries become
// one-child groups using the recorded unqualified name. A restored search
// declaration uses only tool_search type. These declarations let the next
// adaptation pass reconstruct effective upstream tools when a turn omitted
// them; nested schemas remain shared and the helper cannot recover custom
// grammar removed during lowering.
func restoreInheritedResponsesClientToolDeclarations(lowered []any, mapping ResponsesClientToolMapping) []any {
	restored := make([]any, 0, len(lowered))
	for _, raw := range lowered {
		tool, ok := raw.(map[string]any)
		if !ok {
			restored = append(restored, raw)
			continue
		}
		name := strings.TrimSpace(stringValue(tool["name"]))
		// An omitted declaration list needs client identities before fresh adaptation
		// can promote discoveries. Reconstruct those identities from the earlier mapping;
		// removed custom grammar and search extensions cannot be recovered here.
		switch {
		case mapping.ToolSearch && name == toolSearchProxyName:
			restored = append(restored, map[string]any{"type": "tool_search"})
		case mapping.CustomTools[name]:
			copy := copyClientTool(tool)
			copy["type"] = "custom"
			restored = append(restored, copy)
		case mapping.NamespaceTools[name].Namespace != "":
			identity := mapping.NamespaceTools[name]
			child := copyClientTool(tool)
			child["type"] = "function"
			child["name"] = identity.Name
			restored = append(restored, map[string]any{
				"type": "namespace", "name": identity.Namespace, "tools": []any{child},
			})
		default:
			restored = append(restored, copyClientTool(tool))
		}
	}
	return restored
}

// responsesNamespaceToolDiscovery collects named function children from a
// decoded namespace declaration. A blank namespace or no eligible children
// returns nil values with false. It prefers namespaceChildren's nonempty tools
// array and otherwise its children alias, while unsupported or unencodable
// children are skipped.
//
// The returned parent and child maps are shallow copies. Each candidate keeps
// an unqualified child identity plus its separately derived bounded flat name,
// allowing later admission to detect collisions before replacing the parent's
// child list. Parent extensions and nested schema values remain shared;
// callers choose which admitted children appear in the promoted namespace.
func responsesNamespaceToolDiscovery(tool map[string]any) (map[string]any, []responsesNamespaceToolCandidate, bool) {
	namespace := strings.TrimSpace(stringValue(tool["name"]))
	children := namespaceChildren(tool)
	if namespace == "" || len(children) == 0 {
		return nil, nil, false
	}
	copy := copyClientTool(tool)
	copy["type"] = "namespace"
	copy["name"] = namespace
	identities := make([]responsesNamespaceToolCandidate, 0, len(children))
	for _, rawChild := range children {
		child, ok := rawChild.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(child["type"])) != "function" {
			continue
		}
		childCopy, direct, ok := responsesDirectToolDiscovery(child, "function")
		if !ok {
			continue
		}
		flat := flattenNamespaceToolName(namespace, direct.name)
		identities = append(identities, responsesNamespaceToolCandidate{
			flat:  flat,
			child: childCopy,
			identity: responsesDiscoveredToolIdentity{
				typ: "namespace", name: direct.name, namespace: namespace, encoded: direct.encoded,
			},
		})
	}
	if len(identities) == 0 {
		return nil, nil, false
	}
	return copy, identities, true
}
