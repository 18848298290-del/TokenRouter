package provider_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

type grokOAuthClientStub struct {
	refreshResponse     *xai.TokenResponse
	ssoResponse         *xai.TokenResponse
	loginResult         *providercore.GrokPasswordLoginResult
	loginEmail          string
	loginPassword       string
	exchangeCalls       int
	exchangeRedirectURI string
}

// credentialReadStore supplies provider records for credential tests.
type credentialReadStore struct {
	gatewayprovider.ExecutionProviderStore
	providersByID map[int64]*gatewayprovider.ExecutionProvider
}

// The lock fixture calls Apply and blocks during the read.
// Cancel and join the operation before releasing the fixture; it performs no conditional writes.
type (
	credentialMutationHoldKey struct{}
	credentialMutationHold    struct {
		core    *providercore.GrokCredentialRecovery
		id      int64
		started chan struct{}
		done    chan struct{}
		cancel  context.CancelFunc
		restore func()
	}
)

type tokenRefreshProviderRepo struct {
	credentialReadStore
	updateCalls                  int
	fullUpdateCalls              int
	updateCredentialsCalls       int
	setErrorCalls                int
	clearTempCalls               int
	setTempUnschedCalls          int
	updateExtraCalls             int
	lastErrorMessage             string
	lastTempUnschedReason        string
	lastExtraUpdates             map[string]any
	lastProvider                 *gatewayprovider.ExecutionProvider
	updateErr                    error
	cancelOnUpdate               context.CancelFunc
	conditionalErrorCalls        int
	conditionalTempCalls         int
	conditionalSuccessCalls      int
	conditionalErrorErr          error
	conditionalTempErr           error
	conditionalSuccessErr        error
	snapshotReads                bool
	respectReadContext           bool
	getByIDCalls                 int
	durableReadDelay             time.Duration
	mutateSchedulingOnSuccessCAS bool
	reauthorizeOnErrorCAS        bool
	reauthorizeOnTempCAS         bool
	repairProxyOnErrorCAS        bool
	repairProxyOnTempCAS         bool
	setErrorErr                  error
	setTempUnschedErr            error
	beforeConditionalState       func()
}

type tokenRefresherStub struct {
	credentials map[string]any
	err         error
	calls       int
}

// tokenSourceFixtureReader supplies gateway test records to the provider module's refresh implementation.
type tokenSourceFixtureReader struct {
	source gatewayprovider.ExecutionProviderStore
}

type grokTokenCacheForProviderTest struct {
	token        string
	setKey       string
	setToken     string
	setTTL       time.Duration
	lockResult   bool
	releaseCalls int
	deletedKeys  []string
	deleteErr    error
	getCalls     int
	mu           sync.Mutex
}

type grokCredentialPersistingRepo struct {
	*tokenRefreshProviderRepo
}

type grokCredentialProxyRepoStub struct {
	egress.ProxyRepository
	proxy *egress.Proxy
	err   error
}

type grokCredentialBlockingRepo struct {
	*tokenRefreshProviderRepo
	setErrorStarted chan struct{}
	setTempStarted  chan struct{}
	onceError       sync.Once
	onceTemp        sync.Once
}

type grokCredentialCommitThenCancelRepo struct {
	*tokenRefreshProviderRepo
	returnErr error
}

type grokCredentialUncommittedDeadlineRepo struct {
	*tokenRefreshProviderRepo
}

type grokCredentialBlockingCache struct {
	providercore.AccessTokenCache
	deleteStarted chan struct{}
	releaseDelete chan struct{}
	once          sync.Once
	mu            sync.Mutex
	deleted       bool
}

type grokCredentialSequencedRepo struct {
	*tokenRefreshProviderRepo
	mu      sync.Mutex
	latest  *gatewayprovider.ExecutionProvider
	getCall int
}

type grokCredentialRereadFailureRepo struct {
	*tokenRefreshProviderRepo
	provider *gatewayprovider.ExecutionProvider
	err      error
}

type grokCredentialCountingRefresher struct {
	refreshCalls int
}

func (s *grokOAuthClientStub) ExchangeCode(_ context.Context, _, _, redirectURI, _, _ string) (*xai.TokenResponse, error) {
	s.exchangeCalls++
	s.exchangeRedirectURI = redirectURI
	return &xai.TokenResponse{AccessToken: "access-token"}, nil
}

func (s *grokOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return s.refreshResponse, nil
}

func (s *grokOAuthClientStub) LoginWithPassword(_ context.Context, email, password, _ string) (*providercore.GrokPasswordLoginResult, error) {
	s.loginEmail = email
	s.loginPassword = password
	return s.loginResult, nil
}

func (s *grokOAuthClientStub) ConvertSSOToBuild(context.Context, string, string) (*xai.TokenResponse, error) {
	return s.ssoResponse, nil
}

// newGrokAuthorizationForTest binds the proxy reader and authorization client fixture.
func newGrokAuthorizationForTest(proxies egress.ProxyRepository, client providercore.GrokAuthorizationClient) *providercore.GrokAuthorization {
	return providercore.NewGrokAuthorization(client, provideradapter.GrokAuthorizationOptions(proxies, nil))
}

func stopGrokAuthorizationForTest(t *testing.T, authorization *providercore.GrokAuthorization) {
	t.Helper()
	require.NoError(t, authorization.StopContext(context.Background()))
}

func (s *credentialReadStore) GetByID(_ context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	if value, ok := s.providersByID[id]; ok {
		return value, nil
	}
	return nil, errors.New("provider not found")
}

func grokCredentialMutationSnapshot(value *gatewayprovider.ExecutionProvider) providercore.CredentialMutationSnapshot {
	return providercore.GrokCredentialMutationSnapshot(gatewayprovider.ExecutionRecord(value))
}

func credentialMutationForTest(value forwardcore.GrokCredentialFailure) providercore.GrokCredentialMutation {
	return providercore.GrokCredentialMutation{Permanent: value.Permanent, Transient: value.Transient, Reason: string(value.Reason), Snapshot: value.Snapshot(), VerifyMissing: value.Reason == forwardcore.GrokCredentialReasonMissing, VerifyProxy: value.Reason == forwardcore.GrokCredentialReasonProxyInvalid}
}

func credentialBlocked(value *gatewayhttp.RequestCredentialExecutor, target *gatewayprovider.ExecutionProvider) bool {
	return value.Runtime.Runtime.Blocked(target.Record.ID, func() string { return providercore.RefreshCredentialIdentity(gatewayprovider.ExecutionRecord(target)) })
}

func newRequestCredentialsFixture(store gatewayprovider.ExecutionProviderStore, tokens *providercore.GrokTokenSource) *gatewayhttp.RequestCredentialExecutor {
	blocks := providercore.NewRuntimeBlockState(time.Now)
	recovery := &providercore.GrokCredentialRecovery{Runtime: blocks, Warn: slog.Warn}
	source := &providercore.OpenAIExecutionCredentials{}
	if store != nil {
		source.Parent = func(ctx context.Context, id int64) (*providercore.Record, error) {
			value, err := store.GetByID(ctx, id)
			return gatewayprovider.ExecutionRecord(value), err
		}
		recovery.Read = func(ctx context.Context, id int64) (*providercore.Record, error) {
			if held, ok := ctx.Value(credentialMutationHoldKey{}).(*credentialMutationHold); ok {
				return held.read(ctx)
			}
			return source.Parent(ctx, id)
		}
		recovery.State, _ = store.(providercore.GrokCredentialStateWriter)
	}
	if tokens != nil {
		source.Grok = tokens.GetAccessToken
		recovery.Invalidate = tokens.InvalidateToken
	}
	return &gatewayhttp.RequestCredentialExecutor{Runtime: &gatewayprovider.RequestCredentials{Source: source, HasGrokTokenSource: tokens != nil, Recovery: recovery, Runtime: blocks}}
}

func newCredentialMutationHold(core *providercore.GrokCredentialRecovery, id int64) *credentialMutationHold {
	return &credentialMutationHold{core: core, id: id, started: make(chan struct{}), done: make(chan struct{})}
}

func (h *credentialMutationHold) read(ctx context.Context) (*providercore.Record, error) {
	close(h.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *credentialMutationHold) Lock(ctx context.Context) error {
	if h.core.Read == nil {
		h.core.Read = func(ctx context.Context, _ int64) (*providercore.Record, error) { return h.read(ctx) }
		h.restore = func() { h.core.Read = nil }
	}
	run, cancel := context.WithCancel(context.WithValue(ctx, credentialMutationHoldKey{}, h))
	h.cancel = cancel
	go func() {
		defer close(h.done)
		_, _ = h.core.Apply(run, &providercore.Record{ID: h.id}, providercore.GrokCredentialMutation{Transient: true})
	}()
	select {
	case <-h.started:
		return nil
	case <-ctx.Done():
		cancel()
		<-h.done
		return ctx.Err()
	}
}

func (h *credentialMutationHold) Unlock() {
	h.cancel()
	<-h.done
	if h.restore != nil {
		h.restore()
	}
}

func (r *tokenRefreshProviderRepo) Update(ctx context.Context, provider *gatewayprovider.ExecutionProvider) error {
	r.updateCalls++
	r.fullUpdateCalls++
	r.lastProvider = provider
	return r.updateErr
}

func (r *tokenRefreshProviderRepo) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.updateCalls++
	r.updateCredentialsCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	cloned := querycache.ShallowMap(credentials)
	if r.providersByID != nil {
		if acc, ok := r.providersByID[id]; ok && acc != nil {
			acc.Record.Credentials = cloned
			r.lastProvider = acc
			if r.cancelOnUpdate != nil {
				r.cancelOnUpdate()
			}
			return nil
		}
	}
	r.lastProvider = &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: id, Credentials: cloned}}
	if r.cancelOnUpdate != nil {
		r.cancelOnUpdate()
	}
	return nil
}

