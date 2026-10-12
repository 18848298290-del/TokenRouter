package upstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCNProviderResponseIndicatesInsufficientBalance covers Chinese and English error wording.
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

func TestIsHTMLResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
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
