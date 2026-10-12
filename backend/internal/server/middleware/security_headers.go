package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/httpconfig"
)

const (
	// CSPNonceKey is the context key for storing the CSP nonce.
	CSPNonceKey = "csp_nonce"
	// NonceTemplate is the placeholder in CSP policy for nonce.
	NonceTemplate = "__CSP_NONCE__"
	// CloudflareInsightsDomain is the domain for Cloudflare Web Analytics.
	CloudflareInsightsDomain = "https://static.cloudflareinsights.com"
	// GoogleIdentityScriptURL is the Google Identity Services script URL.
	GoogleIdentityScriptURL = "https://accounts.google.com/gsi/client"
	// GoogleIdentityResourceURL is the source for One Tap frames and network requests.
	GoogleIdentityResourceURL = "https://accounts.google.com/gsi/"
	// GoogleIdentityStyleURL is the stylesheet URL for the One Tap UI.
	GoogleIdentityStyleURL = "https://accounts.google.com/gsi/style"
	// TencentCaptchaDomain is the Chinese Tencent CAPTCHA 2.0 Web SDK origin.
	TencentCaptchaDomain = "https://turing.captcha.qcloud.com"
	// TencentCaptchaStaticDomain matches Tencent CAPTCHA static resource hosts.
	TencentCaptchaStaticDomain = "https://*.captcha.gtimg.com"
	// TencentCaptchaCDNDomain serves the Chinese CAPTCHA SDK's core JavaScript.
	// TJCaptcha.js loads /1/tgJCap.*.js from this host, which also needs script-src permission.
	TencentCaptchaCDNDomain = "https://turing.captcha.gtimg.com"
	// TencentCaptchaGlobalDomain serves the international CAPTCHA SDK and challenge frames.
	TencentCaptchaGlobalDomain = "https://ca.turing.captcha.qcloud.com"
	// TencentCaptchaGlobalCDNDomain serves the international CAPTCHA SDK's core JavaScript.
	TencentCaptchaGlobalCDNDomain = "https://global.turing.captcha.gtimg.com"
	// TencentCaptchaPrehandleDomain serves CAPTCHA preprocessing scripts and requests.
	TencentCaptchaPrehandleDomain = "https://www.tycaptcha.com"
	// TencentCaptchaJQueryDomain serves jQuery loaded by the Chinese CAPTCHA entry script.
	TencentCaptchaJQueryDomain = "https://cloudcache.tencentcs.com"
	// TencentCaptchaRceDomain serves international CAPTCHA risk checks.
	TencentCaptchaRceDomain = "https://rce.tencentrio.com"
	// TencentCaptchaWorkerSource allows blob-backed workers for international CAPTCHA.
	TencentCaptchaWorkerSource = "blob:"
	// AliyunCaptchaStaticDomain matches Aliyun CAPTCHA SDK and stylesheet hosts.
	AliyunCaptchaStaticDomain = "https://*.alicdn.com"
	// StripeDomain is the domain for Stripe.js SDK.
	StripeDomain = "https://*.stripe.com"
	// AirwallexStaticDomain serves the production Airwallex SDK.
	AirwallexStaticDomain = "https://static.airwallex.com"
	// AirwallexCheckoutDomain serves production checkout elements and frames.
	AirwallexCheckoutDomain = "https://checkout.airwallex.com"
	// AirwallexDemoStaticDomain serves the sandbox Airwallex SDK.
	AirwallexDemoStaticDomain = "https://static-demo.airwallex.com"
	// AirwallexDemoCheckoutDomain serves sandbox checkout elements and frames.
	AirwallexDemoCheckoutDomain = "https://checkout-demo.airwallex.com"
)

var requiredCSPDirectiveValues = []struct {
	directive string
	value     string
}{
	{"script-src", CloudflareInsightsDomain},
	{"script-src", GoogleIdentityScriptURL},
	{"frame-src", GoogleIdentityResourceURL},
	{"connect-src", GoogleIdentityResourceURL},
	{"style-src", GoogleIdentityStyleURL},
	{"script-src", TencentCaptchaDomain},
	{"frame-src", TencentCaptchaDomain},
	{"style-src", TencentCaptchaStaticDomain},
	{"script-src", TencentCaptchaCDNDomain},
	{"script-src", TencentCaptchaGlobalDomain},
	{"script-src", TencentCaptchaGlobalCDNDomain},
	{"script-src", TencentCaptchaPrehandleDomain},
	{"script-src", TencentCaptchaJQueryDomain},
	{"connect-src", TencentCaptchaDomain},
	{"connect-src", TencentCaptchaPrehandleDomain},
	{"connect-src", TencentCaptchaRceDomain},
	{"frame-src", TencentCaptchaGlobalDomain},
	{"frame-src", TencentCaptchaPrehandleDomain},
	{"worker-src", TencentCaptchaWorkerSource},
	{"script-src", AliyunCaptchaStaticDomain},
	{"style-src", AliyunCaptchaStaticDomain},
	{"script-src", StripeDomain},
	{"frame-src", StripeDomain},
	{"script-src", AirwallexStaticDomain},
	{"script-src", AirwallexCheckoutDomain},
	{"style-src", AirwallexStaticDomain},
	{"style-src", AirwallexCheckoutDomain},
	{"frame-src", AirwallexCheckoutDomain},
	{"script-src", AirwallexDemoStaticDomain},
	{"script-src", AirwallexDemoCheckoutDomain},
	{"style-src", AirwallexDemoStaticDomain},
	{"style-src", AirwallexDemoCheckoutDomain},
	{"frame-src", AirwallexDemoCheckoutDomain},
	// @project-doc docs/interfaces/tf_cli_web_import.md#browser_security_headers
	{"connect-src", "http://127.0.0.1:43110"},
	{"connect-src", "http://127.0.0.1:43111"},
	{"connect-src", "http://127.0.0.1:43112"},
	{"connect-src", "http://127.0.0.1:43113"},
	{"connect-src", "http://127.0.0.1:43114"},
	{"connect-src", "http://127.0.0.1:43115"},
	{"connect-src", "http://127.0.0.1:43116"},
	{"connect-src", "http://127.0.0.1:43117"},
	{"connect-src", "http://127.0.0.1:43118"},
	{"connect-src", "http://127.0.0.1:43119"},
}

