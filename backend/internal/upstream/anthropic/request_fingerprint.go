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

// request_fingerprint.go maintains provider fingerprints and rewrites OAuth
// metadata identities and masked sessions.
// OAuth requests can reuse one provider fingerprint even when client headers
// change between requests. User-Agent acceptance precedes persistence so
// malformed local-build versions and implausible Claude CLI major versions
// cannot direct the normal upgrade path.
//
// Cached identity is repaired when invalid, upgraded for acceptable newer
// versions of the same product, and lazily written after the renewal interval.
// Missing SDK headers keep cached values during merging. Metadata rewriting
// combines the cached device, provider account, and provider-scoped session
// seed, then selects its wire format from the fingerprint User-Agent. Optional
// masking reuses a provider session value and renews its cache expiry. Cache
// reads, writes, and metadata edits are best effort where documented, and
// separate cache operations do not serialize concurrent creation or masking
// decisions.

const (
	// ClaudeCLIUserAgentProduct identifies the product whose cached UA also
	// obeys the configured Claude CLI major-version window.
	ClaudeCLIUserAgentProduct = "claude-cli"
	// MaxFingerprintUserAgentLength caps the trimmed UA's byte length before
	// persistent identity selection, limiting the accepted cache value size.
	MaxFingerprintUserAgentLength = 256
	// MaxClaudeCLIMajorVersionSkew permits a Claude CLI UA two major versions
	// ahead of the configured current version during cache admission.
	MaxClaudeCLIMajorVersionSkew = 2
)

