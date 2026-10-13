package anthropic

import (
	"bytes"
	"strings"
	"testing"
)

// TestNormalizeText_ASCIIHyphenIsIdentity supplies an already canonical
// ASCII-apostrophe, hyphenated dateline. The returned text stays equal and
// produces no hits, so observation records count changes rather than merely
// matching sentences.
func TestNormalizeText_ASCIIHyphenIsIdentity(t *testing.T) {
	in := "Today's date is 2026-07-01."
	out, hits := NormalizeText(in)
	if out != in {
		t.Fatalf("canonical form should be returned identity, got %q", out)
	}
	if len(hits) != 0 {
		t.Fatalf("no hits expected on canonical input, got %d", len(hits))
	}
}

// TestNormalizeText_SlashSeparatorASCIIApostrophe supplies an ASCII-apostrophe
// dateline with slash separators. The returned text uses hyphens and the single
// hit retains ascii and slash labels for the supplied punctuation.
func TestNormalizeText_SlashSeparatorASCIIApostrophe(t *testing.T) {
	in := "Today's date is 2026/07/01."
	out, hits := NormalizeText(in)
	want := "Today's date is 2026-07-01."
	if out != want {
		t.Fatalf("want %q, got %q", want, out)
	}
	if len(hits) != 1 || hits[0].ApostropheVariant != "ascii" || hits[0].DateSeparator != "/" {
		t.Fatalf("unexpected hit: %+v", hits)
	}
}

// TestNormalizeText_U2019Apostrophe supplies a dateline containing the U+2019
// apostrophe and hyphen separators. The result uses the ASCII apostrophe while
// its hit retains the u2019 and hyphen labels.
func TestNormalizeText_U2019Apostrophe(t *testing.T) {
	in := "Today’s date is 2026-07-01."
	out, hits := NormalizeText(in)
	want := "Today's date is 2026-07-01."
	if out != want {
		t.Fatalf("want %q, got %q", want, out)
	}
	if len(hits) != 1 || hits[0].ApostropheVariant != "u2019" || hits[0].DateSeparator != "-" {
		t.Fatalf("unexpected hit: %+v", hits)
	}
}

// TestNormalizeText_U02BCApostropheWithSlash supplies a U+02BC apostrophe and
// slash-form date. Normalized text uses the canonical spelling, while one hit
// retains both original punctuation labels.
func TestNormalizeText_U02BCApostropheWithSlash(t *testing.T) {
	in := "Todayʼs date is 2026/07/01."
	out, hits := NormalizeText(in)
	want := "Today's date is 2026-07-01."
	if out != want {
		t.Fatalf("want %q, got %q", want, out)
	}
	if len(hits) != 1 || hits[0].ApostropheVariant != "u02bc" || hits[0].DateSeparator != "/" {
		t.Fatalf("unexpected hit: %+v", hits)
	}
}

// TestNormalizeText_U02B9Apostrophe supplies a U+02B9 apostrophe and slash-form
// date. The result uses ASCII/hyphen spelling and reports one hit identifying
// that apostrophe variant and the original separator.
func TestNormalizeText_U02B9Apostrophe(t *testing.T) {
	in := "Todayʹs date is 2026/07/01."
	out, hits := NormalizeText(in)
	want := "Today's date is 2026-07-01."
	if out != want {
		t.Fatalf("want %q, got %q", want, out)
	}
	if len(hits) != 1 || hits[0].ApostropheVariant != "u02b9" || hits[0].DateSeparator != "/" {
		t.Fatalf("unexpected hit: %+v", hits)
	}
}

// TestNormalizeText_MixedSeparatorsNoMatch supplies a date whose delimiters mix
// hyphen and slash. The text stays equal and yields no hits, keeping mixed-date
// spelling outside the two recognized pattern families.
func TestNormalizeText_MixedSeparatorsNoMatch(t *testing.T) {
	// Mixed date separators must not match.
	in := "Today's date is 2026-07/01."
	out, hits := NormalizeText(in)
	if out != in {
		t.Fatalf("mixed-separator input should not be matched, got %q", out)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %d", len(hits))
	}
}