func (r *tokenRefreshProviderRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	if r.respectReadContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.getByIDCalls++
	if r.getByIDCalls > 1 && r.durableReadDelay > 0 {
		timer := time.NewTimer(r.durableReadDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	provider, err := r.credentialReadStore.GetByID(ctx, id)
	if err != nil || !r.snapshotReads {
		return provider, err
	}
	return grokCredentialStoredSnapshot(provider), nil
}

func (r *tokenRefreshProviderRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	return r.setErrorErr
}

func (r *tokenRefreshProviderRepo) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempCalls++
	return nil
}

func (r *tokenRefreshProviderRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	return r.setTempUnschedErr
}

func (r *tokenRefreshProviderRepo) SetGrokCredentialErrorIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	errorMsg string,
) (bool, error) {
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	provider := r.providersByID[id]
	if !grokCredentialSnapshotMatchesProvider(provider, snapshot) ||
		(errorMsg == string(forwardcore.GrokCredentialReasonProxyInvalid) && provider.Record.Proxy != nil) {
		return false, nil
	}
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	if r.setErrorErr != nil {
		return false, r.setErrorErr
	}
	provider.Record.Status = providercore.StatusError
	provider.Record.Schedulable = false
	provider.Record.ErrorMessage = errorMsg
	return true, nil
}

func (r *tokenRefreshProviderRepo) SetGrokCredentialTempUnschedulableIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	provider := r.providersByID[id]
	if !grokCredentialSnapshotMatchesProvider(provider, snapshot) {
		return false, nil
	}
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	if r.setTempUnschedErr != nil {
		return false, r.setTempUnschedErr
	}
	value := until
	provider.Record.TempUnschedulableUntil = &value
	return true, nil
}

func grokCredentialSnapshotMatchesProvider(provider *gatewayprovider.ExecutionProvider, snapshot providercore.CredentialMutationSnapshot) bool {
	return provider != nil && provider.View().IsGrokOAuth() && provider.View().IsSchedulable() &&
		grokCredentialMutationSnapshot(provider).CredentialsJSON == snapshot.CredentialsJSON &&
		providercore.GrokCredentialProxyIDsEqual(provider.Record.ProxyID, snapshot.ProxyID)
}

func (r *tokenRefreshProviderRepo) SetGrokOAuthRefreshErrorIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	errorMsg string,
) (bool, error) {
	r.conditionalErrorCalls++
	if r.conditionalErrorErr != nil {
		return false, r.conditionalErrorErr
	}
	provider := r.providersByID[id]
	if provider == nil {
		return false, nil
	}
	if r.reauthorizeOnErrorCAS {
		r.reauthorizeOnErrorCAS = false
		provider.Record.Credentials = map[string]any{
			"access_token":   "fresh-access",
			"refresh_token":  "fresh-refresh",
			"_token_version": int64(2),
		}
		provider.Record.Status = billing.StatusActive
		provider.Record.Schedulable = true
	}
	if r.repairProxyOnErrorCAS {
		r.repairProxyOnErrorCAS = false
		proxyID := int64(902)
		provider.Record.ProxyID = &proxyID
	}
	if provider.Record.Status != billing.StatusActive || provider.Record.Platform != capability.PlatformGrok || provider.Record.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(provider.Record.Credentials, expectedCredentials) || !reflect.DeepEqual(provider.Record.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	provider.Record.Status = providercore.StatusError
	provider.Record.Schedulable = false
	provider.Record.ErrorMessage = errorMsg
	return true, nil
}

func (r *tokenRefreshProviderRepo) UpdateGrokOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	r.conditionalSuccessCalls++
	if r.conditionalSuccessErr != nil {
		return false, r.conditionalSuccessErr
	}
	provider := r.providersByID[id]
	if provider != nil && r.mutateSchedulingOnSuccessCAS {
		r.mutateSchedulingOnSuccessCAS = false
		provider.Record.Status = billing.StatusDisabled
		provider.Record.Schedulable = false
		resetAt := time.Now().Add(30 * time.Minute)
		provider.Record.RateLimitResetAt = &resetAt
	}
	if provider == nil || provider.Record.Platform != capability.PlatformGrok ||
		provider.Record.Type != capability.ProviderTypeOAuth || !reflect.DeepEqual(provider.Record.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(provider.Record.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.updateCalls++
	r.updateCredentialsCalls++
	provider.Record.Credentials = querycache.ShallowMap(credentials)
	r.lastProvider = provider
	if r.cancelOnUpdate != nil {
		r.cancelOnUpdate()
	}
	return true, nil
}

func (r *tokenRefreshProviderRepo) SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	until time.Time,
	reason string,
) (bool, error) {
	r.conditionalTempCalls++
	if r.conditionalTempErr != nil {
		return false, r.conditionalTempErr
	}
	provider := r.providersByID[id]
	if provider == nil {
		return false, nil
	}
	if r.reauthorizeOnTempCAS {
		r.reauthorizeOnTempCAS = false
		provider.Record.Credentials = map[string]any{
			"access_token":   "fresh-access",
			"refresh_token":  "fresh-refresh",
			"_token_version": int64(2),
		}
		provider.Record.Status = billing.StatusActive
		provider.Record.Schedulable = true
	}
	if r.repairProxyOnTempCAS {
		r.repairProxyOnTempCAS = false
		proxyID := int64(902)
		provider.Record.ProxyID = &proxyID
	}
	if provider.Record.Status != billing.StatusActive || provider.Record.Platform != capability.PlatformGrok || provider.Record.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(provider.Record.Credentials, expectedCredentials) || !reflect.DeepEqual(provider.Record.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	provider.Record.TempUnschedulableUntil = &until
	provider.Record.TempUnschedulableReason = reason
	return true, nil
}

func (r *tokenRefreshProviderRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.updateExtraCalls++
	r.lastExtraUpdates = querycache.ShallowMap(updates)
	if r.providersByID != nil {
		if acc, ok := r.providersByID[id]; ok && acc != nil {
			if acc.Record.Extra == nil {
				acc.Record.Extra = make(map[string]any, len(updates))
			}
			maps.Copy(acc.Record.Extra, updates)
		}
	}
	return nil
}

func (r *tokenRefresherStub) CanRefresh(provider *providercore.Record) bool {
	return true
}

func (r *tokenRefresherStub) NeedsRefresh(provider *providercore.Record, refreshWindowDuration time.Duration) bool {
	return true
}

func (r *tokenRefresherStub) Refresh(ctx context.Context, provider *providercore.Record) (map[string]any, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.credentials, nil
}

func (r *tokenRefresherStub) CacheKey(provider *providercore.Record) string {
	return "test:stub:" + provider.Platform
}

// grokCredentialStoredSnapshot copies the execution target and initializes missing credentials.
func grokCredentialStoredSnapshot(value *gatewayprovider.ExecutionProvider) *gatewayprovider.ExecutionProvider {
	copy := gatewayprovider.NewExecutionProvider(gatewayprovider.ExecutionRecord(value))
	if copy != nil && copy.Record.Credentials == nil {
		copy.Record.Credentials = map[string]any{}
	}
	return copy
}

func (r tokenSourceFixtureReader) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	v, err := r.source.GetByID(ctx, id)
	return gatewayprovider.ExecutionRecord(v), err
}

func tokenSourceFixtureRepository(repo gatewayprovider.ExecutionProviderStore) providercore.RefreshRepository {
	if repo == nil {
		return nil
	}
	reader := tokenSourceFixtureReader{repo}
	writer, hasWriter := repo.(providercore.CredentialRefreshWriter)
	grok, hasGrok := repo.(providercore.GrokRefreshSuccessWriter)
	if hasWriter && hasGrok {
		return struct {
			providercore.RefreshRepository
			providercore.CredentialRefreshWriter
			providercore.GrokRefreshSuccessWriter
		}{reader, writer, grok}
	}
	if hasWriter {
		return struct {
			providercore.RefreshRepository
			providercore.CredentialRefreshWriter
		}{reader, writer}
	}
	if hasGrok {
		return struct {
			providercore.RefreshRepository
			providercore.GrokRefreshSuccessWriter
		}{reader, grok}
	}
	return reader
}

func (c *grokTokenCacheForProviderTest) GetAccessToken(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	if c.token == "" {
		return "", errors.New("not cached")
	}
	return c.token, nil
}

func (c *grokTokenCacheForProviderTest) SetAccessToken(_ context.Context, key string, token string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setKey = key
	c.setToken = token
	c.setTTL = ttl
	return nil
}

func (c *grokTokenCacheForProviderTest) DeleteAccessToken(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletedKeys = append(c.deletedKeys, key)
	return c.deleteErr
}

func (c *grokTokenCacheForProviderTest) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return c.lockResult, nil
}

