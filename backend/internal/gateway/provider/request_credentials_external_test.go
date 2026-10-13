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

// grokOAuthClientStub returns configured authorization results and records
// selected exchange/login inputs.
type grokOAuthClientStub struct {
	// configured refresh exchange response
	refreshResponse *xai.TokenResponse
	// configured SSO exchange response
	ssoResponse *xai.TokenResponse
	// configured password login response
	loginResult         *providercore.GrokPasswordLoginResult
	loginEmail          string // observed password login email
	loginPassword       string // observed password login secret
	exchangeCalls       int    // authorization code exchange count
	exchangeRedirectURI string // observed exchange redirect URI
}

// credentialReadStore supplies borrowed targets indexed by ID for credential
// resolution fixtures.
type credentialReadStore struct {
	// unused execution storage operations
	gatewayprovider.ExecutionProviderStore
	// borrowed target fixtures by ID
	providersByID map[int64]*gatewayprovider.ExecutionProvider
}

// The lock fixture calls Apply and blocks during the read.
// Cancel and join the operation before releasing the fixture; it performs no conditional writes.
type (
	// credentialMutationHoldKey identifies the controlled read holder in its recovery context.
	credentialMutationHoldKey struct{}
	// credentialMutationHold owns one blocked recovery operation until its
	// caller cancels and joins it.
	credentialMutationHold struct {
		// core and id select the held provider operation. started signals locked read
		// admission, done joins recovery completion, and cancel releases the blocked read
		// before restore can replace any temporarily installed reader callback.
		//
		// shared mutation recovery owner
		core    *providercore.GrokCredentialRecovery
		id      int64              // held provider mutation identity
		started chan struct{}      // controlled read admission signal
		done    chan struct{}      // held recovery completion signal
		cancel  context.CancelFunc // held recovery cancellation handle
		restore func()             // optional fixture reader restoration
	}
)

// tokenRefreshProviderRepo simulates conditional credential/state writes and
// records their observations.
type tokenRefreshProviderRepo struct {
	credentialReadStore     // configured provider read fixtures
	updateCalls         int // all credential update attempts
	// whole-target credential update attempts
	fullUpdateCalls int
	// field-specific credential update attempts
	updateCredentialsCalls int
	setErrorCalls          int    // recorded permanent failure writes
	clearTempCalls         int    // recorded refresh cooldown cleanup
	setTempUnschedCalls    int    // recorded temporary failure writes
	updateExtraCalls       int    // recorded provider extras writes
	lastErrorMessage       string // last permanent failure message
	lastTempUnschedReason  string // last temporary failure reason
	// lastExtraUpdates captures a top-level copy of UpdateExtra input. Nested
	// values remain shared with the supplied patch, and tests keep them stable
	// through assertions.
	lastExtraUpdates map[string]any
	// lastProvider retains the target involved in the accepted credential
	// assignment or whole-record attempt; its nested data stays borrowed until
	// the scenario finishes its assertions.
	lastProvider *gatewayprovider.ExecutionProvider
	// updateErr rejects field-specific installation after recording an attempted
	// write; whole-record Update returns it after retaining the supplied target
	// pointer for observation.
	updateErr error
	// cancelOnUpdate runs after successful credential assignment in the fixture
	// writers. Tests use it to make cancellation compete with acknowledgement of
	// an already installed credential update.
	cancelOnUpdate context.CancelFunc
	// conditional permanent refresh write count
	conditionalErrorCalls int
	// conditional temporary refresh write count
	conditionalTempCalls int
	// conditional refresh success write count
	conditionalSuccessCalls int
	// conditionalErrorErr returns before the conditional permanent refresh writer
	// reads or mutates the target, while its invocation counter still records the
	// attempted operation.
	conditionalErrorErr error
	// conditionalTempErr returns before conditional temporary refresh comparison
	// or installation, with the call counter retained for failure-stage
	// assertions.
	conditionalTempErr error
	// conditionalSuccessErr fails the conditional successful refresh writer
	// before scheduling injection, identity comparison, or credential assignment;
	// the attempted call remains counted.
	conditionalSuccessErr error
	// snapshotReads selects a supported-container execution copy for each
	// successful lookup; false returns the configured borrowed target so tests
	// can observe live fixture mutation.
	snapshotReads bool
	// respectReadContext checks ctx.Err before incrementing read counters,
	// allowing confirmation tests to distinguish rejected reads from attempted
	// stored-state inspection.
	respectReadContext bool
	getByIDCalls       int // recorded provider reread count
	// durableReadDelay postpones lookups after the first observation. The wait
	// returns the context error when its deadline wins, so confirmation can fail
	// after an earlier write.
	durableReadDelay time.Duration
	// mutateSchedulingOnSuccessCAS disables scheduling and installs a rate reset
	// before success comparison once. That writer compares credential/proxy
	// identity and retains the separately changed scheduling fields.
	mutateSchedulingOnSuccessCAS bool
	// reauthorizeOnErrorCAS replaces credentials and restores active scheduling
	// once before permanent refresh comparison, simulating a newer authorization
	// that makes stale error evidence fail.
	reauthorizeOnErrorCAS bool
	// reauthorizeOnTempCAS replaces credentials and restores active scheduling
	// once before temporary refresh comparison; the prior observation then cannot
	// install its pause.
	reauthorizeOnTempCAS bool
	// repairProxyOnErrorCAS changes the configured proxy ID once before permanent
	// refresh comparison, allowing the test to reject credential errors observed
	// on an earlier route.
	repairProxyOnErrorCAS bool
	// repairProxyOnTempCAS changes proxy identity before temporary refresh
	// comparison once, so the older proxy observation cannot install its
	// requested cooldown.
	repairProxyOnTempCAS bool
	// setErrorErr is returned after permanent-write count/message observation.
	// Conditional state writers install disabled/error fields only when that
	// configured error is absent.
	setErrorErr error
	// setTempUnschedErr is returned after temporary-write count/reason recording;
	// conditional state writers keep the target deadline unchanged when the
	// configured error rejects installation.
	setTempUnschedErr error
	// beforeConditionalState is cleared before invocation, so a simulated competing
	// mutation runs once immediately before the fixture compares failed credential evidence.
	//
	// one-shot conditional state mutation hook
	beforeConditionalState func()
}

// tokenRefresherStub supplies a configured refresh result or error with
// invocation counts.
type tokenRefresherStub struct {
	credentials map[string]any // configured borrowed refresh credentials
	err         error          // configured refresh exchange error
	calls       int            // recorded refresh exchange count
}

