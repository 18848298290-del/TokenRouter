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

// DynamicToolMapThreshold is the maximum tool count that uses static rewrites only.
const DynamicToolMapThreshold = 5

var (
	// StaticToolNameRewrites maps session-related tool prefixes to CLI aliases.
	StaticToolNameRewrites = map[string]string{"sessions_": "cc_sess_", "session_": "cc_ses_"}

	// FakeToolNamePrefixes supplies the shuffled prefixes for dynamic tool aliases.
	FakeToolNamePrefixes = []string{
		"analyze_", "compute_", "fetch_", "generate_", "lookup_", "modify_",
		"process_", "query_", "render_", "resolve_", "sync_", "update_",
		"validate_", "convert_", "extract_", "manage_", "monitor_", "parse_",
		"review_", "search_", "transform_", "handle_", "invoke_", "notify_",
	}
)

// ToolNameRewrite maps tool names within one request. Forward maps client names
// to aliases; Reverse maps aliases back. ReverseOrdered stores (alias, original)
// pairs longest-first so replacing a short alias cannot corrupt a longer one.
type ToolNameRewrite struct {
	Forward        map[string]string
	Reverse        map[string]string
	ReverseOrdered [][2]string
}

// BuildDynamicToolMap returns aliases when toolNames exceeds the threshold.
// It hashes the ordered names with FNV-1a to seed a prefix shuffle, so the same
// ordered list produces stable names for prompt caching. Smaller lists return nil.
func BuildDynamicToolMap(toolNames []string) map[string]string {
	if len(toolNames) <= DynamicToolMapThreshold {
		return nil
	}
	h := fnv.New64a()
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
		headLen := min(len(name), 3)
		fake := fmt.Sprintf("%s%s%02d", prefix, name[:headLen], i)
		mapping[name] = fake
	}
	return mapping
}

// SanitizeToolName uses a dynamic alias when present, then a static prefix
// rewrite, otherwise the supplied name.
func SanitizeToolName(name string, dynamic map[string]string) string {
	if dynamic != nil {
		if fake, ok := dynamic[name]; ok {
			return fake
		}
	}
	for prefix, replacement := range StaticToolNameRewrites {
		if strings.HasPrefix(name, prefix) {
			return replacement + name[len(prefix):]
		}
	}
	return name
}

// ShouldMimicToolName permits unnamed, function, and custom tool types.
// Server tool types such as web_search_20250305 retain protocol-defined names.
func ShouldMimicToolName(toolType string) bool {
	if toolType == "" || toolType == "function" || toolType == "custom" {
		return true
	}
	return false
}

// BuildToolNameRewriteFromBody reads eligible tool names and builds both mappings.
// It returns nil when no names change. ApplyToolNameRewriteToBody performs the edits.
func BuildToolNameRewriteFromBody(body []byte) *ToolNameRewrite {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}

	mimicableNames := make([]string, 0)
	toolsArr := tools.Array()
	for _, t := range toolsArr {
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
	sort.SliceStable(rw.ReverseOrdered, func(i, j int) bool {
		return len(rw.ReverseOrdered[i][0]) > len(rw.ReverseOrdered[j][0])
	})

	return rw
}

// ApplyToolNameRewriteToBody updates eligible tool declarations, named tool_choice,
// and historical tool_use names. It also applies the last eligible tool cache
// breakpoint, including when rw is nil. RestoreToolNamesInBytes reverses aliases
// in response data.
func ApplyToolNameRewriteToBody(body []byte, rw *ToolNameRewrite) []byte {
	if rw == nil || len(rw.Forward) == 0 {
		body = ApplyToolsLastCacheBreakpoint(body)
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		idx := -1
		tools.ForEach(func(_, t gjson.Result) bool {
			idx++
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
			if next, err := sjson.SetBytes(body, fmt.Sprintf("tools.%d.name", idx), fake); err == nil {
				body = next
			}
			return true
		})
	}

	if tc := gjson.GetBytes(body, "tool_choice"); tc.Exists() && tc.Get("type").String() == "tool" {
		name := tc.Get("name").String()
		if fake, ok := rw.Forward[name]; ok {
			if next, err := sjson.SetBytes(body, "tool_choice.name", fake); err == nil {
				body = next
			}
		}
	}

	// History must reference the same aliases as tools[] or upstream tool validation fails.
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

// ApplyToolsLastCacheBreakpoint removes cache markers from deferred tools and
// adds a breakpoint to the last non-deferred tool. A nonempty client TTL is
// retained; missing TTLs use DefaultCacheControlTTL. It returns the updated body.
func ApplyToolsLastCacheBreakpoint(body []byte) []byte {
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

func IsDeferredLoadingTool(tool gjson.Result) bool {
	return tool.Get("defer_loading").Type == gjson.True ||
		tool.Get("custom.defer_loading").Type == gjson.True
}

// StripDeferredToolCacheControl removes cache markers rejected on deferred tools.
// Only JSON true in defer_loading or custom.defer_loading marks a deferred tool.
func StripDeferredToolCacheControl(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	for idx, tool := range tools.Array() {
		if !IsDeferredLoadingTool(tool) || !tool.Get("cache_control").Exists() {
			continue
		}
		if next, err := sjson.DeleteBytes(body, fmt.Sprintf("tools.%d.cache_control", idx)); err == nil {
			body = next
		}
	}
	return body
}

// RestoreToolNamesInBytes replaces aliases longest-first, then reverses static
// prefix mappings. Replacements affect all matching bytes, including text values.
// A nil rw still applies static prefix restoration.
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
	for prefix, replacement := range StaticToolNameRewrites {
		data = ReplaceAllBytes(data, replacement, prefix)
	}
	return data
}

// ReplaceAllBytes replaces every occurrence and reuses data when no replacement is needed.
func ReplaceAllBytes(data []byte, from, to string) []byte {
	if len(data) == 0 || from == to || !strings.Contains(string(data), from) {
		return data
	}
	return []byte(strings.ReplaceAll(string(data), from, to))
}
