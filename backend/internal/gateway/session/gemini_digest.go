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

// geminiDigestSessionKeyPrefix identifies Gemini digest-derived sessions.
const geminiDigestSessionKeyPrefix = "gemini:digest:"

// BuildGeminiDigestChain hashes Gemini system instructions and message parts.
// Format: s:<hash>-u:<hash>-m:<hash>-u:<hash>-...
// s = systemInstruction, u = user, m = model.
func BuildGeminiDigestChain(req *protocolgemini.GeminiRequest) string {
	if req == nil {
		return ""
	}

	var parts []string

	if req.SystemInstruction != nil && len(req.SystemInstruction.Parts) > 0 {
		partsData, _ := json.Marshal(req.SystemInstruction.Parts)
		parts = append(parts, "s:"+upstream.ShortHash(partsData))
	}

	for _, c := range req.Contents {
		prefix := "u"
		if c.Role == "model" {
			prefix = "m"
		}
		partsData, _ := json.Marshal(c.Parts)
		parts = append(parts, prefix+":"+upstream.ShortHash(partsData))
	}

	return strings.Join(parts, "-")
}

// GenerateGeminiPrefixHash scopes sessions by user, API key, source, platform, and model.
// The source includes the IP and normalized user agent.
// It returns the first 12 SHA-256 bytes as 16 URL-safe Base64 characters.
func GenerateGeminiPrefixHash(userID, apiKeyID int64, ip, userAgent, platform, model string) string {
	// Normalize client version noise before combining identifiers.
	normalizedUserAgent := requeststate.NormalizeSessionUserAgent(userAgent)
	combined := strconv.FormatInt(userID, 10) + ":" +
		strconv.FormatInt(apiKeyID, 10) + ":" +
		ip + ":" +
		normalizedUserAgent + ":" +
		platform + ":" +
		model

	hash := sha256.Sum256([]byte(combined))
	// Twelve bytes encode to exactly 16 Base64 characters.
	return base64.RawURLEncoding.EncodeToString(hash[:12])
}

// ParseGeminiSessionValue parses a cached Gemini session binding.
// Format: {uuid}:{providerID}.
func ParseGeminiSessionValue(value string) (uuid string, providerID int64, ok bool) {
	if value == "" {
		return "", 0, false
	}

	// Split at the last colon because the UUID may contain colons.
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

// FormatGeminiSessionValue encodes a Gemini session binding.
// Format: {uuid}:{providerID}.
func FormatGeminiSessionValue(uuid string, providerID int64) string {
	return uuid + ":" + strconv.FormatInt(providerID, 10)
}

// GenerateGeminiDigestSessionKey joins up to eight bytes each of prefixHash and uuid.
// Provider selection uses the result for sticky routing.
func GenerateGeminiDigestSessionKey(prefixHash, uuid string) string {
	prefix := prefixHash
	if len(prefixHash) >= 8 {
		prefix = prefixHash[:8]
	}
	uuidPart := uuid
	if len(uuid) >= 8 {
		uuidPart = uuid[:8]
	}
	return geminiDigestSessionKeyPrefix + prefix + ":" + uuidPart
}