// tokenSourceFixtureReader adapts gateway execution-target reads to provider
// refresh records.
type tokenSourceFixtureReader struct {
	// bound execution target reader
	source gatewayprovider.ExecutionProviderStore
}

// grokTokenCacheForProviderTest records token cache calls and configured
// lock/read outcomes.
type grokTokenCacheForProviderTest struct {
	token        string        // configured cache lookup token
	setKey       string        // last cache-write namespace key
	setToken     string        // last cache-write token value
	setTTL       time.Duration // last cache-write lifetime duration
	lockResult   bool          // configured refresh lock outcome
	releaseCalls int           // refresh lock release count
	deletedKeys  []string      // attempted token deletion keys
	deleteErr    error         // configured cache deletion error
	getCalls     int           // recorded cache lookup count
	// mu protects token lookup and token-write/deletion observations. Refresh-lock
	// methods use their configured outcomes directly, so tests coordinate those
	// separate observations through their fixture sequencing.
	mu sync.Mutex
}

// grokCredentialPersistingRepo installs accepted error state onto the fixture
// provider record.
type grokCredentialPersistingRepo struct {
	*tokenRefreshProviderRepo // embedded conditional-write fixture recorder
}

// grokCredentialProxyRepoStub returns a configured proxy record or lookup
// error.
type grokCredentialProxyRepoStub struct {
	egress.ProxyRepository               // unused proxy repository operations
	proxy                  *egress.Proxy // configured borrowed proxy record
	err                    error         // configured proxy lookup error
}

// grokCredentialBlockingRepo signals entered state writers and waits for
// context cancellation.
type grokCredentialBlockingRepo struct {
	*tokenRefreshProviderRepo // embedded conditional-write fixture recorder
	// setErrorStarted and setTempStarted signal their writer admission once through
	// onceError and onceTemp. Writers remain blocked until their contexts end, letting
	// tests observe tentative runtime quarantine before any simulated commit.
	setErrorStarted chan struct{} // permanent write admission signal
	setTempStarted  chan struct{} // temporary write admission signal
	onceError       sync.Once     // single permanent admission notification
	onceTemp        sync.Once     // single temporary admission notification
}

// grokCredentialCommitThenCancelRepo mutates fixture state before returning a
// timeout observation.
type grokCredentialCommitThenCancelRepo struct {
	*tokenRefreshProviderRepo       // embedded conditional-write fixture recorder
	returnErr                 error // injected post-mutation write error
}

// grokCredentialUncommittedDeadlineRepo returns conditional-write deadlines
// without mutating fixture provider state.
type grokCredentialUncommittedDeadlineRepo struct {
	*tokenRefreshProviderRepo // embedded conditional-write fixture recorder
}

// grokCredentialBlockingCache controls deletion completion and records
// successful cleanup after a gate.
type grokCredentialBlockingCache struct {
	providercore.AccessTokenCache // unused access-cache fixture operations
	// deleteStarted signals admission once. releaseDelete permits successful cleanup,
	// while context cancellation can win the wait; deleted records the accepted outcome
	// under mu and is read after joining the recovery result in tests.
	deleteStarted chan struct{} // cache deletion admission signal
	releaseDelete chan struct{} // cache deletion success gate
	once          sync.Once     // single deletion admission notification
	mu            sync.Mutex    // deletion outcome synchronization lock
	deleted       bool          // successful token deletion observation
}

// grokCredentialSequencedRepo returns an initial fixture record then a
// configured latest target.
type grokCredentialSequencedRepo struct {
	*tokenRefreshProviderRepo            // embedded conditional-write fixture recorder
	mu                        sync.Mutex // sequenced read observation lock
	// borrowed latest reread target
	latest  *gatewayprovider.ExecutionProvider
	getCall int // sequenced provider read count
}

// grokCredentialRereadFailureRepo supplies a configured reread target and
// error pair.
type grokCredentialRereadFailureRepo struct {
	*tokenRefreshProviderRepo // embedded conditional-write fixture recorder
	// configured borrowed reread target
	provider *gatewayprovider.ExecutionProvider
	err      error // configured provider reread error
}

// grokCredentialCountingRefresher counts refresh invocations that structural
// validation should bypass.
type grokCredentialCountingRefresher struct {
	refreshCalls int // unexpected refresh exchange count
}

// ExchangeCode records invocation and redirect URI, then returns a fixed
// access token fixture.
func (s *grokOAuthClientStub) ExchangeCode(_ context.Context, _, _, redirectURI, _, _ string) (*xai.TokenResponse, error) {
	s.exchangeCalls++
	s.exchangeRedirectURI = redirectURI
	return &xai.TokenResponse{AccessToken: "access-token"}, nil
}

// RefreshToken returns the configured borrowed refresh-response pointer with a
// nil error. The fixture ignores exchange parameters, allowing tests to choose
// the token result through its saved response.
func (s *grokOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return s.refreshResponse, nil
}

// LoginWithPassword records email/password and returns the configured borrowed
// login result. The fixture ignores proxy input, so tests supply their
// expected credential values before invoking authorization.
func (s *grokOAuthClientStub) LoginWithPassword(_ context.Context, email, password, _ string) (*providercore.GrokPasswordLoginResult, error) {
	s.loginEmail = email
	s.loginPassword = password
	return s.loginResult, nil
}

// ConvertSSOToBuild returns the configured borrowed SSO-response pointer and a
// nil error. The exchange parameters are ignored, leaving the fixture response
// to define the authorization observation.
func (s *grokOAuthClientStub) ConvertSSOToBuild(context.Context, string, string) (*xai.TokenResponse, error) {
	return s.ssoResponse, nil
}

// newGrokAuthorizationForTest binds proxy lookup and an authorization client
// to the production Grok service. Tests start it before credential work and
// stop its lifecycle through stopGrokAuthorizationForTest.
func newGrokAuthorizationForTest(proxies egress.ProxyRepository, client providercore.GrokAuthorizationClient) *providercore.GrokAuthorization {
	return providercore.NewGrokAuthorization(client, provideradapter.GrokAuthorizationOptions(proxies, nil))
}

// stopGrokAuthorizationForTest waits for successful authorization shutdown
// with Background and fails the caller on error. It marks itself as a helper
// before enforcing test-owned lifecycle cleanup.
func stopGrokAuthorizationForTest(t *testing.T, authorization *providercore.GrokAuthorization) {
	t.Helper()
	require.NoError(t, authorization.StopContext(context.Background()))
}

