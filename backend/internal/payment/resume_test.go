package payment

import (
	"net/url"
	"testing"
)

// Return URL canonicalization removes user query/fragment values before server
// order parameters are rebuilt. These fixtures exercise supported authority
// forms and unsafe authority rejection, checking both the canonical path and
// the final four parameter values with independent parallel cases.

// TestCanonicalizeReturnURLRemovesUserParameters supplies query/fragment
// suffixes, including forged order values and malformed escapes. Canonical
// output must equal the base URL, and rebuilding must retain exactly the four
// expected server-controlled parameters.
func TestCanonicalizeReturnURLRemovesUserParameters(t *testing.T) {
	t.Parallel()

	const base = "https://site.example.com/payment/result"
	for _, suffix := range []string{
		"", "?", "?trade_status=TRADE_SUCCESS", "?trade_status=TRADE_SUCCESS#fragment",
		"?order_id=1&out_trade_no=attacker&resume_token=attacker&status=success&trade_status=TRADE_SUCCESS",
		"?from=checkout&nested=%26trade_status%3DTRADE_SUCCESS", "?trade_status=%ZZ",
	} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			canonical, err := CanonicalizeReturnURL(base+suffix, "site.example.com", "")
			if err != nil {
				t.Fatal(err)
			}
			if canonical != base {
				t.Fatalf("return URL = %q, want %q", canonical, base)
			}
			result, err := ResumeBuildPaymentReturnURL(canonical, 99, "ORDER123", "server-token")
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(result)
			if err != nil {
				t.Fatal(err)
			}
			query := parsed.Query()
			if len(query) != 4 || query.Get("order_id") != "99" || query.Get("out_trade_no") != "ORDER123" ||
				query.Get("resume_token") != "server-token" || query.Get("status") != "success" {
				t.Fatalf("unexpected result parameters: %v", query)
			}
		})
	}
}

// TestCanonicalizeReturnURLRejectsUnsafeAuthority supplies userinfo and
// delimiter-bearing authorities while also making their request-origin inputs
// match. Every listed URL must be rejected with empty canonical output, so
// host matching cannot bypass authority validation.
func TestCanonicalizeReturnURLRejectsUnsafeAuthority(t *testing.T) {
	t.Parallel()

	for _, authority := range []string{
		"reader@site.example.com",
		"reader:password@site.example.com",
		"@site.example.com",
		"reader&label=demo@site.example.com",
		"reader%26label%3Ddemo@site.example.com",
		"site&label.example.com",
		"site=label.example.com",
	} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			raw := "https://" + authority + ResumePaymentResultReturnPath
			// Match both caller-origin inputs to the unsafe authority deliberately. The
			// assertion then distinguishes authority validation from merely rejecting
			// an unrelated destination host.
			canonical, err := CanonicalizeReturnURL(raw, authority, raw)
			if err == nil || canonical != "" {
				t.Fatalf("unsafe authority accepted: URL=%q, err=%v", canonical, err)
			}
		})
	}
}

// TestCanonicalizeReturnURLRebuildsAddress supplies domain, local, IPv4, IPv6
// and punycode authorities with an encoded result path. Each canonical URL
// must retain its authority and normalized fixed path while dropping query and
// fragment.
func TestCanonicalizeReturnURLRebuildsAddress(t *testing.T) {
	t.Parallel()

	for _, authority := range []string{
		"site.example.com", "localhost:3000", "127.0.0.1:8080", "[::1]:8443",
		"xn--fsqu00a.example",
	} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			raw := "https://" + authority + "/%70ayment/result?label=demo#section"
			canonical, err := CanonicalizeReturnURL(raw, authority, "")
			if err != nil {
				t.Fatal(err)
			}
			want := "https://" + authority + ResumePaymentResultReturnPath
			if canonical != want {
				t.Fatalf("return URL = %q, want %q", canonical, want)
			}
		})
	}
}