func (c *grokTokenCacheForProviderTest) ReleaseRefreshLock(context.Context, string) error {
	c.releaseCalls++
	return nil
}

// UpdateOAuthCredentialsIfUnchanged updates fixture credentials when the observed version matches.
func (r *tokenRefreshProviderRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	current := r.providersByID[version.ID]
	if current == nil {
		return false, nil
	}
	expected := current.Record.Credentials
	if expected == nil {
		expected = map[string]any{}
	}
	if current.Record.ID != version.ID || current.Record.Platform != version.Platform || current.Record.Type != version.Type || current.Record.Status != version.Status || !reflect.DeepEqual(current.Record.ProxyID, version.ProxyID) || !reflect.DeepEqual(expected, version.Credentials) {
		return false, nil
	}
	err := r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

// ClearRefreshCooldownIfUnchanged checks the current row when present and records accepted cleanup calls.
func (r *tokenRefreshProviderRepo) ClearRefreshCooldownIfUnchanged(ctx context.Context, v providercore.RefreshCooldownVersion) (bool, error) {
	if current := r.providersByID[v.ID]; current != nil && !reflect.DeepEqual(providercore.ObserveRefreshCooldown(gatewayprovider.ExecutionRecord(current)), v) {
		return false, nil
	}
	return true, r.ClearTempUnschedulable(ctx, v.ID)
}

// refreshFailureMatchesFixture compares identity and credential versions, treating nil credentials as empty.
func refreshFailureMatchesFixture(value *gatewayprovider.ExecutionProvider, version providercore.RefreshFailureVersion) bool {
	if value == nil {
		return false
	}
	credentials := value.Record.Credentials
	if credentials == nil {
		credentials = map[string]any{}
	}
	return value.Record.ID == version.ID && value.Record.Platform == version.Platform && value.Record.Type == version.Type && value.Record.Status == version.Status && value.Record.Schedulable == version.Schedulable && reflect.DeepEqual(credentials, version.Credentials) && reflect.DeepEqual(value.Record.ProxyID, version.ProxyID)
}

// ApplyOAuthRefreshFailure compares the current row when a read set is configured; otherwise it accepts the supplied version.
func (r *tokenRefreshProviderRepo) ApplyOAuthRefreshFailure(ctx context.Context, version providercore.RefreshFailureVersion, failure providercore.RefreshFailure) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.providersByID != nil && !refreshFailureMatchesFixture(r.providersByID[version.ID], version) {
		return false, nil
	}
	var err error
	if failure.Kind == providercore.RefreshFailurePermanent {
		err = r.SetError(ctx, version.ID, failure.Message)
	} else {
		err = r.SetTempUnschedulable(ctx, version.ID, failure.Until, failure.Message)
	}
	if err == nil && failure.Kind == providercore.RefreshFailurePermanent && r.providersByID != nil {
		if value := r.providersByID[version.ID]; value != nil {
			value.Record.Status = providercore.StatusError
			value.Record.Schedulable = false
			value.Record.ErrorMessage = failure.Message
		}
	}
	return err == nil, err
}

func (r *tokenRefreshProviderRepo) ClearAntigravityRefreshRequest(ctx context.Context, version providercore.CredentialVersion) (bool, error) {
	if r.providersByID != nil {
		value := r.providersByID[version.ID]
		if value == nil || !refreshFailureMatchesFixture(value, providercore.RefreshFailureVersion{CredentialVersion: version, Schedulable: value.Record.Schedulable}) {
			return false, nil
		}
	}
	err := r.UpdateExtra(ctx, version.ID, providercore.ClearedAntigravityRefreshRequest())
	return err == nil, err
}

// newGrokTokenSourceForTest binds the fixture repository and token cache.
func newGrokTokenSourceForTest(repo gatewayprovider.ExecutionProviderStore, cache providercore.AccessTokenCache) *providercore.GrokTokenSource {
	return &providercore.GrokTokenSource{Repository: tokenSourceFixtureRepository(repo), Cache: cache, Policy: providercore.GrokProviderRefreshPolicy()}
}

// bindGrokRefreshForTest binds the test refresher to the token source.
func bindGrokRefreshForTest(source *providercore.GrokTokenSource, refresh *providercore.OAuthRefreshAPI, executor providercore.OAuthRefreshExecutor) {
	source.Refresh = func(ctx context.Context, record *providercore.Record, window time.Duration) (*providercore.OAuthRefreshResult, error) {
		return refresh.RefreshIfNeeded(ctx, record, executor, window)
	}
}

// newGrokCredentialRefreshForTest creates a refresher using the fixture repository and cache.
func newGrokCredentialRefreshForTest(repo gatewayprovider.ExecutionProviderStore, cache providercore.AccessTokenCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(tokenSourceFixtureRepository(repo), cache, providercore.RefreshOptions{Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error, Platform: providercore.ProviderRefreshPlatformPolicy()})
}

func (r *grokCredentialPersistingRepo) SetError(ctx context.Context, id int64, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.tokenRefreshProviderRepo.SetError(ctx, id, message); err != nil {
		return err
	}
	if provider := r.providersByID[id]; provider != nil {
		provider.Record.Status = providercore.StatusError
		provider.Record.Schedulable = false
		provider.Record.ErrorMessage = message
	}
	return nil
}

func (r *grokCredentialProxyRepoStub) GetByID(context.Context, int64) (*egress.Proxy, error) {
	return r.proxy, r.err
}

func (r *grokCredentialUncommittedDeadlineRepo) SetGrokCredentialErrorIfMatch(
	context.Context,
	int64,
	providercore.CredentialMutationSnapshot,
	string,
) (bool, error) {
	return false, context.DeadlineExceeded
}

func (r *grokCredentialUncommittedDeadlineRepo) SetGrokCredentialTempUnschedulableIfMatch(
	context.Context,
	int64,
	providercore.CredentialMutationSnapshot,
	time.Time,
	string,
) (bool, error) {
	return false, context.DeadlineExceeded
}

func (r *grokCredentialCommitThenCancelRepo) SetGrokCredentialErrorIfMatch(
	ctx context.Context,
	id int64,
	_ providercore.CredentialMutationSnapshot,
	reason string,
) (bool, error) {
	provider := r.providersByID[id]
	provider.Record.Status = providercore.StatusError
	provider.Record.Schedulable = false
	provider.Record.ErrorMessage = reason
	if r.returnErr != nil {
		return false, r.returnErr
	}
	<-ctx.Done()
	return false, ctx.Err()
}

func (r *grokCredentialCommitThenCancelRepo) SetGrokCredentialTempUnschedulableIfMatch(
	ctx context.Context,
	id int64,
	_ providercore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	provider := r.providersByID[id]
	provider.Record.TempUnschedulableUntil = &until
	provider.Record.TempUnschedulableReason = reason
	if r.returnErr != nil {
		return false, r.returnErr
	}
	<-ctx.Done()
	return false, ctx.Err()
}

func (r *grokCredentialBlockingRepo) SetError(ctx context.Context, _ int64, _ string) error {
	r.onceError.Do(func() { close(r.setErrorStarted) })
	<-ctx.Done()
	return ctx.Err()
}

func (r *grokCredentialBlockingRepo) SetTempUnschedulable(ctx context.Context, _ int64, _ time.Time, _ string) error {
	r.onceTemp.Do(func() { close(r.setTempStarted) })
	<-ctx.Done()
	return ctx.Err()
}

func (r *grokCredentialBlockingRepo) SetGrokCredentialErrorIfMatch(
	ctx context.Context,
	_ int64,
	_ providercore.CredentialMutationSnapshot,
	_ string,
) (bool, error) {
	r.onceError.Do(func() { close(r.setErrorStarted) })
	<-ctx.Done()
	return false, ctx.Err()
}

func (r *grokCredentialBlockingRepo) SetGrokCredentialTempUnschedulableIfMatch(
	ctx context.Context,
	_ int64,
	_ providercore.CredentialMutationSnapshot,
	_ time.Time,
	_ string,
) (bool, error) {
	r.onceTemp.Do(func() { close(r.setTempStarted) })
	<-ctx.Done()
	return false, ctx.Err()
}

func (r *grokCredentialCountingRefresher) CacheKey(provider *providercore.Record) string {
	return providercore.GrokTokenCacheKey(provider)
}

func (r *grokCredentialCountingRefresher) CanRefresh(*providercore.Record) bool { return true }

func (r *grokCredentialCountingRefresher) NeedsRefresh(*providercore.Record, time.Duration) bool {
	return true
}

func (r *grokCredentialCountingRefresher) Refresh(context.Context, *providercore.Record) (map[string]any, error) {
	r.refreshCalls++
	return map[string]any{"access_token": "must-not-be-used"}, nil
}

func (r *grokCredentialRereadFailureRepo) GetByID(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return r.provider, r.err
}

func (r *grokCredentialSequencedRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCall++
	if r.getCall > 1 && r.latest != nil {
		return r.latest, nil
	}
	return r.tokenRefreshProviderRepo.GetByID(ctx, id)
}

func (c *grokCredentialBlockingCache) DeleteAccessToken(ctx context.Context, _ string) error {
	c.once.Do(func() { close(c.deleteStarted) })
	select {
	case <-c.releaseDelete:
		c.mu.Lock()
		c.deleted = true
		c.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *grokCredentialBlockingCache) wasDeleted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleted
}