// GetByID returns the fixture target for a configured ID or a
// provider-not-found error. Returned targets remain borrowed, so sequential
// fixture mutation can affect later reads.
func (s *credentialReadStore) GetByID(_ context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	if value, ok := s.providersByID[id]; ok {
		return value, nil
	}
	return nil, errors.New("provider not found")
}

// grokCredentialMutationSnapshot constructs provider comparison evidence from
// an execution-record copy.
func grokCredentialMutationSnapshot(value *gatewayprovider.ExecutionProvider) providercore.CredentialMutationSnapshot {
	return providercore.GrokCredentialMutationSnapshot(gatewayprovider.ExecutionRecord(value))
}

// credentialMutationForTest transfers failure flags, reason, snapshot, and
// structural verification choices to recovery input.
func credentialMutationForTest(value forwardcore.GrokCredentialFailure) providercore.GrokCredentialMutation {
	return providercore.GrokCredentialMutation{Permanent: value.Permanent, Transient: value.Transient, Reason: string(value.Reason), Snapshot: value.Snapshot(), VerifyMissing: value.Reason == forwardcore.GrokCredentialReasonMissing, VerifyProxy: value.Reason == forwardcore.GrokCredentialReasonProxyInvalid}
}

// credentialBlocked checks fixture runtime quarantine against the target's
// current credential identity.
func credentialBlocked(value *gatewayhttp.RequestCredentialExecutor, target *gatewayprovider.ExecutionProvider) bool {
	return value.Runtime.Runtime.Blocked(target.Record.ID, func() string { return providercore.RefreshCredentialIdentity(gatewayprovider.ExecutionRecord(target)) })
}

// newRequestCredentialsFixture binds shared runtime blocking, token
// resolution, and credential recovery for request tests. Optional storage
// supplies parent/current reads and conditional state capability; optional
// tokens supply acquisition and invalidation callbacks.
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

// newCredentialMutationHold creates one controlled provider mutation-lock
// fixture with fresh start/done channels. The caller runs Lock before Unlock
// and uses a fresh fixture for each held operation.
func newCredentialMutationHold(core *providercore.GrokCredentialRecovery, id int64) *credentialMutationHold {
	return &credentialMutationHold{core: core, id: id, started: make(chan struct{}), done: make(chan struct{})}
}

