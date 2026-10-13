package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Audit output retains operational names and identifiers while removing
// sensitive values from structured request bodies. These cases cover nested
// provider credentials, normalized payment-secret names, browser sessions,
// unsupported form bodies, empty input, and the assembled sensitive-key tables.

// TestRedactAuditBody_JSONRedactsSecrets supplies nested credential and
// identity secrets in JSON. It checks valid output with every canary removed
// while operational base URL and name remain available for audit attribution.
func TestRedactAuditBody_JSONRedactsSecrets(t *testing.T) {
	redactor := provideAuditRedactor()
	raw := []byte(`{
		"name": "acc1",
		"base_url": "https://evil.example.com",
		"credentials": {"api_key": "sk-secret-123", "base_url": "https://evil.example.com"},
		"credential": "google-id-token-canary",
		"new_password": "hunter2",
		"totp_code": "123456",
		"nested": [{"access_token": "tok_abc"}]
	}`)
	out := redactor.RedactBody(raw, "application/json")

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	// Sensitive values must be removed.
	for _, secret := range []string{"sk-secret-123", "google-id-token-canary", "hunter2", "123456", "tok_abc"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted body still contains secret %q: %s", secret, out)
		}
	}
	// Keep base_url and name for audit attribution.
	if !strings.Contains(out, "evil.example.com") {
		t.Fatalf("base_url should be preserved for providerability: %s", out)
	}
	if !strings.Contains(out, "acc1") {
		t.Fatalf("name should be preserved: %s", out)
	}
}

// TestRedactAuditBody_BareSessionKeyRedacted supplies a browser cookie under
// session beside session_id. It checks the cookie canary is removed while the
// operational session identifier remains in the audit body.
func TestRedactAuditBody_BareSessionKeyRedacted(t *testing.T) {
	redactor := provideAuditRedactor()
	raw := []byte(`{"session": "wos-session=cookie-canary", "session_id": "sid-visible"}`)
	out := redactor.RedactBody(raw, "application/json")

	if strings.Contains(out, "cookie-canary") {
		t.Fatalf("redacted body still contains the session cookie: %s", out)
	}
	if !strings.Contains(out, "sid-visible") {
		t.Fatalf("session_id should be preserved for providerability: %s", out)
	}
}

// TestRedactAuditBody_AuthoritativeTablesSynced supplies credential blobs,
// proxy strings, and normalized payment-secret fields. It checks every secret
// canary is removed while payment provider_key and the instance name remain
// available.
func TestRedactAuditBody_AuthoritativeTablesSynced(t *testing.T) {
	redactor := provideAuditRedactor()
	raw := []byte(`{
		"credentials": {
			"session_key": "sk-session-aaa",
			"service_account_json": "{\"private_key\":\"pem-body-bbb\"}",
			"service_account": "sa-blob-ccc"
		},
		"proxy_key": "socks5|1.2.3.4|1080|proxyuser|proxypass-ddd",
		"custom_key": "sk-custom-eee",
		"config": {
			"pkey": "easypay-merchant-fff",
			"privateKey": "alipay-pem-ggg",
			"apiv3key": "wxpay-v3-hhh",
			"SecretKey": "stripe-sk-iii",
			"webhookSecret": "whsec-jjj"
		},
		"provider_key": "stripe",
		"name": "instance-1"
	}`)
	out := redactor.RedactBody(raw, "application/json")

	for _, secret := range []string{
		"sk-session-aaa", "pem-body-bbb", "sa-blob-ccc",
		"proxypass-ddd", "sk-custom-eee",
		"easypay-merchant-fff", "alipay-pem-ggg", "wxpay-v3-hhh",
		"stripe-sk-iii", "whsec-jjj",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted body still contains secret %q: %s", secret, out)
		}
	}
	// provider_key identifies the payment channel and remains available for audit tracing.
	if !strings.Contains(out, `"provider_key":"stripe"`) {
		t.Fatalf("provider_key should be preserved for providerability: %s", out)
	}
	if !strings.Contains(out, "instance-1") {
		t.Fatalf("name should be preserved: %s", out)
	}
}

// TestAuditSensitiveKeys_CoverCredentialTable reads the declared provider and
// payment sensitive-key tables. Every listed key must be recognized by the
// application-assembled audit redactor so another declared secret cannot
// silently escape coverage.
func TestAuditSensitiveKeys_CoverCredentialTable(t *testing.T) {
	redactor := provideAuditRedactor()
	for _, k := range providercore.SensitiveCredentialKeys {
		if !redactor.IsSensitiveKey(k) {
			t.Fatalf("credential key %q is not covered by audit redaction", k)
		}
	}
	for provider, fields := range payment.ConfigProviderSensitiveConfigFields {
		for k := range fields {
			if !redactor.IsSensitiveKey(k) {
				t.Fatalf("payment provider %q sensitive field %q is not covered by audit redaction", provider, k)
			}
		}
	}
}

// TestRedactAuditBody_NonJSONOmitted supplies a form body containing a password
// canary. The redactor must omit its content, exclude the canary, and return
// the omission marker.
func TestRedactAuditBody_NonJSONOmitted(t *testing.T) {
	redactor := provideAuditRedactor()
	out := redactor.RedactBody([]byte("username=admin&password=secret"), "application/x-www-form-urlencoded")
	if strings.Contains(out, "secret") {
		t.Fatalf("non-json body must not leak content: %s", out)
	}
	if !strings.Contains(out, "omitted") {
		t.Fatalf("expected omission marker, got: %s", out)
	}
}

// TestRedactAuditBody_Empty supplies a nil body with JSON content type. It
// checks the audit redactor returns an empty string for absent content rather
// than a populated audit value.
func TestRedactAuditBody_Empty(t *testing.T) {
	redactor := provideAuditRedactor()
	if got := redactor.RedactBody(nil, "application/json"); got != "" {
		t.Fatalf("expected empty for nil body, got %q", got)
	}
}
