package anthropic

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CLI-mimicked tools need the same alias in their declarations, named choices,
// and historical calls. Request-local mappings pair outbound aliases with
// response replacements, while server-defined tool types keep their declared
// names during preparation.
//
// Ordered tool names seed dynamic alias selection above the configured
// threshold; smaller lists can still use static session prefixes. Reverse
// replacements are ordered by alias length so overlapping names are restored in
// a usable sequence. Body edits also remove cache markers from literally
// deferred tools and select the last eligible tool for a cache breakpoint.
// Response restoration replaces matching bytes throughout the payload,
// including text and identifiers, so its mapping scope is broader than the JSON
// name paths used on requests.

// DynamicToolMapThreshold is the greatest eligible tool count that skips
// dynamic alias selection, while matching static session prefixes can still be
// rewritten.
const DynamicToolMapThreshold = 5

var (
	// StaticToolNameRewrites pairs client session prefixes with CLI aliases for
	// outbound selection and global response-byte restoration after
	// request-specific aliases.
	StaticToolNameRewrites = map[string]string{"sessions_": "cc_sess_", "session_": "cc_ses_"}

	// FakeToolNamePrefixes supplies the dynamic alias prefix pool whose copied
	// values are deterministically shuffled from the ordered tool-name hash
	// during request preparation.
	FakeToolNamePrefixes = []string{
		"analyze_", "compute_", "fetch_", "generate_", "lookup_", "modify_",
		"process_", "query_", "render_", "resolve_", "sync_", "update_",
		"validate_", "convert_", "extract_", "manage_", "monitor_", "parse_",
		"review_", "search_", "transform_", "handle_", "invoke_", "notify_",
	}
)

// ToolNameRewrite holds request-local aliases used across outbound tool paths
// and response restoration. Callers retain the mapping paired with the prepared
// request until its response is processed.
type ToolNameRewrite struct {
	// Forward maps client names to outbound aliases, while Reverse maps
	// selected aliases back to client names. BuildToolNameRewriteFromBody
	// creates both for one request, and its response uses the same mapping.
	Forward map[string]string // original tool names to aliases
	Reverse map[string]string // tool aliases to original names
	// ReverseOrdered holds alias/original pairs in descending alias byte length
	// when built by BuildToolNameRewriteFromBody. Restoration consumes that
	// order so a shorter match cannot replace part of a longer alias first.
	ReverseOrdered [][2]string
}

// BuildDynamicToolMap returns dynamic aliases when toolNames exceeds the
// configured threshold; smaller lists return nil. The supplied order determines
// the hash, shuffled prefixes, and indexed names, so identical ordered input
// yields the same mapping within this implementation.
//
// Zero-byte separators mark ordered name boundaries while hashing. Each alias
// uses a selected prefix, up to three leading name bytes, and a decimal
// position with at least two digits. Repeated client names overwrite earlier
// map entries. Callers keep the same ordered names when preparing related
// history and cache inputs.
func BuildDynamicToolMap(toolNames []string) map[string]string {
	// Small catalogs skip dynamic identity assignment and keep static prefix
	// selection for matching names, reducing the set of request-local reverse
	// aliases needed for their responses.
	if len(toolNames) <= DynamicToolMapThreshold {
		return nil
	}
	h := fnv.New64a()
	// Separators distinguish adjacent name lists such as ab,c and a,bc during
	// hashing. That ordered hash chooses the deterministic prefix shuffle used
	// by related prompt preparation.
	for i, n := range toolNames {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(n))
	}
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	available := make([]string, len(FakeToolNamePrefixes))
	copy(available, FakeToolNamePrefixes)
	rng.Shuffle(len(available), func(i, j int) { available[i], available[j] = available[j], available[i] })

	mapping := make(map[string]string, len(toolNames))
	for i, name := range toolNames {
		prefix := available[i%len(available)]
		// Alias heads keep up to three bytes of the supplied name.
		headLen := min(len(name), 3)
		fake := fmt.Sprintf("%s%s%02d", prefix, name[:headLen], i)
		mapping[name] = fake
	}
	return mapping
}

// SanitizeToolName selects a dynamic alias when name has a supplied entry.
// Otherwise it applies a matching static session prefix or returns name.
// Dynamic mappings take precedence even when the original name also matches a
// static prefix.
func SanitizeToolName(name string, dynamic map[string]string) string {
	if dynamic != nil {
		if fake, ok := dynamic[name]; ok {
			return fake
		}
	}
	// Names absent from the dynamic map can still use CLI session aliases.
	// Consult static prefixes before returning an unchanged name so both
	// supported alias paths remain available.
	for prefix, replacement := range StaticToolNameRewrites {
		if strings.HasPrefix(name, prefix) {
			return replacement + name[len(prefix):]
		}
	}
	return name
}

// ShouldMimicToolName reports whether an empty, function, or custom tool type
// permits aliasing. Other tool types retain their declaration names in request
// preparation, including server-defined tool forms.
func ShouldMimicToolName(toolType string) bool {
	if toolType == "" || toolType == "function" || toolType == "custom" {
		return true
	}
	return false
}