// read signals that the controlled recovery has reached its locked reread and
// waits for context cancellation. Use it once per hold; closing started again
// would panic.
func (h *credentialMutationHold) read(ctx context.Context) (*providercore.Record, error) {
	// Signal locked read admission before waiting, so Lock can observe the held
	// recovery operation and release it later through cancellation in Unlock.
	close(h.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// Lock starts a recovery operation whose controlled reread holds the provider
// mutation lock until cancellation. It returns after started signals that read
// admission was reached; caller cancellation before admission cancels and
// joins the operation.
//
// When no reader was bound, the fixture temporarily installs its reader and
// records restoration for Unlock. The derived context carries the holder for
// the existing fixture reader path. Callers invoke Unlock after either return
// to restore any temporary reader; the cancellation error path has already
// cancelled and joined the operation but leaves restoration pending. Use a
// fresh holder and serialize fixture-reader replacement.
func (h *credentialMutationHold) Lock(ctx context.Context) error {
	if h.core.Read == nil {
		h.core.Read = func(ctx context.Context, _ int64) (*providercore.Record, error) { return h.read(ctx) }
		h.restore = func() { h.core.Read = nil }
	}
	// The recovery reaches Read after taking its provider mutation lock; carrying
	// this holder in context routes that read to the gate owned by this fixture.
	run, cancel := context.WithCancel(context.WithValue(ctx, credentialMutationHoldKey{}, h))
	h.cancel = cancel
	// Start the held operation asynchronously. Its expected cancellation result
	// is discarded, and closing done lets Unlock join it before restoring Read.
	go func() {
		defer close(h.done)
		_, _ = h.core.Apply(run, &providercore.Record{ID: h.id}, providercore.GrokCredentialMutation{Transient: true})
	}()
	select {
	case <-h.started:
		return nil
	case <-ctx.Done():
		// Admission lost to caller cancellation. Stop and join the held operation
		// here; Unlock still owns restoration of any reader installed before launch.
		cancel()
		<-h.done
		return ctx.Err()
	}
}

// Unlock cancels and joins the held recovery operation before restoring a
// temporarily installed reader. Call it after Lock has returned with the same
// holder, including an error return that left temporary reader restoration
// pending. Its cancellation function and done channel belong to that operation.
//
// Joining done keeps later fixture operations from racing with its blocked
// read callback. Restoration runs after that join so the shared core can be
// reused with its prior reader availability. The holder is intended for one
// Lock/Unlock lifetime; callers create another holder for a later controlled
// operation.
func (h *credentialMutationHold) Unlock() {
	// Join the cancelled reader before restoring its callback, so a later fixture
	// lookup cannot overlap the held operation through a replaced read dependency.
	h.cancel()
	<-h.done
	if h.restore != nil {
		h.restore()
	}
}

// Update records a whole-target update attempt and returns the configured
// update error. It retains the supplied target pointer; this fixture operation
// does not itself install that target into providersByID.
func (r *tokenRefreshProviderRepo) Update(ctx context.Context, provider *gatewayprovider.ExecutionProvider) error {
	r.updateCalls++
	r.fullUpdateCalls++
	r.lastProvider = provider
	return r.updateErr
}

// UpdateCredentials records its attempt, copies top-level credentials on
// success, and attaches them to a matching fixture target or lastProvider. A
// configured update error stops installation; optional cancellation runs after
// successful assignment. Nested credential containers remain borrowed, and
// tests serialize their mutation with fixture reads.
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

// GetByID optionally rejects cancelled reads and delays reads after the first,
// then delegates target lookup. snapshotReads returns a copied execution
// target; otherwise the configured target stays borrowed, and callers
// serialize fixture counters.
func (r *tokenRefreshProviderRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	if r.respectReadContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.getByIDCalls++
	// Delay confirmation rereads after the initial observation, respecting context
	// expiry so tests can distinguish a write deadline from successful durable confirmation.
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

// SetError records error-write count/message and returns its configured
// fixture error.
func (r *tokenRefreshProviderRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	return r.setErrorErr
}

// ClearTempUnschedulable increments the cleanup count and returns success for
// fixture assertions.
func (r *tokenRefreshProviderRepo) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempCalls++
	return nil
}

// SetTempUnschedulable records temporary-write count/reason and returns its
// configured error.
func (r *tokenRefreshProviderRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	return r.setTempUnschedErr
}

// SetGrokCredentialErrorIfMatch installs fixture error state when current
// schedulable Grok OAuth credentials and proxy ID match the supplied snapshot.
// A proxy-invalid reason additionally rejects a target whose proxy has become
// hydrated since the failed observation.
//
// The one-shot hook runs before comparison so a simulated competing change can
// defeat stale evidence. Matching attempts increment the write count and record
// the message before checking setErrorErr; that error leaves status unchanged.
// Successful writes set error/disabled fields. Tests serialize these in-memory
// comparisons and mutations; this fixture supplies no database atomicity.
func (r *tokenRefreshProviderRepo) SetGrokCredentialErrorIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	errorMsg string,
) (bool, error) {
	// Apply one simulated competing change immediately before comparison; clearing
	// the hook first prevents later fixture reads/writes from repeating that interference.
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

// SetGrokCredentialTempUnschedulableIfMatch installs a fixture deadline when
// current schedulable Grok OAuth credentials and proxy ID match the snapshot.
// The one-shot hook runs first, allowing a simulated competing change to reject
// stale failure evidence before the write count increases.
//
// Matching attempts record lastTempUnschedReason before checking the configured
// write error. Success replaces the target deadline while retaining its existing
// TempUnschedulableReason field; a write error retains the prior deadline.
// Tests serialize fixture state access, since comparison and installation here
// simulate recovery behavior without a database transaction.
func (r *tokenRefreshProviderRepo) SetGrokCredentialTempUnschedulableIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	// Apply one simulated competing change immediately before comparison; clearing
	// the hook first prevents later fixture reads/writes from repeating that interference.
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

// grokCredentialSnapshotMatchesProvider requires schedulable Grok OAuth
// identity plus matching credential JSON and proxy ID. Nil or ineligible
// fixture records fail, allowing recovery tests to reject stale mutation
// evidence.
func grokCredentialSnapshotMatchesProvider(provider *gatewayprovider.ExecutionProvider, snapshot providercore.CredentialMutationSnapshot) bool {
	return provider != nil && provider.View().IsGrokOAuth() && provider.View().IsSchedulable() &&
		grokCredentialMutationSnapshot(provider).CredentialsJSON == snapshot.CredentialsJSON &&
		providercore.GrokCredentialProxyIDsEqual(provider.Record.ProxyID, snapshot.ProxyID)
}

// SetGrokOAuthRefreshErrorIfCredentialsUnchanged installs fixture error state
// after matching active Grok OAuth type, credentials, and proxy identity.
// conditionalErrorErr records the attempted call and returns before any
// configured reauthorization or proxy repair occurs.
//
// Otherwise each enabled interference runs once before comparison, allowing
// fresher authentication or another proxy route to defeat the old observation.
// A successful comparison increments the permanent-write count and installs
// disabled/error fields; it does not inspect transient scheduling windows.
// Tests serialize these fixture mutations, which model the caller's comparison
// inputs and outcomes rather than store-level atomicity.
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

// UpdateGrokOAuthCredentialsIfUnchanged installs a top-level credential copy
// after matching Grok OAuth platform/type, credential values, and proxy ID.
// conditionalSuccessErr returns after counting the attempt, before scheduling
// interference or credential assignment.
//
// The optional one-shot scheduling change happens before comparison and can
// remain even when identity mismatches. Status and scheduling flags do not
// participate in this fixture's success comparison, so an accepted refresh
// retains those separate changes. Nested credential values stay borrowed, and
// cancellation can run after assignment. Tests serialize fixture access; these
// observations simulate conditional persistence rather than database atomicity.
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

// SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged replaces fixture
// deadline/reason after matching active Grok OAuth credentials and proxy ID.
// conditionalTempErr counts the attempt and returns before any configured
// credential or proxy interference.
//
// Otherwise each enabled interference runs once before comparison, so a newer
// authorization or repaired route can reject stale failure evidence. Accepted
// writes record their count/reason and assign the supplied deadline directly;
// this fixture has no extension-only deadline check. Tests serialize its
// in-memory state, and assertions describe comparison outcomes without claiming
// database isolation or atomicity.
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

// UpdateExtra records a top-level copied update map and merges keys into a
// found fixture target. Nested values remain shared, and a missing ID still
// returns success with the update observation recorded.
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

// CanRefresh reports true so this fixture always qualifies for refresh
// capability.
func (r *tokenRefresherStub) CanRefresh(provider *providercore.Record) bool {
	return true
}

// NeedsRefresh reports true so tests force the configured refresh exchange
// path.
func (r *tokenRefresherStub) NeedsRefresh(provider *providercore.Record, refreshWindowDuration time.Duration) bool {
	return true
}

// Refresh increments call count and returns its configured error or borrowed
// credential map. Consumers keep that map stable while the refresh
// implementation clones or persists its entries.
func (r *tokenRefresherStub) Refresh(ctx context.Context, provider *providercore.Record) (map[string]any, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.credentials, nil
}

// CacheKey builds the fixture refresh namespace from the provider platform.
func (r *tokenRefresherStub) CacheKey(provider *providercore.Record) string {
	return "test:stub:" + provider.Platform
}

// grokCredentialStoredSnapshot copies supported execution-record data and
// initializes a missing credential map. Nil targets remain nil; unsupported
// nested values follow the provider copy helper's retained-value rules.
func grokCredentialStoredSnapshot(value *gatewayprovider.ExecutionProvider) *gatewayprovider.ExecutionProvider {
	copy := gatewayprovider.NewExecutionProvider(gatewayprovider.ExecutionRecord(value))
	if copy != nil && copy.Record.Credentials == nil {
		copy.Record.Credentials = map[string]any{}
	}
	return copy
}

// GetByID converts the bound gateway lookup result to a provider record and
// propagates its error.
func (r tokenSourceFixtureReader) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	v, err := r.source.GetByID(ctx, id)
	return gatewayprovider.ExecutionRecord(v), err
}

// tokenSourceFixtureRepository adapts target reads and preserves conditional
// writer capabilities through embedded interfaces. Nil input returns nil; the
// returned shape exposes whichever credential and Grok success writers the
// fixture actually implements.
func tokenSourceFixtureRepository(repo gatewayprovider.ExecutionProviderStore) providercore.RefreshRepository {
	if repo == nil {
		return nil
	}
	reader := tokenSourceFixtureReader{repo}
	writer, hasWriter := repo.(providercore.CredentialRefreshWriter)
	grok, hasGrok := repo.(providercore.GrokRefreshSuccessWriter)
	if hasWriter && hasGrok {
		return struct {
			// execution-backed provider record reader
			providercore.RefreshRepository
			// conditional credential update capability
			providercore.CredentialRefreshWriter
			// Grok success observation capability
			providercore.GrokRefreshSuccessWriter
		}{reader, writer, grok}
	}
	if hasWriter {
		return struct {
			// execution-backed provider record reader
			providercore.RefreshRepository
			// conditional credential update capability
			providercore.CredentialRefreshWriter
		}{reader, writer}
	}
	if hasGrok {
		return struct {
			// execution-backed provider record reader
			providercore.RefreshRepository
			// Grok success observation capability
			providercore.GrokRefreshSuccessWriter
		}{reader, grok}
	}
	return reader
}

// GetAccessToken increments its read count under the mutex and returns the
// configured token. Blank token produces a cache-miss error; key input is
// ignored in this recording fixture.
func (c *grokTokenCacheForProviderTest) GetAccessToken(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	if c.token == "" {
		return "", errors.New("not cached")
	}
	return c.token, nil
}

// SetAccessToken records key, token, and TTL under the mutex for later
// assertions. The separate configured Get token stays unchanged, so write
// observation and cache-read behavior are independently controlled.
func (c *grokTokenCacheForProviderTest) SetAccessToken(_ context.Context, key string, token string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setKey = key
	c.setToken = token
	c.setTTL = ttl
	return nil
}

// DeleteAccessToken records the requested key under the mutex before returning
// deleteErr. A failed deletion is still counted as an attempted call, allowing
// tests to assert stage ordering.
func (c *grokTokenCacheForProviderTest) DeleteAccessToken(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletedKeys = append(c.deletedKeys, key)
	return c.deleteErr
}

// AcquireRefreshLock returns the fixture's configured lock result with nil
// error. It performs no actual exclusion, leaving production/local lock
// coordination and fixture test setup to control concurrency.
func (c *grokTokenCacheForProviderTest) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return c.lockResult, nil
}

