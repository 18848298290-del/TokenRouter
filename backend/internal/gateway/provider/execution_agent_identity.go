package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type agentIdentityWSConnectionInvalidator interface {
	InvalidateAgentIdentityWSConnections(providerID int64)
}

// ExecutionAgentIdentity coordinates request identity registration and connection invalidation.
type ExecutionAgentIdentity struct {
	store       ExecutionProviderStore
	coordinator *acctcore.OpenAITaskCoordinator
	register    func(context.Context, *acctcore.Record) (string, error)
	invalidate  func(int64)
	taskMu      sync.Mutex
}

// ensureAgentIdentityTaskForProvider adapts records and writeback; the provider coordinator owns locking, rechecks, and registration.
func ensureAgentIdentityTaskForProvider(ctx context.Context, coordinator *acctcore.OpenAITaskCoordinator, register func(context.Context, *acctcore.Record) (string, error), repo ExecutionProviderStore, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, value *ExecutionProvider, expectedTaskID string) error {
	input := ExecutionRecord(value)
	originals := map[*acctcore.Record]*ExecutionProvider{input: value}
	legacyValue := func(record *acctcore.Record) *ExecutionProvider {
		if original, ok := originals[record]; ok {
			return original
		}
		return NewExecutionProvider(record)
	}
	options := acctcore.OpenAITaskOptions{
		FallbackMutex: taskMu,
		Register: func(ctx context.Context, record *acctcore.Record) (string, error) {
			return register(ctx, legacyValue(record).View())
		},
		Persist: func(ctx context.Context, record *acctcore.Record, credentials map[string]any) error {
			original := legacyValue(record)
			err := PersistExecutionCredentials(ctx, repo, original, credentials)
			record.Credentials = original.Record.Credentials
			return err
		},
	}
	if repo != nil {
		options.Read = func(ctx context.Context, id int64) (*acctcore.Record, error) {
			original, err := repo.GetByID(ctx, id)
			record := ExecutionRecord(original)
			originals[record] = original
			return record, err
		}
	}
	if wsInvalidator != nil {
		options.Invalidate = wsInvalidator.InvalidateAgentIdentityWSConnections
	}
	err := coordinator.Ensure(ctx, options, input, expectedTaskID)
	if value != nil {
		value.Record.Credentials = input.Credentials
	}
	return err
}

func NewExecutionAgentIdentity(coordinator *acctcore.OpenAITaskCoordinator, store ExecutionProviderStore, register func(context.Context, *acctcore.Record) (string, error), invalidate func(int64)) *ExecutionAgentIdentity {
	return &ExecutionAgentIdentity{store: store, coordinator: coordinator, register: register, invalidate: invalidate}
}

func (s *ExecutionAgentIdentity) Ensure(ctx context.Context, provider *ExecutionProvider, expectedTaskID string) error {
	if s == nil {
		return errors.New("openai gateway service is nil")
	}
	return ensureAgentIdentityTaskForProvider(ctx, s.coordinator, s.register, s.store, s, &s.taskMu, provider, expectedTaskID)
}

func (s *ExecutionAgentIdentity) Headers(ctx context.Context, provider *ExecutionProvider, token string) (http.Header, error) {
	if provider == nil {
		return nil, errors.New("provider is nil")
	}
	credProvider := provider
	if provider.View().IsShadow() {
		resolved, err := CredentialProvider(ctx, s.store, provider)
		if err != nil {
			return nil, err
		}
		credProvider = resolved
	}
	headers := make(http.Header)
	if credProvider != nil && credProvider.View().IsOpenAIAgentIdentity() {
		agentHeaders, err := buildAgentIdentityAuthenticationHeaders(ctx, s.coordinator, s.register, s.store, s, &s.taskMu, credProvider)
		if err != nil {
			return nil, err
		}
		return agentHeaders, nil
	}
	headers.Set("Authorization", "Bearer "+token)
	return headers, nil
}

func buildAgentIdentityAuthenticationHeaders(ctx context.Context, coordinator *acctcore.OpenAITaskCoordinator, register func(context.Context, *acctcore.Record) (string, error), repo ExecutionProviderStore, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, provider *ExecutionProvider) (http.Header, error) {
	headers, _, err := buildAgentIdentityAuthenticationHeadersWithTask(ctx, coordinator, register, repo, wsInvalidator, taskMu, provider)
	return headers, err
}

