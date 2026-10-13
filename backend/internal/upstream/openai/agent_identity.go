package openai

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// Agent credentials bind a runtime, registered task, and Ed25519 signing key to
// the Authorization header. The signed payload includes those identifiers and a
// UTC timestamp; the outer JSON envelope and signature use different Base64
// encodings that callers must keep paired when constructing an assertion.
//
// Task registration accepts snake-case and camel-case response fields,
// preferring a plain task identifier before attempting sealed-box decryption.
// Encrypted identifiers use an X25519 key derived from the Ed25519 seed, so the
// registration response can be opened with the same identity that signs
// requests. Registration owns its HTTP response body, while the provider
// coordinator owns task reuse and invalid-task recovery.

// agentIdentityTaskRegistrationTimeout limits the full registration exchange
// after the provider coordinator elects a registering caller.
const agentIdentityTaskRegistrationTimeout = 30 * time.Second

// AgentIdentityKey holds the signing credentials for one registered agent task.
type AgentIdentityKey struct {
	// RuntimeID and TaskID identify the registered task signed by PrivateKey.
	// Callers keep this credential tuple together when building assertions or
	// decrypting a registration response for the same runtime.
	RuntimeID  string             `json:"-"` // registered agent runtime identifier
	PrivateKey ed25519.PrivateKey `json:"-"` // Ed25519 signing key bytes
	TaskID     string             `json:"-"` // plain registered task identifier
}

// agentIdentityTaskRegistrationResponse accepts plain and encrypted task
// identifiers in both supported response spellings.
type agentIdentityTaskRegistrationResponse struct {
	TaskID               string `json:"task_id"`           // plain registered task identifier
	TaskIDCamel          string `json:"taskId"`            // camel-case plain task identifier
	EncryptedTaskID      string `json:"encrypted_task_id"` // snake-case sealed-box task ciphertext
	EncryptedTaskIDCamel string `json:"encryptedTaskId"`   // camel-case sealed-box task ciphertext
}

// String returns a fixed label that omits agent signing credentials.
func (AgentIdentityKey) String() string { return "agent identity key" }

// GoString returns the redacted label for Go-syntax formatting.
func (key AgentIdentityKey) GoString() string { return key.String() }

// BuildAgentAssertion signs the runtime, task, and UTC timestamp for an
// Authorization value. RuntimeID and TaskID must be nonempty, and PrivateKey
// must contain a valid Ed25519 key. The signature uses standard Base64 inside a
// JSON envelope; the envelope uses unpadded URL-safe Base64 after the
// AgentAssertion prefix.
func BuildAgentAssertion(key AgentIdentityKey, now time.Time) (string, error) {
	if key.RuntimeID == "" || key.TaskID == "" {
		return "", errors.New("agent identity runtime or task id is missing")
	}
	timestamp := now.UTC().Format(time.RFC3339)
	payload := []byte(key.RuntimeID + ":" + key.TaskID + ":" + timestamp)
	signature, err := key.PrivateKey.Sign(nil, payload, crypto.Hash(0))
	if err != nil {
		return "", errors.New("failed to sign agent assertion")
	}
	envelope := map[string]string{
		"agent_runtime_id": key.RuntimeID,
		"task_id":          key.TaskID,
		"timestamp":        timestamp,
		"signature":        base64.StdEncoding.EncodeToString(signature),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", errors.New("failed to serialize agent assertion")
	}
	return "AgentAssertion " + base64.RawURLEncoding.EncodeToString(encoded), nil
}

// SignAgentTaskRegistration returns the UTC timestamp and standard-Base64
// signature for task registration. RuntimeID must be nonempty and PrivateKey
// must be a valid Ed25519 key; signing failures return a sanitized error.
func SignAgentTaskRegistration(key AgentIdentityKey, timestamp time.Time) (string, string, error) {
	if key.RuntimeID == "" {
		return "", "", errors.New("agent identity runtime id is missing")
	}
	formatted := timestamp.UTC().Format(time.RFC3339)
	signature, err := key.PrivateKey.Sign(nil, []byte(key.RuntimeID+":"+formatted), crypto.Hash(0))
	if err != nil {
		return "", "", errors.New("failed to sign agent task registration")
	}
	return formatted, base64.StdEncoding.EncodeToString(signature), nil
}

