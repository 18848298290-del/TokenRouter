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

// session_identity.go derives scoped session identifiers and generates
// protocol-specific message and session IDs.
// Client session identifiers can be reused by different API keys, so an
// isolation digest includes both the key ID and trimmed client value. Empty
// client identifiers stay absent; a digest is a compact correlation value
// whose finite size still permits collisions. Logging uses a separate
// truncated SHA-256 representation when the raw value needs to remain out of
// diagnostic text.
//
// Seeded UUID-shaped values use SHA-256 bytes with UUID version and variant
// bits set for the expected wire format. Empty seeds request a random UUID.
// Anthropic message IDs instead use a fixed prefix and width, with random
// bytes mapped to a base-62 alphabet. These formats serve different consumers,
// so their lengths and input units belong to their individual constructors.

// IsolateSessionID returns a sixteen-character lowercase hex digest scoped by
// API key and trimmed client identifier. Empty or whitespace-only input
// returns empty so an absent session remains absent.
//
// The digest includes a textual key prefix before the client value, making
// identical session text under different keys use different hash input. It is
// a 64-bit noncryptographic digest, so callers use it for compact routing
// correlation and do not treat it as a collision-free ownership proof.
func IsolateSessionID(apiKeyID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	h := xxhash.New()
	// Hash the key scope before session text so reused identifiers feed distinct
	// routing inputs under different keys. The concrete xxhash writer methods
	// return nil errors, so these discarded write results cannot hide an I/O failure.
	_, _ = fmt.Fprintf(h, "k%d:", apiKeyID)
	_, _ = h.WriteString(raw)
	// Leading zeros keep routing digests at sixteen hex characters.
	return fmt.Sprintf("%016x", h.Sum64())
}

// HashSensitiveValueForLog returns sixteen lowercase hex characters from the
// first eight SHA-256 bytes of trimmed text. Empty input returns empty; the
// digest supports diagnostic correlation without printing the supplied value.
func HashSensitiveValueForLog(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	// Eight digest bytes keep log correlation values at sixteen hex characters.
	return hex.EncodeToString(sum[:8])
}

// GenerateSessionUUID derives a UUID-shaped string from an exact nonempty
// seed, or delegates random UUID creation to uuid.NewString for an empty seed.
// Whitespace in a nonempty seed participates in the hash; the random path
// panics if its dependency returns a generation error.
//
// The first sixteen SHA-256 bytes supply the value, with version-four and RFC
// variant bits imposed before formatting. Equal seeds therefore yield the same
// formatted identifier; this seeded path is a digest-derived identifier and
// callers do not infer random UUID generation from its version bits.
func GenerateSessionUUID(seed string) string {
	if seed == "" {
		return uuid.NewString()
	}
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]
	// Impose the UUID version and variant bits after hashing. The result meets
	// the wire shape while the remaining digest bits retain deterministic seed input.
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// ShortHash returns an unpadded base-36 encoding of a 64-bit XXHash digest.
// Its length is at most thirteen characters; callers use this noncryptographic
// value for compact identifiers and allow possible collisions.
func ShortHash(data []byte) string {
	h := xxhash.Sum64(data)
	return strconv.FormatUint(h, 36)
}

// RandomHex returns lowercase hex for nBytes cryptographically random bytes.
// Zero produces empty text and a negative count panics during allocation;
// successful results have twice the requested byte count in characters.
func RandomHex(nBytes int) string {
	b := make([]byte, nBytes)
	// crypto/rand.Read fills the requested bytes or terminates on entropy
	// failure, so this call supplies no recoverable read error to the hex consumer.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateAnthropicMsgID returns msg_01 followed by twenty-two characters from
// the base-62 alphabet. Random bytes select characters by remainder, so this
// mapping is not a uniform base-62 sampler and callers handle IDs as opaque
// values.
func GenerateAnthropicMsgID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// The Anthropic message suffix uses twenty-two characters.
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		// This defensive branch formats a timestamp with the required suffix
		// width if Read returns an error. The current crypto/rand implementation
		// terminates on read failure before returning such an error.
		return fmt.Sprintf("msg_01%022x", uint64(time.Now().UnixNano()))
	}
	b := make([]byte, idLen)
	// Mapping byte remainders retains the required alphabet and width, while
	// the byte range makes the first eight alphabet characters slightly more likely.
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}
