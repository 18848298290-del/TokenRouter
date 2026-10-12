package anthropic

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

const (
	ClaudeCLIUserAgentProduct = "claude-cli"
	// MaxFingerprintUserAgentLength bounds values persisted in the provider cache.
	MaxFingerprintUserAgentLength = 256
	// MaxClaudeCLIMajorVersionSkew permits two major versions ahead of CLIVersion
	// while rejecting sentinel versions such as 999.
	MaxClaudeCLIMajorVersionSkew = 2
)

var (
	// UserAgentVersionRegex finds a three-part version after a slash.
	UserAgentVersionRegex = regexp.MustCompile(`/(\d+)\.(\d+)\.(\d+)`)

	// FingerprintUserAgentPattern requires whitespace or end-of-input after the version.
	FingerprintUserAgentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/\d+\.\d+\.\d+(\s|$)`)

	// DefaultFingerprint supplies values missing from client headers.
	DefaultFingerprint = Fingerprint{
		UserAgent:               "claude-cli/" + CLIVersion() + " (external, cli)",
		StainlessLang:           "js",
		StainlessPackageVersion: "0.94.0",
		StainlessOS:             "Linux",
		StainlessArch:           "arm64",
		StainlessRuntime:        "node",
		StainlessRuntimeVersion: "v24.3.0",
	}
)

// Fingerprint represents provider fingerprint data.
type Fingerprint struct {
	ClientID                string
	UserAgent               string
	StainlessLang           string
	StainlessPackageVersion string
	StainlessOS             string
	StainlessArch           string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	UpdatedAt               int64 `json:",omitempty"` // Unix seconds of the last write, used for lazy TTL renewal.
}

// FingerprintCache stores provider fingerprints and masked session IDs.
type FingerprintCache interface {
	GetFingerprint(ctx context.Context, providerID int64) (*Fingerprint, error)
	SetFingerprint(ctx context.Context, providerID int64, fp *Fingerprint) error
	// GetMaskedSessionID returns the provider's cached session UUID, or empty
	// when absent or expired after 15 minutes without renewal.
	GetMaskedSessionID(ctx context.Context, providerID int64) (string, error)
	// SetMaskedSessionID stores the session UUID and renews its 15-minute TTL.
	SetMaskedSessionID(ctx context.Context, providerID int64, sessionID string) error
}

// RequestFingerprint manages OAuth provider request identities.
type RequestFingerprint struct {
	cache FingerprintCache
}

// IsAcceptableFingerprintUserAgent validates a UA before it becomes persistent
// provider identity. Monotonic upgrades and renewal would otherwise retain
// local build suffixes or implausible versions across later requests.
func IsAcceptableFingerprintUserAgent(userAgent string) bool {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" || len(userAgent) > MaxFingerprintUserAgentLength {
		return false
	}
	if !FingerprintUserAgentPattern.MatchString(userAgent) {
		return false
	}
	// Other products use the format check without the Claude CLI version window.
	if ExtractProduct(userAgent) != ClaudeCLIUserAgentProduct {
		return true
	}
	major, _, _, ok := ParseUserAgentVersion(userAgent)
	if !ok {
		return false
	}
	currentMajor, _, _, currentOK := ParseUserAgentVersion(ClaudeCLIUserAgentProduct + "/" + CLIVersion())
	if !currentOK {
		return true
	}
	return major <= currentMajor+MaxClaudeCLIMajorVersionSkew
}

// NewRequestFingerprint uses cache to store provider request identities.
func NewRequestFingerprint(cache FingerprintCache) *RequestFingerprint {
	return &RequestFingerprint{cache: cache}
}

// GetOrCreateFingerprint loads, repairs, or upgrades a provider fingerprint.
// Cache misses or read errors create one from client headers and a new ClientID.
// Cache write failures are logged; the selected fingerprint is still returned.
func (s *RequestFingerprint) GetOrCreateFingerprint(ctx context.Context, providerID int64, headers http.Header) (*Fingerprint, error) {
	// Use the same UA validation for creation, upgrades, and cached-value repair.
	clientUA := strings.TrimSpace(headers.Get("User-Agent"))
	uaAcceptable := IsAcceptableFingerprintUserAgent(clientUA)

	cached, err := s.cache.GetFingerprint(ctx, providerID)
	if err == nil && cached != nil {
		needWrite := false

		// Log invalid client versions only when they would otherwise upgrade the cache.
		if !uaAcceptable && clientUA != "" && IsNewerVersion(clientUA, cached.UserAgent) {
			logger.LegacyPrintf("service.identity",
				"Rejected fingerprint user-agent for provider %d: %q (malformed or implausible version)",
				providerID, clientUA)
		}

		if !IsAcceptableFingerprintUserAgent(cached.UserAgent) {
			// Repair invalid cached UAs before active traffic can renew them indefinitely.
			poisonedUA := cached.UserAgent
			if uaAcceptable {
				MergeHeadersIntoFingerprint(cached, headers)
			} else {
				cached.UserAgent = DefaultFingerprint.UserAgent
			}
			needWrite = true
			logger.LegacyPrintf("service.identity",
				"Replaced malformed cached fingerprint for provider %d: %q -> %q",
				providerID, poisonedUA, cached.UserAgent)
		} else if uaAcceptable && IsNewerVersion(clientUA, cached.UserAgent) {
			// Merge supplied fields on upgrades so missing SDK headers retain their
			// cached values rather than mismatched defaults.
			MergeHeadersIntoFingerprint(cached, headers)
			needWrite = true
			logger.LegacyPrintf("service.identity", "Updated fingerprint for provider %d: %s (merge update)", providerID, clientUA)
		}

		if !needWrite && time.Since(time.Unix(cached.UpdatedAt, 0)) > 24*time.Hour {
			// Renew lazily after 24 hours since the last write.
			needWrite = true
		}

		if needWrite {
			cached.UpdatedAt = time.Now().Unix()
			if err := s.cache.SetFingerprint(ctx, providerID, cached); err != nil {
				logger.LegacyPrintf("service.identity", "Warning: failed to refresh fingerprint for provider %d: %v", providerID, err)
			}
		}
		return cached, nil
	}

	// Creation also validates the UA before persistence.
	if !uaAcceptable && clientUA != "" {
		logger.LegacyPrintf("service.identity",
			"Rejected fingerprint user-agent for provider %d: %q (malformed or implausible version)",
			providerID, clientUA)
	}
	fp := s.createFingerprintFromHeaders(headers)

	fp.ClientID = GenerateClientID()
	fp.UpdatedAt = time.Now().Unix()

	// Write the fingerprint; active requests renew it lazily.
	if err := s.cache.SetFingerprint(ctx, providerID, fp); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache fingerprint for provider %d: %v", providerID, err)
	}

	logger.LegacyPrintf("service.identity", "Created new fingerprint for provider %d with client_id: %s", providerID, fp.ClientID)
	return fp, nil
}

// createFingerprintFromHeaders fills missing headers from DefaultFingerprint.
func (s *RequestFingerprint) createFingerprintFromHeaders(headers http.Header) *Fingerprint {
	fp := &Fingerprint{}

	// Persist only an acceptable client UA.
	if ua := strings.TrimSpace(headers.Get("User-Agent")); IsAcceptableFingerprintUserAgent(ua) {
		fp.UserAgent = ua
	} else {
		fp.UserAgent = DefaultFingerprint.UserAgent
	}

	fp.StainlessLang = GetHeaderOrDefault(headers, "X-Stainless-Lang", DefaultFingerprint.StainlessLang)
	fp.StainlessPackageVersion = GetHeaderOrDefault(headers, "X-Stainless-Package-Version", DefaultFingerprint.StainlessPackageVersion)
	fp.StainlessOS = GetHeaderOrDefault(headers, "X-Stainless-OS", DefaultFingerprint.StainlessOS)
	fp.StainlessArch = GetHeaderOrDefault(headers, "X-Stainless-Arch", DefaultFingerprint.StainlessArch)
	fp.StainlessRuntime = GetHeaderOrDefault(headers, "X-Stainless-Runtime", DefaultFingerprint.StainlessRuntime)
	fp.StainlessRuntimeVersion = GetHeaderOrDefault(headers, "X-Stainless-Runtime-Version", DefaultFingerprint.StainlessRuntimeVersion)

	return fp
}

// MergeHeadersIntoFingerprint overwrites fingerprint fields with nonempty
// client headers, retaining cached values for omitted headers. The caller
// validates the UA before using this for upgrades or repair.
func MergeHeadersIntoFingerprint(fp *Fingerprint, headers http.Header) {
	if ua := headers.Get("User-Agent"); ua != "" {
		fp.UserAgent = ua
	}
	MergeHeader(headers, "X-Stainless-Lang", &fp.StainlessLang)
	MergeHeader(headers, "X-Stainless-Package-Version", &fp.StainlessPackageVersion)
	MergeHeader(headers, "X-Stainless-OS", &fp.StainlessOS)
	MergeHeader(headers, "X-Stainless-Arch", &fp.StainlessArch)
	MergeHeader(headers, "X-Stainless-Runtime", &fp.StainlessRuntime)
	MergeHeader(headers, "X-Stainless-Runtime-Version", &fp.StainlessRuntimeVersion)
}

// MergeHeader overwrites target when the header value is nonempty.
func MergeHeader(headers http.Header, key string, target *string) {
	if v := headers.Get(key); v != "" {
		*target = v
	}
}

// GetHeaderOrDefault returns a nonempty header value or defaultValue.
func GetHeaderOrDefault(headers http.Header, key, defaultValue string) string {
	if v := headers.Get(key); v != "" {
		return v
	}
	return defaultValue
}

// ApplyFingerprint overwrites UA and SDK headers with nonempty fingerprint
// values, retaining CLI wire casing such as X-Stainless-OS.
func (s *RequestFingerprint) ApplyFingerprint(req *http.Request, fp *Fingerprint) {
	if fp == nil {
		return
	}

	if fp.UserAgent != "" {
		SetHeaderRaw(req.Header, "User-Agent", fp.UserAgent)
	}

	if fp.StainlessLang != "" {
		SetHeaderRaw(req.Header, "X-Stainless-Lang", fp.StainlessLang)
	}
	if fp.StainlessPackageVersion != "" {
		SetHeaderRaw(req.Header, "X-Stainless-Package-Version", fp.StainlessPackageVersion)
	}
	if fp.StainlessOS != "" {
		SetHeaderRaw(req.Header, "X-Stainless-OS", fp.StainlessOS)
	}
	if fp.StainlessArch != "" {
		SetHeaderRaw(req.Header, "X-Stainless-Arch", fp.StainlessArch)
	}
	if fp.StainlessRuntime != "" {
		SetHeaderRaw(req.Header, "X-Stainless-Runtime", fp.StainlessRuntime)
	}
	if fp.StainlessRuntimeVersion != "" {
		SetHeaderRaw(req.Header, "X-Stainless-Runtime-Version", fp.StainlessRuntimeVersion)
	}
}

// RewriteUserID replaces identity components in metadata.user_id and derives
// a provider-scoped session UUID. It accepts both metadata formats and selects
// the output format from fingerprintUA. The JSON path update preserves other
// body fields, including signed thinking blocks. Invalid metadata is unchanged.
func (s *RequestFingerprint) RewriteUserID(body []byte, providerID int64, providerUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	if len(body) == 0 || providerUUID == "" || cachedClientID == "" {
		return body, nil
	}

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return body, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return body, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return body, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return body, nil
	}

	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return body, nil
	}

	sessionTail := parsed.SessionID // Client-supplied session UUID.

	// Scope the client session to this provider before formatting it as a UUID.
	seed := fmt.Sprintf("%d::%s", providerID, sessionTail)
	newSessionHash := GenerateUUIDFromSeed(seed)

	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(cachedClientID, providerUUID, newSessionHash, version)
	if newUserID == userID {
		return body, nil
	}

	newBody, err := sjson.SetBytes(body, "metadata.user_id", newUserID)
	if err != nil {
		return body, nil
	}
	return newBody, nil
}

// RewriteUserIDWithMasking applies RewriteUserID, then optionally substitutes
// a provider-wide session UUID renewed on each request with a 15-minute TTL.
// Cache read failure leaves the ordinary rewrite in place; write failure is
// logged while the selected ID is still used. Only metadata.user_id is replaced.
func (s *RequestFingerprint) RewriteUserIDWithMasking(ctx context.Context, body []byte, providerID int64, masking bool, providerUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	newBody, err := s.RewriteUserID(body, providerID, providerUUID, cachedClientID, fingerprintUA)
	if err != nil {
		return newBody, err
	}

	if !masking {
		return newBody, nil
	}

	metadata := gjson.GetBytes(newBody, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return newBody, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return newBody, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return newBody, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return newBody, nil
	}

	uidParsed := ParseMetadataUserID(userID)
	if uidParsed == nil {
		return newBody, nil
	}

	// Load the provider-wide masked session UUID.
	maskedSessionID, err := s.cache.GetMaskedSessionID(ctx, providerID)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to get masked session ID for provider %d: %v", providerID, err)
		return newBody, nil
	}

	if maskedSessionID == "" {
		// A missing or expired session starts a new mask.
		maskedSessionID = GenerateRandomUUID()
		logger.LegacyPrintf("service.identity", "Generated new masked session ID for provider %d: %s", providerID, maskedSessionID)
	}

	// Renew the idle expiry on each request.
	if err := s.cache.SetMaskedSessionID(ctx, providerID, maskedSessionID); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to set masked session ID for provider %d: %v", providerID, err)
	}

	// Use the same output format as the ordinary identity rewrite.
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(uidParsed.DeviceID, uidParsed.AccountUUID, maskedSessionID, version)

	slog.Debug("session_id_masking_applied",
		"provider_id", providerID,
		"before", userID,
		"after", newUserID,
	)

	if newUserID == userID {
		return newBody, nil
	}

	maskedBody, setErr := sjson.SetBytes(newBody, "metadata.user_id", newUserID)
	if setErr != nil {
		return newBody, nil
	}
	return maskedBody, nil
}

// GenerateRandomUUID returns a UUID with v4 bits, using a time hash if random reads fail.
func GenerateRandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Use a time hash if the random source fails.
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}

	// Set UUID version and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GenerateClientID returns 64 hex digits from 32 random bytes, or a time hash on failure.
func GenerateClientID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Log failure before using a time-derived ID.
		logger.LegacyPrintf("service.identity", "Warning: crypto/rand.Read failed: %v, using fallback", err)
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
}

// GenerateUUIDFromSeed hashes seed into a deterministic UUID with v4 format bits.
func GenerateUUIDFromSeed(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]

	// Set UUID version and variant bits.
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// ParseUserAgentVersion extracts the first /major.minor.patch version in ua.
func ParseUserAgentVersion(ua string) (major, minor, patch int, ok bool) {
	matches := UserAgentVersionRegex.FindStringSubmatch(ua)
	if len(matches) != 4 {
		return 0, 0, 0, false
	}
	var err error
	if major, err = strconv.Atoi(matches[1]); err != nil {
		return 0, 0, 0, false
	}
	if minor, err = strconv.Atoi(matches[2]); err != nil {
		return 0, 0, 0, false
	}
	if patch, err = strconv.Atoi(matches[3]); err != nil {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}

// ExtractProduct returns the lowercase product before the first slash, or empty.
func ExtractProduct(ua string) string {
	if idx := strings.Index(ua, "/"); idx > 0 {
		return strings.ToLower(ua[:idx])
	}
	return ""
}

// IsNewerVersion compares three-part versions for matching product names.
// Different products, missing products, and invalid versions return false.
func IsNewerVersion(newUA, cachedUA string) bool {
	newProduct := ExtractProduct(newUA)
	cachedProduct := ExtractProduct(cachedUA)
	if newProduct == "" || cachedProduct == "" || newProduct != cachedProduct {
		return false
	}

	newMajor, newMinor, newPatch, newOk := ParseUserAgentVersion(newUA)
	cachedMajor, cachedMinor, cachedPatch, cachedOk := ParseUserAgentVersion(cachedUA)

	if !newOk || !cachedOk {
		return false
	}

	if newMajor > cachedMajor {
		return true
	}
	if newMajor < cachedMajor {
		return false
	}

	if newMinor > cachedMinor {
		return true
	}
	if newMinor < cachedMinor {
		return false
	}

	return newPatch > cachedPatch
}
