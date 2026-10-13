package middleware

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// CSP preparation connects a per-request nonce with configured browser source
// permissions. These cases inspect nonce encoding, context lookup, enabled and
// excluded response paths, and policy supplementation, while focused benchmarks
// measure nonce calls and fresh middleware request handling.

// TestGenerateNonce samples nonce generation and decodes its padded base64
// representation. The cases check the sixteen-byte payload,
// twenty-four-character text, and distinct values within the generated sample.
func TestGenerateNonce(t *testing.T) {
	t.Run("generates_valid_base64_string", func(t *testing.T) {
		nonce, err := GenerateNonce()
		require.NoError(t, err)

		decoded, err := base64.StdEncoding.DecodeString(nonce)
		require.NoError(t, err)

		assert.Len(t, decoded, 16)
	})

	t.Run("generates_unique_nonces", func(t *testing.T) {
		nonces := make(map[string]bool)
		for range 100 {
			nonce, err := GenerateNonce()
			require.NoError(t, err)
			assert.False(t, nonces[nonce], "nonce should be unique")
			nonces[nonce] = true
		}
	})

	t.Run("nonce_has_expected_length", func(t *testing.T) {
		nonce, err := GenerateNonce()
		require.NoError(t, err)
		// Padded standard base64 encodes sixteen bytes as twenty-four characters.
		assert.Len(t, nonce, 24)
	})
}

// TestGetNonceFromContext reads a stored nonce alongside missing and wrong-type
// entries. It protects the context lookup result used by frontend rendering to
// match the response policy's request nonce.
func TestGetNonceFromContext(t *testing.T) {
	t.Run("returns_nonce_when_present", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		expectedNonce := "test-nonce-123"
		c.Set(CSPNonceKey, expectedNonce)

		nonce := GetNonceFromContext(c)
		assert.Equal(t, expectedNonce, nonce)
	})

	t.Run("returns_empty_string_when_not_present", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		nonce := GetNonceFromContext(c)
		assert.Empty(t, nonce)
	})

	t.Run("returns_empty_for_wrong_type", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		c.Set(CSPNonceKey, 12345)

		nonce := GetNonceFromContext(c)
		assert.Empty(t, nonce)
	})
}