// ReleaseRefreshLock records the fixture lock-release invocation and returns a
// nil error.
func (c *grokTokenCacheForProviderTest) ReleaseRefreshLock(context.Context, string) error {
	c.releaseCalls++
	return nil
}

// UpdateOAuthCredentialsIfUnchanged compares captured ID/platform/type/status,
// proxy, and credentials with the fixture record. Nil credentials are treated
// as empty; a successful match delegates UpdateCredentials, whose configured
// error determines the returned write result.
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

// ClearRefreshCooldownIfUnchanged compares current cooldown evidence when a
// fixture record exists. A missing current record still accepts cleanup, while
// mismatched observations return false before incrementing the cleanup count.
func (r *tokenRefreshProviderRepo) ClearRefreshCooldownIfUnchanged(ctx context.Context, v providercore.RefreshCooldownVersion) (bool, error) {
	if current := r.providersByID[v.ID]; current != nil && !reflect.DeepEqual(providercore.ObserveRefreshCooldown(gatewayprovider.ExecutionRecord(current)), v) {
		return false, nil
	}
	return true, r.ClearTempUnschedulable(ctx, v.ID)
}

// refreshFailureMatchesFixture compares fixture identity, scheduling,
// credential, and proxy evidence against a captured failure version. Missing
// targets fail and nil credential maps normalize to empty for this comparison.
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

// ApplyOAuthRefreshFailure rejects cancellation or changed configured failure
// versions before recording permanent/temporary writes. With no read map it
// accepts the supplied version; successful permanent writes also install
// disabled/error fields on a found record.
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

// ClearAntigravityRefreshRequest compares fixture identity when a read map is
// configured before clearing refresh extras. With no map it records the update
// directly; UpdateExtra errors govern the returned acceptance flag.
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

// newGrokTokenSourceForTest binds fixture read/writer capabilities, token
// cache, and production Grok refresh policy.
func newGrokTokenSourceForTest(repo gatewayprovider.ExecutionProviderStore, cache providercore.AccessTokenCache) *providercore.GrokTokenSource {
	return &providercore.GrokTokenSource{Repository: tokenSourceFixtureRepository(repo), Cache: cache, Policy: providercore.GrokProviderRefreshPolicy()}
}

// bindGrokRefreshForTest installs a callback that delegates token-source
// refresh to the shared OAuthRefreshAPI. Tests supply the executor and window
// for RefreshIfNeeded, keeping acquisition tied to production refresh
// coordination.
func bindGrokRefreshForTest(source *providercore.GrokTokenSource, refresh *providercore.OAuthRefreshAPI, executor providercore.OAuthRefreshExecutor) {
	source.Refresh = func(ctx context.Context, record *providercore.Record, window time.Duration) (*providercore.OAuthRefreshResult, error) {
		return refresh.RefreshIfNeeded(ctx, record, executor, window)
	}
}

// newGrokCredentialRefreshForTest builds production refresh coordination from
// the fixture repository and cache. It supplies the wall clock, logging
// callbacks, and provider refresh platform policy for the scenario.
func newGrokCredentialRefreshForTest(repo gatewayprovider.ExecutionProviderStore, cache providercore.AccessTokenCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(tokenSourceFixtureRepository(repo), cache, providercore.RefreshOptions{Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error, Platform: providercore.ProviderRefreshPlatformPolicy()})
}

// SetError rejects cancellation, records the base fixture write, and installs
// disabled/error state after success. Base write errors stop installation, so
// the mutation represents an accepted persistent fixture outcome.
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

// GetByID returns the fixture's configured borrowed proxy and error pair.
// Lookup parameters are ignored, and tests control missing versus unavailable
// proxy classification through those supplied values.
func (r *grokCredentialProxyRepoStub) GetByID(context.Context, int64) (*egress.Proxy, error) {
	return r.proxy, r.err
}

// SetGrokCredentialErrorIfMatch returns false with DeadlineExceeded while
// leaving the provider fixture unchanged. Recovery uses this synthetic
// ambiguous write outcome to test readback that cannot confirm a commit.
func (r *grokCredentialUncommittedDeadlineRepo) SetGrokCredentialErrorIfMatch(
	context.Context,
	int64,
	providercore.CredentialMutationSnapshot,
	string,
) (bool, error) {
	return false, context.DeadlineExceeded
}

// SetGrokCredentialTempUnschedulableIfMatch returns false with
// DeadlineExceeded and installs no fixture deadline. Recovery sees an
// ambiguous temporary write whose later readback has no committed state to
// confirm.
func (r *grokCredentialUncommittedDeadlineRepo) SetGrokCredentialTempUnschedulableIfMatch(
	context.Context,
	int64,
	providercore.CredentialMutationSnapshot,
	time.Time,
	string,
) (bool, error) {
	return false, context.DeadlineExceeded
}