func TestGetRequestCredentialMapsPermanentGrokOAuthFailureAndRedactsSecrets(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(701)
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{
		err: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant access_token=leaked-access refresh_token=leaked-refresh"),
	})
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	token, kind, err := svc.Resolve(context.Background(), c, provider)
	require.Error(t, err)
	require.Empty(t, token)
	require.Empty(t, kind)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GatewayFailureStageProviderAuth, failoverErr.Stage)
	require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, forwardcore.GrokCredentialReasonRevoked, failoverErr.Reason)
	require.True(t, failoverErr.ShouldRetryNextProvider())
	require.Equal(t, 0, failoverErr.StatusCode)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.ClientStatusCode)
	require.NotContains(t, err.Error(), "leaked-access")
	require.NotContains(t, err.Error(), "leaked-refresh")

	require.Equal(t, 1, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.True(t, credentialBlocked(svc, provider))
	require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
	require.NotContains(t, repo.lastErrorMessage, "leaked-access")
	require.NotContains(t, repo.lastErrorMessage, "leaked-refresh")

	rawEvents, ok := c.Get(gatewayhttp.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, string(forwardcore.GatewayFailureStageProviderAuth), events[0].Stage)
	require.Equal(t, string(forwardcore.GatewayFailureScopeProvider), events[0].Scope)
	require.Equal(t, string(forwardcore.GrokCredentialReasonRevoked), events[0].Reason)
	require.Zero(t, events[0].UpstreamStatusCode)
	require.NotContains(t, events[0].Message, "leaked-access")
	require.NotContains(t, events[0].Message, "leaked-refresh")
}

func TestGetRequestCredentialPermanentMappingsPersistAndInvalidate(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(*gatewayprovider.ExecutionProvider)
		refreshErr  error
		wantReason  forwardcore.GatewayFailureReason
		cachedToken string
	}{
		{
			name: "missing refresh credential",
			prepare: func(provider *gatewayprovider.ExecutionProvider) {
				delete(provider.Record.Credentials, "refresh_token")
			},
			wantReason: forwardcore.GrokCredentialReasonMissing,
		},
		{
			name: "missing access credential",
			prepare: func(provider *gatewayprovider.ExecutionProvider) {
				delete(provider.Record.Credentials, "access_token")
				provider.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
			},
			wantReason:  forwardcore.GrokCredentialReasonMissing,
			cachedToken: "stale-cached-access",
		},
		{
			name:       "explicit entitlement action required",
			prepare:    func(*gatewayprovider.ExecutionProvider) {},
			refreshErr: apperror.New(http.StatusForbidden, "GROK_OAUTH_ENTITLEMENT_DENIED", "access_denied"),
			wantReason: forwardcore.GrokCredentialReasonEntitlement,
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(720 + index))
			provider.Record.Status = billing.StatusActive
			provider.Record.Schedulable = true
			tt.prepare(provider)
			baseRepo := &tokenRefreshProviderRepo{}
			baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo := &grokCredentialPersistingRepo{tokenRefreshProviderRepo: baseRepo}
			cache := &grokTokenCacheForProviderTest{lockResult: true, token: tt.cachedToken}
			tokenSource := newGrokTokenSourceForTest(repo, cache)
			bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{err: tt.refreshErr})
			svc := newRequestCredentialsFixture(repo, tokenSource)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			_, _, err := svc.Resolve(context.Background(), c, provider)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, tt.wantReason, failoverErr.Reason)
			require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
			require.Equal(t, 1, baseRepo.setErrorCalls)
			require.Equal(t, providercore.StatusError, provider.Record.Status)
			require.False(t, provider.Record.Schedulable)
			require.True(t, credentialBlocked(svc, provider))
			require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
		})
	}
}

func TestGetRequestCredentialMissingAccessNeverRefreshesAndPermanentlyFailsOver(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt *time.Time
	}{
		{name: "expiry missing"},
		{name: "expired", expiresAt: func() *time.Time { value := time.Now().Add(-time.Minute); return &value }()},
		{name: "near expiry", expiresAt: func() *time.Time { value := time.Now().Add(30 * time.Minute); return &value }()},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(760 + index))
			provider.Record.Schedulable = true
			delete(provider.Record.Credentials, "access_token")
			if tt.expiresAt == nil {
				delete(provider.Record.Credentials, "expires_at")
			} else {
				provider.Record.Credentials["expires_at"] = tt.expiresAt.UTC().Format(time.RFC3339)
			}
			baseRepo := &tokenRefreshProviderRepo{}
			baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo := &grokCredentialPersistingRepo{tokenRefreshProviderRepo: baseRepo}
			cache := &grokTokenCacheForProviderTest{lockResult: true, token: "stale-cache-must-not-win"}
			refresher := &grokCredentialCountingRefresher{}
			tokenSource := newGrokTokenSourceForTest(repo, cache)
			bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), refresher)
			svc := newRequestCredentialsFixture(repo, tokenSource)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			token, kind, err := svc.Resolve(context.Background(), c, provider)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, token)
			require.Empty(t, kind)
			require.Equal(t, forwardcore.GrokCredentialReasonMissing, failoverErr.Reason)
			require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
			require.Zero(t, refresher.refreshCalls, "structurally missing access credentials must not reach the token endpoint")
			require.Equal(t, 1, baseRepo.setErrorCalls)
			require.Equal(t, providercore.StatusError, provider.Record.Status)
			require.False(t, provider.Record.Schedulable)
			require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
			require.True(t, credentialBlocked(svc, provider))
		})
	}
}

func TestGetRequestCredentialWarmCachedAccessWithMissingRefreshPermanentlyFailsOver(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(764)
	provider.Record.Credentials["access_token"] = "valid-access"
	provider.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	delete(provider.Record.Credentials, "refresh_token")
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	repo := &grokCredentialPersistingRepo{tokenRefreshProviderRepo: baseRepo}
	cache := &grokTokenCacheForProviderTest{lockResult: true, token: "valid-access"}
	refresher := &grokCredentialCountingRefresher{}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), refresher)
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, _, err := svc.Resolve(context.Background(), c, provider)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GrokCredentialReasonMissing, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Zero(t, refresher.refreshCalls)
	require.Equal(t, 1, baseRepo.setErrorCalls)
	require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
	require.True(t, credentialBlocked(svc, provider))
}

