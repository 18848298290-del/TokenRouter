package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Agent Identity requests authenticate a runtime and its registered task with a
// signed assertion. Provider credentials hold the signing key and task
// identifier; registration returns a task for the coordinator to persist, while
// response redaction can resolve a shadow back to the credential owner.

// AgentIdentityKey parses the Ed25519 private key and trimmed runtime and task
// IDs. A nil record, invalid key, or missing runtime ID returns an error; an
// empty task ID remains available for registration.
func AgentIdentityKey(value *provider.Record) (openai.AgentIdentityKey, error) {
	if value == nil {
		return openai.AgentIdentityKey{}, errors.New("agent identity provider is nil")
	}
	privateKey, err := openai.ParseAgentIdentityPrivateKey(value.GetCredential("agent_private_key"))
	if err != nil {
		return openai.AgentIdentityKey{}, err
	}
	runtimeID := strings.TrimSpace(value.GetCredential("agent_runtime_id"))
	if runtimeID == "" {
		return openai.AgentIdentityKey{}, errors.New("agent identity runtime id is missing")
	}
	return openai.AgentIdentityKey{RuntimeID: runtimeID, PrivateKey: privateKey, TaskID: strings.TrimSpace(value.GetCredential("task_id"))}, nil
}

// RegisterAgentIdentityTask registers a task with the upstream runtime using
// the record's key and current time. A loaded proxy is used when both ProxyID
// and Proxy exist; the provider coordinator claims and persists the returned
// task.
func RegisterAgentIdentityTask(ctx context.Context, value *provider.Record, baseURL string) (string, error) {
	key, err := AgentIdentityKey(value)
	if err != nil {
		return "", err
	}
	now := time.Now()
	proxyURL := ""
	if value.ProxyID != nil && value.Proxy != nil {
		proxyURL = value.Proxy.URL()
	}
	return openai.RegisterAgentIdentityTask(ctx, key, proxyURL, baseURL, now)
}

// AgentIdentityHeaders ensures a task exists and returns its signed
// Authorization header and task ID. Callers supply a nonnil ensure callback and
// an Agent Identity record, then target recovery at the returned task if the
// upstream rejects the assertion.
func AgentIdentityHeaders(ctx context.Context, value *provider.Record, ensure func(context.Context, *provider.Record, string) error) (http.Header, string, error) {
	if value == nil || !value.IsOpenAIAgentIdentity() {
		return nil, "", errors.New("agent identity provider is required")
	}
	if err := ensure(ctx, value, ""); err != nil {
		return nil, "", err
	}
	key, err := AgentIdentityKey(value)
	if err != nil {
		return nil, "", err
	}
	assertion, err := openai.BuildAgentAssertion(key, time.Now())
	if err != nil {
		return nil, "", err
	}
	headers := make(http.Header)
	headers.Set("Authorization", assertion)
	return headers, key.TaskID, nil
}

// RedactAgentIdentityBody removes Agent Identity credential values from an
// upstream body. It returns the input for empty bodies or other authentication
// modes; a failed shadow resolution retains the supplied record for the
// redaction check.
func RedactAgentIdentityBody(ctx context.Context, read func(context.Context, int64) (*provider.Record, error), value *provider.Record, body []byte) []byte {
	if value == nil || len(body) == 0 {
		return body
	}
	credential := value
	if value.IsCredentialShadow() {
		// A failed parent lookup leaves the shadow as the candidate, so best-effort
		// logging redaction can continue without turning the lookup error into a
		// request failure.
		if resolved, err := provider.ResolveCredentialRecord(ctx, read, value); err == nil && resolved != nil {
			credential = resolved
		}
	}
	if credential == nil || !credential.IsOpenAIAgentIdentity() {
		return body
	}
	return openai.RedactAgentIdentityBody(body, credential.GetCredential)
}
