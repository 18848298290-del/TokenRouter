package upstream

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/google/uuid"
)

// IsolateSessionID hashes the API key ID with the trimmed session identifier.
// This scopes reused client session_id or conversation_id values to an API key.
// Empty identifiers remain empty.
func IsolateSessionID(apiKeyID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	h := xxhash.New()
	_, _ = fmt.Fprintf(h, "k%d:", apiKeyID)
	_, _ = h.WriteString(raw)
	return fmt.Sprintf("%016x", h.Sum64())
}

func HashSensitiveValueForLog(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// GenerateSessionUUID derives a UUID-shaped value from a nonempty seed, or generates a random UUID for an empty seed.
func GenerateSessionUUID(seed string) string {
	if seed == "" {
		return uuid.NewString()
	}
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// ShortHash returns an unpadded base-36 encoding of the XXHash64 digest.
// The encoding has at most 13 characters.
// Callers use this compact digest for non-cryptographic identifiers.
func ShortHash(data []byte) string {
	h := xxhash.Sum64(data)
	return strconv.FormatUint(h, 36)
}

func RandomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateAnthropicMsgID returns msg_01 followed by 22 base-62 characters.
func GenerateAnthropicMsgID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		// Keep the fixed-width ID format if random bytes cannot be obtained.
		return fmt.Sprintf("msg_01%022x", uint64(time.Now().UnixNano()))
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}