// DecryptAgentTaskID opens a standard-Base64 sealed-box task identifier using
// the agent signing key. Callers supply a valid Ed25519 PrivateKey because
// deriving its seed assumes the key has the required length. Malformed
// ciphertext, authentication failure, and blank decrypted identifiers return
// sanitized errors.
//
// The Ed25519 seed is hashed and clamped into an X25519 private key, with its
// public key derived from the same bytes. That conversion keeps registration
// encryption tied to the signing identity; trimming occurs after authentication
// so the returned identifier can be used directly in later assertions.
func DecryptAgentTaskID(key AgentIdentityKey, encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", errors.New("encrypted agent task id is not valid base64")
	}
	seed := key.PrivateKey.Seed()
	digest := sha512.Sum512(seed)
	var curvePrivate [32]byte
	copy(curvePrivate[:], digest[:32])
	// Clamp the seed-derived scalar for X25519 so the sealed-box recipient
	// key corresponds to this Ed25519 identity when registration encrypts its task ID.
	curvePrivate[0] &= 248
	curvePrivate[31] &= 127
	curvePrivate[31] |= 64
	curvePublicBytes, err := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
	if err != nil {
		return "", errors.New("failed to derive agent identity decryption key")
	}
	var curvePublic [32]byte
	copy(curvePublic[:], curvePublicBytes)
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &curvePublic, &curvePrivate)
	if !ok {
		return "", errors.New("failed to decrypt encrypted agent task id")
	}
	taskID := strings.TrimSpace(string(plaintext))
	if taskID == "" {
		return "", errors.New("decrypted agent task id is empty")
	}
	return taskID, nil
}

// IsAgentTaskInvalidHTTPResponse reports whether a 401 response identifies an
// invalid, missing, or expired agent task. Other statuses return false, and a
// signature error alone does not authorize task-registration recovery.
func IsAgentTaskInvalidHTTPResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	lower := strings.ToLower(string(body))
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(lower)
	for _, marker := range []string{
		`"code":"invalid_task_id"`,
		`"code":"task_not_found"`,
		`"code":"task_expired"`,
		`"error":"invalid_task_id"`,
	} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	for _, marker := range []string{
		"invalid task_id",
		"invalid task id",
		"task_id is invalid",
		"task id is invalid",
		"task not found",
		"task expired",
		"unknown task_id",
		"unknown task id",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ParseAgentIdentityPrivateKey decodes a standard-Base64 PKCS#8 Ed25519 private
// key. It trims surrounding whitespace and rejects missing, malformed,
// differently typed, or incorrectly sized keys before callers sign or decrypt
// with them.
func ParseAgentIdentityPrivateKey(raw string) (ed25519.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("agent identity private key is missing")
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("agent identity private key is not valid base64")
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("agent identity private key is not valid PKCS#8")
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("agent identity private key is not Ed25519")
	}
	return privateKey, nil
}

// RegisterAgentIdentityTask signs and posts a registration request, returning
// the accepted task identifier. Callers provide the registration base URL,
// proxy configuration, timestamp, and a context controlling the HTTP exchange.
// The client adds a thirty-second request timeout and this function closes
// every successful HTTP response body.
//
// The response first checks task_id, then taskId, and returns the first
// nonblank plain identifier. If neither exists, encrypted_task_id takes
// precedence over encryptedTaskId and is opened with the same signing identity.
// This order allows the supported response formats to share one registration
// path while keeping failed decryption distinguishable from an omitted
// identifier.
func RegisterAgentIdentityTask(ctx context.Context, key AgentIdentityKey, proxyURL, baseURL string, now time.Time) (string, error) {
	timestamp, signature, err := SignAgentTaskRegistration(key, now)
	if err != nil {
		return "", err
	}
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              proxyURL,
		Timeout:               agentIdentityTaskRegistrationTimeout,
		ResponseHeaderTimeout: 15 * time.Second,
	})
	if err != nil {
		return "", errors.New("invalid proxy configuration for agent task registration")
	}
	body, err := json.Marshal(map[string]string{
		"timestamp": timestamp,
		"signature": signature,
	})
	if err != nil {
		return "", errors.New("failed to serialize agent task registration")
	}
	targetURL := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/v1/agent/" + url.PathEscape(key.RuntimeID) + "/task/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("failed to build agent task registration request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("agent task registration request failed")
	}
	// Release the response on every status and decode path so registration
	// failures do not retain a connection in the shared HTTP client.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("agent task registration returned status %d", resp.StatusCode)
	}
	// Restrict the decoder to the first 64 KiB of the registration envelope;
	// fields beyond that read limit cannot participate in identifier selection.
	var result agentIdentityTaskRegistrationResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return "", errors.New("agent task registration response is invalid")
	}
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" {
		return taskID, nil
	}
	if taskID := strings.TrimSpace(result.TaskIDCamel); taskID != "" {
		return taskID, nil
	}
	encrypted := strings.TrimSpace(result.EncryptedTaskID)
	if encrypted == "" {
		encrypted = strings.TrimSpace(result.EncryptedTaskIDCamel)
	}
	if encrypted == "" {
		return "", errors.New("agent task registration response omitted task id")
	}
	return DecryptAgentTaskID(key, encrypted)
}

// IsAgentTaskInvalidWSDialError reports whether a failed handshake carries
// invalid-task evidence. A nil error or a response outside the HTTP
// invalid-task rule returns false, so callers can reserve re-registration for
// task failures.
func IsAgentTaskInvalidWSDialError(err *WSDialError) bool {
	return err != nil && IsAgentTaskInvalidHTTPResponse(err.StatusCode, err.ResponseBody)
}
