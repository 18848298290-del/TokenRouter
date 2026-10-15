package postgres

import (
	"database/sql"
	"strings"
)

// values.go converts optional IDs, truncates diagnostic text, and escapes
// literal SQL LIKE input.
// Audit and storage queries represent optional IDs, bounded diagnostic text,
// and literal LIKE input with different SQL values. Positive-ID filtering is
// separate from nullable integer conversion, while diagnostic truncation fits
// whole decoded runes inside a byte budget after whitespace trimming.

// NullPositiveInt64 returns a positive pointed-to ID as a SQL argument value.
// Nil pointers, zero, and negative IDs become nil so audit storage records
// an absent actor identifier as SQL NULL.
func NullPositiveInt64(v *int64) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

// TruncateText trims whitespace and removes trailing decoded runes until text
// fits max bytes. Nonpositive limits produce empty text; already-fitting
// input keeps its byte spelling, while truncation decodes invalid UTF-8
// through rune conversion.
func TruncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	// Remove complete decoded runes so audit fields fit their byte limits
	// while a final multibyte character is not cut into an invalid suffix.
	for len(string(runes)) > max && len(runes) > 0 {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

// EscapeLikePattern escapes backslash, percent, and underscore for literal
// SQL LIKE matching.
func EscapeLikePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// NullableInt64 marks any supplied integer as valid, including zero and
// negative values. Nil input returns an invalid sql.NullInt64 for a SQL NULL
// value; the pointed-to integer is copied.
func NullableInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}
