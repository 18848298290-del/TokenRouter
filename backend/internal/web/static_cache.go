package web

import (
	"net/http"
	"path"
	"strings"
)

// static_cache.go recognizes fingerprinted embedded assets and applies their
// immutable browser-cache policy.
// Bundle assets whose filenames contain an eight-character fingerprint can be
// reused under a long immutable cache lifetime. The recognizer checks the
// assets path and suffix spelling, and callers apply this policy after disk
// overrides so replaceable local files keep their ordinary caching behavior.

// staticAssetsCacheControl supplies the one-year immutable browser policy for
// recognized embedded fingerprints, applied after serving decisions have
// excluded replaceable local override files.
const staticAssetsCacheControl = "public, max-age=31536000, immutable"

// isFingerprintedEmbeddedAssetPath reports whether cleanPath has the expected
// assets prefix and filename suffix. It accepts a nonempty extension after a
// hyphen and eight URL-safe characters; this spelling check does not establish
// file existence.
func isFingerprintedEmbeddedAssetPath(cleanPath string) bool {
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	if !strings.HasPrefix(cleanPath, "assets/") {
		return false
	}

	filename := path.Base(cleanPath)
	extension := path.Ext(filename)
	stem := strings.TrimSuffix(filename, extension)
	// Vite bundle names use eight fingerprint characters before the extension.
	const fingerprintLength = 8
	delimiterIndex := len(stem) - fingerprintLength - 1
	if extension == "" || delimiterIndex < 1 || stem[delimiterIndex] != '-' {
		return false
	}

	// Match the bundle's URL-safe fingerprint alphabet rather than interpreting its
	// characters as hexadecimal; generated asset names can contain letters, underscores, and hyphens.
	fingerprint := stem[delimiterIndex+1:]
	for _, char := range fingerprint {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

// applyStaticAssetCacheHeaders sets the immutable policy for a recognized
// embedded fingerprint path. Nil headers and other path spellings remain
// unchanged; callers finish local override selection first because overrides
// can change under the same name.
func applyStaticAssetCacheHeaders(header http.Header, cleanPath string) {
	if header == nil || !isFingerprintedEmbeddedAssetPath(cleanPath) {
		return
	}
	header.Set("Cache-Control", staticAssetsCacheControl)
}
