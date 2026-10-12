package grok

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	wireopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// CountGrokNativeSearchCallsFromJSONBytes counts search calls in Responses output:
// web_search_call, x_search_call, tool_search_call, and matching function/custom calls.
func CountGrokNativeSearchCallsFromJSONBytes(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	// Prefer nested output when a compatibility layer also copies it to the root.
	if nested := gjson.GetBytes(body, "response.output"); nested.IsArray() {
		return CountGrokNativeSearchCallsInOutputArray(nested)
	}
	return CountGrokNativeSearchCallsInOutputArray(gjson.GetBytes(body, "output"))
}

func CountGrokNativeSearchCallsFromSSEBody(body string) int {
	if strings.TrimSpace(body) == "" {
		return 0
	}
	seen := make(map[string]struct{})
	total := 0
	wireopenai.ForEachSSEDataPayload(body, func(data []byte) {
		total += CountGrokNativeSearchCallsInSSEDataDedup(data, seen)
	})
	return total
}

// CountGrokNativeSearchCallsInSSEData counts one payload without cross-event deduplication.
// Streaming callers use the Dedup variant to avoid charging item.done and response.completed twice.
func CountGrokNativeSearchCallsInSSEData(data []byte) int {
	n, _ := CountGrokNativeSearchCallsInSSEDataWithKeys(data)
	return n
}

// CountGrokNativeSearchCallsInSSEDataDedup counts unseen calls. Reuse a non-nil seen map for the
// whole stream.
// Missing call_id/id values use type, name, and ordinal keys; nil seen disables deduplication.
func CountGrokNativeSearchCallsInSSEDataDedup(data []byte, seen map[string]struct{}) int {
	if seen == nil {
		return CountGrokNativeSearchCallsInSSEData(data)
	}
	n, keys := CountGrokNativeSearchCallsInSSEDataWithKeys(data)
	if n <= 0 {
		return 0
	}
	// Every counted item needs an ID or synthetic key before it can be accumulated.
	if len(keys) < n {
		// Rebuild all keys so items lacking IDs also have a fingerprint.
		keys = CollectGrokNativeSearchCallKeys(data)
	}
	if len(keys) == 0 {
		// Omit the surcharge when calls cannot be identified safely.
		return 0
	}
	added := 0
	local := make(map[string]struct{}, len(keys))
	isItemDone := strings.TrimSpace(gjson.GetBytes(data, "type").String()) == "response.output_item.done"
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := local[k]; ok {
			continue
		}
		local[k] = struct{}{}
		if _, ok := seen[k]; ok {
			if !isItemDone || !strings.HasPrefix(k, "synth:") {
				continue
			}
			// Treat each anonymous item.done as a completed call, including interrupted streams.
			separator := strings.LastIndexByte(k, ':')
			if separator < 0 {
				continue
			}
			base := k[:separator]
			for ordinal := 2; ; ordinal++ {
				candidate := base + ":" + strconv.Itoa(ordinal)
				if _, exists := seen[candidate]; !exists {
					k = candidate
					break
				}
			}
		}
		seen[k] = struct{}{}
		added++
	}
	return added
}

func CollectGrokNativeSearchCallKeys(data []byte) []string {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil
	}
	// An empty type denotes a bare item; other non-completion events are not chargeable.
	switch strings.TrimSpace(gjson.GetBytes(data, "type").String()) {
	case "response.output_item.done", "response.completed", "response.done", "":
	default:
		return nil
	}
	var keys []string
	syntheticOrdinals := make(map[string]int)
	consider := func(item gjson.Result) {
		if !IsGrokNativeSearchOutputItem(item) {
			return
		}
		key := firstNonEmpty(
			strings.TrimSpace(item.Get("call_id").String()),
			strings.TrimSpace(item.Get("id").String()),
			strings.TrimSpace(item.Get("item.call_id").String()),
			strings.TrimSpace(item.Get("item.id").String()),
		)
		if key == "" {
			// Ordinals distinguish multiple anonymous calls of the same type and name.
			base := "synth:" + strings.ToLower(strings.TrimSpace(item.Get("type").String())) +
				":" + strings.ToLower(strings.TrimSpace(item.Get("name").String()))
			syntheticOrdinals[base]++
			key = base + ":" + strconv.Itoa(syntheticOrdinals[base])
		}
		keys = append(keys, key)
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() {
		consider(item)
	}
	gjson.GetBytes(data, "response.output").ForEach(func(_, item gjson.Result) bool {
		consider(item)
		return true
	})
	gjson.GetBytes(data, "output").ForEach(func(_, item gjson.Result) bool {
		consider(item)
		return true
	})
	if len(keys) == 0 && IsGrokNativeSearchOutputItem(gjson.ParseBytes(data)) {
		consider(gjson.ParseBytes(data))
	}
	return keys
}

func CountGrokNativeSearchCallsInSSEDataWithKeys(data []byte) (int, []string) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return 0, nil
	}
	// Count item or response completion events; deltas do not add calls.
	switch strings.TrimSpace(gjson.GetBytes(data, "type").String()) {
	case "response.output_item.done", "response.completed", "response.done", "":
	default:
		return 0, nil
	}
	var keys []string
	n := 0
	consider := func(item gjson.Result) {
		if !IsGrokNativeSearchOutputItem(item) {
			return
		}
		n++
		key := firstNonEmpty(
			strings.TrimSpace(item.Get("call_id").String()),
			strings.TrimSpace(item.Get("id").String()),
			strings.TrimSpace(item.Get("item.call_id").String()),
			strings.TrimSpace(item.Get("item.id").String()),
		)
		if key != "" {
			keys = append(keys, key)
		}
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() {
		consider(item)
	}
	gjson.GetBytes(data, "response.output").ForEach(func(_, item gjson.Result) bool {
		consider(item)
		return true
	})
	gjson.GetBytes(data, "output").ForEach(func(_, item gjson.Result) bool {
		consider(item)
		return true
	})
	// Accept bare output items without a nested item field.
	if n == 0 && IsGrokNativeSearchOutputItem(gjson.ParseBytes(data)) {
		consider(gjson.ParseBytes(data))
	}
	return n, keys
}

func CountGrokNativeSearchCallsInOutputArray(output gjson.Result) int {
	if !output.IsArray() {
		return 0
	}
	count := 0
	output.ForEach(func(_, item gjson.Result) bool {
		if IsGrokNativeSearchOutputItem(item) {
			count++
		}
		return true
	})
	return count
}

func IsGrokNativeSearchOutputItem(item gjson.Result) bool {
	if !item.Exists() {
		return false
	}
	itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	switch itemType {
	case "web_search_call", "x_search_call", "tool_search_call":
		return true
	case "function_call", "custom_tool_call":
		name := strings.ToLower(strings.TrimSpace(item.Get("name").String()))
		return name == "web_search" || name == "x_search" || name == "tool_search"
	default:
		return false
	}
}
