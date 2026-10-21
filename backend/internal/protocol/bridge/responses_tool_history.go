package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// responses_tool_history.go assigns function-only names to historical calls
// whose declarations may have disappeared from the current request. Known
// declaration and inherited aliases retain their spelling. A remaining
// namespace/name pair receives a request-local name, with occupied executable
// names and recognized custom aliases reserved before allocation so replay
// cannot impersonate another tool.
// Call IDs, arguments, and results remain the responsibility of the input
// converter and its existing pairing rules.
//
// Historical names describe previous execution; they do not make a tool
// callable again. The resulting lookup is separate from declaration mappings,
// selectors, response restoration, and future WebSocket tool state. Allocation
// sorts unresolved identities and hashes their individual components when
// joined names collide. Input order therefore cannot choose which identity
// receives a spelling, and shared inherited maps remain unchanged.

// responsesToolHistoryNames maps original historical identities to request-local
// function names independently of the current callable declarations.
type responsesToolHistoryNames map[NamespacedToolName]string

// newResponsesToolHistoryNames prepares names for direct function_call input
// items using known declaration aliases before allocating missing identities.
// Nonarray input contributes no calls; the request converter validates its
// shape. Empty qualified names remain unresolved for validation at consumption.
//
// Known executable names, custom aliases recognized by response restoration,
// and bare historical names are reserved before allocation. Unknown pairs are
// sorted, so separator collisions and shortened names resolve consistently
// when input order changes. Hash candidates encode
// the namespace and name separately and are checked against occupied names.
// Returned maps belong to this conversion and never mutate the supplied
// declaration maps or authorize a tool choice or future tool declaration.
func newResponsesToolHistoryNames(input json.RawMessage, mapping ResponsesClientToolMapping) (responsesToolHistoryNames, error) {
	names := make(responsesToolHistoryNames)
	reserved := map[string]bool{toolSearchProxyName: true}
	for _, upstream := range mapping.CallNames {
		reserved[upstream] = true
	}
	for upstream := range mapping.NamespaceTools {
		reserved[upstream] = true
	}
	for upstream := range mapping.FunctionTools {
		reserved[upstream] = true
	}
	for upstream := range mapping.CustomTools {
		reserved[upstream] = true
	}

	parsed := gjson.ParseBytes(input)
	if !parsed.IsArray() {
		return names, nil
	}
	unresolved := make(map[NamespacedToolName]bool)
	for _, item := range parsed.Array() {
		if item.Get("type").String() == "custom_tool_call" {
			reserved[item.Get("name").String()] = true
		}
		if item.Get("type").String() != "function_call" {
			continue
		}
		identity := NamespacedToolName{
			Namespace: item.Get("namespace").String(),
			Name:      item.Get("name").String(),
		}
		// A missing declaration permits replay-name allocation for a valid child
		// name; it does not add evidence to the selector or response mappings.
		if upstream, err := mapping.resolveCallName(identity.Namespace, identity.Name); err == nil {
			names[identity] = upstream
			reserved[upstream] = true
		} else if strings.TrimSpace(identity.Name) != "" {
			unresolved[identity] = true
		}
	}
	if len(unresolved) == 0 {
		return names, nil
	}
	// Chat readers recognize namespace-prefixed spellings of custom tools.
	// Reserve their alias set before synthesizing missing identities, so replay
	// names cannot acquire a current custom identity in a response. Known-only
	// history skips this additional cross-product of names and namespaces.
	for customName := range mapping.CustomTools {
		for _, identity := range mapping.NamespaceTools {
			reserved[customToolAlias(identity.Namespace, customName)] = true
		}
	}

	identities := make([]NamespacedToolName, 0, len(unresolved))
	for identity := range unresolved {
		identities = append(identities, identity)
	}
	slices.SortFunc(identities, func(a, b NamespacedToolName) int {
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, identity := range identities {
		// Function-only targets restrict executable labels to ASCII identifier
		// characters. Keep the original pair as the lookup key while sanitizing
		// its derived spelling and checking replacements against occupied names.
		base := strings.Map(func(ch rune) rune {
			if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' {
				return ch
			}
			return '_'
		}, flattenNamespaceToolName(identity.Namespace, identity.Name))
		candidate := base
		// Reserve room for a bounded hash suffix when a joined label belongs to
		// another tool. Quoted components distinguish pairs such as a/b__c and
		// a__b/c, and bounded retries also check occupied hash candidates.
		attemptLimit := len(reserved) + 1
		for attempt := 0; reserved[candidate] && attempt < attemptLimit; attempt++ {
			digest := sha256.Sum256(fmt.Appendf(nil, "%q/%q/%d", identity.Namespace, identity.Name, attempt))
			suffix := "__" + hex.EncodeToString(digest[:8])
			candidate = base[:min(len(base), chatToolNameMaxLen-len(suffix))] + suffix
		}
		if reserved[candidate] {
			return nil, fmt.Errorf("cannot allocate a distinct historical tool name for %q/%q", identity.Namespace, identity.Name)
		}
		names[identity] = candidate
		reserved[candidate] = true
	}
	return names, nil
}

// resolve returns the prepared name for a direct historical function call.
// Bare and default-scoped calls also accept already-lowered spellings. Unknown
// qualified identities indicate an invalid name or an unprepared input item.
func (names responsesToolHistoryNames) resolve(namespace, name string) (string, error) {
	if upstream, ok := names[NamespacedToolName{Namespace: namespace, Name: name}]; ok {
		return upstream, nil
	}
	if namespace == "" || namespace == responsesDefaultToolNamespace {
		return name, nil
	}
	return "", fmt.Errorf("cannot convert historical tool call %q in namespace %q without a prepared identity", name, namespace)
}