// TestSecurityHeaders varies enablement, policy placeholders, and a gateway
// exclusion path while serving isolated requests. It checks baseline headers,
// default-policy selection, context nonce assignment, replacement consistency,
// and distinct sampled request nonces.
func TestSecurityHeaders(t *testing.T) {
	t.Run("sets_basic_security_headers", func(t *testing.T) {
		cfg := config.CSPConfig{Enabled: false}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
		assert.Equal(t, "strict-origin-when-cross-origin", w.Header().Get("Referrer-Policy"))
	})

	t.Run("csp_disabled_no_csp_header", func(t *testing.T) {
		cfg := config.CSPConfig{Enabled: false}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		assert.Empty(t, w.Header().Get("Content-Security-Policy"))
	})

	t.Run("csp_enabled_sets_csp_header", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "default-src 'self'",
		}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		csp := w.Header().Get("Content-Security-Policy")
		assert.NotEmpty(t, csp)
		// The policy gains a nonce and required integration sources.
		assert.Contains(t, csp, "default-src 'self'")
		assert.Contains(t, csp, "'nonce-")
		assert.Contains(t, csp, CloudflareInsightsDomain)
		assert.Equal(t, 1, countDirectiveValue(csp, "worker-src", TencentCaptchaWorkerSource))
	})

	t.Run("api_route_skips_csp_nonce_generation", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "default-src 'self'; script-src 'self' __CSP_NONCE__",
		}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

		middleware(c)

		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
		assert.Equal(t, "strict-origin-when-cross-origin", w.Header().Get("Referrer-Policy"))
		assert.Empty(t, w.Header().Get("Content-Security-Policy"))
		assert.Empty(t, GetNonceFromContext(c))
	})

	t.Run("csp_enabled_with_nonce_placeholder", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "script-src 'self' __CSP_NONCE__",
		}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		csp := w.Header().Get("Content-Security-Policy")
		assert.NotEmpty(t, csp)
		assert.NotContains(t, csp, "__CSP_NONCE__", "placeholder should be replaced")
		assert.Contains(t, csp, "'nonce-", "should contain nonce directive")

		nonce := GetNonceFromContext(c)
		assert.NotEmpty(t, nonce)
		assert.Contains(t, csp, "'nonce-"+nonce+"'")
	})

	t.Run("uses_default_policy_when_empty", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "",
		}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		csp := w.Header().Get("Content-Security-Policy")
		assert.NotEmpty(t, csp)

		assert.Contains(t, csp, "default-src 'self'")
		assert.Contains(t, csp, TencentCaptchaDomain)
	})

	t.Run("uses_default_policy_when_whitespace_only", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "   \t\n  ",
		}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		csp := w.Header().Get("Content-Security-Policy")
		assert.NotEmpty(t, csp)
		assert.Contains(t, csp, "default-src 'self'")
	})

	t.Run("multiple_nonce_placeholders_replaced", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "script-src __CSP_NONCE__; style-src __CSP_NONCE__",
		}
		middleware := SecurityHeaders(cfg, nil)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		middleware(c)

		csp := w.Header().Get("Content-Security-Policy")
		nonce := GetNonceFromContext(c)

		count := strings.Count(csp, "'nonce-"+nonce+"'")
		assert.Equal(t, 2, count, "both placeholders should be replaced with same nonce")
	})

	t.Run("calls_next_handler", func(t *testing.T) {
		cfg := config.CSPConfig{Enabled: true, Policy: "default-src 'self'"}
		middleware := SecurityHeaders(cfg, nil)

		nextCalled := false
		router := gin.New()
		router.Use(middleware)
		router.GET("/test", func(c *gin.Context) {
			nextCalled = true
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		router.ServeHTTP(w, req)

		assert.True(t, nextCalled, "next handler should be called")
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("nonce_unique_per_request", func(t *testing.T) {
		cfg := config.CSPConfig{
			Enabled: true,
			Policy:  "script-src __CSP_NONCE__",
		}
		middleware := SecurityHeaders(cfg, nil)

		nonces := make(map[string]bool)
		for range 10 {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

			middleware(c)

			nonce := GetNonceFromContext(c)
			assert.False(t, nonces[nonce], "nonce should be unique per request")
			nonces[nonce] = true
		}
	})
}

// TestCSPNonceKey asserts the string used to store nonce text in Gin context.
// That exact name keeps SecurityHeaders assignment and frontend context lookup
// connected.
func TestCSPNonceKey(t *testing.T) {
	t.Run("constant_value", func(t *testing.T) {
		assert.Equal(t, "csp_nonce", CSPNonceKey)
	})
}

// TestNonceTemplate asserts the placeholder text consumed by CSP replacement.
// Its exact spelling protects configured policy templates that expect one
// request nonce to replace every matching position.
func TestNonceTemplate(t *testing.T) {
	t.Run("constant_value", func(t *testing.T) {
		assert.Equal(t, "__CSP_NONCE__", NonceTemplate)
	})
}

// TestEnhanceCSPPolicy supplements sparse policies and policies already
// containing selected permissions. The assertions protect nonce syntax,
// configured integration sources, repeated-value avoidance, and the ten exact
// loopback-import origins.
func TestEnhanceCSPPolicy(t *testing.T) {
	t.Run("adds_nonce_placeholder_if_missing", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self'"
		enhanced := enhanceCSPPolicy(policy)

		assert.Contains(t, enhanced, NonceTemplate)
		assert.Contains(t, enhanced, CloudflareInsightsDomain)
	})

	t.Run("does_not_duplicate_nonce_placeholder", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self' __CSP_NONCE__"
		enhanced := enhanceCSPPolicy(policy)

		count := strings.Count(enhanced, NonceTemplate)
		assert.Equal(t, 1, count)
	})

	t.Run("does_not_duplicate_cloudflare_domain", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self' https://static.cloudflareinsights.com"
		enhanced := enhanceCSPPolicy(policy)

		count := strings.Count(enhanced, CloudflareInsightsDomain)
		assert.Equal(t, 1, count)
	})

	t.Run("adds_tencent_captcha_domain_for_web_sdk", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self' __CSP_NONCE__"
		enhanced := enhanceCSPPolicy(policy)

		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", TencentCaptchaDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "frame-src", TencentCaptchaDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "style-src", TencentCaptchaStaticDomain))
		assert.Contains(t, config.DefaultCSPPolicy, "style-src 'self' 'unsafe-inline' https://*.captcha.gtimg.com")

		// The source list grants separate script, frame, and connection permissions.
		// Assert each configured pair because finding a hostname anywhere in CSP
		// would miss a grant placed in the wrong directive.
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", TencentCaptchaCDNDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", TencentCaptchaGlobalDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", TencentCaptchaGlobalCDNDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", TencentCaptchaPrehandleDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", TencentCaptchaJQueryDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "connect-src", TencentCaptchaDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "connect-src", TencentCaptchaPrehandleDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "connect-src", TencentCaptchaRceDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "frame-src", TencentCaptchaGlobalDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "frame-src", TencentCaptchaPrehandleDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "worker-src", TencentCaptchaWorkerSource))
	})

	t.Run("does_not_duplicate_tencent_captcha_worker_source", func(t *testing.T) {
		policy := "default-src 'self'; worker-src 'self' blob:; script-src 'self' __CSP_NONCE__"
		enhanced := enhanceCSPPolicy(policy)

		assert.Equal(t, 1, countDirectiveValue(enhanced, "worker-src", TencentCaptchaWorkerSource))
	})

	t.Run("adds_only_the_tf_cli_loopback_ports", func(t *testing.T) {
		policy := "default-src 'self'; connect-src 'self'"
		enhanced := enhanceCSPPolicy(policy)

		// The loopback importer listens on these ten configured ports. Check each
		// exact origin so supplementation does not broaden permission to arbitrary
		// local ports.
		for port := 43110; port <= 43119; port++ {
			origin := fmt.Sprintf("http://127.0.0.1:%d", port)
			assert.Equal(t, 1, countDirectiveValue(enhanced, "connect-src", origin))
		}
		assert.NotContains(t, enhanced, "http://127.0.0.1:*")
		assert.NotContains(t, enhanced, "http://localhost")
	})

	t.Run("default_policy_already_carries_required_domains", func(t *testing.T) {
		// Default and runtime-supplemented policies advertise the same configured
		// integration permissions. Comparing each pair catches a missing default
		// source even when runtime insertion would supply it.
		for _, required := range requiredCSPDirectiveValues {
			assert.Equal(t, 1, countDirectiveValue(config.DefaultCSPPolicy, required.directive, required.value),
				"DefaultCSPPolicy 缺少 %s %s", required.directive, required.value)
		}
	})

	t.Run("adds_aliyun_captcha_domain_for_web_sdk", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self' __CSP_NONCE__; style-src 'self'"
		enhanced := enhanceCSPPolicy(policy)

		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", AliyunCaptchaStaticDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "style-src", AliyunCaptchaStaticDomain))
		assert.Contains(t, config.DefaultCSPPolicy, "https://*.alicdn.com")
	})

	t.Run("handles_policy_without_script_src", func(t *testing.T) {
		policy := "default-src 'self'"
		enhanced := enhanceCSPPolicy(policy)

		assert.Contains(t, enhanced, "script-src")
		assert.Contains(t, enhanced, NonceTemplate)
		assert.Contains(t, enhanced, CloudflareInsightsDomain)
	})

	t.Run("preserves_existing_nonce", func(t *testing.T) {
		policy := "script-src 'self' 'nonce-existing'"
		enhanced := enhanceCSPPolicy(policy)

		assert.NotContains(t, enhanced, NonceTemplate)
		assert.Contains(t, enhanced, "'nonce-existing'")
	})

	t.Run("adds_airwallex_domains_for_payment_sdk", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self' __CSP_NONCE__; style-src 'self'; frame-src 'self'"
		enhanced := enhanceCSPPolicy(policy)

		assert.Contains(t, enhanced, "script-src 'self' __CSP_NONCE__")
		assert.Contains(t, enhanced, AirwallexStaticDomain)
		assert.Contains(t, enhanced, AirwallexCheckoutDomain)
		assert.Contains(t, enhanced, AirwallexDemoStaticDomain)
		assert.Contains(t, enhanced, AirwallexDemoCheckoutDomain)
		assert.Contains(t, enhanced, "style-src 'self'")
		assert.Contains(t, enhanced, "frame-src 'self'")
	})

	t.Run("does_not_duplicate_airwallex_domains", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self' https://static.airwallex.com https://static-demo.airwallex.com; frame-src https://checkout.airwallex.com https://checkout-demo.airwallex.com"
		enhanced := enhanceCSPPolicy(policy)

		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", AirwallexStaticDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", AirwallexCheckoutDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "style-src", AirwallexStaticDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "style-src", AirwallexCheckoutDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "frame-src", AirwallexCheckoutDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", AirwallexDemoStaticDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "script-src", AirwallexDemoCheckoutDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "style-src", AirwallexDemoStaticDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "style-src", AirwallexDemoCheckoutDomain))
		assert.Equal(t, 1, countDirectiveValue(enhanced, "frame-src", AirwallexDemoCheckoutDomain))
	})
}