// SetGrokCredentialErrorIfMatch installs fixture error and disabled scheduling
// before reporting a failed acknowledgement. Callers supply a configured target
// for id; the snapshot argument does not select or compare that synthetic state.
//
// A configured returnErr is returned immediately after mutation. Otherwise the
// method waits for context cancellation and returns false with that error.
// This order gives detached recovery readback a committed fixture observation
// even though the write returned no success. Tests coordinate reads with the
// returned operation, and the fixture simulates ambiguity without a database
// transaction.
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
	// The synthetic committed state is already visible. Waiting lets cancellation
	// supply the failed acknowledgement while detached readback can still confirm it.
	<-ctx.Done()
	return false, ctx.Err()
}

// SetGrokCredentialTempUnschedulableIfMatch installs a fixture deadline and
// reason before reporting a failed acknowledgement. Callers supply a configured
// target for id; this synthetic write ignores its snapshot argument.
//
// A configured returnErr returns immediately after mutation. Otherwise context
// cancellation supplies the returned error and false result while the installed
// state remains visible. Detached recovery readback can therefore distinguish
// this accepted state from the uncommitted fixture writer. Tests coordinate
// later reads with operation completion; the scenario models write ambiguity
// without a database transaction.
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
	// The synthetic committed state is already visible. Waiting lets cancellation
	// supply the failed acknowledgement while detached readback can still confirm it.
	<-ctx.Done()
	return false, ctx.Err()
}

// SetError signals write admission once and waits for context cancellation
// before returning its error. The underlying fixture state is unchanged, so
// cancellation can exercise uncommitted local-block rollback.
func (r *grokCredentialBlockingRepo) SetError(ctx context.Context, _ int64, _ string) error {
	// Notify the test before waiting for cancellation. This permanent writer
	// installs no state, so recovery can assert quarantine rollback after it returns.
	r.onceError.Do(func() { close(r.setErrorStarted) })
	<-ctx.Done()
	return ctx.Err()
}

// SetTempUnschedulable signals temporary write admission once and waits for
// context cancellation. It leaves base fixture state unchanged so tests can
// inspect cancellation before commit confirmation.
func (r *grokCredentialBlockingRepo) SetTempUnschedulable(ctx context.Context, _ int64, _ time.Time, _ string) error {
	// Notify the test before waiting for cancellation. This temporary writer
	// installs no state, so recovery can assert quarantine rollback after it returns.
	r.onceTemp.Do(func() { close(r.setTempStarted) })
	<-ctx.Done()
	return ctx.Err()
}

// SetGrokCredentialErrorIfMatch signals conditional write admission once, then
// waits for context cancellation and returns false. It performs no state
// write, enabling recovery's uncommitted cancellation path to be asserted.
func (r *grokCredentialBlockingRepo) SetGrokCredentialErrorIfMatch(
	ctx context.Context,
	_ int64,
	_ providercore.CredentialMutationSnapshot,
	_ string,
) (bool, error) {
	r.onceError.Do(func() { close(r.setErrorStarted) })
	// Writer admission has been signalled while fixture state stays unchanged.
	// Wait for cancellation so readback exercises recovery's uncommitted outcome.
	<-ctx.Done()
	return false, ctx.Err()
}

// SetGrokCredentialTempUnschedulableIfMatch signals temporary conditional
// admission once and waits until the context ends. It returns false with that
// error and leaves the base provider state unchanged.
func (r *grokCredentialBlockingRepo) SetGrokCredentialTempUnschedulableIfMatch(
	ctx context.Context,
	_ int64,
	_ providercore.CredentialMutationSnapshot,
	_ time.Time,
	_ string,
) (bool, error) {
	r.onceTemp.Do(func() { close(r.setTempStarted) })
	// Writer admission has been signalled while fixture state stays unchanged.
	// Wait for cancellation so readback exercises recovery's uncommitted outcome.
	<-ctx.Done()
	return false, ctx.Err()
}

// CacheKey delegates the counting fixture's namespace to the production Grok
// token cache helper.
func (r *grokCredentialCountingRefresher) CacheKey(provider *providercore.Record) string {
	return providercore.GrokTokenCacheKey(provider)
}

// CanRefresh reports true so structural-failure tests can detect an unexpected
// refresh call.
func (r *grokCredentialCountingRefresher) CanRefresh(*providercore.Record) bool { return true }

// NeedsRefresh reports true so the counting fixture would exchange if
// validation permitted it.
func (r *grokCredentialCountingRefresher) NeedsRefresh(*providercore.Record, time.Duration) bool {
	return true
}

// Refresh increments the unexpected-exchange counter and returns a sentinel
// token map. Tests assert zero calls when structural credentials or proxy
// validation should reject acquisition earlier.
func (r *grokCredentialCountingRefresher) Refresh(context.Context, *providercore.Record) (map[string]any, error) {
	r.refreshCalls++
	return map[string]any{"access_token": "must-not-be-used"}, nil
}

// GetByID returns the configured borrowed provider pointer and error pair for
// reread classification. Parameters are ignored so scenarios can independently
// choose missing record, read failure, or a usable target.
func (r *grokCredentialRereadFailureRepo) GetByID(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return r.provider, r.err
}

// GetByID counts reads under its mutex and returns latest after the first read
// when configured. Earlier or unset-latest reads delegate to the base fixture,
// exposing stale/fresh read ordering to recovery.
func (r *grokCredentialSequencedRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCall++
	if r.getCall > 1 && r.latest != nil {
		return r.latest, nil
	}
	return r.tokenRefreshProviderRepo.GetByID(ctx, id)
}

// DeleteAccessToken signals deletion admission once and waits for
// releaseDelete or context cancellation. Tests configure a nonnil
// deleteStarted channel and control releaseDelete; cancellation returns its
// context error.
//
// When release wins, deleted is set under mu before success returns. The
// caller joins recovery before inspecting this flag through wasDeleted,
// separating a completed cleanup from a merely entered delete call. sync.Once
// prevents repeated channel closure, but this fixture does not wait for or
// serialize every independently invoked delete beyond the recorded flag.
func (c *grokCredentialBlockingCache) DeleteAccessToken(ctx context.Context, _ string) error {
	// Signal cache-cleanup admission before waiting so the test can cancel recovery
	// after its state write, then choose whether cleanup completes through this gate.
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

// wasDeleted reads the fixture's successful cache-cleanup flag under its
// mutex.
func (c *grokCredentialBlockingCache) wasDeleted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleted
}

