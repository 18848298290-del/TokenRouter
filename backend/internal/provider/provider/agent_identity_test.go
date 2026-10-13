package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestRegisterAgentIdentityTaskAcceptsPlaintextAndEncryptedResponses serves
// both task response encodings through the same runtime registration endpoint.
// Each call must recover the task ID and send a timestamp and signature.
func TestRegisterAgentIdentityTaskAcceptsPlaintextAndEncryptedResponses(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/agent/runtime-test/task/register", r.URL.Path)
		var request map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NotEmpty(t, request["timestamp"])
		require.NotEmpty(t, request["signature"])
		requestCount++
		if requestCount == 2 {
			// Match the platform's Ed25519-to-X25519 conversion before sealing the
			// second response, so registration must decrypt a task encrypted for the
			// imported signing key.
			digest := sha512.Sum512(key.PrivateKey.Seed())
			var curvePrivate [32]byte
			copy(curvePrivate[:], digest[:32])
			curvePrivate[0] &= 248
			curvePrivate[31] &= 127
			curvePrivate[31] |= 64
			curvePublicBytes, curveErr := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
			require.NoError(t, curveErr)
			var curvePublic [32]byte
			copy(curvePublic[:], curvePublicBytes)
			ciphertext, sealErr := box.SealAnonymous(nil, []byte("task-encrypted"), &curvePublic, rand.Reader)
			require.NoError(t, sealErr)
			_, _ = fmt.Fprintf(w, `{"encrypted_task_id":%q}`, base64.StdEncoding.EncodeToString(ciphertext))
			return
		}
		_, _ = w.Write([]byte(`{"task_id":"task-plain"}`))
	}))
	defer server.Close()

	provider := &providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformOpenAI, Credentials: map[string]any{
		"auth_mode":         providercore.OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  key.RuntimeID,
		"agent_private_key": privateKey,
	}}
	taskID, err := RegisterAgentIdentityTask(context.Background(), provider, server.URL)
	require.NoError(t, err)
	require.Equal(t, "task-plain", taskID)
	taskID, err = RegisterAgentIdentityTask(context.Background(), provider, server.URL)
	require.NoError(t, err)
	require.Equal(t, "task-encrypted", taskID)
}

// newTestAgentIdentityKey generates an Ed25519 runtime key and its base64 PKCS8
// credential. It fails the test on generation or encoding errors and returns
// the same private key in both representations.
func newTestAgentIdentityKey(t *testing.T) (openai.AgentIdentityKey, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return openai.AgentIdentityKey{
		RuntimeID:  "runtime-test",
		PrivateKey: privateKey,
		TaskID:     "task-test",
	}, base64.StdEncoding.EncodeToString(der)
}
