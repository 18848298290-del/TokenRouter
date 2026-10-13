package openai

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// newTestAgentIdentityKey creates fresh Ed25519 credentials and a PKCS#8 Base64
// fixture. Random generation and encoding failures fail the calling test, and
// the returned key includes fixed runtime and task identifiers.
func newTestAgentIdentityKey(t *testing.T) (AgentIdentityKey, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return AgentIdentityKey{
		RuntimeID:  "runtime-test",
		PrivateKey: privateKey,
		TaskID:     "task-test",
	}, base64.StdEncoding.EncodeToString(der)
}

// TestBuildAgentAssertionMatchesCodexEnvelopeAndSignature signs a fixed
// timestamp supplied in a non-UTC zone. It decodes both envelope layers and
// verifies the UTC fields and Ed25519 signature over the exact
// runtime/task/timestamp payload.
func TestBuildAgentAssertionMatchesCodexEnvelopeAndSignature(t *testing.T) {
	key, _ := newTestAgentIdentityKey(t)
	now := time.Date(2026, 7, 14, 8, 9, 10, 0, time.FixedZone("UTC+8", 8*60*60))
	assertion, err := BuildAgentAssertion(key, now)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(assertion, "AgentAssertion "))

	encoded := strings.TrimPrefix(assertion, "AgentAssertion ")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var envelope struct {
		AgentRuntimeID string `json:"agent_runtime_id"` // signed agent runtime identifier
		TaskID         string `json:"task_id"`          // signed registered task identifier
		Timestamp      string `json:"timestamp"`        // RFC3339
		Signature      string `json:"signature"`        // base64
	}
	require.NoError(t, json.Unmarshal(decoded, &envelope))
	require.Equal(t, "runtime-test", envelope.AgentRuntimeID)
	require.Equal(t, "task-test", envelope.TaskID)
	require.Equal(t, "2026-07-14T00:09:10Z", envelope.Timestamp)
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	require.NoError(t, err)
	publicKey, ok := key.PrivateKey.Public().(ed25519.PublicKey)
	require.True(t, ok)
	require.True(t, ed25519.Verify(publicKey, []byte("runtime-test:task-test:2026-07-14T00:09:10Z"), signature))
}

// TestDecryptAgentTaskIDSupportsCodexSealedBoxResponse encrypts a task
// identifier to the seed-derived X25519 public key. Decryption must recover the
// plaintext identifier with the matching agent Ed25519 credentials.
func TestDecryptAgentTaskIDSupportsCodexSealedBoxResponse(t *testing.T) {
	key, _ := newTestAgentIdentityKey(t)
	digest := sha512.Sum512(key.PrivateKey.Seed())
	var curvePrivate [32]byte
	copy(curvePrivate[:], digest[:32])
	// Derive the recipient through the same Ed25519-seed conversion used by
	// registration decryption, so this fixture exercises sealed-box interoperability.
	curvePrivate[0] &= 248
	curvePrivate[31] &= 127
	curvePrivate[31] |= 64
	curvePublicBytes, err := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
	require.NoError(t, err)
	var curvePublic [32]byte
	copy(curvePublic[:], curvePublicBytes)
	ciphertext, err := box.SealAnonymous(nil, []byte("task-sealed"), &curvePublic, rand.Reader)
	require.NoError(t, err)
	got, err := DecryptAgentTaskID(key, base64.StdEncoding.EncodeToString(ciphertext))
	require.NoError(t, err)
	require.Equal(t, "task-sealed", got)
}

// TestOpenAIWSAgentIdentityRecoveryRequiresTaskInvalidBody compares two
// unauthorized handshake bodies with different error codes. A signature failure
// must remain ineligible for task recovery while invalid_task_id enables it.
func TestOpenAIWSAgentIdentityRecoveryRequiresTaskInvalidBody(t *testing.T) {
	require.False(t, IsAgentTaskInvalidWSDialError(&WSDialError{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"code":"invalid_signature"}}`),
	}))
	require.True(t, IsAgentTaskInvalidWSDialError(&WSDialError{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"code":"invalid_task_id"}}`),
	}))
}