// TestGetRequestCredentialMapsPermanentGrokOAuthFailureAndRedactsSecrets
// supplies revoked refresh credentials with leaked secret text in the error.
// Provider-scoped failover, persistent/runtime quarantine, cache invalidation,
// and Ops evidence retain sanitized classifications without those secrets.
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

// TestGetRequestCredentialPermanentMappingsPersistAndInvalidate compares
// missing access/refresh and explicit entitlement failures. Each classified
// provider error persists disabled state, blocks locally, and invalidates its
// token cache.
func TestGetRequestCredentialPermanentMappingsPersistAndInvalidate(t *testing.T) {
	tests := []struct {
		name string // credential scenario case label
		// initial provider state mutation
		prepare    func(*gatewayprovider.ExecutionProvider)
		refreshErr error // configured credential exchange failure
		// expected credential failure reason
		wantReason  forwardcore.GatewayFailureReason
		cachedToken string // configured cache lookup value
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

// TestGetRequestCredentialMissingAccessNeverRefreshesAndPermanentlyFailsOver
// removes access credentials across missing, expired, and near-expiry cases
// despite cached token data. No refresh exchange occurs, and provider error
// plus token invalidation follow structural failure.
func TestGetRequestCredentialMissingAccessNeverRefreshesAndPermanentlyFailsOver(t *testing.T) {
	tests := []struct {
		name      string     // credential scenario case label
		expiresAt *time.Time // optional stored token expiry
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

// TestGetRequestCredentialWarmCachedAccessWithMissingRefreshPermanentlyFailsOver
// supplies unexpired stored/cached access with no refresh credential.
// Structural checks still choose provider retry with permanent state, cache
// invalidation, and no refresh call.
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

// TestGetRequestCredentialMapsTransientAndProviderFailuresSeparately varies
// transient exchange, shared configuration/store, missing records, fresh
// ineligibility, credential repair, and proxy failures. Assertions distinguish
// provider retries from shared stop decisions and record the tested mutation
// and cache outcomes for each case.
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
			name string // credential scenario case label
			// configured borrowed reread target
			provider *gatewayprovider.ExecutionProvider
			err      error // configured provider reread error
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
			name string // credential scenario case label
			// injected competing provider mutation
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

// TestGetRequestCredentialRuntimeBlockWinsBeforeWarmTokenCache preinstalls a
// runtime block while stored/cached access remains valid. Resolve returns
// provider-changed retry before cache reads and records no additional
// persistent penalties.
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

// TestGetRequestCredentialWarmCachedAccessWithMissingConfiguredProxyPermanentlyFailsOver
// supplies valid access but a configured proxy ID with no loaded proxy.
// Resolve rejects structural proxy failure before refresh, records a provider
// error, invalidates its token cache, and blocks that provider.
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

// TestGetRequestCredentialCancellationAndBudgetDoNotMutateProvider compares
// already cancelled request context with an expired credential budget.
// Cancellation returns directly, budget expiry creates a request-scoped stop,
// and neither changes persistent or runtime state.
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

// TestGetRequestCredentialStateMutationFailureStopsAndKeepsRuntimeBlock
// injects permanent/temporary storage failure or token invalidation failure
// after refresh rejection. Resolve returns shared state-update stop while
// recorded write/delete attempts and immediate runtime block match the failing
// stage.
func TestGetRequestCredentialStateMutationFailureStopsAndKeepsRuntimeBlock(t *testing.T) {
	tests := []struct {
		name       string // credential scenario case label
		refreshErr error  // configured credential exchange failure
		// fixture failure-stage configuration callback
		configure       func(*tokenRefreshProviderRepo, *grokTokenCacheForProviderTest)
		wantSetError    int // expected permanent write count
		wantSetTemp     int // expected temporary write count
		wantCacheDelete int // expected token deletion count
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

// TestGrokCredentialMutationBoundariesHonorParentCancellation cancels while
// permanent or temporary writes are blocked, then after a committed write
// reaches cache deletion. Uncommitted paths roll back local quarantine, while
// confirmed permanent state completes cache cleanup and retains blocking.
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
		// Run recovery asynchronously so the test can observe writer or cleanup
		// admission, cancel the request, and join its error after the controlled
		// operation returns.
		go func() {
			_, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
				Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true,
			}))
			result <- err
		}()
		// Wait for conditional writer admission before cancellation so this scenario
		// exercises tentative runtime blocking while the fixture still has no committed state.
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
		// Run recovery asynchronously so the test can observe writer or cleanup
		// admission, cancel the request, and join its error after the controlled
		// operation returns.
		go func() {
			_, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
				Reason: forwardcore.GrokCredentialReasonRefreshTransient, Transient: true,
			}))
			result <- err
		}()
		// Wait for temporary writer admission before cancelling; joining its result
		// then exposes recovery rollback after the deliberately uncommitted fixture operation.
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
		// Run recovery asynchronously so the test can observe writer or cleanup
		// admission, cancel the request, and join its error after the controlled
		// operation returns.
		go func() {
			_, err := svc.Runtime.Recovery.Apply(ctx, gatewayprovider.ExecutionRecord(provider), credentialMutationForTest(forwardcore.GrokCredentialFailure{
				Reason: forwardcore.GrokCredentialReasonRevoked, Permanent: true,
			}))
			result <- err
		}()
		// Wait until committed-state cleanup starts before cancelling, then release
		// deletion and join recovery so assertions distinguish confirmed fixture
		// quarantine from uncommitted work.
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

// TestGrokCredentialMutationLockWaitHonorsCredentialBudget holds the provider
// mutation lock through a blocked read before starting a short-deadline
// contender. The contender returns deadline failure promptly with no
// persistent penalty or runtime block.
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

// TestGetRequestCredentialBudgetBoundsBlockedConditionalMutation uses a short
// request credential deadline and a blocking conditional quarantine writer.
// Resolve returns request-scoped timeout promptly while uncommitted
// storage/cache effects stay unused and local quarantine is rolled back.
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

