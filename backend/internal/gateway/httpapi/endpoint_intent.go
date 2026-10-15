package httpapi

import (
	"net/http"
	"strings"
)

// endpoint_intent.go classifies endpoint paths and methods for funding bypass
// and non-consuming request admission.
// Existing batch resources and usage reads have different admission needs from
// new inference. Path and method classifiers identify those operations before
// consumption checks, while the batch-model catalog still requires funding eligibility
// even though broader request-limit classification treats model lists as non-consuming.

// IsBatchImageManagementRequest identifies batch reads, deletion, and cancellation.
// It removes trailing slashes before checking method and path. GET accepts
// the batch root or descendants, DELETE requires a descendant, and POST requires
// the descendant cancel suffix.
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

// IsBatchImageBillingBypassRequest identifies batch management that skips funding enforcement.
// It excludes the exact batch-model catalog after removing trailing slashes.
// Other paths follow IsBatchImageManagementRequest, keeping model discovery subject
// to the funding checks performed by authentication.
func IsBatchImageBillingBypassRequest(method, path string) bool {
	path = strings.TrimRight(path, "/")
	return path != "/v1/images/batches/models" && IsBatchImageManagementRequest(method, path)
}

// IsAPIKeyNonConsumingRequest identifies routes exempt from member and key request-limit checks.
// Unknown paths retain those checks. Matching removes
// trailing slashes; GET model suffixes and existing-resource operations qualify,
// as do batch deletion or cancellation and POST Messages count-token suffixes.
// This classification is separate from the narrower billing-bypass selection.
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
// Only GET on the supported usage paths qualifies after trailing slashes are
// removed; other methods or similar endpoint names keep their ordinary admission path.
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
