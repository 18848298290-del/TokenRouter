package httpapi

import (
	"net/http"
	"strings"
)

// IsBatchImageManagementRequest identifies batch queries, deletion, and cancellation rather than new generation.
func IsBatchImageManagementRequest(method, path string) bool {
	path = strings.TrimRight(path, "/")
	const root = "/v1/images/batches"
	if method == http.MethodGet && (path == root || strings.HasPrefix(path, root+"/")) {
		return true
	}
	if method == http.MethodDelete && strings.HasPrefix(path, root+"/") {
		return true
	}
	return method == http.MethodPost && strings.HasPrefix(path, root+"/") && strings.HasSuffix(path, "/cancel")
}

// IsBatchImageBillingBypassRequest identifies existing-batch management; model lists still undergo billing checks.
func IsBatchImageBillingBypassRequest(method, path string) bool {
	path = strings.TrimRight(path, "/")
	return path != "/v1/images/batches/models" && IsBatchImageManagementRequest(method, path)
}

// IsAPIKeyNonConsumingRequest identifies queries, counting, and resource cleanup exempt from consumption limits.
// Unknown paths undergo consuming-request team-limit checks.
func IsAPIKeyNonConsumingRequest(method, path string) bool {
	path = strings.TrimRight(path, "/")
	if IsAPIKeyUsageRequest(method, path) {
		return true
	}
	if method == http.MethodGet {
		if strings.HasSuffix(path, "/models") || IsBatchImageManagementRequest(method, path) || IsGrokVideoTaskRead(method, path) {
			return true
		}
	}
	if IsBatchImageManagementRequest(method, path) {
		return true
	}
	if method == http.MethodPost && strings.HasSuffix(path, "/messages/count_tokens") {
		return true
	}
	return false
}

// IsAPIKeyUsageRequest identifies the two Claude-style key usage endpoints.
// They expose key state even when subscription or key quotas are exhausted.
func IsAPIKeyUsageRequest(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	switch strings.TrimRight(path, "/") {
	case "/v1/usage", "/antigravity/v1/usage":
		return true
	default:
		return false
	}
}