// TestGetRequestCredentialLockHeldTimeoutDoesNotQuarantineProvider denies the
// token refresh lock with unchanged, missing, or unavailable authoritative
// provider reads. Failure scope/action follows the reread result, completes
// within the asserted bound, and applies no new quarantine/cache deletion.
func TestGetRequestCredentialLockHeldTimeoutDoesNotQuarantineProvider(t *testing.T) {
	tests := []struct {
		name string // credential scenario case label
		// scenario repository constructor callback
		buildRepo func(*gatewayprovider.ExecutionProvider) gatewayprovider.ExecutionProviderStore
		// expected credential failure scope
		wantScope forwardcore.GatewayFailureScope
		// expected credential failure reason
		wantReason forwardcore.GatewayFailureReason
		// expected provider-loop next action
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

// TestGrokCredentialMutationCancellationAmbiguityConfirmsDurableCommit
// simulates a state write that mutates the fixture then returns caller
// deadline failure. Detached confirmation recognizes committed
// permanent/temporary state, retains its runtime block, and invalidates cache
// for permanent state.
func TestGrokCredentialMutationCancellationAmbiguityConfirmsDurableCommit(t *testing.T) {
	tests := []struct {
		name string // credential scenario case label
		// observed credential failure classification
		class forwardcore.GrokCredentialFailure
		// fixture commit observation predicate
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

// TestGrokCredentialInnerStateDeadlineAmbiguityConfirmsDurableCommit returns
// an inner deadline error after fixture state mutation with a live parent
// context. Readback confirms the state, so recovery succeeds while retaining
// quarantine and the appropriate permanent cache cleanup.
func TestGrokCredentialInnerStateDeadlineAmbiguityConfirmsDurableCommit(t *testing.T) {
	tests := []struct {
		name string // credential scenario case label
		// observed credential failure classification
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

// TestGrokCredentialUnconfirmedInnerStateDeadlineStopsAndRetainsSafetyBlock
// returns a conditional-write deadline without changing fixture state.
// Recovery reports state-update failure, retains local safety quarantine, and
// performs no cache deletion despite the still-active persistent provider.
func TestGrokCredentialUnconfirmedInnerStateDeadlineStopsAndRetainsSafetyBlock(t *testing.T) {
	tests := []struct {
		name string // credential scenario case label
		// observed credential failure classification
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

// TestGrokCredentialRuntimeRollbackOwnership installs later extending or
// shorter independent blocks before the first rollback, then performs
// serialized tentative rollbacks. Independent ownership keeps a block alive,
// while uncontested tentative operations leave none.
func TestGrokCredentialRuntimeRollbackOwnership(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(734)
	t.Run("later extending block survives", func(t *testing.T) {
		svc := newRequestCredentialsFixture(nil, nil)
		until := time.Now().Add(time.Minute)
		rollbackFirst := svc.Runtime.Runtime.BlockRollback(provider.Record.ID, until, "first")

		secondInstalled := make(chan struct{})
		// Join the independent block installation before invoking the first rollback,
		// so the assertion observes ownership replacement rather than a scheduling race.
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

// TestGetRequestCredentialAPIKeyBypassesOAuthFailureMapping supplies a Grok
// API Key record with its configured key. Resolve returns that key and apikey
// kind successfully, leaving OAuth credential Ops failure events absent.
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

// TestPermanentCredentialFailureDoesNotDisableConcurrentlyRefreshedProvider
// offers a fresher stored token/version when permanent recovery starts from
// stale credentials. Conditional recovery creates no provider error, cache
// deletion, or runtime quarantine.
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

// TestCredentialFailureConditionalMutationLosesToConcurrentRefresh installs
// fresher valid credentials immediately before permanent/temporary conditional
// failure writes. Resolve returns the refreshed token with no quarantine
// writes, cache deletion, or retained local block.
func TestCredentialFailureConditionalMutationLosesToConcurrentRefresh(t *testing.T) {
	for index, tt := range []struct {
		name       string // credential scenario case label
		refreshErr error  // configured credential exchange failure
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

// TestCredentialFailureConditionalMutationLosesToConcurrentProxyRepair changes
// the proxy ID before permanent/temporary conditional mutation. The stale
// failure becomes provider-changed retry and creates no persistent or local
// penalty.
func TestCredentialFailureConditionalMutationLosesToConcurrentProxyRepair(t *testing.T) {
	for index, tt := range []struct {
		name       string // credential scenario case label
		refreshErr error  // configured credential exchange failure
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

// TestCredentialFailureConditionalMutationLosesToSameIDProxyRestoration
// hydrates the original configured proxy during the conditional missing-proxy
// failure. Resolve chooses provider-changed retry and leaves persistent state,
// cache, and runtime block untouched.
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

// TestCredentialFailureConditionalMutationLosesToConcurrentUnschedulableState
// injects manual, temporary, rate, or overload ineligibility before
// permanent/temporary writes. Recovery reports provider-state change and
// returns no token while applying no stale penalty or runtime block.
func TestCredentialFailureConditionalMutationLosesToConcurrentUnschedulableState(t *testing.T) {
	future := time.Now().Add(time.Hour)
	states := []struct {
		name string // credential scenario case label
		// injected competing provider mutation
		mutate func(*gatewayprovider.ExecutionProvider)
	}{
		{name: "admin schedulable false", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.Schedulable = false }},
		{name: "temporary cooldown", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.TempUnschedulableUntil = &future }},
		{name: "rate limit cooldown", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.RateLimitResetAt = &future }},
		{name: "overload cooldown", mutate: func(provider *gatewayprovider.ExecutionProvider) { provider.Record.OverloadUntil = &future }},
	}
	classes := []struct {
		name string // credential scenario case label
		// observed credential failure classification
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

// TestCredentialFailureCASMissDoesNotRecoverIneligibleLatestCredential
// installs fresh tokens together with disabled or cooling state before failure
// mutation. Recovery returns provider-state change, rejects those tokens, and
// retains only a separately installed runtime block.
func TestCredentialFailureCASMissDoesNotRecoverIneligibleLatestCredential(t *testing.T) {
	future := time.Now().Add(time.Hour)
	states := []struct {
		name string // credential scenario case label
		// injected competing provider mutation
		mutate             func(*gatewayhttp.RequestCredentialExecutor, *gatewayprovider.ExecutionProvider)
		wantRuntimeBlocked bool // expected retained independent block
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
		name string // credential scenario case label
		// observed credential failure classification
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

// TestGetRequestCredentialSharedCredentialPersistenceFailureStopsWithoutProviderMutation
// fails the conditional successful refresh write. Resolve returns a shared
// store-unavailable stop, records the attempted conditional write, and
// performs no ordinary update, penalty, cache deletion, or runtime block.
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

// TestGetRequestCredentialRecoversConcurrentRefreshWithoutFailover returns
// stale data for the first read and a valid refreshed record afterward despite
// entitlement failure. Resolve returns the fresh token successfully and leaves
// provider penalties, cache deletion, runtime quarantine, and Ops events
// absent.
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

// expiredGrokOAuthProviderForCredentialTest creates an active schedulable Grok
// OAuth record with expired access and a refresh token. The fixture uses the
// CLI base URL and a fresh credential map for each scenario.
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