// GenerateNonce generates a cryptographically secure random nonce.
// It returns an error if crypto/rand cannot supply randomness.
func GenerateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate CSP nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// GetNonceFromContext retrieves the CSP nonce from gin context.
func GetNonceFromContext(c *gin.Context) string {
	if nonce, exists := c.Get(CSPNonceKey); exists {
		if s, ok := nonce.(string); ok {
			return s
		}
	}
	return ""
}

// SecurityHeaders sets baseline security headers for all responses.
// getFrameSrcOrigins is an optional function that returns extra origins to inject into frame-src;
// pass nil to disable dynamic frame-src injection.
func SecurityHeaders(cfg httpconfig.CSPConfig, getFrameSrcOrigins func() []string) gin.HandlerFunc {
	policy := strings.TrimSpace(cfg.Policy)
	if policy == "" {
		policy = httpconfig.DefaultCSPPolicy
	}

	// Supplement configured policies with the sources required by frontend integrations.
	policy = enhanceCSPPolicy(policy)

	return func(c *gin.Context) {
		finalPolicy := policy
		if getFrameSrcOrigins != nil {
			for _, origin := range getFrameSrcOrigins() {
				if origin != "" {
					finalPolicy = addToDirective(finalPolicy, "frame-src", origin)
				}
			}
		}

		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		if isAPIRoutePath(c) {
			c.Next()
			return
		}

		if cfg.Enabled {
			nonce, err := GenerateNonce()
			if err != nil {
				// Allow inline scripts if nonce generation fails.
				log.Printf("[SecurityHeaders] %v — 降级为无 nonce 的 CSP", err)
				c.Header("Content-Security-Policy", strings.ReplaceAll(finalPolicy, NonceTemplate, "'unsafe-inline'"))
			} else {
				c.Set(CSPNonceKey, nonce)
				c.Header("Content-Security-Policy", strings.ReplaceAll(finalPolicy, NonceTemplate, "'nonce-"+nonce+"'"))
			}
		}
		c.Next()
	}
}

func isAPIRoutePath(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := c.Request.URL.Path
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/v1beta/") ||
		strings.HasPrefix(path, "/antigravity/") ||
		strings.HasPrefix(path, "/responses") ||
		strings.HasPrefix(path, "/images")
}

// enhanceCSPPolicy adds nonce support and sources required by frontend integrations.
// This lets CAPTCHA and payment components load with older custom policies.
func enhanceCSPPolicy(policy string) string {
	if !strings.Contains(policy, NonceTemplate) && !strings.Contains(policy, "'nonce-") {
		policy = addToDirective(policy, "script-src", NonceTemplate)
	}

	for _, required := range requiredCSPDirectiveValues {
		if !directiveHasValue(policy, required.directive, required.value) {
			policy = addToDirective(policy, required.directive, required.value)
		}
	}

	return policy
}

func directiveHasValue(policy, directive, value string) bool {
	for rawDirective := range strings.SplitSeq(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(rawDirective))
		if len(fields) == 0 || fields[0] != directive {
			continue
		}
		return slices.Contains(fields[1:], value)
	}
	return false
}

// addToDirective adds a value to a specific CSP directive.
// A missing directive is inserted after a terminated default-src directive, or prepended.
func addToDirective(policy, directive, value string) string {
	directivePrefix := directive + " "
	idx := strings.Index(policy, directivePrefix)

	if idx == -1 {
		defaultSrcIdx := strings.Index(policy, "default-src ")
		if defaultSrcIdx != -1 {
			endIdx := strings.Index(policy[defaultSrcIdx:], ";")
			if endIdx != -1 {
				insertPos := defaultSrcIdx + endIdx + 1

				return policy[:insertPos] + " " + directive + " 'self' " + value + ";" + policy[insertPos:]
			}
		}

		return directive + " 'self' " + value + "; " + policy
	}

	endIdx := strings.Index(policy[idx:], ";")

	if endIdx == -1 {
		return policy + " " + value
	}

	insertPos := idx + endIdx
	return policy[:insertPos] + " " + value + policy[insertPos:]
}