// TestAddToDirective supplies existing, missing, final unterminated, and empty
// directive lists. The returned text contains the requested source and the
// expected directive fragment for each represented insertion case.
func TestAddToDirective(t *testing.T) {
	t.Run("adds_to_existing_directive", func(t *testing.T) {
		policy := "script-src 'self'; style-src 'self'"
		result := addToDirective(policy, "script-src", "https://example.com")

		assert.Contains(t, result, "script-src 'self' https://example.com")
	})

	t.Run("creates_directive_if_not_exists", func(t *testing.T) {
		policy := "default-src 'self'"
		result := addToDirective(policy, "script-src", "https://example.com")

		assert.Contains(t, result, "script-src")
		assert.Contains(t, result, "https://example.com")
	})

	t.Run("handles_directive_at_end_without_semicolon", func(t *testing.T) {
		policy := "default-src 'self'; script-src 'self'"
		result := addToDirective(policy, "script-src", "https://example.com")

		assert.Contains(t, result, "https://example.com")
	})

	t.Run("handles_empty_policy", func(t *testing.T) {
		policy := ""
		result := addToDirective(policy, "script-src", "https://example.com")

		assert.Contains(t, result, "script-src")
		assert.Contains(t, result, "https://example.com")
	})
}

// BenchmarkGenerateNonce repeatedly generates nonce text through the entropy
// and base64 path. It measures per-call overhead while discarding the results,
// with no HTTP request construction included in the timed loop.
func BenchmarkGenerateNonce(b *testing.B) {
	for range b.N {
		_, _ = GenerateNonce()
	}
}

