package ollama

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// Ollama settings pages place percentages, reset times, and model counters in
// neighboring DOM elements rather than a single JSON record. Window parsing
// starts from a recognized label and searches bounded ancestor blocks so a
// nearby reset can be paired with its percentage. Text percentages take
// precedence over CSS track values, and a percentage described as remaining is
// converted to used unless the same block also says used.
//
// Model counters retain both model name and window identity; a repeated model
// in another window therefore remains a separate observation. Segment markers
// are preferred, with direct attributes and labeled text used only when earlier
// extraction yields no accepted models. The result is sorted before the display
// cap is applied. Optional plan, balance, and reset fields can be absent, while
// sign-in text rejects the whole page before any otherwise-recognizable usage
// is accepted.

// Text and CSS patterns recognize supported percentages, balances, resets, and
// request-count rows when structured settings attributes cannot supply those
// observations.
var (
	// ollamaUsagePercentPattern matches a percentage in settings text.
	ollamaUsagePercentPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*%`)
	// ollamaUsageWidthPattern recognizes a percentage width in inline CSS.
	ollamaUsageWidthPattern = regexp.MustCompile(`(?i)(?:^|;)\s*width\s*:\s*([0-9]+(?:\.[0-9]+)?)%`)
	// ollamaBalancePattern matches an amount after balance or credits.
	ollamaBalancePattern = regexp.MustCompile(`(?i)(?:balance|credits?)(?:\s+[[:alpha:]]+){0,4}\s*[:\n]?\s*((?:USD\s*)?\$?\s*-?[0-9][0-9,]*(?:\.[0-9]{1,4})?)`)
	// ollamaResetPattern matches reset text for display and date parsing.
	ollamaResetPattern = regexp.MustCompile(`(?i)\breset(?:s|ting)?\s*(?:at|in|on)?\s*[:\-]?\s*([^\n|]+)`)
	// ollamaModelFallbackPattern matches model names followed by request
	// counts.
	ollamaModelFallbackPattern = regexp.MustCompile(`(?i)^(.+?)\s+([0-9][0-9,]*)\s+requests?$`)

	// The two alias lists identify distinct quota windows during label
	// search and model attribution; shared enclosing text must resolve to
	// one family before its model counts are assigned.
	ollamaFiveHourUsageAliases = []string{"session usage", "5 hour usage", "5-hour usage", "5h usage", "5 hour limit", "5-hour limit"}
	// ollamaSevenDayUsageAliases identifies the weekly usage label family.
	ollamaSevenDayUsageAliases = []string{"weekly usage", "7 day usage", "7-day usage", "7d usage", "weekly limit", "7 day limit"}

	// ErrUnauthorizedHTML identifies sign-in text that replaces an
	// authenticated settings observation, even when the same page includes
	// usage-like text.
	ErrUnauthorizedHTML = errors.New("settings HTML is a sign-in page")
)

// ParseOllamaCloudUsageHTML extracts recognized plan, window, balance, and
// model observations from settings markup. It rejects sign-in text before
// interpreting usage, and rejects a page when every recognized category is
// absent. A successful result can contain just one category.
//
// Cloud usage supplies the preferred plan label; Plan and Subscription are
// alternates. Balance remaining is preferred over the credits/balance text
// pattern. Window and model parsers independently associate observations with
// nearby labels, while missing optional fields remain unset for provider
// display and refresh handling.
func ParseOllamaCloudUsageHTML(body []byte) (*usageview.OllamaCloudUsageData, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse settings HTML: %w", err)
	}
	pageText := normalizedNodeText(doc)
	lowerPage := strings.ToLower(pageText)
	if containsAny(lowerPage, "sign in to ollama", "log in to ollama", "continue to sign in") {
		return nil, ErrUnauthorizedHTML
	}

	data := &usageview.OllamaCloudUsageData{}
	// Plan and Balance label searches use an eighty-byte text bound, limiting
	// those searches to short sibling or ancestor values in settings rows.
	data.Plan = valueBesideLabel(doc, []string{"cloud usage"}, 80)
	if data.Plan == "" {
		// Try alternate plan labels when Cloud usage yields no bounded
		// value, allowing fragments with only a Plan or Subscription
		// row to be displayed.
		data.Plan = valueBesideLabel(doc, []string{"plan", "subscription"}, 80)
	}
	data.FiveHour = parseOllamaUsageWindow(doc, ollamaFiveHourUsageAliases)
	data.SevenDay = parseOllamaUsageWindow(doc, ollamaSevenDayUsageAliases)
	data.Balance = valueBesideLabel(doc, []string{"balance remaining"}, 80)
	if data.Balance == "" {
		// When the dedicated balance row is absent, retain a money-like
		// value from balance or credits text so that optional wallet
		// information can still be shown.
		if match := ollamaBalancePattern.FindStringSubmatch(pageText); len(match) == 2 {
			data.Balance = strings.Join(strings.Fields(match[1]), "")
		}
	}
	data.Models = parseOllamaModels(doc)

	if data.Plan == "" && data.FiveHour == nil && data.SevenDay == nil && data.Balance == "" && len(data.Models) == 0 {
		return nil, fmt.Errorf("settings HTML does not contain recognizable usage fields")
	}
	return data, nil
}

// parseOllamaUsageWindow associates one recognized usage label with a
// percentage and optional reset metadata. It searches at most six ancestor
// blocks and stops when their normalized text exceeds six hundred bytes,
// returning nil if none yields a usable percentage.
//
// The first text percentage is preferred over CSS track extraction. Remaining
// text becomes used percentage when the block lacks used wording. The first
// percentage candidate stays available while ancestor search looks for reset
// metadata; a candidate with a parsed reset or reset text returns immediately.
// This lets a sibling reset augment the nearby usage row.
func parseOllamaUsageWindow(root *html.Node, aliases []string) *usageview.OllamaCloudUsageWindow {
	label := findLabelElement(root, aliases)
	if label == nil {
		return nil
	}
	var candidate *usageview.OllamaCloudUsageWindow
	block := label
	// Bound ancestor search so one window label cannot grow into an
	// unrestricted page-wide percentage scan while looking for nearby reset
	// metadata.
	for depth := 0; block != nil && depth < 6; depth, block = depth+1, block.Parent {
		text := normalizedNodeText(block)
		// Stop at broad text blocks to limit unrelated label content.
		if len(text) > 600 {
			break
		}
		percent, ok := ollamaUsagePercentFromText(text)
		if !ok {
			// Use marked CSS track widths when the current block
			// has no valid text percentage, retaining a usable
			// window from graphical usage markup.
			percent, ok = ollamaUsagePercentFromTrack(block)
		}
		if !ok {
			continue
		}
		if strings.Contains(strings.ToLower(text), "remaining") && !strings.Contains(strings.ToLower(text), "used") {
			// Convert remaining percentage to the used unit
			// expected by display.
			percent = 100 - percent
		}
		window := &usageview.OllamaCloudUsageWindow{UsedPercent: percent}
		window.ResetAt = timeElementValue(block)
		if reset := ollamaResetPattern.FindStringSubmatch(text); len(reset) == 2 {
			window.ResetText = strings.TrimSpace(reset[1])
			if window.ResetAt == nil {
				// A reset element is preferred; parse retained
				// reset wording when no element yields a
				// timestamp, allowing supported textual dates
				// to augment the window.
				window.ResetAt = parseOllamaResetTime(window.ResetText)
			}
		}
		if candidate == nil {
			candidate = window
		}
		if window.ResetAt != nil || window.ResetText != "" {
			return window
		}
	}
	return candidate
}

// valueBesideLabel reads a bounded value next to a recognized lowercase label.
// A usable next element sibling takes precedence; otherwise at most four
// ancestor blocks are considered after removing one supplied alias.
//
// maxLen bounds normalized text in bytes, and Manage text is excluded from
// returned values. An absent label or wholly unusable neighborhood returns an
// empty string, allowing plan and balance callers to try their alternate
// sources. Callers supply aliases in lowercase because matching lowercases node
// text before comparison.
func valueBesideLabel(root *html.Node, aliases []string, maxLen int) string {
	label := findLabelElement(root, aliases)
	if label == nil {
		return ""
	}
	if sibling := nextElementSibling(label); sibling != nil {
		value := strings.Trim(normalizedNodeText(sibling), ":-| ")
		if value != "" && len(value) <= maxLen && !strings.EqualFold(value, "manage") {
			return value
		}
	}
	// Limit label-value ancestor search to four blocks to keep plan and
	// balance extraction near the selected row rather than scanning its
	// entire page.
	for depth, block := 0, label.Parent; block != nil && depth < 4; depth, block = depth+1, block.Parent {
		text := normalizedNodeText(block)
		if text == "" || len(text) > maxLen {
			continue
		}
		value := text
		for _, alias := range aliases {
			if index := strings.Index(strings.ToLower(value), alias); index >= 0 {
				value = strings.TrimSpace(value[:index] + " " + value[index+len(alias):])
				break
			}
		}
		value = strings.Trim(value, ":-| ")
		if value != "" && !strings.EqualFold(value, "manage") {
			return value
		}
	}
	return ""
}

// findLabelElement selects the shortest normalized container text matching one
// of the supplied lowercase aliases. Matching accepts the alias exactly, or as
// a prefix followed by a space or colon, within the parser's recognized
// container tags.
//
// Depth-first traversal determines ties because equal-length candidates leave
// the earlier node selected. Choosing the smallest matching text gives value
// and window readers a local label from which they can inspect siblings or
// ancestors, instead of starting at a broader enclosing section.
func findLabelElement(root *html.Node, aliases []string) *html.Node {
	var best *html.Node
	bestLen := int(^uint(0) >> 1)
	walkHTML(root, func(node *html.Node) {
		if node.Type != html.ElementNode || !isOllamaParserContainer(node.Data) {
			return
		}
		text := strings.ToLower(normalizedNodeText(node))
		if text == "" {
			return
		}
		for _, alias := range aliases {
			if (text == alias || strings.HasPrefix(text, alias+" ") || strings.HasPrefix(text, alias+":")) && len(text) < bestLen {
				best, bestLen = node, len(text)
			}
		}
	})
	return best
}

// parseOllamaModels collects request counts with their model and usage-window
// identity. It first reads segment subtrees, then direct model/count attributes
// when no accepted segment model exists, then list or code text near a Models
// label when attribute extraction also yields nothing.
//
// The first accepted model/window pair wins, including when a later duplicate
// reports another count. Blank or overlong models, invalid counts, and entries
// without window identity are skipped. Results sort by window and model before
// being capped at one hundred, giving display consumers deterministic order and
// a bounded list.
func parseOllamaModels(root *html.Node) []usageview.OllamaCloudUsageModel {
	models := make([]usageview.OllamaCloudUsageModel, 0)
	seen := make(map[string]struct{})
	appendModel := func(node *html.Node, modelValue, requestsValue string) {
		model := strings.TrimSpace(modelValue)
		requests, ok := parseOllamaRequestCount(requestsValue)
		window := ollamaModelWindow(node)
		// A model counter needs a usable name, count, and window before
		// display. Model names also have a 128-byte display bound. Skip
		// incomplete associations so a request count cannot enter an
		// unidentified usage period.
		if !ok || model == "" || len(model) > 128 || window == "" {
			return
		}
		// Pair model identity with its usage window before
		// deduplication so one model's weekly requests cannot replace
		// or suppress its five-hour count.
		key := model + "\x00" + string(window)
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		models = append(models, usageview.OllamaCloudUsageModel{Model: model, Window: window, Requests: requests})
	}

	walkHTML(root, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}
		if _, segment := htmlAttribute(node, "data-usage-segment"); !segment {
			return
		}
		model, modelOK := htmlAttributeInSubtree(node, "data-model")
		requests, requestsOK := htmlAttributeInSubtree(node, "data-requests")
		if modelOK && requestsOK {
			appendModel(node, model, requests)
		}
	})

	// When segment extraction supplies no accepted model, try direct
	// attributes. This also covers pages whose segment entries were present
	// but unusable.
	if len(models) == 0 {
		walkHTML(root, func(node *html.Node) {
			if node.Type != html.ElementNode {
				return
			}
			model, modelOK := htmlAttribute(node, "data-model")
			requests, requestsOK := htmlAttribute(node, "data-requests")
			if modelOK && requestsOK {
				appendModel(node, model, requests)
			}
		})
	}

	if len(models) == 0 {
		// Try labeled request-count text only when attributes produced
		// no accepted models, allowing a textual settings
		// representation to provide the same display observations.
		heading := findLabelElement(root, []string{"models", "available models"})
		// Inspect at most four enclosing model-label blocks so textual
		// extraction stays near its heading when looking for list or
		// code request counts.
		for depth, block := 0, parentNode(heading); block != nil && depth < 4; depth, block = depth+1, block.Parent {
			walkHTML(block, func(node *html.Node) {
				if node.Type != html.ElementNode || (node.Data != "li" && node.Data != "code") {
					return
				}
				match := ollamaModelFallbackPattern.FindStringSubmatch(strings.TrimSpace(normalizedNodeText(node)))
				if len(match) == 3 {
					appendModel(node, match[1], match[2])
				}
			})
			if len(models) > 0 {
				break
			}
		}
	}

	sort.Slice(models, func(i, j int) bool {
		if models[i].Window != models[j].Window {
			return models[i].Window < models[j].Window
		}
		return models[i].Model < models[j].Model
	})
	// Cap the sorted model list to bound displayed request-count detail.
	if len(models) > 100 {
		models = models[:100]
	}
	return models
}

// ollamaModelWindow associates a model node with a five-hour or weekly
// ancestor. It begins at the parent, preferring each ancestor's normalized
// usage-window attribute before inspecting that ancestor's label text.
//
// Attribute matching tests short-window aliases first. Text matching requires
// exactly one window family, so a broad block mentioning both windows leaves
// search continuing toward another ancestor. An unresolved association returns
// the empty window, causing parseOllamaModels to skip that model observation
// rather than attach its count to an unidentified period.
func ollamaModelWindow(node *html.Node) usageview.OllamaCloudUsageModelWindow {
	for block := parentNode(node); block != nil; block = block.Parent {
		if value, ok := htmlAttribute(block, "data-usage-window"); ok {
			normalized := strings.ToLower(strings.NewReplacer("-", " ", "_", " ").Replace(value))
			if containsAny(normalized, "five hour", "5 hour", "5h", "session") {
				return usageview.OllamaCloudUsageModelWindowFiveHour
			}
			if containsAny(normalized, "seven day", "7 day", "7d", "weekly") {
				return usageview.OllamaCloudUsageModelWindowSevenDay
			}
		}
		text := strings.ToLower(normalizedNodeText(block))
		fiveHour := containsAny(text, ollamaFiveHourUsageAliases...)
		sevenDay := containsAny(text, ollamaSevenDayUsageAliases...)
		// Text mentioning both window families cannot identify this
		// model's period. Require one family before attribution, then
		// continue searching ancestors when the label remains
		// ambiguous.
		if fiveHour && !sevenDay {
			return usageview.OllamaCloudUsageModelWindowFiveHour
		}
		if sevenDay && !fiveHour {
			return usageview.OllamaCloudUsageModelWindowSevenDay
		}
	}
	return ""
}

// parseOllamaRequestCount parses a nonnegative int64 request count after
// trimming whitespace and removing commas. Invalid or out-of-range values
// return false so model extraction can skip an unusable count.
func parseOllamaRequestCount(value string) (int64, bool) {
	value = strings.ReplaceAll(strings.TrimSpace(value), ",", "")
	requests, err := strconv.ParseInt(value, 10, 64)
	return requests, err == nil && requests >= 0
}

// parentNode returns a node's parent, or nil when the node is absent.
// Model-label extraction uses that nil result to skip ancestor search when no
// heading was found.
func parentNode(node *html.Node) *html.Node {
	if node == nil {
		return nil
	}
	return node.Parent
}

// nextElementSibling returns the next sibling whose node type is an element.
// Nil input and an exhausted sibling chain return nil, allowing label
// extraction to try ancestor text.
func nextElementSibling(node *html.Node) *html.Node {
	if node == nil {
		return nil
	}
	for sibling := node.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		if sibling.Type == html.ElementNode {
			return sibling
		}
	}
	return nil
}

// timeElementValue finds the first parseable reset value in depth-first order.
// It reads time datetime attributes or data-time on local-time elements and
// class tokens; an absent or unreadable timestamp returns nil.
func timeElementValue(root *html.Node) *time.Time {
	var parsed *time.Time
	// Prefer a usable time datetime value at each visited element, then try
	// its local-time representation; an unreadable value leaves later
	// elements eligible to supply the optional reset.
	walkHTML(root, func(node *html.Node) {
		if parsed != nil || node.Type != html.ElementNode {
			return
		}
		if node.Data == "time" {
			if value, ok := htmlAttribute(node, "datetime"); ok {
				parsed = parseOllamaResetTime(value)
			}
			if parsed != nil {
				return
			}
		}
		if node.Data == "local-time" || htmlClassToken(node, "local-time") {
			if value, ok := htmlAttribute(node, "data-time"); ok {
				parsed = parseOllamaResetTime(value)
			}
		}
	})
	return parsed
}

// ollamaUsagePercentFromText parses the first percentage-shaped match in value.
// A missing, unreadable, or out-of-range match returns false; later text
// matches are not reconsidered when that first match is unusable.
func ollamaUsagePercentFromText(value string) (float64, bool) {
	match := ollamaUsagePercentPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0, false
	}
	percent, err := strconv.ParseFloat(match[1], 64)
	// Text observations use the display window's bounded percentage scale.
	return percent, err == nil && percent >= 0 && percent <= 100
}

// ollamaUsagePercentFromTrack selects a usable percentage from marked usage
// tracks in depth-first order. Within a track it prefers the sum of valid
// segment widths when that sum falls within zero to one hundred.
//
// If segment extraction yields nothing or its sum exceeds the percentage range,
// the first valid width in that track's subtree is used. A successful track
// ends selection; otherwise later tracks remain eligible. The returned boolean
// distinguishes an observed zero percentage from no usable track, letting
// window parsing retain genuine zero usage.
func ollamaUsagePercentFromTrack(root *html.Node) (float64, bool) {
	var percent float64
	var found bool
	walkHTML(root, func(node *html.Node) {
		if found || node.Type != html.ElementNode {
			return
		}
		if _, ok := htmlAttribute(node, "data-usage-track"); !ok {
			return
		}
		var segmentTotal float64
		var segmentFound bool
		walkHTML(node, func(segment *html.Node) {
			if segment.Type != html.ElementNode {
				return
			}
			if _, ok := htmlAttribute(segment, "data-usage-segment"); !ok {
				return
			}
			if value, ok := cssWidthPercent(segment); ok {
				segmentTotal += value
				segmentFound = true
			}
		})
		// Segment widths describe pieces of one usage track. Prefer
		// their in-range sum so one model's segment width is not
		// mistaken for total window usage.
		if segmentFound && segmentTotal >= 0 && segmentTotal <= 100 {
			percent, found = segmentTotal, true
			return
		}
		// When segment widths provide no usable total, select a valid
		// descendant width so alternate track markup can still supply a
		// percentage observation.
		walkHTML(node, func(child *html.Node) {
			if found || child.Type != html.ElementNode {
				return
			}
			if value, ok := cssWidthPercent(child); ok {
				percent, found = value, true
			}
		})
	})
	return percent, found
}

// cssWidthPercent parses the first percentage width declaration in a node's
// style attribute. Missing declarations and values outside zero to one hundred
// return false so track parsing can continue searching.
func cssWidthPercent(node *html.Node) (float64, bool) {
	style, ok := htmlAttribute(node, "style")
	if !ok {
		return 0, false
	}
	match := ollamaUsageWidthPattern.FindStringSubmatch(style)
	if len(match) != 2 {
		return 0, false
	}
	percent, err := strconv.ParseFloat(match[1], 64)
	// CSS widths use the enclosing usage track's percentage scale.
	return percent, err == nil && percent >= 0 && percent <= 100
}

// htmlAttribute returns the first case-insensitive matching attribute value.
// Nil nodes and missing keys return false; the supplied value remains as
// written for the caller's own parsing.
func htmlAttribute(node *html.Node, key string) (string, bool) {
	if node == nil {
		return "", false
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val, true
		}
	}
	return "", false
}

// htmlAttributeInSubtree returns the first matching element attribute in
// depth-first order, including the root. The boolean distinguishes an absent
// attribute from a present empty value used by segment extraction.
func htmlAttributeInSubtree(root *html.Node, key string) (string, bool) {
	var value string
	var found bool
	walkHTML(root, func(node *html.Node) {
		if found || node.Type != html.ElementNode {
			return
		}
		value, found = htmlAttribute(node, key)
	})
	return value, found
}

// htmlClassToken reports whether a node's whitespace-separated class list
// contains token. Comparison is case-sensitive after the class attribute has
// been located, and missing attributes return false.
func htmlClassToken(node *html.Node, token string) bool {
	value, ok := htmlAttribute(node, "class")
	if !ok {
		return false
	}
	return slices.Contains(strings.Fields(value), token)
}

// parseOllamaResetTime accepts supported RFC3339 or named-month timestamp text
// and returns a nonzero UTC instant. Blank, unrecognized, and zero times return
// nil, leaving an optional reset unobserved.
func parseOllamaResetTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	// Try the supported timestamp layouts in order so an unreadable
	// spelling can still use a later accepted form; relative wording remains
	// unparsed and available separately as ResetText.
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "Jan 2, 2006 3:04 PM MST", "January 2, 2006 3:04 PM MST"} {
		if parsed, err := time.Parse(layout, value); err == nil && !parsed.IsZero() {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

// normalizedNodeText joins text-node contents into single-spaced text, skipping
// script, style, and noscript subtrees. Nil input returns empty text; DOM
// paragraph and newline separation are collapsed for label and percentage
// matching.
func normalizedNodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var parts []string
	var collect func(*html.Node)
	collect = func(current *html.Node) {
		// Exclude script, style, and noscript contents before matching
		// visible settings labels, so embedded code or alternate
		// non-rendered text cannot supply a usage value.
		if current.Type == html.ElementNode && (current.Data == "script" || current.Data == "style" || current.Data == "noscript") {
			return
		}
		if current.Type == html.TextNode {
			if value := strings.TrimSpace(current.Data); value != "" {
				parts = append(parts, value)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)
	return strings.Join(strings.Fields(strings.Join(parts, "\n")), " ")
}

// walkHTML visits the supplied node before its descendants in depth-first
// order. Nil roots are skipped; visit must be usable for a nonnil root, and
// callbacks do not prune traversal.
func walkHTML(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

// isOllamaParserContainer reports whether label search accepts the supplied
// tag.
func isOllamaParserContainer(tag string) bool {
	switch tag {
	case "div", "section", "article", "li", "p", "span", "dt", "dd", "h1", "h2", "h3", "h4":
		return true
	default:
		return false
	}
}

// containsAny reports whether value contains any supplied substring using
// case-sensitive matching.
func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
