package logredact

import (
	"strings"
	"testing"
)

// TestRedactText_JSONLike supplies valid JSON containing access and refresh
// tokens alongside an ordinary field. Output must change and contain masked
// values for both recognized credential keys.
func TestRedactText_JSONLike(t *testing.T) {
	in := `{"access_token":"ya29.a0AfH6SMDUMMY","refresh_token":"1//0gDUMMY","other":"ok"}`
	out := RedactText(in)
	if out == in {
		t.Fatalf("expected redaction, got unchanged")
	}
	if want := `"access_token":"***"`; !strings.Contains(out, want) {
		t.Fatalf("expected %q in %q", want, out)
	}
	if want := `"refresh_token":"***"`; !strings.Contains(out, want) {
		t.Fatalf("expected %q in %q", want, out)
	}
}

// TestRedactText_QueryLike supplies plain text containing access and refresh
// token assignments. Redaction must remove both fixture token substrings from
// the returned text.
func TestRedactText_QueryLike(t *testing.T) {
	in := "access_token=ya29.a0AfH6SMDUMMY refresh_token=1//0gDUMMY"
	out := RedactText(in)
	if strings.Contains(out, "ya29") || strings.Contains(out, "1//0") {
		t.Fatalf("expected tokens redacted, got %q", out)
	}
}

// TestRedactText_GOCSPX supplies a client_secret assignment containing a
// credential-shaped prefix. The fixture secret must disappear and the key/value
// assignment must contain the redaction marker.
func TestRedactText_GOCSPX(t *testing.T) {
	in := "client_secret=GOCSPX-your-client-secret"
	out := RedactText(in)
	if strings.Contains(out, "your-client-secret") {
		t.Fatalf("expected secret redacted, got %q", out)
	}
	if !strings.Contains(out, "client_secret=***") {
		t.Fatalf("expected key redacted, got %q", out)
	}
}

// TestRedactText_ExtraKeyCacheUsesNormalizedSortedKey repeats a custom field
// through differently cased, padded, and duplicated names. Both values must be
// masked while the compiled-expression cache contains one normalized key set.
func TestRedactText_ExtraKeyCacheUsesNormalizedSortedKey(t *testing.T) {
	clearExtraTextPatternCache()

	out1 := RedactText("custom_secret=abc", "Custom_Secret", " custom_secret ")
	out2 := RedactText("custom_secret=xyz", "custom_secret")
	if !strings.Contains(out1, "custom_secret=***") {
		t.Fatalf("expected custom key redacted in first call, got %q", out1)
	}
	if !strings.Contains(out2, "custom_secret=***") {
		t.Fatalf("expected custom key redacted in second call, got %q", out2)
	}

	if got := countExtraTextPatternCacheEntries(); got != 1 {
		t.Fatalf("expected 1 cached pattern set, got %d", got)
	}
}

// TestRedactText_DefaultPathDoesNotUseExtraCache masks a built-in credential
// field after clearing the custom cache. The result must be masked and the
// custom compiled-expression cache must remain empty.
func TestRedactText_DefaultPathDoesNotUseExtraCache(t *testing.T) {
	clearExtraTextPatternCache()

	out := RedactText("access_token=abc")
	if !strings.Contains(out, "access_token=***") {
		t.Fatalf("expected default key redacted, got %q", out)
	}
	if got := countExtraTextPatternCacheEntries(); got != 0 {
		t.Fatalf("expected extra cache to remain empty, got %d", got)
	}
}

// clearExtraTextPatternCache deletes currently observed custom pattern entries
// for serial cache-count assertions. Callers avoid concurrent pattern lookups
// while this fixture reset and its following count assertions execute.
func clearExtraTextPatternCache() {
	// Remove patterns before serial assertions so entries left by earlier tests
	// do not contribute to the custom-key publication count checked by this fixture.
	extraTextPatternCache.Range(func(key, value any) bool {
		extraTextPatternCache.Delete(key)
		return true
	})
}

// countExtraTextPatternCacheEntries counts entries visited by the shared
// custom-pattern cache iterator. Serial tests use this observation after
// redaction calls; concurrent updates could change the set while Range runs.
func countExtraTextPatternCacheEntries() int {
	count := 0
	extraTextPatternCache.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}