// TestNormalizeText_NegativeLookalike supplies phrases sharing dates or
// sentence fragments with a dateline. Every supplied phrase retains its text
// and produces no hits because it lacks the recognized complete sentence.
func TestNormalizeText_NegativeLookalike(t *testing.T) {
	cases := []string{
		"Today is a great day.",
		"His date is 2026-07-01.",
		"Yesterday's date was 2026-06-30.",
		"'s date is 2026-07-01.",
	}
	for _, c := range cases {
		out, hits := NormalizeText(c)
		if out != c {
			t.Fatalf("input %q should not be modified, got %q", c, out)
		}
		if len(hits) != 0 {
			t.Fatalf("input %q should produce no hits", c)
		}
	}
}

// TestNormalizeText_Idempotent normalizes a Unicode-apostrophe slash date and
// processes the result again. The second result equals the first and yields no
// further hits for the canonical spelling.
func TestNormalizeText_Idempotent(t *testing.T) {
	in := "Today’s date is 2026/07/01."
	out1, _ := NormalizeText(in)
	out2, hits2 := NormalizeText(out1)
	if out1 != out2 {
		t.Fatalf("normalization not idempotent: %q vs %q", out1, out2)
	}
	if len(hits2) != 0 {
		t.Fatalf("second pass should produce no hits, got %d", len(hits2))
	}
}

// TestNormalizeText_MultipleOccurrences supplies two noncanonical datelines
// separated by other lines. Both become canonical, the original punctuation
// disappears, and two observations reflect the two changed occurrences.
func TestNormalizeText_MultipleOccurrences(t *testing.T) {
	in := "First line.\nToday’s date is 2026/07/01.\nMore text.\nTodayʼs date is 2026-07-01.\nEnd."
	out, hits := NormalizeText(in)
	if strings.Count(out, "Today's date is") != 2 {
		t.Fatalf("expected two canonicalized sentences, got: %q", out)
	}
	if strings.Contains(out, "’") || strings.Contains(out, "ʼ") || strings.Contains(out, "2026/07/01") {
		t.Fatalf("fingerprint characters must be gone, got: %q", out)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
}

// TestNormalizeDateline_SystemString supplies a scalar system prompt containing
// a Unicode slash dateline. The result reports one change and contains the
// canonical sentence with the slash date removed.
func TestNormalizeDateline_SystemString(t *testing.T) {
	body := []byte(`{"system":"You are helpful.\nToday’s date is 2026/07/01.\nBe brief.","messages":[]}`)
	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if !bytes.Contains(out, []byte("Today's date is 2026-07-01.")) {
		t.Fatalf("output missing canonical dateline: %s", string(out))
	}
	if bytes.Contains(out, []byte("2026/07/01")) {
		t.Fatalf("output should not contain slash date: %s", string(out))
	}
}

// TestNormalizeDateline_SystemBlocksArray places a noncanonical dateline in a
// later text system block. The result contains the canonical sentence and a
// single observation retaining its original apostrophe label.
func TestNormalizeDateline_SystemBlocksArray(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"You are helpful."},{"type":"text","text":"Todayʼs date is 2026/07/01."}],"messages":[]}`)
	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if len(hits) != 1 || hits[0].ApostropheVariant != "u02bc" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
	if !bytes.Contains(out, []byte("Today's date is 2026-07-01.")) {
		t.Fatalf("output missing canonical dateline: %s", string(out))
	}
}

// TestNormalizeDateline_MessagesContentStringInSystemReminder places a dateline
// inside reminder tags within scalar message content. Normalization reports one
// hit and inserts the canonical sentence in that tagged segment.
func TestNormalizeDateline_MessagesContentStringInSystemReminder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\n# currentDate\nToday’s date is 2026/07/01.\n</system-reminder>\nHello, please help."}]}`)
	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if !bytes.Contains(out, []byte("Today's date is 2026-07-01.")) {
		t.Fatalf("canonical dateline missing: %s", string(out))
	}
}