var (
	// UserAgentVersionRegex finds the slash-prefixed numeric triple used by
	// version comparison; fingerprint admission separately checks the full
	// leading product shape.
	UserAgentVersionRegex = regexp.MustCompile(`/(\d+)\.(\d+)\.(\d+)`)

	// FingerprintUserAgentPattern requires a leading product and three numeric
	// version parts followed by whitespace or end-of-input before a UA can be
	// cached.
	FingerprintUserAgentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/\d+\.\d+\.\d+(\s|$)`)

	// DefaultFingerprint supplies the accepted default UA and SDK values used
	// when client creation inputs are missing; upgrades retain omitted cached
	// SDK fields.
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

// Fingerprint contains provider-level device identity, UA, and SDK headers used
// during OAuth request preparation. Returned cached values can be updated
// during repair or renewal.
type Fingerprint struct {
	// ClientID is generated when a fingerprint is created and retained through
	// cached UA repair or upgrade. Metadata rewriting uses it as the provider's
	// selected device component.
	ClientID string
	// UserAgent selects outbound UA and metadata wire format. Cached creation,
	// upgrade, and repair paths validate it before selecting provider identity,
	// while ApplyFingerprint writes a supplied nonempty value.
	UserAgent string
	// StainlessLang, StainlessPackageVersion, StainlessOS, StainlessArch,
	// StainlessRuntime, and StainlessRuntimeVersion supply SDK headers.
	// Creation fills empty values from DefaultFingerprint; upgrades retain
	// cached values for omitted headers.
	StainlessLang           string // SDK implementation language label
	StainlessPackageVersion string // SDK package version string
	StainlessOS             string // SDK operating system label
	StainlessArch           string // SDK processor architecture label
	StainlessRuntime        string // SDK execution runtime label
	StainlessRuntimeVersion string // SDK execution runtime version
	// UpdatedAt records the last selected write in Unix seconds. Zero or a
	// timestamp more than twenty-four hours old triggers lazy renewal when no
	// repair or version upgrade already requires a write.
	UpdatedAt int64 `json:",omitempty"`
}

// FingerprintCache reads and writes provider identity and optional session
// masks. RequestFingerprint performs separate operations, so implementations
// define persistence failures, expiry, and value ownership.
type FingerprintCache interface {
	// GetFingerprint returns the cached fingerprint or a missing/error result.
	// RequestFingerprint creates identity when the read fails or returns nil,
	// and mutates returned values during repair or renewal. Implementations
	// permit that mutation and coordinate any shared storage.
	GetFingerprint(ctx context.Context, providerID int64) (*Fingerprint, error)
	// SetFingerprint stores the selected provider fingerprint and reports
	// persistence failure. RequestFingerprint can still use the selected value
	// after an error; the Redis adapter renews its seven-day expiry on success.
	SetFingerprint(ctx context.Context, providerID int64, fp *Fingerprint) error
	// GetMaskedSessionID returns the provider's session mask or an absent/error
	// result. The Redis adapter returns empty after missing or idle-expired
	// entries, while RequestFingerprint keeps ordinary rewriting when this read
	// fails.
	GetMaskedSessionID(ctx context.Context, providerID int64) (string, error)
	// SetMaskedSessionID stores a provider mask and reports write failure. The
	// Redis adapter renews its fifteen-minute idle expiry; RequestFingerprint
	// still attempts the metadata edit with that mask after persistence fails.
	SetMaskedSessionID(ctx context.Context, providerID int64, sessionID string) error
}

// RequestFingerprint prepares OAuth provider identity through a configured
// cache and metadata transformation rules.
type RequestFingerprint struct {
	// cache supplies provider fingerprint and session-mask reads and writes.
	// The service requires a usable implementation and keeps those operations
	// separate through identity preparation.
	cache FingerprintCache
}

// IsAcceptableFingerprintUserAgent reports whether a trimmed User-Agent is
// eligible for provider identity caching. It requires the configured byte limit
// and product/version shape. Claude CLI values also parse their version and
// stay within the current major-version window; other products use the shape
// and length checks.
func IsAcceptableFingerprintUserAgent(userAgent string) bool {
	userAgent = strings.TrimSpace(userAgent)
	// Bound the trimmed UA bytes admitted to provider caching.
	if userAgent == "" || len(userAgent) > MaxFingerprintUserAgentLength {
		return false
	}
	if !FingerprintUserAgentPattern.MatchString(userAgent) {
		return false
	}
	// The Claude major-version window applies to that product's cached
	// identity. Other products stop after the leading version shape and trimmed
	// length are accepted.
	if ExtractProduct(userAgent) != ClaudeCLIUserAgentProduct {
		return true
	}
	major, _, _, ok := ParseUserAgentVersion(userAgent)
	if !ok {
		return false
	}
	currentMajor, _, _, currentOK := ParseUserAgentVersion(ClaudeCLIUserAgentProduct + "/" + CLIVersion())
	// If the configured current CLI version cannot be parsed, the
	// format-accepted client UA remains eligible. This path leaves the
	// major-window comparison unavailable for admission.
	if !currentOK {
		return true
	}
	// Reject implausible CLI majors before persistent upgrade selection can use
	// them.
	return major <= currentMajor+MaxClaudeCLIMajorVersionSkew
}

// NewRequestFingerprint binds provider identity preparation to the supplied
// FingerprintCache. Callers provide a usable cache for fingerprint lookup and
// masking; the service performs separate reads and writes through that
// dependency.
func NewRequestFingerprint(cache FingerprintCache) *RequestFingerprint {
	return &RequestFingerprint{cache: cache}
}

// GetOrCreateFingerprint selects a cached, repaired, upgraded, or newly created
// provider fingerprint. Cache read failure or an absent value creates identity
// from client headers and defaults with a generated ClientID. Handled cache
// failures return the selected fingerprint with a nil error; failed writes are
// logged while that identity remains usable.
//
// An invalid cached User-Agent is repaired before renewal; an acceptable newer
// version of the same product merges supplied SDK headers. Otherwise the cached
// fields remain until a lazy write is due. The method mutates the returned
// cache value during repair or renewal and logs persistence failures. Cache
// operations remain separate, so callers cannot infer atomic selection across
// concurrent requests from this method.
func (s *RequestFingerprint) GetOrCreateFingerprint(ctx context.Context, providerID int64, headers http.Header) (*Fingerprint, error) {
	// Use the same UA validation for creation, upgrades, and cached-value
	// repair.
	clientUA := strings.TrimSpace(headers.Get("User-Agent"))
	uaAcceptable := IsAcceptableFingerprintUserAgent(clientUA)

	cached, err := s.cache.GetFingerprint(ctx, providerID)
	if err == nil && cached != nil {
		needWrite := false

		// Log invalid client versions only when they would otherwise upgrade
		// the cache.
		if !uaAcceptable && clientUA != "" && IsNewerVersion(clientUA, cached.UserAgent) {
			logger.LegacyPrintf("service.identity",
				"Rejected fingerprint user-agent for provider %d: %q (malformed or implausible version)",
				providerID, clientUA)
		}

		if !IsAcceptableFingerprintUserAgent(cached.UserAgent) {
			// Repair an invalid cached UA before checking lazy renewal so
			// continued traffic cannot keep selecting the poisoned value solely
			// by updating its timestamp.
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
			// A version upgrade merges the client's supplied SDK fields into
			// cached identity. Omitted SDK headers keep their cached values,
			// maintaining that provider's selected header set.
			MergeHeadersIntoFingerprint(cached, headers)
			needWrite = true
			logger.LegacyPrintf("service.identity", "Updated fingerprint for provider %d: %s (merge update)", providerID, clientUA)
		}

		if !needWrite && time.Since(time.Unix(cached.UpdatedAt, 0)) > 24*time.Hour {
			// Renew active fingerprints before their Redis seven-day expiry can
			// lapse.
			needWrite = true
		}

		if needWrite {
			cached.UpdatedAt = time.Now().Unix()
			// The repaired or renewed value remains usable for this request
			// even if the cache write fails. Log that persistence failure while
			// returning the selected identity.
			if err := s.cache.SetFingerprint(ctx, providerID, cached); err != nil {
				logger.LegacyPrintf("service.identity", "Warning: failed to refresh fingerprint for provider %d: %v", providerID, err)
			}
		}
		return cached, nil
	}

	// A missing cache value or failed read starts creation from accepted client
	// headers and defaults. This request can select identity even while cache
	// availability is degraded.
	if !uaAcceptable && clientUA != "" {
		logger.LegacyPrintf("service.identity",
			"Rejected fingerprint user-agent for provider %d: %q (malformed or implausible version)",
			providerID, clientUA)
	}
	fp := s.createFingerprintFromHeaders(headers)

	fp.ClientID = GenerateClientID()
	fp.UpdatedAt = time.Now().Unix()

	// Creation assigns device identity and a selected write timestamp before
	// cache publication. A failed write is logged while the new fingerprint is
	// still returned for preparation.
	if err := s.cache.SetFingerprint(ctx, providerID, fp); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache fingerprint for provider %d: %v", providerID, err)
	}

	logger.LegacyPrintf("service.identity", "Created new fingerprint for provider %d with client_id: %s", providerID, fp.ClientID)
	return fp, nil
}

// createFingerprintFromHeaders returns a fingerprint with accepted client UA
// and supplied SDK fields. Rejected or absent UA uses DefaultFingerprint, and
// empty SDK header values use its defaults. ClientID and UpdatedAt are assigned
// by GetOrCreateFingerprint after this header preparation.
func (s *RequestFingerprint) createFingerprintFromHeaders(headers http.Header) *Fingerprint {
	fp := &Fingerprint{}

	// An unacceptable client UA selects the package default during creation.
	// Only admitted version text can become the client's contribution to this
	// provider fingerprint.
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

// MergeHeadersIntoFingerprint copies nonempty client UA and SDK values into fp.
// Missing values keep their cached fields. Callers provide nonnil fp and
// validate the User-Agent before using this merge for cached identity upgrades
// or repair.
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

// MergeHeader replaces target with a nonempty value read from headers. Empty
// values retain the pointed-to string; callers supply a nonnil target, and
// whitespace-only values count as supplied values.
func MergeHeader(headers http.Header, key string, target *string) {
	if v := headers.Get(key); v != "" {
		*target = v
	}
}

// GetHeaderOrDefault returns the nonempty header value or defaultValue.
// Whitespace-only values remain supplied values, so callers trim them
// separately when their consumer requires normalized header text.
func GetHeaderOrDefault(headers http.Header, key, defaultValue string) string {
	if v := headers.Get(key); v != "" {
		return v
	}
	return defaultValue
}

// ApplyFingerprint writes nonempty UA and SDK values into req's header map. A
// nil fingerprint leaves the request alone; callers supply a usable request
// when values are present. Raw header setters retain the package's chosen wire
// casing while replacing each selected value.
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

// RewriteUserID replaces usable metadata identity with the cached device and
// provider account, returning the resulting body. Empty identity inputs, absent
// or malformed metadata, and failed JSON updates retain the supplied bytes. The
// fingerprint User-Agent selects legacy or JSON-string metadata encoding.
//
// The session seed combines providerID with the client session before UUID
// formatting, separating the same incoming session across providers. A path
// update changes metadata.user_id while neighboring body data remains available
// in its existing representation. Reapplying the method hashes the session
// present in that input, so callers pass the intended incoming body for each
// preparation rather than treating the transform as idempotent.
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

	// Combine the incoming session with providerID before UUID formatting so
	// the same client session has a different deterministic seed for another
	// selected provider.
	seed := fmt.Sprintf("%d::%s", providerID, sessionTail)
	newSessionHash := GenerateUUIDFromSeed(seed)

	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(cachedClientID, providerUUID, newSessionHash, version)
	if newUserID == userID {
		return body, nil
	}

	// Edit the metadata path after selecting all identity components. A failed
	// path update keeps the supplied body available, including neighboring
	// history and system data used for this request.
	newBody, err := sjson.SetBytes(body, "metadata.user_id", newUserID)
	if err != nil {
		return body, nil
	}
	return newBody, nil
}

// RewriteUserIDWithMasking applies ordinary provider identity rewriting, then
// optionally replaces a usable metadata session with the provider's cached
// mask. Disabled masking or unusable rewritten metadata returns the ordinary
// result. A mask read error also keeps that result.
//
// A missing mask generates a selected UUID; every usable masking request writes
// it back to renew the cache lifetime. Write failure is logged and leaves the
// selected UUID available for the following metadata edit. Device and account
// components from ordinary rewriting remain in the output format selected by
// fingerprintUA. Separate cache reads and writes allow concurrent initial
// requests to select different masks; JSON update failure returns the ordinary
// rewrite.
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

	// Mask selection follows ordinary identity rewriting. A cache read failure
	// retains that ordinary device/account/session result, leaving this body
	// usable without a selected cached mask.
	maskedSessionID, err := s.cache.GetMaskedSessionID(ctx, providerID)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to get masked session ID for provider %d: %v", providerID, err)
		return newBody, nil
	}

	if maskedSessionID == "" {
		// A missing provider mask starts a selected session value for this
		// request. Separate reads and writes can leave concurrent initial
		// requests with different generated values.
		maskedSessionID = GenerateRandomUUID()
		logger.LegacyPrintf("service.identity", "Generated new masked session ID for provider %d: %s", providerID, maskedSessionID)
	}

	// Write the selected mask on each usable masking request to renew the Redis
	// idle expiry. Persistence failure is logged while this request keeps its
	// chosen mask.
	if err := s.cache.SetMaskedSessionID(ctx, providerID, maskedSessionID); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to set masked session ID for provider %d: %v", providerID, err)
	}

	// Retain the device and account parsed after ordinary rewriting while
	// changing the session. Select metadata wire format from the same
	// fingerprint UA used by that earlier step.
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

	// The mask is already selected and its cache write attempted. If the
	// metadata edit fails, return the ordinary rewrite so no partially edited
	// mask body is published.
	maskedBody, setErr := sjson.SetBytes(newBody, "metadata.user_id", newUserID)
	if setErr != nil {
		return newBody, nil
	}
	return maskedBody, nil
}

// GenerateRandomUUID returns a random UUID string with version-four and variant
// bits. crypto/rand.Read fills the bytes or terminates the process on source
// failure, so the timestamp-hash error branch is inactive under the configured
// Go API behavior. Callers cannot use that branch to recover from an entropy
// source failure.
func GenerateRandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// This branch encodes a timestamp digest if Read reports an error. The
		// configured crypto/rand.Read terminates first, leaving that
		// error-return path inactive for current callers.
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}

	// Random or timestamp-derived bytes need UUID format bits before rendering
	// the session identifier used by metadata preparation. Reserve those bits
	// while retaining the remaining selected bytes.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GenerateClientID returns 64 hexadecimal digits from 32 random bytes.
// crypto/rand.Read fills those bytes or terminates the process on source
// failure. Its timestamp-hash error branch does not provide runtime recovery
// under the configured Go API, so callers receive the random-byte encoding when
// this function returns normally.
func GenerateClientID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// This error-return path would log before selecting a timestamp-derived
		// ID. The configured random API terminates on source failure, so
		// ordinary execution does not reach this recovery log.
		logger.LegacyPrintf("service.identity", "Warning: crypto/rand.Read failed: %v, using fallback", err)
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
}

// GenerateUUIDFromSeed hashes seed into a deterministic UUID-shaped string.
// Identical seeds yield identical output with version-four and variant bits;
// callers provide the provider and session scope used by metadata rewriting.
func GenerateUUIDFromSeed(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]

	// The provider/session digest needs the UUID string shape used by metadata.
	// Reserve its version and variant positions before rendering the remaining
	// deterministic digest bytes into that identifier.
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// ParseUserAgentVersion extracts the first slash-prefixed three-part numeric
// version in ua. A missing match or int conversion overflow returns false. This
// extraction alone does not establish the stricter fingerprint acceptance
// shape.
func ParseUserAgentVersion(ua string) (major, minor, patch int, ok bool) {
	matches := UserAgentVersionRegex.FindStringSubmatch(ua)
	if len(matches) != 4 {
		return 0, 0, 0, false
	}
	// All version components need representable integer values before
	// comparison. If any conversion fails, discard that incomplete tuple so
	// callers treat the version as unavailable for upgrade selection.
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

// ExtractProduct returns the lowercase prefix before the first slash. A missing
// slash or an empty prefix returns an empty string; the prefix is otherwise
// retained without whitespace normalization.
func ExtractProduct(ua string) string {
	if idx := strings.Index(ua, "/"); idx > 0 {
		return strings.ToLower(ua[:idx])
	}
	return ""
}

// IsNewerVersion compares numeric version triples for matching nonempty product
// prefixes. Missing products, different products, or failed version parsing
// return false. Equal versions also return false; callers apply fingerprint
// admissibility checks separately before persisting an upgrade.
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
