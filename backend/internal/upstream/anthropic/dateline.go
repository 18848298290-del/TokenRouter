package anthropic

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	// Separate patterns enforce matching date separators because RE2 has no
	// backreferences. Restrict matching to the observed dateline phrase and
	// apostrophe variants to avoid rewriting unrelated user text.
	datelineRegexHyphen = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})-(\d{2})-(\d{2})\.`)
	datelineRegexSlash  = regexp.MustCompile(`Today(['’ʼʹ])s date is (\d{4})/(\d{2})/(\d{2})\.`)

	// systemReminderRegex scopes message-text normalization to system reminders.
	systemReminderRegex = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
)

// DatelineHit records the original punctuation of a normalized dateline.
type DatelineHit struct {
	// ApostropheVariant is "ascii", "u2019", "u02bc", or "u02b9".
	ApostropheVariant string
	// DateSeparator is the original "-" or "/".
	DateSeparator string
}

type datelineMatch struct {
	start, end       int
	apoRune          rune
	sep              string
	year, month, day string
}

// canonicalize uses an ASCII apostrophe and hyphenated date.
func canonicalize(year, month, day string) string {
	return fmt.Sprintf("Today's date is %s-%s-%s.", year, month, day)
}

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

// NormalizeText canonicalizes matching datelines. If no text changes, it returns
// the original string and a nil hit list.
func NormalizeText(text string) (string, []DatelineHit) {
	if !strings.Contains(text, "date is ") {
		return text, nil
	}
	matches := collectMatches(text, datelineRegexHyphen, "-")
	matches = append(matches, collectMatches(text, datelineRegexSlash, "/")...)
	if len(matches) == 0 {
		return text, nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })

	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	hits := make([]DatelineHit, 0, len(matches))
	changed := false
	for _, m := range matches {
		full := text[m.start:m.end]
		canonical := canonicalize(m.year, m.month, m.day)
		if canonical == full {
			// Already canonical text does not count as a hit.
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

// normalizeSystemReminderScopedText canonicalizes datelines inside system-reminder
// tags, preserving text outside the tags byte for byte.
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

// NormalizeDateline canonicalizes datelines in system text and message text
// inside system-reminder tags. Tool inputs, tool results, and other block types
// are excluded so user code and command output remain unchanged.
// It does not mutate body. When no changes are needed, it returns body, nil, false.
func NormalizeDateline(body []byte) ([]byte, []DatelineHit, bool) {
	if len(body) == 0 {
		return body, nil, false
	}
	out := body
	var hits []DatelineHit
	changed := false

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
