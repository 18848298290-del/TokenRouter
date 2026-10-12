package upstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCNProviderResponseIndicatesInsufficientBalance checks Chinese and
// English phrases in several raw response shapes. It also checks that
// unrelated quota or rate-limit wording and empty input stay unmatched.
func TestCNProviderResponseIndicatesInsufficientBalance(t *testing.T) {
	t.Parallel()
	positive := []string{
		`{"error":{"message":"余额不足"}}`,
		`{"error":{"message":"Insufficient balance"}}`,
		`{"code":"insufficient_credit"}`,
		`"balance is not enough"`,
		`"no enough balance"`,
	}
	for _, body := range positive {
		require.True(t, CNResponseIndicatesInsufficientBalance([]byte(body)), body)
	}
	negative := []string{
		`{"error":{"message":"rate limit exceeded"}}`,
		`{"error":{"message":"quota exhausted"}}`,
		``,
	}
	for _, body := range negative {
		require.False(t, CNResponseIndicatesInsufficientBalance([]byte(body)), body)
	}
}

// TestIsHTMLResponse varies HTML prefix case and leading whitespace alongside
// JSON, plain text, empty, and XML inputs. It checks prefix classification
// against each expected result.
func TestIsHTMLResponse(t *testing.T) {
	cases := []struct {
		name string // named response prefix scenario
		body string // raw response text fixture
		want bool   // expected HTML prefix classification
	}{
		{"doctype_lower", "<!doctype html><html></html>", true},
		{"doctype_upper", "<!DOCTYPE HTML>", true},
		{"bare_html", "<html lang=\"en\">", true},
		{"leading_whitespace", "\n\n   <html>", true},
		{"json_error", `{"error":{"message":"forbidden"}}`, false},
		{"plain_text", "Forbidden", false},
		{"empty", "", false},
		{"xml_declaration", `<?xml version="1.0"?><error/>`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsHTMLResponse([]byte(tc.body)))
		})
	}
}
