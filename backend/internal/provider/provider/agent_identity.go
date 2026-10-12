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

// AgentIdentityKey validates the runtime ID and parses the private key from provider credentials.
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

// RegisterAgentIdentityTask registers a task upstream; the provider coordinator owns claiming,
// rechecking, and persisting the result.
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

// AgentIdentityHeaders returns signed headers and the task ID used to sign them. Recovery must target
// that task.
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

// RedactAgentIdentityBody resolves shadow credentials before passing credential values to the platform
// redactor.
func RedactAgentIdentityBody(ctx context.Context, read func(context.Context, int64) (*provider.Record, error), value *provider.Record, body []byte) []byte {
	if value == nil || len(body) == 0 {
		return body
	}
	credential := value
	if value.IsCredentialShadow() {
		if resolved, err := provider.ResolveCredentialRecord(ctx, read, value); err == nil && resolved != nil {
			credential = resolved
		}
	}
	if credential == nil || !credential.IsOpenAIAgentIdentity() {
		return body
	}
	return openai.RedactAgentIdentityBody(body, credential.GetCredential)
}
