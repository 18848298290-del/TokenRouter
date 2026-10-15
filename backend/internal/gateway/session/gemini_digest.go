package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// gemini_digest.go derives Gemini conversation digests, request-source hashes,
// and serialized sticky-session values. System parts and message parts produce
// role-labelled digest components; user, credential, client source, platform,
// and model produce a prefix hash. The memory store combines that prefix with
// the selected group when matching a conversation's longer transcript against
// stored prefixes.
//
// Serialized session values retain UUID text by splitting their provider suffix
// at the last colon. Sticky session keys use only the first eight bytes of
// prefix and UUID, while the prefix hash itself keeps twelve SHA-256 bytes.
// These compact representations serve routing identity and can collapse
// distinct full inputs; callers keep complete digest and binding values where
// those are needed.

// geminiDigestSessionKeyPrefix labels digest-derived sticky keys so provider routing
// can distinguish these compact Gemini session values from other client session identifiers.
const geminiDigestSessionKeyPrefix = "gemini:digest:"

// BuildGeminiDigestChain hashes serialized Gemini parts into ordered role-labelled
// components. Nil or empty usable requests return empty. A nonempty system parts
// slice produces an s component; every content entry produces a component even
// when its parts slice is empty or nil.
//
// The exact role model receives m, while every other role receives u. Role text
// outside this classification is omitted from each hash; the serialized parts
// carry text, media, and function values. Marshal failures are discarded and hash
// the returned bytes, normally empty, so callers supply JSON-marshalable parts
// when they need distinct content to remain distinguishable.
func BuildGeminiDigestChain(req *protocolgemini.GeminiRequest) string {
	if req == nil {
		return ""
	}

	var parts []string

	if req.SystemInstruction != nil && len(req.SystemInstruction.Parts) > 0 {
		// Digest derivation exposes no marshal error result. Invalid caller-built
		// function values therefore contribute the returned bytes, losing their distinct content identity.
		partsData, _ := json.Marshal(req.SystemInstruction.Parts)
		parts = append(parts, "s:"+upstream.ShortHash(partsData))
	}

	for _, c := range req.Contents {
		prefix := "u"
		if c.Role == "model" {
			prefix = "m"
		}
		// Every message contributes one component, including nil parts; discarded
		// marshal failure likewise hashes returned bytes and can collapse invalid message identities.
		partsData, _ := json.Marshal(c.Parts)
		parts = append(parts, prefix+":"+upstream.ShortHash(partsData))
	}

	return strings.Join(parts, "-")
}

// GenerateGeminiPrefixHash returns sixteen unpadded URL-safe Base64 characters
// from the first twelve bytes of a SHA-256 source hash. The seed includes decimal
// user and API Key IDs, supplied IP, normalized User-Agent, platform, and model;
// only User-Agent receives normalization here.
//
// Version noise removed by NormalizeSessionUserAgent therefore leaves the source
// prefix stable while the other supplied components remain significant. Fields
// are colon-separated without escaping, so this compact hash is routing evidence
// rather than an authenticated or collision-free encoding of arbitrary tuples.
// The memory digest namespace also includes the selected group separately.
func GenerateGeminiPrefixHash(userID, apiKeyID int64, ip, userAgent, platform, model string) string {
	// Remove supported User-Agent version noise so ordinary client upgrades
	// keep the same digest namespace when credential, address, platform, and model remain equal.
	normalizedUserAgent := requeststate.NormalizeSessionUserAgent(userAgent)
	combined := strconv.FormatInt(userID, 10) + ":" +
		strconv.FormatInt(apiKeyID, 10) + ":" +
		ip + ":" +
		normalizedUserAgent + ":" +
		platform + ":" +
		model

	hash := sha256.Sum256([]byte(combined))
	// Keep twelve hash bytes to produce sixteen unpadded URL-safe characters.
	return base64.RawURLEncoding.EncodeToString(hash[:12])
}

// ParseGeminiSessionValue splits a cached UUID/provider value at its last colon.
// A missing nonempty UUID or suffix, or an invalid signed decimal int64 suffix,
// returns false. UUID colons are retained, and zero or negative provider IDs are
// accepted when parsing succeeds.
//
// The last-colon rule allows FormatGeminiSessionValue to retain colon-containing
// UUID text while appending one numeric provider suffix. Successful parsing does
// not validate UUID syntax or provider existence; consumers apply those eligibility
// checks when using the returned binding. No surrounding whitespace is trimmed
// from either side of the stored value.
func ParseGeminiSessionValue(value string) (uuid string, providerID int64, ok bool) {
	if value == "" {
		return "", 0, false
	}

	// Read the numeric provider suffix after the final colon so an embedded
	// colon in UUID text remains part of the recovered session identity.
	i := strings.LastIndex(value, ":")
	if i <= 0 || i >= len(value)-1 {
		return "", 0, false
	}

	uuid = value[:i]
	providerID, err := strconv.ParseInt(value[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}

	return uuid, providerID, true
}

// FormatGeminiSessionValue appends a signed decimal provider ID after UUID text.
// It validates neither component; an empty UUID can produce a value that
// ParseGeminiSessionValue rejects, while UUID colons remain recoverable.
func FormatGeminiSessionValue(uuid string, providerID int64) string {
	return uuid + ":" + strconv.FormatInt(providerID, 10)
}

// GenerateGeminiDigestSessionKey labels up to eight bytes each of prefixHash and
// uuid for sticky routing. Short or empty components pass through; byte slicing
// can split UTF-8, so generated ASCII prefixes and UUIDs provide the intended inputs.
func GenerateGeminiDigestSessionKey(prefixHash, uuid string) string {
	prefix := prefixHash
	// Retain eight bytes for the compact sticky key; the full namespace hash
	// remains separate in DigestSessionStore and can distinguish longer prefix values.
	if len(prefixHash) >= 8 {
		prefix = prefixHash[:8]
	}
	uuidPart := uuid
	// Limit the UUID component to eight bytes for this routing representation;
	// differing suffixes alone consequently do not produce different sticky keys.
	if len(uuid) >= 8 {
		uuidPart = uuid[:8]
	}
	return geminiDigestSessionKeyPrefix + prefix + ":" + uuidPart
}
