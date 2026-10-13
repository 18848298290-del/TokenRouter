package grok

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	wireopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Search usage can appear in an item completion event and again in the final
// Responses output. Stream counting compares selected keys in a caller-owned
// seen set, with separate ordinal handling for synthetic-prefix keys. Buffered
// JSON instead selects the nested response.output array before a root output
// array, avoiding a duplicate compatibility copy.
//
// Anonymous calls need an ordinal fingerprint because no stable ID links
// their events. Each anonymous item.done advances to the next available
// ordinal of its type/name, while final arrays enumerate corresponding
// ordinal keys. This counts multiple completed anonymous items even when the
// stream ends before a final response. Completion event labels are matched
// exactly; search item types and function names are trimmed and lowercased.

// CountGrokNativeSearchCallsFromJSONBytes counts recognized search items in a
// buffered Responses output array. Empty or invalid JSON returns zero. A
// nested response.output array wins over root output, including when the
// nested array is empty.
//
// Selecting one array prevents compatibility bodies that repeat the same
// items in both places from counting both representations. A nonarray nested
// value allows the root array to be considered. The counter recognizes search
// item type/name shapes rather than requiring their status to be completed;
// callers invoke this on the appropriate buffered response.
func CountGrokNativeSearchCallsFromJSONBytes(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	// Choose one output representation when compatibility JSON contains both,
	// so a nested response array and its root copy cannot both contribute usage here.
	if nested := gjson.GetBytes(body, "response.output"); nested.IsArray() {
		return CountGrokNativeSearchCallsInOutputArray(nested)
	}
	return CountGrokNativeSearchCallsInOutputArray(gjson.GetBytes(body, "output"))
}

// CountGrokNativeSearchCallsFromSSEBody scans SSE data payloads with one
// private seen set for the body. Blank input returns zero, and completion
// payloads share the same deduplication rules used by live stream callers.
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

// CountGrokNativeSearchCallsInSSEData counts one accepted completion payload
// without cross-event deduplication. Use
// CountGrokNativeSearchCallsInSSEDataDedup with one stream-owned set when
// item and final response events can repeat the same call.
func CountGrokNativeSearchCallsInSSEData(data []byte) int {
	n, _ := CountGrokNativeSearchCallsInSSEDataWithKeys(data)
	return n
}

// CountGrokNativeSearchCallsInSSEDataDedup adds unseen keys to one stream's
// set and returns their count. Nil seen counts the payload independently.
// Reuse a nonnil set for one stream and serialize concurrent map access.
//
// Matching identifiers outside the synth: namespace deduplicate across
// events and within a payload. Missing IDs use normalized type/name ordinals;
// a final item needs the same fingerprint to match its earlier completion.
// Repeated item.done keys beginning synth: advance to the next unused
// ordinal, including when an explicit identifier supplied that text. If no
// usable keys can be constructed, the payload contributes zero.
func CountGrokNativeSearchCallsInSSEDataDedup(data []byte, seen map[string]struct{}) int {
	if seen == nil {
		return CountGrokNativeSearchCallsInSSEData(data)
	}
	n, keys := CountGrokNativeSearchCallsInSSEDataWithKeys(data)
	if n <= 0 {
		return 0
	}
	// Counts can exceed ID keys when an item lacks identifiers. Rebuild every
	// key together so anonymous calls receive ordinals in the same payload encounter order.
	if len(keys) < n {
		// Mixing only identified keys with the total would omit anonymous calls,
		// so regenerate a complete key sequence before changing the stream's seen set.
		keys = CollectGrokNativeSearchCallKeys(data)
	}
	if len(keys) == 0 {
		// A count without usable keys cannot update the seen set safely across
		// repeated completion events, so the caller receives no additional chargeable calls.
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
			// Each anonymous item.done denotes another completed item. Allocate its next
			// unused ordinal so interrupted streams retain these counts before a final array arrives.
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

// CollectGrokNativeSearchCallKeys collects identifiers for recognized search
// items in accepted completion payloads. Empty or invalid JSON and other
// event types return nil. It inspects item, nested response.output, and root
// output; missing event type passes the filter, while typed bare search items
// do not.
//
// Each item prefers call_id, id, then nested item identifiers. When absent,
// type/name plus an ordinal supplies a synthetic key; ordinals restart for
// each payload and distinguish anonymous items with the same shape inside it.
// The stream deduplication caller decides how repeated anonymous item.done
// keys relate to its existing set.
func CollectGrokNativeSearchCallKeys(data []byte) []string {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil
	}
	// Retain completion events and payloads lacking an event label for recovery;
	// other labels, including bare search-item types, are excluded by this event-level gate.
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
			// Missing IDs need positions within their type/name group. These ordinals
			// let final arrays match anonymous item completions already counted by the stream set.
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

// CountGrokNativeSearchCallsInSSEDataWithKeys returns a search-item count and
// any present identifiers for an accepted completion payload. Empty or
// invalid JSON and other event types return zero. It reads item, nested
// response.output, and root output; missing event type passes the gate but
// typed bare search items do not.
//
// Items without identifiers still increase the count but add no key.
// Consequently callers cannot infer that count and key length match;
// CountGrokNativeSearchCallsInSSEDataDedup rebuilds complete synthetic keys
// when needed. This helper counts each examined representation independently
// and performs no deduplication on its own.
func CountGrokNativeSearchCallsInSSEDataWithKeys(data []byte) (int, []string) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return 0, nil
	}
	// Only accepted completion labels or missing event labels enter this counter;
	// counting deltas as calls would add repeated observations of still-progressing search work.
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
	// When nested locations yield no matches, check the root with the same
	// predicate; this scan remains subject to the completion-label gate above.
	if n == 0 && IsGrokNativeSearchOutputItem(gjson.ParseBytes(data)) {
		consider(gjson.ParseBytes(data))
	}
	return n, keys
}

// CountGrokNativeSearchCallsInOutputArray counts recognized search items in
// the supplied array. Other shapes return zero, and matching depends on item
// type and name rather than status or uniqueness; callers select the response
// array before invoking it.
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

// IsGrokNativeSearchOutputItem recognizes search call types and named
// function/custom search calls after trimming and lowercasing their labels.
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