// BuildToolNameRewriteFromBody builds request-local forward and reverse aliases
// from named eligible tools. Missing tools arrays or unchanged names return
// nil. The selected name order is passed to dynamic mapping, while
// server-defined tool types are excluded from that list.
//
// Forward entries support declaration, tool_choice, and history edits. Reverse
// pairs are sorted by descending alias byte length so response restoration can
// replace a longer overlapping alias before its shorter prefix. Callers retain
// the resulting mapping for the response associated with this prepared request;
// the method constructs state and leaves body edits to its consumer.
func BuildToolNameRewriteFromBody(body []byte) *ToolNameRewrite {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}

	mimicableNames := make([]string, 0)
	toolsArr := tools.Array()
	for _, t := range toolsArr {
		// Exclude server-defined tool forms from the alias map so their
		// declarations stay under the names supplied in the request while
		// eligible user tools can change.
		if !ShouldMimicToolName(t.Get("type").String()) {
			continue
		}
		name := t.Get("name").String()
		if name == "" {
			continue
		}
		mimicableNames = append(mimicableNames, name)
	}

	dynamic := BuildDynamicToolMap(mimicableNames)

	rw := &ToolNameRewrite{
		Forward: make(map[string]string),
		Reverse: make(map[string]string),
	}
	for _, name := range mimicableNames {
		fake := SanitizeToolName(name, dynamic)
		if fake == name {
			continue
		}
		rw.Forward[name] = fake
		rw.Reverse[fake] = name
	}
	if len(rw.Forward) == 0 {
		return nil
	}

	rw.ReverseOrdered = make([][2]string, 0, len(rw.Reverse))
	for fake, real := range rw.Reverse {
		rw.ReverseOrdered = append(rw.ReverseOrdered, [2]string{fake, real})
	}
	// A shorter alias can be a prefix inside another alias. Restore the longer
	// matching bytes first so the shorter replacement cannot corrupt that later
	// reverse match.
	sort.SliceStable(rw.ReverseOrdered, func(i, j int) bool {
		return len(rw.ReverseOrdered[i][0]) > len(rw.ReverseOrdered[j][0])
	})

	return rw
}

// ApplyToolNameRewriteToBody applies request-local aliases to eligible
// declarations, named tool_choice, and historical tool_use names. A nil or
// empty mapping still applies the last eligible tool cache breakpoint. JSON
// update failures retain the most recently prepared body and later edits
// continue.
//
// Declaration and history names need the same mapping so a prior call remains
// recognizable after outbound aliases are selected. Tool-result IDs and other
// content paths retain their own fields. The final cache pass removes deferred
// markers and fills the selected eligible breakpoint. Callers retain rw for
// response-byte restoration, whose matching scope includes all occurrences of
// its aliases.
func ApplyToolNameRewriteToBody(body []byte, rw *ToolNameRewrite) []byte {
	// Cache-breakpoint eligibility is independent of tool aliasing. Even a
	// request with no changed names runs cache preparation so literal deferred
	// flags and the final eligible tool receive their rules.
	if rw == nil || len(rw.Forward) == 0 {
		body = ApplyToolsLastCacheBreakpoint(body)
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		idx := -1
		tools.ForEach(func(_, t gjson.Result) bool {
			idx++
			// Request declarations reuse the mapper's tool-type selection.
			// Leave excluded server-defined names intact while path edits apply
			// the request-local aliases to eligible declarations.
			if !ShouldMimicToolName(t.Get("type").String()) {
				return true
			}
			name := t.Get("name").String()
			if name == "" {
				return true
			}
			fake, ok := rw.Forward[name]
			if !ok {
				return true
			}
			// Declaration edits are best effort for each mapped name. Keep the
			// current body after a failed path update so other declaration and
			// history edits can still proceed.
			if next, err := sjson.SetBytes(body, fmt.Sprintf("tools.%d.name", idx), fake); err == nil {
				body = next
			}
			return true
		})
	}

	// A named tool_choice refers to the selected declaration, so use its alias
	// as well. Failed editing retains the current body while history
	// preparation continues.
	if tc := gjson.GetBytes(body, "tool_choice"); tc.Exists() && tc.Get("type").String() == "tool" {
		name := tc.Get("name").String()
		if fake, ok := rw.Forward[name]; ok {
			if next, err := sjson.SetBytes(body, "tool_choice.name", fake); err == nil {
				body = next
			}
		}
	}

	// Historical tool_use names refer to the same declarations sent upstream.
	// Apply the request-local aliases at those paths while leaving tool-result
	// IDs and neighboring content fields available.
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgKey, msg gjson.Result) bool {
			msgIdx := int(msgKey.Num)
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(blkKey, blk gjson.Result) bool {
				blkIdx := int(blkKey.Num)
				if blk.Get("type").String() != "tool_use" {
					return true
				}
				name := blk.Get("name").String()
				if name == "" {
					return true
				}
				if fake, ok := rw.Forward[name]; ok {
					path := fmt.Sprintf("messages.%d.content.%d.name", msgIdx, blkIdx)
					// A failed historical name edit retains the body selected
					// by earlier edits. Continue other history paths and cache
					// preparation using those available bytes.
					if next, err := sjson.SetBytes(body, path, fake); err == nil {
						body = next
					}
				}
				return true
			})
			return true
		})
	}

	body = ApplyToolsLastCacheBreakpoint(body)
	return body
}