func TestGetRequestCredentialMapsTransientAndProviderFailuresSeparately(t *testing.T) {
	t.Run("provider transient temporarily unschedules", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(702)
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{err: errors.New("temporary refresh transport failure")})
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonRefreshTransient, failoverErr.Reason)
		require.True(t, failoverErr.ShouldRetryNextProvider())
		require.Zero(t, repo.setErrorCalls)
		require.Equal(t, 1, repo.setTempUnschedCalls)
		require.True(t, credentialBlocked(svc, provider))
	})

	t.Run("shared provider configuration stops without mutation", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(703)
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		tokenSource := newGrokTokenSourceForTest(repo, nil)
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeShared, failoverErr.Scope)
		require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.setTempUnschedCalls)
		require.False(t, credentialBlocked(svc, provider))
	})

	t.Run("provider reread failures preserve shared versus missing-row scope", func(t *testing.T) {
		for _, tt := range []struct {
			name     string
			provider *gatewayprovider.ExecutionProvider
			err      error
		}{
			{name: "repository error", err: errors.New("database temporarily unavailable")},
			{name: "missing row"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				provider := expiredGrokOAuthProviderForCredentialTest(712)
				baseRepo := &tokenRefreshProviderRepo{}
				baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
				repo := &grokCredentialRereadFailureRepo{tokenRefreshProviderRepo: baseRepo, provider: tt.provider, err: tt.err}
				cache := &grokTokenCacheForProviderTest{lockResult: true}
				tokenSource := newGrokTokenSourceForTest(repo, cache)
				bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), providercore.NewGrokTokenRefresher(nil))
				svc := newRequestCredentialsFixture(repo, tokenSource)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())

				_, _, err := svc.Resolve(context.Background(), c, provider)
				var failoverErr *forwardcore.UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				if tt.err != nil {
					require.Equal(t, forwardcore.GatewayFailureScopeShared, failoverErr.Scope)
					require.Equal(t, forwardcore.GrokCredentialReasonProviderDown, failoverErr.Reason)
					require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
				} else {
					require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
					require.Equal(t, forwardcore.GrokCredentialReasonProviderChanged, failoverErr.Reason)
					require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
				}
				require.Zero(t, baseRepo.setErrorCalls)
				require.Zero(t, baseRepo.setTempUnschedCalls)
				require.Empty(t, cache.deletedKeys)
				require.False(t, credentialBlocked(svc, provider))
			})
		}
	})

	t.Run("fresh provider eligibility changes retry without mutating stale state", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			mutate func(*gatewayprovider.ExecutionProvider)
		}{
			{
				name: "account disabled",
				mutate: func(provider *gatewayprovider.ExecutionProvider) {
					provider.Record.Status = billing.StatusDisabled
				},
			},
			{
				name: "provider converted",
				mutate: func(provider *gatewayprovider.ExecutionProvider) {
					provider.Record.Type = capability.ProviderTypeUpstream
				},
			},
			{
				name: "provider manually unschedulable",
				mutate: func(provider *gatewayprovider.ExecutionProvider) {
					provider.Record.Schedulable = false
				},
			},
			{
				name: "provider temporarily unschedulable",
				mutate: func(provider *gatewayprovider.ExecutionProvider) {
					until := time.Now().Add(time.Minute)
					provider.Record.TempUnschedulableUntil = &until
				},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				staleProvider := expiredGrokOAuthProviderForCredentialTest(713)
				freshProvider := *staleProvider
				freshProvider.Record.Credentials = querycache.ShallowMap(staleProvider.Record.Credentials)
				tt.mutate(&freshProvider)
				repo := &tokenRefreshProviderRepo{}
				repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{staleProvider.Record.ID: &freshProvider}
				cache := &grokTokenCacheForProviderTest{lockResult: true}
				tokenSource := newGrokTokenSourceForTest(repo, cache)
				bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), providercore.NewGrokTokenRefresher(nil))
				svc := newRequestCredentialsFixture(repo, tokenSource)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())

				_, _, err := svc.Resolve(context.Background(), c, staleProvider)
				var failoverErr *forwardcore.UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
				require.Equal(t, forwardcore.GrokCredentialReasonProviderChanged, failoverErr.Reason)
				require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
				require.Empty(t, cache.deletedKeys)
				require.False(t, credentialBlocked(svc, staleProvider))
			})
		}
	})

	t.Run("fresh missing refresh credential permanently blocks the provider", func(t *testing.T) {
		staleProvider := expiredGrokOAuthProviderForCredentialTest(714)
		staleProvider.Record.Schedulable = true
		freshProvider := *staleProvider
		freshProvider.Record.Credentials = querycache.ShallowMap(staleProvider.Record.Credentials)
		delete(freshProvider.Record.Credentials, "refresh_token")
		baseRepo := &tokenRefreshProviderRepo{}
		baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{staleProvider.Record.ID: &freshProvider}
		repo := &grokCredentialPersistingRepo{tokenRefreshProviderRepo: baseRepo}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), providercore.NewGrokTokenRefresher(nil))
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, staleProvider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonMissing, failoverErr.Reason)
		require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
		require.Equal(t, 1, baseRepo.setErrorCalls)
		require.Zero(t, baseRepo.setTempUnschedCalls)
		require.Equal(t, providercore.StatusError, freshProvider.Record.Status)
		require.False(t, freshProvider.Record.Schedulable)
		require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(staleProvider))}, cache.deletedKeys)
		require.True(t, credentialBlocked(svc, staleProvider))
	})

	t.Run("refresh added after locked structural failure wins conditional mutation", func(t *testing.T) {
		staleProvider := expiredGrokOAuthProviderForCredentialTest(717)
		freshProvider := *staleProvider
		freshProvider.Record.Credentials = querycache.ShallowMap(staleProvider.Record.Credentials)
		delete(freshProvider.Record.Credentials, "refresh_token")
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{staleProvider.Record.ID: &freshProvider}
		repo.beforeConditionalState = func() {
			repaired := freshProvider
			repaired.Record.Credentials = querycache.ShallowMap(freshProvider.Record.Credentials)
			repaired.Record.Credentials["refresh_token"] = "repaired-refresh-token"
			repaired.Record.Credentials["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			repo.providersByID[staleProvider.Record.ID] = &repaired
		}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), providercore.NewGrokTokenRefresher(nil))
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		token, kind, err := svc.Resolve(context.Background(), c, staleProvider)

		require.NoError(t, err)
		require.Equal(t, "expired-access-token", token)
		require.Equal(t, "oauth", kind)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.setTempUnschedCalls)
		require.Empty(t, cache.deletedKeys)
		require.False(t, credentialBlocked(svc, staleProvider))
	})

	t.Run("expiry-only repair wins full credential fingerprint CAS", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(718)
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		repo.beforeConditionalState = func() {
			repaired := *provider
			repaired.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
			repaired.Record.Credentials["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			repo.providersByID[provider.Record.ID] = &repaired
		}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{
			err: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant"),
		})
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		token, kind, err := svc.Resolve(context.Background(), c, provider)

		require.NoError(t, err)
		require.Equal(t, "expired-access-token", token)
		require.Equal(t, "oauth", kind)
		require.Zero(t, repo.setErrorCalls)
		require.Empty(t, cache.deletedKeys)
		require.False(t, credentialBlocked(svc, provider))
	})

	t.Run("generic token endpoint 403 stops as shared provider failure", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(708)
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{
			err: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "token refresh failed: status 403, body: forbidden"),
		})
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeShared, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonProviderDown, failoverErr.Reason)
		require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.setTempUnschedCalls)
		require.Empty(t, cache.deletedKeys)
		require.False(t, credentialBlocked(svc, provider))
	})

	t.Run("provider proxy generic 403 remains bounded provider transient", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(711)
		proxyID := int64(43)
		provider.Record.ProxyID = &proxyID
		provider.Record.Proxy = &egress.Proxy{}
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{
			err: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "token refresh failed: status 403, body: forbidden"),
		})
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonRefreshTransient, failoverErr.Reason)
		require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
		require.Zero(t, repo.setErrorCalls)
		require.Equal(t, 1, repo.setTempUnschedCalls)
		require.Empty(t, cache.deletedKeys)
		require.True(t, credentialBlocked(svc, provider))
	})

	t.Run("proxy repository read failure stops without provider mutation", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(709)
		proxyID := int64(41)
		provider.Record.ProxyID = &proxyID
		provider.Record.Proxy = &egress.Proxy{}
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		oauthSvc := newGrokAuthorizationForTest(&grokCredentialProxyRepoStub{err: errors.New("database temporarily unavailable")}, &grokOAuthClientStub{})
		oauthSvc.Start()
		defer stopGrokAuthorizationForTest(t, oauthSvc)
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), providercore.NewGrokTokenRefresher(oauthSvc))
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeShared, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonProviderDown, failoverErr.Reason)
		require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.setTempUnschedCalls)
		require.Empty(t, cache.deletedKeys)
		require.False(t, credentialBlocked(svc, provider))
	})

	t.Run("structurally missing configured proxy permanently blocks only that provider", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(710)
		provider.Record.Status = billing.StatusActive
		provider.Record.Schedulable = true
		proxyID := int64(42)
		provider.Record.ProxyID = &proxyID
		baseRepo := &tokenRefreshProviderRepo{}
		baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		repo := &grokCredentialPersistingRepo{tokenRefreshProviderRepo: baseRepo}
		cache := &grokTokenCacheForProviderTest{lockResult: true}
		oauthSvc := newGrokAuthorizationForTest(&grokCredentialProxyRepoStub{err: egress.ErrProxyNotFound}, &grokOAuthClientStub{})
		oauthSvc.Start()
		defer stopGrokAuthorizationForTest(t, oauthSvc)
		tokenSource := newGrokTokenSourceForTest(repo, cache)
		bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), providercore.NewGrokTokenRefresher(oauthSvc))
		svc := newRequestCredentialsFixture(repo, tokenSource)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonProxyInvalid, failoverErr.Reason)
		require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
		require.Equal(t, 1, baseRepo.setErrorCalls)
		require.Equal(t, providercore.StatusError, provider.Record.Status)
		require.False(t, provider.Record.Schedulable)
		require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
		require.True(t, credentialBlocked(svc, provider))
	})
}

func TestGetRequestCredentialRuntimeBlockWinsBeforeWarmTokenCache(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(716)
	provider.Record.Credentials["access_token"] = "valid-access"
	provider.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	cache := &grokTokenCacheForProviderTest{token: "valid-access"}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	svc := newRequestCredentialsFixture(repo, tokenSource)
	svc.Runtime.Runtime.BlockProviderScheduling(gatewayprovider.ExecutionRecord(provider), time.Now().Add(time.Minute), "independent")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, _, err := svc.Resolve(context.Background(), c, provider)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GrokCredentialReasonProviderChanged, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Zero(t, cache.getCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
}

func TestGetRequestCredentialWarmCachedAccessWithMissingConfiguredProxyPermanentlyFailsOver(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(715)
	provider.Record.Credentials["access_token"] = "valid-access"
	provider.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	proxyID := int64(44)
	provider.Record.ProxyID = &proxyID
	provider.Record.Proxy = nil
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	repo := &grokCredentialPersistingRepo{tokenRefreshProviderRepo: baseRepo}
	cache := &grokTokenCacheForProviderTest{lockResult: true, token: "valid-access"}
	refresher := &grokCredentialCountingRefresher{}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), refresher)
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, _, err := svc.Resolve(context.Background(), c, provider)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GrokCredentialReasonProxyInvalid, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Zero(t, refresher.refreshCalls)
	require.Equal(t, 1, baseRepo.setErrorCalls)
	require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
	require.True(t, credentialBlocked(svc, provider))
}

func TestGetRequestCredentialCancellationAndBudgetDoNotMutateProvider(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(704)
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	tokenSource := newGrokTokenSourceForTest(repo, nil)
	svc := newRequestCredentialsFixture(repo, tokenSource)

	t.Run("parent cancellation is returned directly", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		_, _, err := svc.Resolve(ctx, c, provider)
		require.ErrorIs(t, err, context.Canceled)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
	})

	t.Run("request credential budget stops safely", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		gatewayhttp.RequestCredentialBudget(c).Deadline = time.Now().Add(-time.Second)

		_, _, err := svc.Resolve(context.Background(), c, provider)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, forwardcore.GatewayFailureScopeRequest, failoverErr.Scope)
		require.Equal(t, forwardcore.GrokCredentialReasonFailoverTimeout, failoverErr.Reason)
		require.False(t, failoverErr.ShouldRetryNextProvider())
	})

	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.False(t, credentialBlocked(svc, provider))
}

