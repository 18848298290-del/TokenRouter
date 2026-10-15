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

// execution_agent_identity.go prepares signed authorization and associates
// task credential updates with execution targets.
// Agent Identity authorization signs with a registered task belonging to the
// credential owner, including a Spark parent. The provider coordinator
// serializes task work and rereads current credentials; this adapter binds
// execution-record copies to their loaded targets so persistence and signing
// see the corresponding request state.
//
// Registration and credential storage can fail at separate steps, leaving
// different observed or attempted in-memory values. Header creation returns
// the signing task ID when recovery needs it, and connection invalidation is
// passed to the coordinator after successful persistence. Redaction uses known
// credential values and remains conditional on resolving the owner, so parent
// lookup failure is not a credential-redaction guarantee.

// agentIdentityWSConnectionInvalidator discards connections tied to a
// provider's replaced signing task.
type agentIdentityWSConnectionInvalidator interface {
	// InvalidateAgentIdentityWSConnections discards connections using the provider's replaced task credentials.
	InvalidateAgentIdentityWSConnections(providerID int64)
}

// ExecutionAgentIdentity adapts shared task registration and signed
// authentication for execution targets.
type ExecutionAgentIdentity struct {
	// store supplies shadow-parent reads and persistence for registered task IDs.
	// Ordinary task reuse can proceed without it, while parent resolution requires it.
	store ExecutionProviderStore
	// coordinator is shared by execution, usage, and probes, keeping positive
	// provider task locks common across those consumers within this process.
	coordinator *acctcore.OpenAITaskCoordinator
	// register creates the upstream task when no reusable ID survives locked
	// checks. An upstream task can remain created if the later credential write fails.
	register   func(context.Context, *acctcore.Record) (string, error)
	invalidate func(int64) // optional task-connection invalidation callback
	// taskMu supplies the coordinator fallback lock for targets without a positive
	// persistent ID. Keep the service at one address after this mutex is used.
	taskMu sync.Mutex
}

// ensureAgentIdentityTaskForProvider connects execution targets to the shared
// provider task coordinator. It retains a record-to-target association so
// registration and persistence callbacks use the target loaded for that
// record; the coordinator owns task locking, rereads, and registration order.
//
// The persistence callback copies credentials onto that target before storage
// and mirrors them back to the record even on errors. The final target
// copy-back also runs after Ensure returns. A failed registration or write may
// leave earlier in-memory updates or an upstream task; successful persistence
// permits optional connection invalidation.
func ensureAgentIdentityTaskForProvider(ctx context.Context, coordinator *acctcore.OpenAITaskCoordinator, register func(context.Context, *acctcore.Record) (string, error), repo ExecutionProviderStore, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, value *ExecutionProvider, expectedTaskID string) error {
	input := ExecutionRecord(value)
	// Repository rereads create distinct record pointers. Retain their loaded targets
	// so persistence writes the same target associated with each locked credential observation.
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
			// Task persistence can mutate credentials before failing. Mirror the target
			// map back so the coordinator receives that attempted update along with its error.
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
	// The coordinator may install a fresher task during its locked reread. Transfer
	// the resulting observation even when later registration or persistence returns an error.
	if value != nil {
		value.Record.Credentials = input.Credentials
	}
	return err
}

// NewExecutionAgentIdentity binds the shared task coordinator, optional store,
// registration callback, and optional connection invalidator. Task work
// requires the coordinator and registration dependencies; the returned service
// supplies its own fallback mutex.
func NewExecutionAgentIdentity(coordinator *acctcore.OpenAITaskCoordinator, store ExecutionProviderStore, register func(context.Context, *acctcore.Record) (string, error), invalidate func(int64)) *ExecutionAgentIdentity {
	return &ExecutionAgentIdentity{store: store, coordinator: coordinator, register: register, invalidate: invalidate}
}

// Ensure delegates task reuse or replacement to the shared coordinator and
// copies observed credentials back. A nil receiver returns an error;
// expectedTaskID requests replacement when it matches the current task, while
// another task is reused.
func (s *ExecutionAgentIdentity) Ensure(ctx context.Context, provider *ExecutionProvider, expectedTaskID string) error {
	if s == nil {
		return errors.New("openai gateway service is nil")
	}
	return ensureAgentIdentityTaskForProvider(ctx, s.coordinator, s.register, s.store, s, &s.taskMu, provider, expectedTaskID)
}

