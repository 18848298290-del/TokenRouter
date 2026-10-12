package clientip

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

const forwardedIPSettingsKey = "tokenrouter.forwarded_ip_settings"

var privateNets []*net.IPNet

type forwardedIPSettings struct {
	trustForwarded bool
	headers        []string
}

func init() {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"::1/128",
		"fc00::/7",
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("invalid CIDR: " + cidr)
		}
		privateNets = append(privateNets, block)
	}
}

// SetForwardedIPSettings copies forwarding mode and custom header names into the request snapshot.
func SetForwardedIPSettings(c *gin.Context, enabled bool, headers []string) {
	if c == nil {
		return
	}
	c.Set(forwardedIPSettingsKey, forwardedIPSettings{
		trustForwarded: enabled,
		headers:        append([]string(nil), headers...),
	})
}

func requestForwardedIPSettings(c *gin.Context) (forwardedIPSettings, bool) {
	if c == nil {
		return forwardedIPSettings{}, false
	}
	value, ok := c.Get(forwardedIPSettingsKey)
	if !ok {
		return forwardedIPSettings{}, false
	}
	settings, ok := value.(forwardedIPSettings)
	return settings, ok
}

func requestUsesLegacyForwardedIPTrust(c *gin.Context) bool {
	settings, ok := requestForwardedIPSettings(c)
	return !ok || settings.trustForwarded
}

// GetClientIP resolves the client address using the request's forwarding policy.
// Legacy mode trusts raw forwarding headers; security-sensitive callers should use
// GetTrustedClientIP or GetSecurityClientIP to select the intended trust policy.
func GetClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if !requestUsesLegacyForwardedIPTrust(c) {
		return GetTrustedClientIP(c)
	}

	settings, _ := requestForwardedIPSettings(c)
	customIP, customFallback := resolveCustomForwardedClientIP(c, settings.headers)
	if customIP != "" {
		return customIP
	}

	// Prefer public forwarded addresses over private proxy bridge addresses,
	// including Docker or Nginx addresses supplied in X-Real-IP.
	legacyIP, legacyFallback := resolveLegacyForwardedHeaderIP(c)
	if legacyIP != "" {
		return legacyIP
	}
	if customFallback != "" {
		return customFallback
	}
	if legacyFallback != "" {
		return legacyFallback
	}
	return ipmatch.NormalizeIP(c.ClientIP())
}

func resolveCustomForwardedClientIP(c *gin.Context, headers []string) (string, string) {
	if c == nil {
		return "", ""
	}
	var fallback string
	for _, header := range headers {
		for _, value := range c.Request.Header.Values(header) {
			for candidate := range strings.SplitSeq(value, ",") {
				parsed := net.ParseIP(strings.TrimSpace(candidate))
				if parsed == nil {
					continue
				}
				normalized := parsed.String()
				if isPrivateIP(normalized) {
					if fallback == "" {
						fallback = normalized
					}
					continue
				}
				return normalized, fallback
			}
		}
	}
	return "", fallback
}

func resolveLegacyForwardedHeaderIP(c *gin.Context) (string, string) {
	var fallback string
	if forwarded := normalizeValidIP(c.GetHeader("CF-Connecting-IP")); forwarded != "" {
		fallback = forwarded
		if !isPrivateIP(forwarded) {
			return forwarded, fallback
		}
	}
	if realIP := normalizeValidIP(c.GetHeader("X-Real-IP")); realIP != "" {
		if fallback == "" {
			fallback = realIP
		}
		if !isPrivateIP(realIP) {
			return realIP, fallback
		}
	}
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		for _, candidate := range ips {
			candidate = normalizeValidIP(candidate)
			if candidate != "" && !isPrivateIP(candidate) {
				return candidate, fallback
			}
		}
		if fallback == "" {
			for _, candidate := range ips {
				if candidate = normalizeValidIP(candidate); candidate != "" {
					fallback = candidate
					break
				}
			}
		}
	}
	return "", fallback
}

// GetTrustedClientIP normalizes the client IP resolved by Gin.
// Configure gin.Engine.SetTrustedProxies to define which peers may supply forwarding headers.
// Use this policy for ACL and other security-sensitive decisions.
func GetTrustedClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return ipmatch.NormalizeIP(c.ClientIP())
}

// GetSecurityClientIP resolves the address used by security-sensitive callers.
// The request snapshot overrides trustForwarded. Legacy mode trusts raw headers;
// otherwise Gin resolves the address using server.trusted_proxies.
// @project-doc docs/operations/edge_security.md#trusted_client_ip
func GetSecurityClientIP(c *gin.Context, trustForwarded bool) string {
	if requestSettings, ok := requestForwardedIPSettings(c); ok {
		trustForwarded = requestSettings.trustForwarded
	}
	if trustForwarded {
		return GetClientIP(c)
	}
	return GetTrustedClientIP(c)
}

// normalizeValidIP rejects hostnames, unknown, and other invalid header values and canonicalizes IP literals.
func normalizeValidIP(value string) string {
	normalized := ipmatch.NormalizeIP(value)
	parsed := net.ParseIP(normalized)
	if parsed == nil {
		return ""
	}
	return parsed.String()
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, block := range privateNets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}
