package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// agentIdentityCredentialsRepo records credential writes and returns a
// configured target for task tests.
type agentIdentityCredentialsRepo struct {
	ExecutionProviderStore // unused execution storage methods

	// credentials retains the supplied map after a mutex-protected assignment.
	// Assertions run after Ensure completes; nested values are left stable by the fixture.
	credentials map[string]any     // borrowed persisted credential map
	provider    *ExecutionProvider // borrowed task reread target
	// credential assignment synchronization lock
	mu sync.Mutex
}

// newTestAgentIdentityKey creates an Ed25519 signing fixture and its Base64
// PKCS8 encoding. Generation or serialization failures fail the calling test
// before the registration endpoint is constructed.
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

// TestEnsureAgentIdentityTaskPersistsAndRedactsCredentials registers a task
// against a local HTTP fixture and persists its ID. Assertions cover
// target/store task values and private-key exclusion through the sensitive-key
// predicate applied by the test.
func TestEnsureAgentIdentityTaskPersistsAndRedactsCredentials(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"task_id":"task-persisted"}`))
	}))
	defer server.Close()
	coordinator := &providercore.OpenAITaskCoordinator{}
	register := func(ctx context.Context, value *providercore.Record) (string, error) {
		return provideradapter.RegisterAgentIdentityTask(ctx, value, server.URL)
	}

	repo := &agentIdentityCredentialsRepo{}
	provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 7, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformOpenAI, Credentials: map[string]any{
		"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":   key.RuntimeID,
		"agent_private_key":  privateKey,
		"chatgpt_account_id": "provider-test",
	}}}
	service := NewExecutionAgentIdentity(coordinator, repo, register, nil)
	require.NoError(t, service.Ensure(context.Background(), provider, ""))
	require.Equal(t, "task-persisted", provider.View().GetCredential("task_id"))
	require.Equal(t, "task-persisted", repo.credentials["task_id"])
	require.True(t, providercore.IsSensitiveCredentialKey("agent_private_key"))
	redacted := make(map[string]any)
	for key, value := range provider.Record.Credentials {
		if !providercore.IsSensitiveCredentialKey(key) {
			redacted[key] = value
		}
	}
	require.NotContains(t, string(mustAgentIdentityJSON(t, redacted)), privateKey)
}

// TestEnsureAgentIdentityTaskSharesLockAcrossServicesForSameProvider starts
// two independent services for the same provider under one coordinator. Both
// Ensure calls succeed while the local HTTP registration endpoint receives one
// call and the stored provider record retains its task ID.
func TestEnsureAgentIdentityTaskSharesLockAcrossServicesForSameProvider(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	provider := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9001, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformOpenAI, Credentials: map[string]any{
		"auth_mode":         providercore.OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  key.RuntimeID,
		"agent_private_key": privateKey,
	}}}
	repo := &agentIdentityCredentialsRepo{provider: provider}
	registerCalls := 0
	var registerMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registerMu.Lock()
		registerCalls++
		registerMu.Unlock()
		_, _ = w.Write([]byte(`{"task_id":"task-shared"}`))
	}))
	defer server.Close()
	coordinator := &providercore.OpenAITaskCoordinator{}
	register := func(ctx context.Context, value *providercore.Record) (string, error) {
		return provideradapter.RegisterAgentIdentityTask(ctx, value, server.URL)
	}

	// Release both services from the same gate with distinct request targets so
	// only the shared provider coordinator can collapse their registration work.
	start := make(chan struct{})
	errors := make(chan error, 2)
	requests := []*ExecutionProvider{cloneAgentIdentityTestProvider(provider), cloneAgentIdentityTestProvider(provider)}
	for _, request := range requests {
		go func() {
			<-start
			errors <- NewExecutionAgentIdentity(coordinator, repo, register, nil).Ensure(context.Background(), request, "")
		}()
	}
	// Wait for both Ensure results before inspecting the stored task and call count;
	// the buffered result channel lets either worker finish independently of receive order.
	close(start)
	require.NoError(t, <-errors)
	require.NoError(t, <-errors)
	registerMu.Lock()
	defer registerMu.Unlock()
	require.Equal(t, 1, registerCalls)
	require.Equal(t, "task-shared", repo.provider.View().GetCredential("task_id"))
}

// cloneAgentIdentityTestProvider shallow-copies the target and detaches its
// top-level credential map. Nested credential values and other referenced
// record fields remain shared, so the fixture limits independent mutation to
// top-level keys.
func cloneAgentIdentityTestProvider(provider *ExecutionProvider) *ExecutionProvider {
	copy := *provider
	copy.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
	return &copy
}

// GetByID returns the configured borrowed target regardless of lookup ID, with
// nil when unset. Tests use the shared coordinator to serialize task
// consumers; this reader supplies no independent synchronization.
func (r *agentIdentityCredentialsRepo) GetByID(_ context.Context, _ int64) (*ExecutionProvider, error) {
	return r.provider, nil
}

// UpdateCredentials stores the supplied credential map under the fixture mutex
// and returns success. The map remains borrowed; test assertions read it after
// the related Ensure calls have completed.
func (r *agentIdentityCredentialsRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentials = credentials
	return nil
}

// mustAgentIdentityJSON encodes a fixture value and fails the caller when JSON
// serialization fails. It marks itself as a test helper before returning
// successful encoded bytes.
func mustAgentIdentityJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
