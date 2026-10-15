package anthropic

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// dateline.go normalizes date sentences in system instructions and message
// reminder segments.
// Dateline punctuation is normalized within system text and complete
// system-reminder segments in message text. Separate patterns recognize
// all-hyphen or all-slash dates with selected apostrophes, keeping mixed
// separators and other phrases outside the rewrite.
//
// Matched ranges are ordered before reconstruction so text between changed
// sentences survives in place. JSON updates touch system strings, text system
// blocks, and text message content at their existing paths; tool inputs, tool
// results, and other block kinds stay outside that traversal. Observations
// record punctuation from successful rewrites, while failed updates leave their
// corresponding text and hit records unchanged for that request.

// Dateline patterns capture supported sentence punctuation, while reminder
// patterns delimit the message segments eligible for the same text
// normalization.
var (
	// Separate expressions accept consistent date delimiters and the selected
	// apostrophe spellings. Requiring the complete dateline phrase keeps other
	// date-bearing sentences outside normalization.
	datelineRegexHyphen = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})-(\d{2})-(\d{2})\.`)
	datelineRegexSlash  = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})/(\d{2})/(\d{2})\.`)

	// systemReminderRegex scopes message-text normalization to system reminders.
	systemReminderRegex = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
)

// DatelineHit records punctuation that changed in a successfully normalized
// sentence.
type DatelineHit struct {
	ApostropheVariant string // ascii or Unicode label
	DateSeparator     string // original date delimiter
}

// datelineMatch holds matched byte ranges and date spelling for reconstruction.
type datelineMatch struct {
	// start and end delimit the matched sentence in bytes. apoRune and sep retain
	// original punctuation, while year, month, and day hold the captured digits.
	start, end       int    // matched byte interval
	apoRune          rune   // original apostrophe rune
	sep              string // original separator text
	year, month, day string // captured date digits
}

// canonicalize formats date components with an ASCII apostrophe and hyphens.
func canonicalize(year, month, day string) string {
	return fmt.Sprintf("Today's date is %s-%s-%s.", year, month, day)
}

// apostropheVariant labels the three named Unicode apostrophes and returns
// ascii for all other runes. collectMatches supplies runes accepted by the
// dateline expressions, including the ASCII apostrophe.
func apostropheVariant(r rune) string {
	switch r {
	case '’':
		return "u2019"
	case 'ʼ':
		return "u02bc"
	case 'ʹ':
		return "u02b9"
	default:
		return "ascii"
	}
}

// collectMatches extracts date components and byte ranges using the supplied
// expression and separator label. Capturing groups must contain apostrophe,
// year, month, and day in that order; no matches returns nil for reconstruction
// callers.
func collectMatches(text string, re *regexp.Regexp, sep string) []datelineMatch {
	locs := re.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	out := make([]datelineMatch, 0, len(locs))
	for _, m := range locs {
		var apoRune rune
		for _, r := range text[m[2]:m[3]] {
			apoRune = r
			break
		}
		out = append(out, datelineMatch{
			start:   m[0],
			end:     m[1],
			apoRune: apoRune,
			sep:     sep,
			year:    text[m[4]:m[5]],
			month:   text[m[6]:m[7]],
			day:     text[m[8]:m[9]],
		})
	}
	return out
}

// NormalizeText rewrites recognized dateline sentences into ASCII-apostrophe,
// hyphenated-date form and records changed punctuation. Sentences already in
// that form do not produce hits, and no changes returns the supplied string and
// nil.
//
// Matches from both separator patterns are sorted before rebuilding the text.
// Unchanged ranges, including already canonical matches, stay between changed
// spans because the copy cursor advances only after replacements. Date digits
// are copied as matched without calendar validation, so normalization concerns
// spelling rather than whether the date exists.
func NormalizeText(text string) (string, []DatelineHit) {
	if !strings.Contains(text, "date is ") {
		return text, nil
	}
	matches := collectMatches(text, datelineRegexHyphen, "-")
	matches = append(matches, collectMatches(text, datelineRegexSlash, "/")...)
	if len(matches) == 0 {
		return text, nil
	}
	// Merge both separator pattern results into source order before copying gaps,
	// so replacement ranges retain the text located between different date
	// spellings.
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })

	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	hits := make([]DatelineHit, 0, len(matches))
	changed := false
	for _, m := range matches {
		// A canonical sentence between changed matches stays behind prev until the
		// next gap is copied. Advancing prev on that skip would discard the canonical
		// sentence from the reconstructed text.
		full := text[m.start:m.end]
		canonical := canonicalize(m.year, m.month, m.day)
		if canonical == full {
			continue
		}
		_, _ = b.WriteString(text[prev:m.start])
		_, _ = b.WriteString(canonical)
		prev = m.end
		changed = true
		hits = append(hits, DatelineHit{
			ApostropheVariant: apostropheVariant(m.apoRune),
			DateSeparator:     m.sep,
		})
	}
	if !changed {
		return text, nil
	}
	_, _ = b.WriteString(text[prev:])
	return b.String(), hits
}

