package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/payment"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

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

// TestRedactAuditBody_BareSessionKeyRedacted checks removal of the entire browser cookie stored in session.
// Operational identifiers such as session_id remain available for audit tracing.
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

// TestRedactAuditBody_AuthoritativeTablesSynced checks sensitive provider credential fields,
// payment keys without separators, credentials embedded in proxy_key and custom_key strings,
// and normalized name variants such as camelCase.
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

// TestAuditSensitiveKeys_CoverCredentialTable checks every declared provider and payment secret field against
// audit redaction.
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

func TestRedactAuditBody_Empty(t *testing.T) {
	redactor := provideAuditRedactor()
	if got := redactor.RedactBody(nil, "application/json"); got != "" {
		t.Fatalf("expected empty for nil body, got %q", got)
	}
}