// TestNormalizeDateline_MessagesContentBlocksInSystemReminder places a
// reminder-scoped dateline in an array text block beside another instruction.
// The result reports a change and contains one canonicalized sentence with one
// hit.
func TestNormalizeDateline_MessagesContentBlocksInSystemReminder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>"},{"type":"text","text":"do X"}]}]}`)
	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if !bytes.Contains(out, []byte("Today's date is 2026-07-01.")) {
		t.Fatalf("canonical dateline missing: %s", string(out))
	}
}

// TestNormalizeDateline_LeavesOutOfScopeUntouched supplies ordinary user text
// and datelines inside tool input and tool results. The returned body remains
// byte-equal and reports no changes for these excluded locations.
func TestNormalizeDateline_LeavesOutOfScopeUntouched(t *testing.T) {
	// User text outside system reminders and tool input/results remain unchanged.
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"Today’s date is 2026/07/01. Please help."},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"y","input":{"note":"Today’s date is 2026/07/01."}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"log: Today’s date is 2026/07/01."}]}` +
		`]}`)
	out, hits, changed := NormalizeDateline(body)
	if changed {
		t.Fatalf("expected changed=false, hits=%v out=%s", hits, string(out))
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("output should equal input byte-for-byte")
	}
}

// TestNormalizeDateline_Idempotent normalizes reminder-scoped JSON and
// processes its returned bytes again. The second pass reports no change and
// returns bytes equal to the first normalized body.
func TestNormalizeDateline_Idempotent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>"}]}`)
	first, _, changed1 := NormalizeDateline(body)
	if !changed1 {
		t.Fatalf("expected first pass to change body")
	}
	second, _, changed2 := NormalizeDateline(first)
	if changed2 {
		t.Fatalf("second pass should not report changes")
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("second pass diverged: %s vs %s", string(first), string(second))
	}
}

// TestNormalizeDateline_EmptyBody passes nil request bytes to dateline
// normalization. Output remains nil, hits remain nil, and the changed flag
// stays false for an absent request body.
func TestNormalizeDateline_EmptyBody(t *testing.T) {
	out, hits, changed := NormalizeDateline(nil)
	if changed || out != nil || hits != nil {
		t.Fatalf("empty body should be no-op")
	}
}

// TestNormalizeDateline_NoDateline supplies a nonempty request containing no
// recognized date sentence. It requires no changes or hits, then checks byte
// equality when the returned slice starts at a different address.
func TestNormalizeDateline_NoDateline(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}],"system":"just a system prompt"}`)
	out, hits, changed := NormalizeDateline(body)
	if changed || len(hits) != 0 {
		t.Fatalf("expected no changes; changed=%v hits=%v", changed, hits)
	}
	if &out[0] != &body[0] {
		// Byte equality is required even if the implementation allocates a new slice.
		if !bytes.Equal(out, body) {
			t.Fatalf("output must byte-match input when no changes needed")
		}
	}
}

// TestNormalizeDateline_MultipleSystemReminderBlocksInSameText supplies two
// tagged reminder segments separated by ordinary prose in one message. Both
// datelines are changed, producing two hits and removing slash-form dates from
// the result.
func TestNormalizeDateline_MultipleSystemReminderBlocksInSameText(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>\nsome prose\n<system-reminder>\nAlso Todayʼs date is 2026/07/01.\n</system-reminder>"}]}`)
	out, hits, changed := NormalizeDateline(body)
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if bytes.Contains(out, []byte("2026/07/01")) {
		t.Fatalf("slash separator must be gone: %s", string(out))
	}
}