// normalizeSystemReminderScopedText applies dateline normalization within
// complete system-reminder segments and returns their combined hits. Missing or
// unmatched tags return the supplied string and nil; text before, between, and
// after matched segments retains its bytes.
//
// Each matched segment is normalized independently, then appended beside the
// untouched surrounding ranges. The outer changed flag reflects textual
// changes, so a segment with canonical dates contributes no hits and does not
// cause an otherwise unchanged message to be replaced. This scope keeps
// ordinary message prose outside dateline rewriting.
func normalizeSystemReminderScopedText(text string) (string, []DatelineHit) {
	if !strings.Contains(text, "<system-reminder>") {
		return text, nil
	}
	locs := systemReminderRegex.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text, nil
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	var hits []DatelineHit
	changed := false
	for _, loc := range locs {
		_, _ = b.WriteString(text[prev:loc[0]])
		block := text[loc[0]:loc[1]]
		normalized, blockHits := NormalizeText(block)
		if normalized != block {
			changed = true
		}
		_, _ = b.WriteString(normalized)
		hits = append(hits, blockHits...)
		prev = loc[1]
	}
	if !changed {
		return text, nil
	}
	_, _ = b.WriteString(text[prev:])
	return b.String(), hits
}

// NormalizeDateline rewrites system text and reminder-scoped message text,
// returning successful hits and a changed flag. Empty or unchanged requests
// return the supplied body, nil hits, and false; tool blocks and nontext
// message blocks stay outside traversal.
//
// System strings and text system blocks use full-text normalization, while
// message strings and text blocks use tagged segments. Every successful JSON
// edit contributes its hits, and failed edits keep the affected value without
// reporting its proposed hits. Edits accumulate by path, retaining surrounding
// JSON fields and the input slice through the non-in-place sjson calls.
func NormalizeDateline(body []byte) ([]byte, []DatelineHit, bool) {
	if len(body) == 0 {
		return body, nil, false
	}
	out := body
	var hits []DatelineHit
	changed := false

	// System and message updates record hits only after the JSON write succeeds,
	// so failed text edits cannot create normalization observations for unchanged
	// request fields.
	sys := gjson.GetBytes(out, "system")
	if sys.Exists() {
		switch {
		case sys.Type == gjson.String:
			normalized, sysHits := NormalizeText(sys.String())
			if normalized != sys.String() {
				if next, err := sjson.SetBytes(out, "system", normalized); err == nil {
					out = next
					changed = true
					hits = append(hits, sysHits...)
				}
			}
		case sys.IsArray():
			idx := 0
			sys.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "text" {
					t := item.Get("text")
					if t.Exists() && t.Type == gjson.String {
						normalized, textHits := NormalizeText(t.String())
						if normalized != t.String() {
							path := fmt.Sprintf("system.%d.text", idx)
							if next, err := sjson.SetBytes(out, path, normalized); err == nil {
								out = next
								changed = true
								hits = append(hits, textHits...)
							}
						}
					}
				}
				idx++
				return true
			})
		}
	}

	messages := gjson.GetBytes(out, "messages")
	if messages.IsArray() {
		msgIdx := -1
		messages.ForEach(func(_, msg gjson.Result) bool {
			msgIdx++
			content := msg.Get("content")
			if !content.Exists() {
				return true
			}
			switch {
			case content.Type == gjson.String:
				normalized, contentHits := normalizeSystemReminderScopedText(content.String())
				if normalized != content.String() {
					path := fmt.Sprintf("messages.%d.content", msgIdx)
					if next, err := sjson.SetBytes(out, path, normalized); err == nil {
						out = next
						changed = true
						hits = append(hits, contentHits...)
					}
				}
			case content.IsArray():
				contentIdx := -1
				content.ForEach(func(_, block gjson.Result) bool {
					contentIdx++
					if block.Get("type").String() != "text" {
						return true
					}
					t := block.Get("text")
					if !t.Exists() || t.Type != gjson.String {
						return true
					}
					normalized, textHits := normalizeSystemReminderScopedText(t.String())
					if normalized != t.String() {
						path := fmt.Sprintf("messages.%d.content.%d.text", msgIdx, contentIdx)
						if next, err := sjson.SetBytes(out, path, normalized); err == nil {
							out = next
							changed = true
							hits = append(hits, textHits...)
						}
					}
					return true
				})
			}
			return true
		})
	}

	if !changed {
		return body, nil, false
	}
	return out, hits, true
}