// buildAgentIdentityAuthenticationHeadersWithTask also returns the signing task ID for compare-and-swap recovery.
func buildAgentIdentityAuthenticationHeadersWithTask(ctx context.Context, coordinator *acctcore.OpenAITaskCoordinator, register func(context.Context, *acctcore.Record) (string, error), repo ExecutionProviderStore, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, provider *ExecutionProvider) (http.Header, string, error) {
	if provider == nil || !provider.View().IsOpenAIAgentIdentity() {
		return nil, "", errors.New("agent identity provider is required")
	}
	if err := ensureAgentIdentityTaskForProvider(ctx, coordinator, register, repo, wsInvalidator, taskMu, provider, ""); err != nil {
		return nil, "", err
	}
	key, err := provideradapter.AgentIdentityKey(provider.View())
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

func (s *ExecutionAgentIdentity) RefreshHeaders(ctx context.Context, provider *ExecutionProvider, headers http.Header) (http.Header, error) {
	if provider == nil {
		return upstream.CloneHeader(headers), nil
	}
	credProvider := provider
	if provider.View().IsShadow() {
		resolved, err := CredentialProvider(ctx, s.store, provider)
		if err != nil {
			return nil, err
		}
		credProvider = resolved
	}
	if !credProvider.View().IsOpenAIAgentIdentity() {
		return upstream.CloneHeader(headers), nil
	}
	refreshed := upstream.CloneHeader(headers)
	if refreshed == nil {
		refreshed = make(http.Header)
	}
	authHeaders, err := buildAgentIdentityAuthenticationHeaders(ctx, s.coordinator, s.register, s.store, s, &s.taskMu, credProvider)
	if err != nil {
		return nil, err
	}
	refreshed.Set("Authorization", authHeaders.Get("Authorization"))
	return refreshed, nil
}

func (s *ExecutionAgentIdentity) Recover(ctx context.Context, provider *ExecutionProvider, expectedTaskID string) error {
	if provider != nil && provider.View().IsShadow() {
		if resolved, err := CredentialProvider(ctx, s.store, provider); err == nil && resolved != nil && strings.TrimSpace(expectedTaskID) == "" {
			expectedTaskID = strings.TrimSpace(resolved.View().GetCredential("task_id"))
		}
	}
	return s.Ensure(ctx, provider, expectedTaskID)
}

func (s *ExecutionAgentIdentity) UsesAgentIdentity(ctx context.Context, provider *ExecutionProvider) bool {
	if provider == nil {
		return false
	}
	credProvider := provider
	if provider.View().IsShadow() {
		resolved, err := CredentialProvider(ctx, s.store, provider)
		if err != nil {
			return false
		}
		credProvider = resolved
	}
	return credProvider != nil && credProvider.View().IsOpenAIAgentIdentity()
}

// RedactExecutionAgentBody removes credential values before upstream errors reach logs, Ops, or clients.
// Redaction also protects against unexpected credential echoes from upstream.
func RedactExecutionAgentBody(ctx context.Context, repo ExecutionProviderStore, provider *ExecutionProvider, body []byte) []byte {
	if provider == nil || len(body) == 0 {
		return body
	}
	credProvider := provider
	if provider != nil && provider.View().IsShadow() {
		if resolved, err := CredentialProvider(ctx, repo, provider); err == nil && resolved != nil {
			credProvider = resolved
		}
	}
	if credProvider == nil || !credProvider.View().IsOpenAIAgentIdentity() {
		return body
	}
	return openai.RedactAgentIdentityBody(body, credProvider.View().GetCredential)
}

func (s *ExecutionAgentIdentity) Redact(ctx context.Context, provider *ExecutionProvider, body []byte) []byte {
	if !s.UsesAgentIdentity(ctx, provider) {
		return body
	}
	return RedactExecutionAgentBody(ctx, s.store, provider, body)
}

func (s *ExecutionAgentIdentity) InvalidateAgentIdentityWSConnections(id int64) {
	if s.invalidate != nil {
		s.invalidate(id)
	}
}