// ApplyToolsLastCacheBreakpoint removes deferred-tool cache markers and returns
// a body with a breakpoint on the last non-deferred tool when one exists.
// Missing tool arrays, empty arrays, or wholly deferred lists return the body
// available after cleanup.
//
// A nonempty client TTL takes precedence. An existing cache_control value
// without TTL receives a ttl path update using DefaultCacheControlTTL; an
// absent value receives the ephemeral object with that default. Existing
// values are not validated as objects before editing. Earlier eligible tools
// retain their markers, and failed JSON edits keep current bytes. Only literal
// JSON true in either defer_loading location selects deferred handling.
func ApplyToolsLastCacheBreakpoint(body []byte) []byte {
	// Deferred definitions are skipped when selecting the final ordinary
	// breakpoint. Remove their markers first so cache selection uses eligible
	// tools while earlier ordinary controls remain available.
	body = StripDeferredToolCacheControl(body)
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	arr := tools.Array()
	if len(arr) == 0 {
		return body
	}
	lastIdx := -1
	for idx, tool := range arr {
		if IsDeferredLoadingTool(tool) {
			continue
		}
		lastIdx = idx
	}
	if lastIdx == -1 {
		return body
	}
	existingCC := arr[lastIdx].Get("cache_control")

	if existingCC.Exists() && existingCC.Get("ttl").String() != "" {
		return body
	}

	// Existence selects a ttl path edit even for a nonobject cache_control
	// value; only an absent value receives the complete ephemeral object.
	// Failed edits keep the available body for request preparation.
	if existingCC.Exists() {
		if next, err := sjson.SetBytes(body, fmt.Sprintf("tools.%d.cache_control.ttl", lastIdx), DefaultCacheControlTTL); err == nil {
			body = next
		}
		return body
	}

	raw := fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, DefaultCacheControlTTL)
	if next, err := sjson.SetRawBytes(body, fmt.Sprintf("tools.%d.cache_control", lastIdx), []byte(raw)); err == nil {
		body = next
	}
	return body
}

// IsDeferredLoadingTool reports whether either supported defer_loading path
// contains literal JSON true. Strings, numbers, and other JSON values remain
// ordinary tools for cache-control cleanup and breakpoint selection.
func IsDeferredLoadingTool(tool gjson.Result) bool {
	return tool.Get("defer_loading").Type == gjson.True ||
		tool.Get("custom.defer_loading").Type == gjson.True
}

// StripDeferredToolCacheControl removes cache_control from literally deferred
// tools and returns the prepared body. Missing tool arrays or absent markers
// retain their bytes. Failed path deletions keep the current body and allow
// later tools to be processed.
func StripDeferredToolCacheControl(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	for idx, tool := range tools.Array() {
		if !IsDeferredLoadingTool(tool) || !tool.Get("cache_control").Exists() {
			continue
		}
		// Literal deferred tools are excluded from this cache-control
		// preparation. Delete their marker by path; if deletion fails, retain
		// the current body and continue checking later definitions.
		if next, err := sjson.DeleteBytes(body, fmt.Sprintf("tools.%d.cache_control", idx)); err == nil {
			body = next
		}
	}
	return body
}

// RestoreToolNamesInBytes restores dynamic aliases in their supplied reverse
// order, then reverses static session prefixes. Nil rw still applies static
// restoration. Callers provide longest-first ReverseOrdered pairs when aliases
// overlap and retain request-local mapping through response delivery.
//
// Each replacement matches all bytes, so text and identifiers can change along
// with JSON tool names. This differs from outbound path edits and allows a
// static CLI prefix in another field to be restored as well. Empty or identity
// reverse pairs are skipped, and later replacements consume the bytes from
// earlier ones.
func RestoreToolNamesInBytes(data []byte, rw *ToolNameRewrite) []byte {
	if rw != nil {
		for _, pair := range rw.ReverseOrdered {
			fake, real := pair[0], pair[1]
			if fake == "" || fake == real {
				continue
			}
			data = ReplaceAllBytes(data, fake, real)
		}
	}
	// Static restoration also runs for nil request mappings. Its global byte
	// matching can restore a CLI session prefix inside text or IDs, as well as
	// tool-name fields.
	for prefix, replacement := range StaticToolNameRewrites {
		data = ReplaceAllBytes(data, replacement, prefix)
	}
	return data
}

// ReplaceAllBytes replaces every matching occurrence of from with to. Empty
// data, equal strings, or an absent match returns the supplied slice; a
// replacement allocates bytes from the resulting string. Empty from follows
// strings.ReplaceAll insertion behavior.
func ReplaceAllBytes(data []byte, from, to string) []byte {
	if len(data) == 0 || from == to || !strings.Contains(string(data), from) {
		return data
	}
	return []byte(strings.ReplaceAll(string(data), from, to))
}