// Headers returns signed Agent Identity authorization or a Bearer header
// containing token. Nil targets return an error; shadow targets resolve their
// credential parent first, and signed authorization ensures a usable task
// before signing.
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

// buildAgentIdentityAuthenticationHeaders delegates task preparation and
// signing, returning the authentication headers and error. It discards the
// signing task ID; callers needing compare-based recovery use the WithTask
// variant instead.
func buildAgentIdentityAuthenticationHeaders(ctx context.Context, coordinator *acctcore.OpenAITaskCoordinator, register func(context.Context, *acctcore.Record) (string, error), repo ExecutionProviderStore, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, provider *ExecutionProvider) (http.Header, error) {
	headers, _, err := buildAgentIdentityAuthenticationHeadersWithTask(ctx, coordinator, register, repo, wsInvalidator, taskMu, provider)
	return headers, err
}

// buildAgentIdentityAuthenticationHeadersWithTask ensures task credentials,
// constructs a current-time assertion, and returns its task ID with
// authorization headers. A non-Agent-Identity target is rejected; the ID
// identifies the observed task for later replacement comparison.
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

// RefreshHeaders clones supplied headers and refreshes authorization when the
// target's credential owner uses Agent Identity. Nil or ordinary targets
// return cloned headers, shadow resolution errors propagate, and other header
// values retain their copied storage.
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

// Recover derives an expected task ID from a successfully resolved shadow
// parent when none was supplied, then invokes Ensure. Parent read failures are
// ignored for that derivation, leaving Ensure to evaluate the original target.
func (s *ExecutionAgentIdentity) Recover(ctx context.Context, provider *ExecutionProvider, expectedTaskID string) error {
	// Recovery can begin with an absent observed task. A successfully loaded parent
	// supplies that comparison seed; failed reads leave it for Ensure to handle.
	if provider != nil && provider.View().IsShadow() {
		if resolved, err := CredentialProvider(ctx, s.store, provider); err == nil && resolved != nil && strings.TrimSpace(expectedTaskID) == "" {
			expectedTaskID = strings.TrimSpace(resolved.View().GetCredential("task_id"))
		}
	}
	return s.Ensure(ctx, provider, expectedTaskID)
}

// UsesAgentIdentity checks the target's credential owner for Agent Identity
// mode. Nil targets and failed shadow resolution return false; resolving a
// parent requires the service's configured store.
func (s *ExecutionAgentIdentity) UsesAgentIdentity(ctx context.Context, provider *ExecutionProvider) bool {
	if provider == nil {
		return false
	}
	credProvider := provider
	if provider.View().IsShadow() {
		resolved, err := CredentialProvider(ctx, s.store, provider)
		if err != nil {
			// Parent lookup supplied no usable mode evidence. Report false here so the
			// caller does not attempt signing with an unresolved shadow credential owner.
			return false
		}
		credProvider = resolved
	}
	return credProvider != nil && credProvider.View().IsOpenAIAgentIdentity()
}

// RedactExecutionAgentBody replaces known Agent Identity credential values
// using the resolved credential owner. Nil targets or empty bodies return
// unchanged; failed shadow lookup retains the original target for
// classification, so this helper cannot guarantee redaction when credentials
// are unavailable.
func RedactExecutionAgentBody(ctx context.Context, repo ExecutionProviderStore, provider *ExecutionProvider, body []byte) []byte {
	if provider == nil || len(body) == 0 {
		return body
	}
	credProvider := provider
	// Use parent values when available because shadows keep routing data locally;
	// a failed lookup leaves the supplied target for the subsequent mode check.
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

// Redact applies known-value credential redaction when UsesAgentIdentity
// recognizes the target's credential owner. Unrecognized targets and failed
// parent resolution return the body unchanged, so callers retain their other
// error-sanitization rules.
func (s *ExecutionAgentIdentity) Redact(ctx context.Context, provider *ExecutionProvider, body []byte) []byte {
	if !s.UsesAgentIdentity(ctx, provider) {
		return body
	}
	return RedactExecutionAgentBody(ctx, s.store, provider, body)
}

// InvalidateAgentIdentityWSConnections invokes the configured connection
// invalidation callback for id after task replacement. A missing callback
// performs no action; the shared task coordinator decides when successful
// persistence permits invalidation.
func (s *ExecutionAgentIdentity) InvalidateAgentIdentityWSConnections(id int64) {
	if s.invalidate != nil {
		s.invalidate(id)
	}
}