func TestGetRequestCredentialStateMutationFailureStopsAndKeepsRuntimeBlock(t *testing.T) {
	tests := []struct {
		name            string
		refreshErr      error
		configure       func(*tokenRefreshProviderRepo, *grokTokenCacheForProviderTest)
		wantSetError    int
		wantSetTemp     int
		wantCacheDelete int
	}{
		{
			name:       "permanent state persistence",
			refreshErr: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant"),
			configure: func(repo *tokenRefreshProviderRepo, _ *grokTokenCacheForProviderTest) {
				repo.setErrorErr = errors.New("database write failed")
			},
			wantSetError: 1,
		},
		{
			name:       "transient state persistence",
			refreshErr: errors.New("temporary refresh transport failure"),
			configure: func(repo *tokenRefreshProviderRepo, _ *grokTokenCacheForProviderTest) {
				repo.setTempUnschedErr = errors.New("database write failed")
			},
			wantSetTemp: 1,
		},
		{
			name:       "permanent token cache invalidation",
			refreshErr: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant"),
			configure: func(_ *tokenRefreshProviderRepo, cache *grokTokenCacheForProviderTest) {
				cache.deleteErr = errors.New("cache delete failed")
			},
			wantSetError:    1,
			wantCacheDelete: 1,
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(740 + index))
			repo := &tokenRefreshProviderRepo{}
			repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			cache := &grokTokenCacheForProviderTest{lockResult: true}
			tt.configure(repo, cache)
			tokenSource := newGrokTokenSourceForTest(repo, cache)
			bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{err: tt.refreshErr})
			svc := newRequestCredentialsFixture(repo, tokenSource)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			_, _, err := svc.Resolve(context.Background(), c, provider)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, forwardcore.GatewayFailureScopeShared, failoverErr.Scope)
			require.Equal(t, forwardcore.GrokCredentialReasonStateUpdate, failoverErr.Reason)
			require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
			require.Equal(t, tt.wantSetError, repo.setErrorCalls)
			require.Equal(t, tt.wantSetTemp, repo.setTempUnschedCalls)
			require.Len(t, cache.deletedKeys, tt.wantCacheDelete)
			require.True(t, credentialBlocked(svc, provider), "failed mutation must retain the immediate local block")
		})
	}
}

func TestGrokCredentialMutationBoundariesHonorParentCancellation(t *testing.T) {
	t.Run("blocked SetError cancellation prevents cache and runtime mutation", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(730)
		baseRepo := &tokenRefreshProviderRepo{}
		baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		repo := &grokCredentialBlockingRepo{
			tokenRefreshProviderRepo: baseRepo,
			setErrorStarted:          make(chan struct{}),
			setTempStarted:           make(chan struct{}),
		}
		cache := &grokTokenCacheForProviderTest{}
		svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, cache))
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
				Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true,
			}))
			result <- err
		}()
		<-repo.setErrorStarted
		require.True(t, credentialBlocked(svc, provider), "runtime block must precede persistent SetError")
		cancel()

		require.ErrorIs(t, <-result, context.Canceled)
		require.Zero(t, baseRepo.setErrorCalls)
		require.Empty(t, cache.deletedKeys)
		require.False(t, credentialBlocked(svc, provider))
	})

	t.Run("blocked temporary unschedule cancellation prevents runtime mutation", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(731)
		baseRepo := &tokenRefreshProviderRepo{}
		baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		repo := &grokCredentialBlockingRepo{
			tokenRefreshProviderRepo: baseRepo,
			setErrorStarted:          make(chan struct{}),
			setTempStarted:           make(chan struct{}),
		}
		svc := newRequestCredentialsFixture(repo, nil)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
				Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true,
			}))
			result <- err
		}()
		<-repo.setTempStarted
		require.True(t, credentialBlocked(svc, provider), "runtime block must precede temporary unscheduling")
		cancel()

		require.ErrorIs(t, <-result, context.Canceled)
		require.Zero(t, baseRepo.setTempUnschedCalls)
		require.False(t, credentialBlocked(svc, provider))
	})

	t.Run("post-commit cancellation finishes cache cleanup and retains quarantine", func(t *testing.T) {
		provider := expiredGrokOAuthProviderForCredentialTest(732)
		repo := &tokenRefreshProviderRepo{}
		repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
		cache := &grokCredentialBlockingCache{deleteStarted: make(chan struct{}), releaseDelete: make(chan struct{})}
		svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, cache))
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
				Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true,
			}))
			result <- err
		}()
		<-cache.deleteStarted
		require.True(t, credentialBlocked(svc, provider), "runtime block must precede cache invalidation")
		cancel()
		close(cache.releaseDelete)

		require.ErrorIs(t, <-result, context.Canceled)
		require.Equal(t, 1, repo.setErrorCalls)
		require.True(t, cache.wasDeleted())
		require.True(t, credentialBlocked(svc, provider))
	})
}

func TestGrokCredentialMutationLockWaitHonorsCredentialBudget(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(735)
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, &grokTokenCacheForProviderTest{}))
	mutationLock := newCredentialMutationHold(svc.Runtime.Recovery, provider.Record.ID)
	require.NoError(t, mutationLock.Lock(context.Background()))
	defer mutationLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	token, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
		Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true,
	}))

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, token)
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.False(t, credentialBlocked(svc, provider))
}

func TestGetRequestCredentialBudgetBoundsBlockedConditionalMutation(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(736)
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	repo := &grokCredentialBlockingRepo{
		tokenRefreshProviderRepo: baseRepo,
		setErrorStarted:          make(chan struct{}),
		setTempStarted:           make(chan struct{}),
	}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{
		err: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant"),
	})
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	gatewayhttp.RequestCredentialBudget(c).Deadline = time.Now().Add(40 * time.Millisecond)

	startedAt := time.Now()
	token, kind, err := svc.Resolve(context.Background(), c, provider)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GatewayFailureScopeRequest, failoverErr.Scope)
	require.Equal(t, forwardcore.GrokCredentialReasonFailoverTimeout, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
	require.Empty(t, token)
	require.Empty(t, kind)
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	require.Zero(t, baseRepo.setErrorCalls)
	require.Zero(t, baseRepo.setTempUnschedCalls)
	require.Empty(t, cache.deletedKeys)
	require.False(t, credentialBlocked(svc, provider))
}

func TestGetRequestCredentialLockHeldTimeoutDoesNotQuarantineProvider(t *testing.T) {
	tests := []struct {
		name       string
		buildRepo  func(*gatewayprovider.ExecutionProvider) gatewayprovider.ExecutionProviderStore
		wantScope  forwardcore.GatewayFailureScope
		wantReason forwardcore.GatewayFailureReason
		wantAction forwardcore.NextProviderAction
	}{
		{
			name: "authoritative row unchanged",
			buildRepo: func(provider *gatewayprovider.ExecutionProvider) gatewayprovider.ExecutionProviderStore {
				repo := &tokenRefreshProviderRepo{}
				repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
				return repo
			},
			wantScope:  forwardcore.GatewayFailureScopeProvider,
			wantReason: forwardcore.GrokCredentialReasonProviderChanged,
			wantAction: forwardcore.NextProviderRetry,
		},
		{
			name: "selected provider was deleted",
			buildRepo: func(provider *gatewayprovider.ExecutionProvider) gatewayprovider.ExecutionProviderStore {
				base := &tokenRefreshProviderRepo{}
				base.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
				return &grokCredentialRereadFailureRepo{tokenRefreshProviderRepo: base}
			},
			wantScope:  forwardcore.GatewayFailureScopeProvider,
			wantReason: forwardcore.GrokCredentialReasonProviderChanged,
			wantAction: forwardcore.NextProviderRetry,
		},
		{
			name: "shared provider store unavailable",
			buildRepo: func(provider *gatewayprovider.ExecutionProvider) gatewayprovider.ExecutionProviderStore {
				base := &tokenRefreshProviderRepo{}
				base.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
				return &grokCredentialRereadFailureRepo{tokenRefreshProviderRepo: base, err: errors.New("database unavailable")}
			},
			wantScope:  forwardcore.GatewayFailureScopeShared,
			wantReason: forwardcore.GrokCredentialReasonProviderDown,
			wantAction: forwardcore.NextProviderStop,
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(7400 + index))
			repo := tt.buildRepo(provider)
			cache := &grokTokenCacheForProviderTest{lockResult: false}
			tokenSource := newGrokTokenSourceForTest(repo, cache)
			bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{})
			svc := newRequestCredentialsFixture(repo, tokenSource)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			startedAt := time.Now()
			_, _, err := svc.Resolve(context.Background(), c, provider)

			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, tt.wantScope, failoverErr.Scope)
			require.Equal(t, tt.wantReason, failoverErr.Reason)
			require.Equal(t, tt.wantAction, failoverErr.NextProviderAction)
			require.Less(t, time.Since(startedAt), 3*time.Second)
			switch countingRepo := repo.(type) {
			case *tokenRefreshProviderRepo:
				require.Zero(t, countingRepo.setErrorCalls)
				require.Zero(t, countingRepo.setTempUnschedCalls)
			case *grokCredentialRereadFailureRepo:
				require.Zero(t, countingRepo.setErrorCalls)
				require.Zero(t, countingRepo.setTempUnschedCalls)
			}
			require.Empty(t, cache.deletedKeys)
			require.False(t, credentialBlocked(svc, provider))
		})
	}
}