// BenchmarkSecurityHeadersMiddleware constructs the enabled policy before
// resetting its timer. Each timed iteration creates a fresh recorder and Gin
// request context, then exercises nonce generation and response-header
// assignment.
func BenchmarkSecurityHeadersMiddleware(b *testing.B) {
	cfg := config.CSPConfig{
		Enabled: true,
		Policy:  "script-src 'self' __CSP_NONCE__",
	}
	middleware := SecurityHeaders(cfg, nil)

	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		middleware(c)
	}
}

// TestGoogleIdentityCSPPreservesOfficialAccountsOrigins compares the configured
// Google source strings in default and supplemented policies. Both forms grant
// the listed script, style, frame, and connection values and exclude the
// substituted provider-domain spelling.
func TestGoogleIdentityCSPPreservesOfficialAccountsOrigins(t *testing.T) {
	expected := map[string]string{
		"script-src":  "https://accounts.google.com/gsi/client",
		"style-src":   "https://accounts.google.com/gsi/style",
		"connect-src": "https://accounts.google.com/gsi/",
		"frame-src":   "https://accounts.google.com/gsi/",
	}
	for _, policy := range []string{config.DefaultCSPPolicy, enhanceCSPPolicy("default-src 'self'; script-src 'self'")} {
		directives := map[string][]string{}
		for part := range strings.SplitSeq(policy, ";") {
			fields := strings.Fields(part)
			if len(fields) > 0 {
				directives[fields[0]] = fields[1:]
			}
		}
		for directive, origin := range expected {
			require.Contains(t, directives[directive], origin, directive)
		}
		require.NotContains(t, policy, "providers.google.com")
	}
}

// countDirectiveValue counts exact source tokens in the first matching
// semicolon-separated directive. Matching is case-sensitive, repeated values
// count individually, and missing directives return zero for the
// supplementation assertions.
func countDirectiveValue(policy, directive, value string) int {
	for rawDirective := range strings.SplitSeq(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(rawDirective))
		if len(fields) == 0 || fields[0] != directive {
			continue
		}
		count := 0
		for _, field := range fields[1:] {
			if field == value {
				count++
			}
		}
		return count
	}
	return 0
}