func TestGrokCredentialMutationCancellationAmbiguityConfirmsDurableCommit(t *testing.T) {
	tests := []struct {
		name      string
		class     forwardcore.GrokCredentialFailure
		committed func(*gatewayprovider.ExecutionProvider) bool
	}{
		{
			name:  "permanent quarantine",
			class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true},
			committed: func(provider *gatewayprovider.ExecutionProvider) bool {
				return provider.Record.Status == providercore.StatusError && !provider.Record.Schedulable && provider.Record.ErrorMessage == string(forwardcore.GrokCredentialReasonRevoked)
			},
		},
		{
			name:  "temporary quarantine",
			class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true},
			committed: func(provider *gatewayprovider.ExecutionProvider) bool {
				return provider.Record.TempUnschedulableUntil != nil && provider.Record.TempUnschedulableReason == string(forwardcore.GrokCredentialReasonRefreshTransient)
			},
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(737 + index))
			baseRepo := &tokenRefreshProviderRepo{}
			baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo := &grokCredentialCommitThenCancelRepo{tokenRefreshProviderRepo: baseRepo}
			cache := &grokTokenCacheForProviderTest{}
			svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, cache))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()

			token, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(tt.class))

			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Empty(t, token)
			require.True(t, tt.committed(provider), "the detached confirmation must recognize the durable mutation")
			require.True(t, credentialBlocked(svc, provider), "a confirmed durable quarantine must retain its runtime block")
			if tt.class.Permanent {
				require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
			} else {
				require.Empty(t, cache.deletedKeys)
			}
		})
	}
}

func TestGrokCredentialInnerStateDeadlineAmbiguityConfirmsDurableCommit(t *testing.T) {
	tests := []struct {
		name  string
		class forwardcore.GrokCredentialFailure
	}{
		{name: "permanent quarantine", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true}},
		{name: "temporary quarantine", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true}},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(7500 + index))
			baseRepo := &tokenRefreshProviderRepo{}
			baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo := &grokCredentialCommitThenCancelRepo{
				tokenRefreshProviderRepo: baseRepo,
				returnErr:                context.DeadlineExceeded,
			}
			cache := &grokTokenCacheForProviderTest{}
			svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, cache))

			token, err := svc.Runtime.Recovery.Apply(context.Background(), gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(tt.class))

			require.NoError(t, err, "the detached readback must resolve the inner timeout's commit ambiguity")
			require.Empty(t, token)
			require.True(t, credentialBlocked(svc, provider))
			if tt.class.Permanent {
				require.Equal(t, providercore.StatusError, provider.Record.Status)
				require.False(t, provider.Record.Schedulable)
				require.Equal(t, []string{providercore.GrokTokenCacheKey(gatewayprovider.ExecutionRecord(provider))}, cache.deletedKeys)
			} else {
				require.NotNil(t, provider.Record.TempUnschedulableUntil)
				require.Equal(t, string(forwardcore.GrokCredentialReasonRefreshTransient), provider.Record.TempUnschedulableReason)
				require.Empty(t, cache.deletedKeys)
			}
		})
	}
}

func TestGrokCredentialUnconfirmedInnerStateDeadlineStopsAndRetainsSafetyBlock(t *testing.T) {
	tests := []struct {
		name  string
		class forwardcore.GrokCredentialFailure
	}{
		{name: "permanent quarantine", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true}},
		{name: "temporary quarantine", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true}},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(7600 + index))
			baseRepo := &tokenRefreshProviderRepo{}
			baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo := &grokCredentialUncommittedDeadlineRepo{tokenRefreshProviderRepo: baseRepo}
			cache := &grokTokenCacheForProviderTest{}
			svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, cache))

			token, err := svc.Runtime.Recovery.Apply(context.Background(), gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(tt.class))

			require.ErrorIs(t, err, providercore.ErrGrokCredentialStateUpdateFailed)
			require.Empty(t, token)
			require.True(t, credentialBlocked(svc, provider), "an unknown commit outcome must retain the local safety block")
			require.Equal(t, billing.StatusActive, provider.Record.Status)
			require.True(t, provider.Record.Schedulable)
			require.Nil(t, provider.Record.TempUnschedulableUntil)
			require.Empty(t, cache.deletedKeys)
		})
	}
}

func TestGrokCredentialRuntimeRollbackOwnership(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(734)
	t.Run("later extending block survives", func(t *testing.T) {
		svc := newRequestCredentialsFixture(nil, nil)
		until := time.Now().Add(time.Minute)
		rollbackFirst := svc.Runtime.Runtime.BlockRollback(provider.Record.ID, until, "first")

		secondInstalled := make(chan struct{})
		go func() {
			svc.Runtime.Runtime.BlockProviderScheduling(gatewayprovider.ExecutionRecord(provider), until.Add(time.Minute), "independent")
			close(secondInstalled)
		}()
		<-secondInstalled
		rollbackFirst()

		require.True(t, credentialBlocked(svc, provider),
			"rollback owned by the first invocation must not remove a later extending block")
	})

	t.Run("independent shorter block steals rollback ownership", func(t *testing.T) {
		svc := newRequestCredentialsFixture(nil, nil)
		until := time.Now().Add(2 * time.Minute)
		rollbackFirst := svc.Runtime.Runtime.BlockRollback(provider.Record.ID, until, "first")
		svc.Runtime.Runtime.BlockProviderScheduling(gatewayprovider.ExecutionRecord(provider), until.Add(-time.Minute), "shorter-no-op")

		rollbackFirst()

		require.True(t, credentialBlocked(svc, provider))
	})

	t.Run("serialized tentative rollbacks leave no block", func(t *testing.T) {
		svc := newRequestCredentialsFixture(nil, nil)
		for range 2 {
			mu := newCredentialMutationHold(svc.Runtime.Recovery, provider.Record.ID)
			require.NoError(t, mu.Lock(context.Background()))
			rollback := svc.Runtime.Runtime.BlockRollback(provider.Record.ID, time.Now().Add(time.Minute), "tentative")
			rollback()
			mu.Unlock()
		}
		require.False(t, credentialBlocked(svc, provider))
	})
}

func TestGetRequestCredentialAPIKeyBypassesOAuthFailureMapping(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 705,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "third-party-key",
				"base_url": "https://grok.example.test/v1",
			},
		},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := newRequestCredentialsFixture(nil, nil)

	token, kind, err := svc.Resolve(context.Background(), c, provider)
	require.NoError(t, err)
	require.Equal(t, "third-party-key", token)
	require.Equal(t, "apikey", kind)
	_, hasEvents := c.Get(gatewayhttp.OpsUpstreamErrorsKey)
	require.False(t, hasEvents)
}

func TestPermanentCredentialFailureDoesNotDisableConcurrentlyRefreshedProvider(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(707)
	latest := *provider
	latest.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
	latest.Record.Credentials["access_token"] = "fresh-access-token"
	latest.Record.Credentials["refresh_token"] = "rotated-refresh-token"
	latest.Record.Credentials["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	latest.Record.Credentials["_token_version"] = time.Now().UnixMilli()
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: &latest}
	cache := &grokTokenCacheForProviderTest{}
	svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, cache))

	_, mutationErr := svc.Runtime.Recovery.Apply(context.Background(), gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
		Scope:     forwardcore.GatewayFailureScopeProvider,
		Reason:    forwardcore.GrokCredentialReasonRevoked,
		Action:    forwardcore.NextProviderRetry,
		Permanent: true,
	}))

	require.NoError(t, mutationErr)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, cache.deletedKeys)
	require.False(t, credentialBlocked(svc, provider))
}

func TestCredentialFailureConditionalMutationLosesToConcurrentRefresh(t *testing.T) {
	for index, tt := range []struct {
		name       string
		refreshErr error
	}{
		{name: "permanent", refreshErr: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant")},
		{name: "transient", refreshErr: errors.New("temporary refresh transport failure")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(770 + index))
			repo := &tokenRefreshProviderRepo{}
			repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo.beforeConditionalState = func() {
				fresh := *provider
				fresh.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
				fresh.Record.Credentials["access_token"] = "refresh-won-token"
				fresh.Record.Credentials["refresh_token"] = "refresh-won-refresh"
				fresh.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
				fresh.Record.Credentials["_token_version"] = time.Now().UnixMilli()
				repo.providersByID[provider.Record.ID] = &fresh
			}
			cache := &grokTokenCacheForProviderTest{lockResult: true}
			tokenSource := newGrokTokenSourceForTest(repo, cache)
			bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{err: tt.refreshErr})
			svc := newRequestCredentialsFixture(repo, tokenSource)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			token, kind, err := svc.Resolve(context.Background(), c, provider)

			require.NoError(t, err)
			require.Equal(t, "refresh-won-token", token)
			require.Equal(t, "oauth", kind)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			require.Empty(t, cache.deletedKeys)
			require.False(t, credentialBlocked(svc, provider))
		})
	}
}

func TestCredentialFailureConditionalMutationLosesToConcurrentProxyRepair(t *testing.T) {
	for index, tt := range []struct {
		name       string
		refreshErr error
	}{
		{name: "permanent", refreshErr: apperror.New(http.StatusBadGateway, "GROK_OAUTH_TOKEN_REFRESH_FAILED", "invalid_grant")},
		{name: "transient", refreshErr: errors.New("temporary refresh transport failure")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(780 + index))
			oldProxyID := int64(10)
			provider.Record.ProxyID = &oldProxyID
			provider.Record.Proxy = &egress.Proxy{}
			repo := &tokenRefreshProviderRepo{}
			repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
			repo.beforeConditionalState = func() {
				fresh := *provider
				fresh.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
				repairedProxyID := int64(11)
				fresh.Record.ProxyID = &repairedProxyID
				repo.providersByID[provider.Record.ID] = &fresh
			}
			cache := &grokTokenCacheForProviderTest{lockResult: true}
			tokenSource := newGrokTokenSourceForTest(repo, cache)
			bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{err: tt.refreshErr})
			svc := newRequestCredentialsFixture(repo, tokenSource)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())

			_, _, err := svc.Resolve(context.Background(), c, provider)

			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, forwardcore.GrokCredentialReasonProviderChanged, failoverErr.Reason)
			require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			require.Empty(t, cache.deletedKeys)
			require.False(t, credentialBlocked(svc, provider))
		})
	}
}

func TestCredentialFailureConditionalMutationLosesToSameIDProxyRestoration(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(790)
	proxyID := int64(10)
	provider.Record.ProxyID = &proxyID
	provider.Record.Proxy = nil
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	repo.beforeConditionalState = func() {
		provider.Record.Proxy = &egress.Proxy{ID: proxyID}
	}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, _, err := svc.Resolve(context.Background(), c, provider)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GrokCredentialReasonProviderChanged, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.Empty(t, cache.deletedKeys)
	require.False(t, credentialBlocked(svc, provider))
}

func TestCredentialFailureConditionalMutationLosesToConcurrentUnschedulableState(t *testing.T) {
	future := time.Now().Add(time.Hour)
	states := []struct {
		name   string
		mutate func(*gatewayprovider.ExecutionProvider)
	}{
		{name: "admin schedulable false", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.Schedulable = false }},
		{name: "temporary cooldown", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.TempUnschedulableUntil = &future }},
		{name: "rate limit cooldown", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.RateLimitResetAt = &future }},
		{name: "overload cooldown", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.OverloadUntil = &future }},
	}
	classes := []struct {
		name  string
		class forwardcore.GrokCredentialFailure
	}{
		{name: "permanent", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true}},
		{name: "transient", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true}},
	}

	for classIndex, classCase := range classes {
		for stateIndex, stateCase := range states {
			t.Run(classCase.name+"/"+stateCase.name, func(t *testing.T) {
				provider := expiredGrokOAuthProviderForCredentialTest(int64(791 + classIndex*10 + stateIndex))
				repo := &tokenRefreshProviderRepo{}
				repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
				repo.beforeConditionalState = func() { stateCase.mutate(provider) }
				svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, &grokTokenCacheForProviderTest{}))

				token, err := svc.Runtime.Recovery.Apply(context.Background(), gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(classCase.class))

				require.ErrorIs(t, err, providercore.ErrRefreshProviderStateChanged)
				require.Empty(t, token)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
				require.False(t, credentialBlocked(svc, provider))
			})
		}
	}
}

func TestCredentialFailureCASMissDoesNotRecoverIneligibleLatestCredential(t *testing.T) {
	future := time.Now().Add(time.Hour)
	states := []struct {
		name               string
		mutate             func(*gatewayhttp.RequestCredentialExecutor, *gatewayprovider.ExecutionProvider)
		wantRuntimeBlocked bool
	}{
		{name: "disabled", mutate: func(_ *gatewayhttp.RequestCredentialExecutor, provider *gatewayprovider.ExecutionProvider) {
			provider.Record.Status = billing.StatusDisabled
		}},
		{name: "not schedulable", mutate: func(_ *gatewayhttp.RequestCredentialExecutor, provider *gatewayprovider.ExecutionProvider) {
			provider.Record.Schedulable = false
		}},
		{name: "temporarily unschedulable", mutate: func(_ *gatewayhttp.RequestCredentialExecutor, provider *gatewayprovider.ExecutionProvider) {
			provider.Record.TempUnschedulableUntil = &future
		}},
		{name: "rate limited", mutate: func(_ *gatewayhttp.RequestCredentialExecutor, provider *gatewayprovider.ExecutionProvider) {
			provider.Record.RateLimitResetAt = &future
		}},
		{name: "overloaded", mutate: func(_ *gatewayhttp.RequestCredentialExecutor, provider *gatewayprovider.ExecutionProvider) {
			provider.Record.OverloadUntil = &future
		}},
		{
			name: "independently runtime blocked",
			mutate: func(svc *gatewayhttp.RequestCredentialExecutor, provider *gatewayprovider.ExecutionProvider) {
				svc.Runtime.Runtime.BlockProviderScheduling(gatewayprovider.ExecutionRecord(provider), time.Now().Add(24*time.Hour), "independent")
			},
			wantRuntimeBlocked: true,
		},
	}
	classes := []struct {
		name  string
		class forwardcore.GrokCredentialFailure
	}{
		{name: "permanent", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true}},
		{name: "transient", class: forwardcore.GrokCredentialFailure{Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true}},
	}

	for classIndex, classCase := range classes {
		for stateIndex, stateCase := range states {
			t.Run(classCase.name+"/"+stateCase.name, func(t *testing.T) {
				provider := expiredGrokOAuthProviderForCredentialTest(int64(800 + classIndex*20 + stateIndex))
				repo := &tokenRefreshProviderRepo{}
				repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
				svc := newRequestCredentialsFixture(repo, newGrokTokenSourceForTest(repo, &grokTokenCacheForProviderTest{}))
				repo.beforeConditionalState = func() {
					latest := *provider
					latest.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
					latest.Record.Credentials["access_token"] = "fresh-but-ineligible-token"
					latest.Record.Credentials["refresh_token"] = "fresh-but-ineligible-refresh"
					latest.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
					latest.Record.Credentials["_token_version"] = time.Now().UnixMilli()
					stateCase.mutate(svc, &latest)
					repo.providersByID[provider.Record.ID] = &latest
				}

				token, err := svc.Runtime.Recovery.Apply(context.Background(), gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(classCase.class))

				require.ErrorIs(t, err, providercore.ErrRefreshProviderStateChanged)
				require.Empty(t, token)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
				require.Equal(t, stateCase.wantRuntimeBlocked, credentialBlocked(svc, provider))
			})
		}
	}
}

func TestGetRequestCredentialSharedCredentialPersistenceFailureStopsWithoutProviderMutation(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(782)
	repo := &tokenRefreshProviderRepo{conditionalSuccessErr: errors.New("database unavailable")}
	repo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{credentials: map[string]any{
		"access_token":  "new-access-token",
		"refresh_token": "new-refresh-token",
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}})
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, _, err := svc.Resolve(context.Background(), c, provider)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GatewayFailureScopeShared, failoverErr.Scope)
	require.Equal(t, forwardcore.GrokCredentialReasonProviderDown, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderStop, failoverErr.NextProviderAction)
	require.Equal(t, 1, repo.conditionalSuccessCalls)
	require.Zero(t, repo.updateCredentialsCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.Empty(t, cache.deletedKeys)
	require.False(t, credentialBlocked(svc, provider))
}

func TestGetRequestCredentialRecoversConcurrentRefreshWithoutFailover(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(733)
	latest := *provider
	latest.Record.Credentials = querycache.ShallowMap(provider.Record.Credentials)
	latest.Record.Credentials["access_token"] = "fresh-concurrent-access"
	latest.Record.Credentials["refresh_token"] = "fresh-concurrent-refresh"
	latest.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	latest.Record.Credentials["_token_version"] = time.Now().UnixMilli()
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}
	repo := &grokCredentialSequencedRepo{tokenRefreshProviderRepo: baseRepo, latest: &latest}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newGrokCredentialRefreshForTest(repo, cache), &tokenRefresherStub{
		err: apperror.New(http.StatusForbidden, "GROK_OAUTH_ENTITLEMENT_DENIED", "access_denied"),
	})
	svc := newRequestCredentialsFixture(repo, tokenSource)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	token, kind, err := svc.Resolve(context.Background(), c, provider)

	require.NoError(t, err)
	require.Equal(t, "fresh-concurrent-access", token)
	require.Equal(t, "oauth", kind)
	require.Zero(t, baseRepo.setErrorCalls)
	require.Zero(t, baseRepo.setTempUnschedCalls)
	require.Empty(t, cache.deletedKeys)
	require.False(t, credentialBlocked(svc, provider))
	_, hasEvents := c.Get(gatewayhttp.OpsUpstreamErrorsKey)
	require.False(t, hasEvents)
}

func expiredGrokOAuthProviderForCredentialTest(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"access_token":  "expired-access-token",
				"refresh_token": "refresh-token",
				"expires_at":    time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
				"base_url":      xai.DefaultCLIBaseURL,
			},
		},
	}
}
