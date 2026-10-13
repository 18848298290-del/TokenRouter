package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Provider regression fixtures let tests distinguish request observations from
// stored credential identity. Some repositories retain caller pointers for
// sequential mutation races, while other getters delegate record copying or
// return separate capture slices. Each getter's ownership rule determines which
// returned collections an assertion can change.
//
// Concurrent Grok captures pair request pointers with their body bytes under a
// mutex, and billing gates can hold a quota query while an active probe
// continues. Sequential refresh hooks instead inject reauthorization, proxy
// repair, or cancellation at selected write phases. Those hooks make a
// comparison stale or preserve a committed write before cleanup, allowing tests
// to check the corresponding identity and recovery decisions.

// Provider fixtures share a missing-setting sentinel and a compile-time
// session-window interface assertion for runtime settings and
// window-observation tests.
var (
	// errCooldownSettingMissing marks absent fixture settings so production
	// readers can exercise configured default and read-error handling.
	errCooldownSettingMissing = errors.New("setting missing")
	// The SessionWindowStore assignment checks sessionWindowMockRepo's
	// implementation of the window-maintenance interface consumed by production
	// health tests at compile time.
	_ providercore.SessionWindowStore = (*sessionWindowMockRepo)(nil)
)

// codexInviteResetAdminServiceStub returns fixed provider and proxy pointers
// for invitation tests.
type codexInviteResetAdminServiceStub struct {
	// provider and proxy retain the fixture initializer's pointers. Client
	// callbacks read those same records, so sequential tests keep their values
	// stable while invitation requests run.
	provider *providercore.Record // shared invitation provider fixture
	proxy    *egress.Proxy        // shared invitation proxy fixture
}

// codexInviteResetHTTPUpstreamStub captures invitation requests and consumes
// sequential scripted response objects.
type codexInviteResetHTTPUpstreamStub struct {
	// responses is consumed from the front by sequential transport calls. An empty
	// queue returns a new empty JSON success response.
	responses []*http.Response
	// requests, bodies, and profiles append together for each transport call.
	// Requests and profiles retain supplied pointers; body text remains available
	// after the request reader is restored for sequential assertions.
	requests []*http.Request           // captured invitation request pointers
	bodies   []string                  // captured invitation request text
	profiles []*tlsfingerprint.Profile // captured invitation TLS profile pointers
}

// tlsProfileTestStore supplies fixed profile pointers to the production profile
// cache loader.
type tlsProfileTestStore struct {
	egress.TLSFingerprintProfileRepository // unimplemented profile repository methods
	// profiles retains the initializer's slice and profile pointers. Keep those
	// inputs stable while the production service loads its cache through List.
	profiles []*egress.TLSFingerprintProfile // shared TLS profile fixture pointers
}

// cooldownSettingsStore retains string settings for sequential production
// runtime-settings queries.
type cooldownSettingsStore struct {
	// data supplies mutable settings for these sequential fixtures. Populate it
	// before runtime reads, and serialize Set with queries because no lock guards it.
	data map[string]string // sequential runtime-setting string values
}

// grokQuotaProviderRepo records quota persistence and recovery arguments for
// sequential Grok assertions.
type grokQuotaProviderRepo struct {
	*grokQuotaReadStore // embedded provider lookup fixture
	// updates retains supplied patches by provider ID. Nested values remain shared
	// with the caller while sequential assertions inspect these writes.
	updates               map[int64]map[string]any
	updateCalls           int       // captured Extra write count
	rateLimitedCalls      int       // captured provider cooldown write count
	lastRateLimitedID     int64     // latest limited provider identifier
	lastRateLimitResetAt  time.Time // latest provider cooldown deadline
	tempUnschedCalls      int       // captured temporary suspension write count
	lastTempUnschedID     int64     // latest suspended provider identifier
	lastTempUnschedUntil  time.Time // latest temporary suspension deadline
	lastTempUnschedReason string    // latest temporary suspension reason
	recoveryClearCalls    int       // captured observed-window recovery attempts
	recoveryObservedAt    time.Time // supplied prior limit observation time
	recoveryObservedReset time.Time // supplied prior reset deadline
	// recoveryClearResult supplies the recovery acceptance flag regardless of
	// observed timestamps. ClearRateLimitIfObserved captures arguments without a
	// stored comparison.
	recoveryClearResult bool
}

// grokQuotaProxyRepo counts lookups and returns stored proxy pointers for Grok
// probes.
type grokQuotaProxyRepo struct {
	// proxies retains configured proxy pointers and returns them directly from
	// GetByID. Tests serialize fixture use and keep returned proxy values stable.
	proxies map[int64]*egress.Proxy // shared proxy fixtures by identifier
	calls   int                     // observed proxy lookup count
}

// grokQuotaUsageLogRepo captures queried window starts and supplies configured
// local usage results.
type grokQuotaUsageLogRepo struct {
	// stats is returned directly with err for each window query. Keep its values
	// stable while the usage service composes its result from this shared pointer.
	stats      *providercore.WindowStats // shared local window statistics result
	err        error                     // configured window statistics read error
	calls      int                       // observed window statistics query count
	startTimes []time.Time               // captured query window start times
}

// grokHybridUpstream serves active probes and independently gated billing
// responses for quota tests.
type grokHybridUpstream struct {
	// mu protects requests and bodies as index-aligned captures. Response
	// configuration and billing gates are set before calls and remain stable while
	// requests execute.
	mu       sync.Mutex      // request capture access lock
	requests []*http.Request // captured active and billing requests
	bodies   [][]byte        // captured corresponding request body bytes
	// weeklyUsagePercent supplies a percentage field in weekly billing JSON when
	// nonnil. Nil leaves that field absent from the fixture response.
	weeklyUsagePercent *float64
	// monthlyLimitCents supplies the monthlyLimit cents value when nonnil. Nil
	// leaves the limit object absent from monthly billing JSON.
	monthlyLimitCents *float64
	// activeStatus supplies the Responses probe status. Zero selects HTTP 200
	// while billing calls use their separate status rules.
	activeStatus int
	// activeHeaders supplies shared probe headers. Nil selects the fixed default
	// token-limit headers, and configured maps stay stable during queries.
	activeHeaders http.Header
	// billingStarted optionally signals the first billing request once. Active
	// Responses probes return before reaching this billing-only signal.
	billingStarted chan struct{}
	// billingRelease optionally gates billing completion. Waiting calls can finish
	// on release or return their request cancellation error.
	billingRelease   <-chan struct{}
	billingStartOnce sync.Once // billing start signal close guard
	// billingStatus, weeklyBillingStatus, and monthlyBillingStatus determine
	// billing response status together. Nonzero format-specific values override
	// the common value; zero common status follows the successful JSON path.
	billingStatus        int // common billing response status
	weeklyBillingStatus  int // weekly billing status override
	monthlyBillingStatus int // monthly billing status override
	// billingHeaders is returned directly on a failed billing response. Configure
	// it before concurrent queries and keep its map and value slices stable.
	billingHeaders http.Header // shared billing error response headers
}

// grokQuotaReadStore counts provider reads and delegates record copying to
// CloneRecord.
type grokQuotaReadStore struct {
	// providersByID retains configured record pointers for lookup. GetByID delegates
	// result copying to CloneRecord; keep the stored inputs stable during reads.
	providersByID map[int64]*providercore.Record // stored provider fixtures by identifier
	getByIDCalls  int                            // observed provider lookup count
}

// grokQuotaHTTPRecorder captures request bodies and supplies sequential quota
// response fixtures.
type grokQuotaHTTPRecorder struct {
	// lastReq and requests retain original request pointers for sequential
	// assertions. lastBody owns the latest read bytes, while bodies appends
	// separate copies of those bytes; body restoration keeps requests readable.
	lastReq      *http.Request   // latest captured request pointer
	lastBody     []byte          // latest captured request body bytes
	lastProxyURL string          // latest selected proxy URL
	requests     []*http.Request // captured quota request pointers
	bodies       [][]byte        // copied captured request body slices
	// resp is returned after queued responses are exhausted. It can be nil, and
	// the caller owns its body when present.
	resp *http.Response
	// responses is consumed from the front after a configured error check. Each
	// returned response body belongs to its caller.
	responses []*http.Response
	// err takes precedence over queued and fallback responses after request
	// capture. A nonnil value returns the configured transport failure.
	err error
}

// grokQuotaUpstreamStep describes one scripted status, body, or transport error
// for quota retry tests.
type grokQuotaUpstreamStep struct {
	status int    // scripted HTTP response status
	body   string // scripted quota response JSON
	err    error  // scripted transport error result
}

// grokQuotaSequenceUpstream pairs each serialized captured request with its
// corresponding scripted step.
type grokQuotaSequenceUpstream struct {
	// mu protects requests while selecting the same request index in steps.
	// Callers configure steps before execution and keep their script stable across
	// concurrent transport calls.
	mu       sync.Mutex              // script and capture access lock
	steps    []grokQuotaUpstreamStep // request-indexed scripted transport outcomes
	requests []*http.Request         // captured sequential request pointers
}

// providerUsageCodexProbeRepo exposes quota and recovery writes through
// optional assertion channels.
type providerUsageCodexProbeRepo struct {
	usageRecordFixture // embedded cloned record lookup fixture
	// updateExtraCh optionally receives shallow patch copies. Nil skips capture;
	// tests buffer it so background writes can finish before assertions.
	updateExtraCh chan map[string]any
	// rateLimitCh optionally receives requested reset deadlines. Nil skips
	// capture, and tests provide buffering for asynchronous writers.
	rateLimitCh chan time.Time
	// clearLimitCh optionally receives provider IDs from recovery calls. Nil skips
	// capture, and callers buffer the channel for asynchronous observation.
	clearLimitCh chan int64
	// clearErrorCh optionally receives provider IDs from error recovery. Nil skips
	// capture, and callers buffer it before asynchronous writes.
	clearErrorCh chan int64
}

// oauthUsageFixtureOptions supplies optional storage, transport, and session
// dependencies for production usage tests.
type oauthUsageFixtureOptions struct {
	// providerRepo supplies usage reads and optional state writes to the
	// production service. Nil remains available for scenarios that require no
	// repository access.
	providerRepo providercore.OAuthUsageReader
	// cache optionally supplies production query caching. The production
	// constructor applies its own default when this dependency is absent.
	cache *providercore.OAuthUsageCache
	// httpUpstream supplies provider-aware usage transport. The constructed
	// platform adapters retain it, including when a test deliberately leaves
	// transport unavailable.
	httpUpstream QoderTransport
	// tlsFPProfileService optionally selects usage request fingerprints. Nil
	// leaves the platform adapter without an explicitly configured profile
	// service.
	tlsFPProfileService *egressadapter.TLSProfiles
	// qoderSessionProvider optionally supplies shared Qoder sessions. Nil creates
	// a fresh token provider and attaches the fixture's transport and profile
	// dependencies.
	qoderSessionProvider *QoderTokenProvider
}

// usageRecordFixture supplies cloned provider records and batch lookup order
// for usage tests.
type usageRecordFixture struct {
	// providers retains the supplied slice and record collections as lookup input.
	// GetByID and GetByIDs delegate result copying to CloneRecord; configure these
	// records before usage queries run.
	providers []providercore.Record // stored usage provider record values
}

// openAIOAuthTokenRouterReaderStub returns shared runtime router fixtures by
// their identifiers.
type openAIOAuthTokenRouterReaderStub struct {
	// routers retains configured pointers and returns them directly by ID. Keep
	// their fields stable while client factories inspect this shared configuration.
	routers map[int64]*egress.TLSFingerprintRouter // shared runtime routers by identifier
}

// qoderObservationIdentityRepo compares delayed usage observations with the
// fixture's current credential identity.
type qoderObservationIdentityRepo struct {
	providercore.OAuthUsageReader // unimplemented usage repository methods
	// current is required for identity comparisons and shallow reads. Tests
	// replace its credential map to make in-flight observations stale.
	current *providercore.Record
	writes  int // accepted usage state write count
}

// tokenRefreshProviderRepo captures sequential refresh writes and injects
// credential, proxy, or cancellation races.
type tokenRefreshProviderRepo struct {
	refreshRecordFixture          // embedded sequential provider pointer store
	updateCalls            int    // total attempted provider update count
	fullUpdateCalls        int    // attempted full-row update count
	updateCredentialsCalls int    // attempted credential-only update count
	setErrorCalls          int    // captured provider error write count
	clearTempCalls         int    // captured cooldown clear count
	setTempUnschedCalls    int    // captured temporary suspension write count
	updateExtraCalls       int    // captured Extra patch write count
	lastErrorMessage       string // latest captured provider error text
	lastTempUnschedReason  string // latest captured suspension reason
	// lastExtraUpdates owns the captured top-level map copied by UpdateExtra.
	// Nested mutable values remain shared with the supplied patch during assertions.
	lastExtraUpdates map[string]any // latest shallow-copied Extra patch
	// lastProvider retains the stored or supplied record depending on the writer.
	// Sequential tests inspect that pointer for publication and persistence
	// assertions.
	lastProvider *providercore.Record
	// updateErr makes ordinary credential writes fail after counting and before
	// mutation. Full-row capture returns it after retaining its record pointer.
	updateErr error
	// cancelOnUpdate optionally cancels the caller after successful credential
	// persistence. This lets cleanup tests inspect detached cache and scheduler
	// updates.
	cancelOnUpdate          context.CancelFunc
	conditionalErrorCalls   int // permanent Grok conditional attempt count
	conditionalTempCalls    int // transient Grok conditional attempt count
	conditionalSuccessCalls int // successful Grok conditional attempt count
	// conditionalErrorErr returns before permanent Grok row comparison or
	// mutation. The conditional attempt counter still increments for that failed
	// storage call.
	conditionalErrorErr error
	// conditionalTempErr returns before transient Grok row comparison or mutation.
	// The conditional attempt counter still records that failed storage call.
	conditionalTempErr error
	// conditionalSuccessErr returns before successful Grok row comparison or
	// mutation. The successful-conditional attempt counter still records the
	// storage failure.
	conditionalSuccessErr error
	// snapshotReads makes GetByID return CloneRecord results. False returns the
	// fixture's shared record pointer after lookup.
	snapshotReads bool
	// respectReadContext rejects a cancelled caller before counting a lookup. The
	// optional delayed reread also watches context cancellation independently.
	respectReadContext bool
	getByIDCalls       int // accepted provider lookup attempt count
	// durableReadDelay applies after the first accepted lookup. Positive delays
	// use a timer that can be interrupted by the lookup context.
	durableReadDelay time.Duration
	// mutateSchedulingOnSuccessCAS disables the stored row and adds a cooldown
	// before the next Grok credential comparison. It clears itself after injecting
	// that race.
	mutateSchedulingOnSuccessCAS bool
	// reauthorizeOnErrorCAS replaces credentials before the next permanent Grok
	// comparison. It clears itself, causing the stale failure identity to lose
	// that comparison.
	reauthorizeOnErrorCAS bool
	// reauthorizeOnTempCAS replaces credentials before the next transient Grok
	// comparison. It clears itself so later calls use the resulting stored
	// identity.
	reauthorizeOnTempCAS bool
	// repairProxyOnErrorCAS changes ProxyID before the next permanent Grok
	// comparison and clears itself. The old proxy identity then fails conditional
	// matching.
	repairProxyOnErrorCAS bool
	// repairProxyOnTempCAS changes ProxyID before the next transient Grok
	// comparison and clears itself. The old proxy identity then fails conditional
	// matching.
	repairProxyOnTempCAS bool
	// setErrorErr supplies ordinary error-write failures and credential-error
	// snapshot failures. Captured call counters increment before that error
	// returns.
	setErrorErr error
	// setTempUnschedErr supplies ordinary and credential-snapshot cooldown
	// failures. Captured call counters and reasons are recorded before the error
	// returns.
	setTempUnschedErr error
	// beforeConditionalState is a one-shot hook run before credential-snapshot
	// comparisons. The writer clears it before invocation so nested calls cannot
	// repeat the injected mutation.
	beforeConditionalState func()
}

// tokenRefresherStub supplies configured exchange credentials or an error and
// counts attempts.
type tokenRefresherStub struct {
	// credentials is returned directly after a successful scripted exchange.
	// Tests keep that map and its nested values stable while refresh consumes it.
	credentials map[string]any // shared scripted exchanged credential map
	err         error          // configured exchange failure result
	calls       int            // observed token exchange count
}

// refreshRecordFixture retains provider pointers for sequential refresh
// identity and write assertions.
type refreshRecordFixture struct {
	// providersByID retains the initializer's record pointers. Sequential getters
	// and refresh writers share those objects to expose identity and state changes.
	providersByID map[int64]*providercore.Record // shared refresh records by identifier
}

// sessionWindowMockRepo captures session-window and Extra updates while
// rejecting unexpected health writes.
type sessionWindowMockRepo struct {
	sessionWindowCalls []swCall // captured ordered session-window updates
	updateExtraCalls   []ueCall // captured ordered Extra patch writes
	clearRateLimitIDs  []int64  // captured cooldown recovery provider IDs
}

// swCall captures one provider session-window update with optional timestamp
// pointers.
type swCall struct {
	ID int64 // target provider record identifier
	// Start and End retain caller timestamp pointers, with nil indicating no
	// timestamp change. Session tests keep pointed-to values stable while
	// inspecting these captured updates.
	Start  *time.Time // optional session-window start time
	End    *time.Time // optional session-window end time
	Status string     // observed session-window status label
}

// ueCall captures one provider Extra patch for session observation assertions.
type ueCall struct {
	ID int64 // target provider record identifier
	// Updates retains the patch map passed to storage. Test callers leave its
	// contents stable while ordered session writes are inspected.
	Updates map[string]any
}

// refreshFailureMatchesFixture compares a nonnil record's complete failure
// version with the supplied observation.
func refreshFailureMatchesFixture(value *providercore.Record, version providercore.RefreshFailureVersion) bool {
	return value != nil && reflect.DeepEqual(providercore.FailureVersion(value), version)
}

// GetProvider returns the fixture's stored provider pointer and ignores the
// requested ID. Callers share that record with the test and use the fixture
// sequentially.
func (s codexInviteResetAdminServiceStub) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	return s.provider, nil
}

// GetProxy returns the fixture's stored proxy pointer regardless of the
// requested ID. A nil configured pointer returns nil without an error, and the
// fixture performs no lookup.
func (s codexInviteResetAdminServiceStub) GetProxy(ctx context.Context, id int64) (*egress.Proxy, error) {
	return s.proxy, nil
}

// Do delegates invitation transport capture to DoWithTLS with a nil fingerprint
// profile.
func (s *codexInviteResetHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

// DoWithTLS captures request, body text, and selected profile, then consumes
// the first scripted response. It recreates the consumed request body for
// assertions; an empty response queue returns an empty JSON success body owned
// by the caller.
func (s *codexInviteResetHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		// Use available request bytes for the sequential invitation fixture even when
		// reading fails; its scripted response remains the outcome under test.
		payload, _ := io.ReadAll(req.Body)
		body = string(payload)
		// Request capture consumes its body. Restore those bytes before retaining the
		// request so later assertions or replay helpers can still read the prepared
		// payload.
		req.Body = io.NopCloser(strings.NewReader(body))
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)
	s.profiles = append(s.profiles, profile)
	if len(s.responses) == 0 {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

// codexInviteResetJSONResponse creates an HTTP 200 fixture carrying the
// supplied JSON body.
func codexInviteResetJSONResponse(body string) *http.Response {
	return codexInviteResetJSONStatusResponse(http.StatusOK, body)
}

// codexInviteResetJSONStatusResponse creates a JSON response with the supplied
// status and text. Each call owns a fresh readable body, and the request
// consumer closes it after use.
func codexInviteResetJSONStatusResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// List returns the shared slice and profile pointers configured for cache
// loading. Callers keep those fixture values stable while the production
// profile service reads them.
func (s *tlsProfileTestStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.profiles, nil
}

// newTLSProfileServiceWithCacheForTest loads configured profile pointers
// through the production service's Start method. It supplies no subscription
// cache, so startup populates local profile state without registering a cache
// subscription.
func newTLSProfileServiceWithCacheForTest(profiles map[int64]*egress.TLSFingerprintProfile) *egressadapter.TLSProfiles {
	values := make([]*egress.TLSFingerprintProfile, 0, len(profiles))
	for _, profile := range profiles {
		values = append(values, profile)
	}
	service := egressadapter.NewTLSProfiles(egress.NewTLSFingerprintProfileService(&tlsProfileTestStore{profiles: values}, nil))
	service.Start()
	return service
}

// newCooldownSettingsStore creates an empty settings map for sequential
// runtime-setting fixtures.
func newCooldownSettingsStore() *cooldownSettingsStore {
	return &cooldownSettingsStore{data: map[string]string{}}
}

// GetValue returns the stored string for an exact key or the missing-setting
// sentinel. The fixture has no synchronization and is configured before its
// sequential settings reads.
func (s *cooldownSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.data[key]
	if !ok {
		return "", errCooldownSettingMissing
	}
	return value, nil
}

// Set stores the supplied string under its exact fixture key.
func (s *cooldownSettingsStore) Set(_ context.Context, key, value string) error {
	s.data[key] = value
	return nil
}

// makeGrokOAuthJWT encodes fixture claims into a token-shaped string with
// placeholder signature text. It performs no signing, and tests supply
// JSON-compatible claim values for metadata decoding.
func makeGrokOAuthJWT(claims map[string]any) string {
	// Tests provide JSON-compatible claims for metadata parsing. The fixture
	// intentionally uses placeholder token framing and signature text, so this
	// encoding does not authenticate any principal.
	payload, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// UpdateExtra retains the patch, counts its write, and merges its values into a
// stored fixture record when present. Patch values remain shared, and missing
// records still leave the captured update available.
func (r *grokQuotaProviderRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updateCalls++
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	if r.grokQuotaReadStore != nil {
		provider := r.providersByID[id]
		if provider == nil {
			return nil
		}
		if provider.Extra == nil {
			provider.Extra = make(map[string]any)
		}
		maps.Copy(provider.Extra, updates)
	}
	return nil
}

// SetRateLimited captures the provider ID and reset deadline and counts the
// write.
func (r *grokQuotaProviderRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedID = id
	r.lastRateLimitResetAt = resetAt
	return nil
}

// SetRateLimitedIfLater delegates directly to the fixture's cooldown capture
// method. It performs no comparison with an earlier deadline, so these tests
// inspect requested writes rather than durable later-only enforcement.
func (r *grokQuotaProviderRepo) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.SetRateLimited(ctx, id, resetAt)
}

// ClearRateLimitIfObserved captures the supplied observed timestamps and
// returns the configured result. It counts the attempt but performs no
// stored-window comparison, leaving these fixture assertions focused on
// recovery arguments.
func (r *grokQuotaProviderRepo) ClearRateLimitIfObserved(_ context.Context, _ int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	r.recoveryClearCalls++
	r.recoveryObservedAt = observedLimitedAt
	r.recoveryObservedReset = observedResetAt
	return r.recoveryClearResult, nil
}

// SetTempUnschedulable captures the provider, recovery deadline, and reason and
// counts the write.
func (r *grokQuotaProviderRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempUnschedUntil = until
	r.lastTempUnschedReason = reason
	return nil
}

// GetProviderWindowStats captures the requested window start and returns the
// configured statistics pointer and error. The statistics remain shared with
// the test, which keeps them stable during usage composition.
func (r *grokQuotaUsageLogRepo) GetProviderWindowStats(_ context.Context, _ int64, start time.Time) (*providercore.WindowStats, error) {
	r.calls++
	r.startTimes = append(r.startTimes, start)
	return r.stats, r.err
}

// GetProviderTodayStats returns no local daily statistics for this window-only
// fixture.
func (r *grokQuotaUsageLogRepo) GetProviderTodayStats(context.Context, int64) (*providercore.WindowStats, error) {
	return nil, nil
}

// Do captures a request and returns active-probe or billing fixtures based on
// its path and query. Requests are nonnil and fixture configuration remains
// stable during calls; returned response bodies belong to their consumers.
//
// Active Responses probes return immediately with configured or default
// headers. Billing calls can signal their first start once and wait for release
// or request cancellation, allowing tests to observe active and billing work
// separately. Weekly and monthly status overrides follow query format, and
// optional utilization or limit pointers determine whether those JSON fields
// are included.
func (u *grokHybridUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var body []byte
	if req != nil && req.Body != nil {
		// Capture available probe bytes even when body reading fails in this fixture.
		// The independently configured active or billing response still controls the
		// tested transport outcome.
		body, _ = io.ReadAll(req.Body)
	}
	// Active and billing queries can progress independently. Capture each request
	// and its corresponding body under one lock before either response path can
	// block.
	u.mu.Lock()
	u.requests = append(u.requests, req)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()

	if req.URL.Path == "/v1/responses" {
		status := u.activeStatus
		if status == 0 {
			status = http.StatusOK
		}
		headers := u.activeHeaders
		if headers == nil {
			headers = http.Header{
				"X-Ratelimit-Limit-Tokens":     []string{"2000000"},
				"X-Ratelimit-Remaining-Tokens": []string{"1500000"},
			}
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`))}, nil
	}
	// Signal billing entry once before waiting for release, so tests can inspect
	// active-probe progress while a separate billing request remains in flight.
	if u.billingStarted != nil {
		u.billingStartOnce.Do(func() { close(u.billingStarted) })
	}
	// A gated billing request remains cancellable even while its release is
	// withheld. This models shutdown or caller cancellation separately from
	// successful billing completion.
	if u.billingRelease != nil {
		select {
		case <-u.billingRelease:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	billingStatus := u.billingStatus
	if req.URL.RawQuery == "format=credits" && u.weeklyBillingStatus != 0 {
		billingStatus = u.weeklyBillingStatus
	}
	if req.URL.RawQuery != "format=credits" && u.monthlyBillingStatus != 0 {
		billingStatus = u.monthlyBillingStatus
	}
	if billingStatus != 0 && billingStatus != http.StatusOK {
		return &http.Response{
			StatusCode: billingStatus,
			Header:     u.billingHeaders,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"billing limited"}}`)),
		}, nil
	}

	if req.URL.RawQuery == "format=credits" {
		usage := ""
		if u.weeklyUsagePercent != nil {
			usage = `,"creditUsagePercent":` + strconv.FormatFloat(*u.weeklyUsagePercent, 'f', -1, 64)
		}
		payload := `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"}` + usage + `}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	}
	monthlyLimit := ""
	if u.monthlyLimitCents != nil {
		monthlyLimit = `,"monthlyLimit":{"val":` + strconv.FormatFloat(*u.monthlyLimitCents, 'f', -1, 64) + `}`
	}
	monthlyPayload := `{"config":{"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"` + monthlyLimit + `}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(monthlyPayload)),
	}, nil
}

// snapshot returns new capture slices and copied body bytes while holding the
// transport mutex. Request pointers remain shared with the fixture, so
// assertions keep their mutable request state unchanged.
//
// Active and billing queries can append concurrently, with each request and
// body recorded as an aligned pair. Copying both slices under their capture
// lock keeps that association intact in the returned snapshot; copying body
// bytes then lets assertions inspect payloads after later transport calls have
// extended the fixture's captures.
func (u *grokHybridUpstream) snapshot() ([]*http.Request, [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	requests := append([]*http.Request(nil), u.requests...)
	bodies := make([][]byte, len(u.bodies))
	for i := range u.bodies {
		bodies[i] = append([]byte(nil), u.bodies[i]...)
	}
	return requests, bodies
}

// GetByID counts the lookup and returns the shared proxy stored under id.
// Missing IDs return nil without an error, matching the optional loaded
// relation used by these fixtures.
func (r *grokQuotaProxyRepo) GetByID(_ context.Context, id int64) (*egress.Proxy, error) {
	r.calls++
	return r.proxies[id], nil
}

// newGrokQuotaFixture binds production quota logic to optional proxy, token,
// and HTTP dependencies. It uses the first supplied local-statistics reader
// when present and retains the supplied validator for upstream root checks.
func newGrokQuotaFixture(store GrokQuotaStore, proxy *grokQuotaProxyRepo, token *providercore.GrokTokenSource, transport interface {
	// Do sends the prepared request with selected proxy, provider ID, and
	// concurrency. The fixture delegates returned errors and response-body
	// ownership to its platform quota caller.
	Do(*http.Request, string, int64, int) (*http.Response, error)
}, validator xai.BaseURLValidator, readers ...providercore.LocalUsageStats,
) *providercore.GrokQuotaService {
	requests := &GrokQuotaTransport{OperatorValidator: validator, MapStatus: forward.MapStatus}
	if proxy != nil {
		requests.Proxy = proxy.GetByID
	}
	if transport != nil {
		requests.Do = transport.Do
	}
	var stats providercore.LocalUsageStats
	if len(readers) > 0 {
		stats = readers[0]
	}
	return NewGrokQuota(store, token, requests, stats)
}

// GetByID counts exact-ID lookup and returns a CloneRecord result for a stored
// provider. Missing IDs return a fixture error, separating request-local record
// state from the repository's retained object.
func (r *grokQuotaReadStore) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	r.getByIDCalls++
	if value, ok := r.providersByID[id]; ok {
		return providercore.CloneRecord(value), nil
	}
	return nil, errors.New("provider not found")
}

// Do records a quota request, reads and closes its body, and restores readable
// request bytes for assertions. Captures are unsynchronized, so callers
// serialize use; configured errors precede queued responses, then the single
// fallback response applies.
func (u *grokQuotaHTTPRecorder) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	u.lastProxyURL = proxyURL
	if req != nil && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		u.lastBody = body
		u.bodies = append(u.bodies, append([]byte(nil), body...))
		if err := req.Body.Close(); err != nil {
			return nil, err
		}
		// Close the consumed body before restoring captured bytes. The retained
		// request then remains readable for assertions while close errors still
		// return through this fixture.
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	u.requests = append(u.requests, req)
	if u.err != nil {
		return nil, u.err
	}
	if len(u.responses) > 0 {
		response := u.responses[0]
		u.responses = u.responses[1:]
		return response, nil
	}
	return u.resp, nil
}

// snapshotRequests copies the request capture slice under the transport mutex.
// The copied slice still contains the original request pointers, which tests
// treat as stable while inspecting them.
func (u *grokQuotaSequenceUpstream) snapshotRequests() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.requests...)
}

// healthyGrokQuotaOAuthProvider creates an active schedulable OAuth fixture
// with expiry beyond the refresh skew. Its credential map is fresh, allowing
// each quota test to alter its record independently.
func healthyGrokQuotaOAuthProvider(id int64) *providercore.Record {
	return &providercore.Record{
		ID:          id,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      providercore.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339),
		},
	}
}

// Do records one request and selects its index-matched scripted step under the
// transport mutex. Missing steps return a fixture error, step errors precede
// response construction, and callers close successful response bodies.
//
// Concurrent callers claim their script position when the request is appended,
// using that same position before the lock is released. Configure steps before
// execution and keep them stable, so every retained request selects its own
// corresponding scripted outcome while later calls advance the capture index.
func (u *grokQuotaSequenceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, req)
	index := len(u.requests) - 1
	if index >= len(u.steps) {
		return nil, errors.New("unexpected upstream request")
	}
	step := u.steps[index]
	if step.err != nil {
		return nil, step.err
	}
	return &http.Response{StatusCode: step.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(step.body))}, nil
}

// UpdateExtra sends a shallow patch copy to its optional assertion channel. A
// nil channel skips capture, and buffered test channels let asynchronous
// persistence finish before assertions consume that patch.
func (r *providerUsageCodexProbeRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		maps.Copy(copied, updates)
		// Publish the captured write to the configured assertion channel after
		// preparing its value. Tests provide buffering so the background operation
		// can finish before assertions consume it.
		r.updateExtraCh <- copied
	}
	return nil
}

// SetRateLimited sends the requested reset time to the optional rate-limit
// channel. Nil channels skip capture; tests buffer configured channels so
// background writes do not wait for assertions.
func (r *providerUsageCodexProbeRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		// Publish the captured write to the configured assertion channel after
		// preparing its value. Tests provide buffering so the background operation
		// can finish before assertions consume it.
		r.rateLimitCh <- resetAt
	}
	return nil
}

// ClearRateLimit sends the requested provider ID to its optional clear channel.
// Nil channels skip capture; tests provide buffering when recovery can run
// before the assertion reads that signal.
func (r *providerUsageCodexProbeRepo) ClearRateLimit(_ context.Context, id int64) error {
	if r.clearLimitCh != nil {
		// Publish the captured write to the configured assertion channel after
		// preparing its value. Tests provide buffering so the background operation
		// can finish before assertions consume it.
		r.clearLimitCh <- id
	}
	return nil
}

// ClearError sends the requested provider ID to its optional error-clear
// channel. Nil channels skip capture, and callers supply buffering for any
// asynchronous recovery observation.
func (r *providerUsageCodexProbeRepo) ClearError(_ context.Context, id int64) error {
	if r.clearErrorCh != nil {
		// Publish the captured write to the configured assertion channel after
		// preparing its value. Tests provide buffering so the background operation
		// can finish before assertions consume it.
		r.clearErrorCh <- id
	}
	return nil
}

// newOAuthUsageFixture constructs production OAuth usage querying with the
// supplied fixtures. Missing Qoder sessions create a new provider with the
// configured transport; the returned service retains its dependencies and owns
// its query lifecycle.
func newOAuthUsageFixture(options oauthUsageFixtureOptions) *providercore.OAuthUsageService {
	sessions := options.qoderSessionProvider
	if sessions == nil {
		sessions = NewQoderTokenProvider(qoder.SessionBuilder{})
		sessions.SetHTTPUpstream(options.httpUpstream, options.tlsFPProfileService)
	}
	qoderQuery := &QoderUsage{Sessions: sessions, Transport: options.httpUpstream, Profiles: options.tlsFPProfileService}
	qoderOptions := qoderQuery.Options()
	qoderOptions.Enrich = EnrichUsageWithProviderError
	requests := &OAuthUsageTransport{Transport: options.httpUpstream, Profiles: options.tlsFPProfileService}
	return providercore.NewOAuthUsageService(options.providerRepo, options.cache, nil, providercore.OAuthUsageOptions{
		Now: time.Now, Log: log.Printf, Warn: slog.Warn,
		Qoder:  qoderOptions,
		OpenAI: providercore.OpenAIUsageOptions{Probe: requests.ProbeOpenAI},
		OpenAIQuotaPause: func(_ context.Context, value *providercore.Record, info *providercore.UsageInfo) {
			if value != nil && info != nil {
				info.QuotaAutoPaused, _ = providercore.EvaluateQuotaAutoPause(value.Platform, value.Extra, providercore.QuotaAutoPauseSettings{}, time.Now())
			}
		},
	})
}

// GetByID returns a CloneRecord result for the first stored provider with the
// requested ID. Missing IDs return a fixture error, and callers can change the
// returned record's copied collections independently.
func (r usageRecordFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	for index := range r.providers {
		if r.providers[index].ID == id {
			return providercore.CloneRecord(&r.providers[index]), nil
		}
	}
	return nil, errors.New("provider not found")
}

// GetByIDs returns cloned records in first-requested-ID order and omits
// duplicates and missing IDs. It reports no error for omissions, letting batch
// usage tests exercise partial lookup results.
func (r usageRecordFixture) GetByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	result := make([]*providercore.Record, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for index := range r.providers {
			if r.providers[index].ID == id {
				result = append(result, providercore.CloneRecord(&r.providers[index]))
				break
			}
		}
	}
	return result, nil
}

// UpdateUsageExtraIfUnchanged forwards the patch under the observed provider ID
// and reports acceptance. This fixture performs no identity comparison, so
// separate identity tests supply a checking repository.
func (r *providerUsageCodexProbeRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	return true, r.UpdateExtra(ctx, version.ID, updates)
}

// SetUsageRateLimitIfUnchanged forwards the deadline under the observed
// provider ID and reports acceptance. It does not compare stored identity,
// keeping these fixture assertions focused on requested cooldown writes.
func (r *providerUsageCodexProbeRepo) SetUsageRateLimitIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion, reset time.Time) (bool, error) {
	return true, r.SetRateLimited(ctx, version.ID, reset)
}

// ClearUsageRateLimitIfUnchanged forwards the observed provider ID to the
// capture channel and reports acceptance. The fixture performs no identity
// comparison or durable clearing beyond that captured call.
func (r *providerUsageCodexProbeRepo) ClearUsageRateLimitIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion) (bool, error) {
	return true, r.ClearRateLimit(ctx, version.ID)
}

// newOpenAIAuthorizationForTest configures the supplied or fresh authorization
// dependencies and creates a session store. A supplied first dependency pointer
// is nonnil; later variadic entries are ignored. The helper overwrites proxy and
// client dependencies and registers cleanup to stop the authorization lifecycle.
func newOpenAIAuthorizationForTest(t *testing.T, proxies egress.ProxyRepository, client OpenAIOAuthClient, dependencies ...*OpenAIAuthorizationDependencies) *providercore.OpenAIAuthorization {
	t.Helper()
	deps := &OpenAIAuthorizationDependencies{}
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}
	deps.Proxies = proxies
	deps.Client = client
	authorization := providercore.NewOpenAIAuthorization(providercore.NewOpenAISessionStore(), OpenAIAuthorizationOptions(deps))
	t.Cleanup(func() { stopOpenAIAuthorizationForTest(t, authorization) })
	return authorization
}

// stopOpenAIAuthorizationForTest stops authorization with a background context
// and fails on a drain error.
func stopOpenAIAuthorizationForTest(t *testing.T, authorization *providercore.OpenAIAuthorization) {
	t.Helper()
	require.NoError(t, authorization.StopContext(context.Background()))
}

// GetRuntimeRouter returns the shared router fixture under an exact ID. A nil
// reader or missing entry returns nil, and callers keep returned configuration
// stable during client preparation.
func (s *openAIOAuthTokenRouterReaderStub) GetRuntimeRouter(routerID int64) *egress.TLSFingerprintRouter {
	if s == nil {
		return nil
	}
	return s.routers[routerID]
}

// GetByID returns a shallow value copy of the fixture's current record and
// ignores id. Nested collections remain shared, so tests replace credential
// maps rather than mutating the captured map during a query.
func (r *qoderObservationIdentityRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	v := *r.current
	return &v, nil
}

// UpdateExtra counts an attempted usage patch write for identity assertions.
func (r *qoderObservationIdentityRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.writes++
	return nil
}

// SetRateLimited counts an attempted usage cooldown write for identity
// assertions.
func (r *qoderObservationIdentityRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.writes++
	return nil
}

// ClearRateLimit counts an attempted usage recovery write for identity
// assertions.
func (r *qoderObservationIdentityRepo) ClearRateLimit(context.Context, int64) error {
	r.writes++
	return nil
}

// Update captures a full-row refresh write and retains the supplied record
// pointer. It returns the configured error and counts both overall and full
// updates without persisting a separate record copy.
func (r *tokenRefreshProviderRepo) Update(ctx context.Context, provider *providercore.Record) error {
	r.updateCalls++
	r.fullUpdateCalls++
	r.lastProvider = provider
	return r.updateErr
}

// UpdateCredentials shallow-copies the supplied map after counting a write
// attempt. Configured errors return before mutation; matching stored records
// receive the map, while missing records create a captured record.
//
// cancelOnUpdate runs after either the stored row or lastProvider holds the
// accepted credentials. Tests use that ordering to model caller cancellation
// after persistence and before refresh cleanup. The map copy separates its
// top-level entries while nested values remain shared with the exchange input,
// and sequential callers inspect the retained record after this hook returns.
func (r *tokenRefreshProviderRepo) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.updateCalls++
	r.updateCredentialsCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	cloned := maps.Clone(credentials)
	if r.providersByID != nil {
		if acc, ok := r.providersByID[id]; ok && acc != nil {
			acc.Credentials = cloned
			r.lastProvider = acc
			if r.cancelOnUpdate != nil {
				r.cancelOnUpdate()
			}
			return nil
		}
	}
	r.lastProvider = &providercore.Record{ID: id, Credentials: cloned}
	if r.cancelOnUpdate != nil {
		r.cancelOnUpdate()
	}
	return nil
}

// GetByID reads a sequential refresh fixture with optional context checks,
// delayed durable rereads, and record copying. The first read is immediate;
// later configured delays can be cancelled, and snapshotReads controls whether
// returned record state is cloned.
func (r *tokenRefreshProviderRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	if r.respectReadContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.getByIDCalls++
	// Delay only rereads after the initial selection, letting tests make
	// durable-state lookup cross an attempt deadline while still respecting the
	// read context's cancellation.
	if r.getByIDCalls > 1 && r.durableReadDelay > 0 {
		timer := time.NewTimer(r.durableReadDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	provider, err := r.refreshRecordFixture.GetByID(ctx, id)
	if err != nil || !r.snapshotReads {
		return provider, err
	}
	return providercore.CloneRecord(provider), nil
}

// SetError counts and captures a refresh error write before returning the
// configured failure.
func (r *tokenRefreshProviderRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	return r.setErrorErr
}

// ClearTempUnschedulable counts a requested cooldown clear and reports fixture
// success.
func (r *tokenRefreshProviderRepo) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempCalls++
	return nil
}

// SetTempUnschedulable counts and captures a refresh cooldown reason before
// returning the configured error.
func (r *tokenRefreshProviderRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	return r.setTempUnschedErr
}

// SetGrokCredentialErrorIfMatch invokes a one-shot precomparison hook and
// checks current Grok credential eligibility. It rejects mismatches and a
// repaired proxy-specific error, then records configured write failure or
// mutates the matched record into error state.
//
// The hook is cleared before invocation so a nested conditional call cannot
// apply the same repair twice. Sequential tests place credential or proxy
// replacement at that point and then inspect which identity wins. Successful
// capture precedes status, scheduling, and message mutation; a configured write
// error leaves those row fields unchanged.
func (r *tokenRefreshProviderRepo) SetGrokCredentialErrorIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	errorMsg string,
) (bool, error) {
	// Clear the one-shot hook before invoking it. A nested conditional call then
	// observes the injected state instead of executing the same repair again.
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	provider := r.providersByID[id]
	if !grokCredentialSnapshotMatchesProvider(provider, snapshot) ||
		(errorMsg == "grok_oauth_proxy_invalid" && provider.Proxy != nil) {
		return false, nil
	}
	// Capture the eligible write before row mutation. A configured storage error
	// remains an attempted call while status and scheduling retain their prior
	// values.
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	if r.setErrorErr != nil {
		return false, r.setErrorErr
	}
	provider.Status = providercore.StatusError
	provider.Schedulable = false
	provider.ErrorMessage = errorMsg
	return true, nil
}

// SetGrokCredentialTempUnschedulableIfMatch invokes the one-shot mutation hook
// before comparing the current credential snapshot. Mismatches skip writes,
// configured errors return after capture, and accepted observations set a new
// cooldown pointer.
//
// The hook is cleared before invocation so nested conditional work sees its
// replacement state without repeating the repair. Tests control that sequence
// to make an in-flight observation stale. Only a matching identity reaches the
// write capture; its configured error leaves the old deadline intact, and a
// successful write stores a pointer to this call's copied until value.
func (r *tokenRefreshProviderRepo) SetGrokCredentialTempUnschedulableIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	// Clear the one-shot hook before invoking it. A nested conditional call then
	// observes the injected state instead of executing the same repair again.
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	provider := r.providersByID[id]
	if !grokCredentialSnapshotMatchesProvider(provider, snapshot) {
		return false, nil
	}
	// Capture the matched cooldown request before mutation. A configured storage
	// failure keeps the previous deadline available for assertions of rejected
	// persistence.
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	if r.setTempUnschedErr != nil {
		return false, r.setTempUnschedErr
	}
	value := until
	provider.TempUnschedulableUntil = &value
	return true, nil
}

// grokCredentialSnapshotMatchesProvider compares eligible Grok OAuth
// credentials and proxy identity against a mutation snapshot. Nil or
// unschedulable records return false, and exact credentials JSON plus proxy
// equality determine the match.
func grokCredentialSnapshotMatchesProvider(provider *providercore.Record, snapshot providercore.CredentialMutationSnapshot) bool {
	return provider != nil && provider.IsGrokOAuth() && provider.IsSchedulable() &&
		providercore.GrokCredentialMutationSnapshot(provider).CredentialsJSON == snapshot.CredentialsJSON &&
		providercore.GrokCredentialProxyIDsEqual(provider.ProxyID, snapshot.ProxyID)
}

// SetGrokOAuthRefreshErrorIfCredentialsUnchanged models a permanent failure
// racing with reauthorization or proxy repair. It counts the conditional
// attempt and can return a configured storage error before examining the row.
//
// One-shot repair flags change the stored record before credentials, proxy,
// platform, type, and active status are compared. A changed identity returns
// false without installing the old error. A matching identity receives error
// status, disabled scheduling, and the captured message. The fixture is used
// sequentially so this controlled ordering models the conditional-write
// decision.
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
		provider.Credentials = map[string]any{
			"access_token":   "fresh-access",
			"refresh_token":  "fresh-refresh",
			"_token_version": int64(2),
		}
		provider.Status = providercore.StatusActive
		provider.Schedulable = true
	}
	if r.repairProxyOnErrorCAS {
		r.repairProxyOnErrorCAS = false
		proxyID := int64(902)
		provider.ProxyID = &proxyID
	}
	if provider.Status != providercore.StatusActive || provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(provider.Credentials, expectedCredentials) || !reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	provider.Status = providercore.StatusError
	provider.Schedulable = false
	provider.ErrorMessage = errorMsg
	return true, nil
}

// UpdateGrokOAuthCredentialsIfUnchanged compares credentials and proxy before
// storing a shallow copy of exchanged values. A configured one-shot scheduling
// mutation occurs before comparison and remains in the record when credential
// identity still matches.
//
// The method compares platform and type along with credentials and proxy, while
// status is intentionally outside this fixture's successful credential
// comparison. Accepted writes count, retain the stored record, and can cancel
// the caller after mutation. Tests then verify that durable reread and cleanup
// retain the concurrent scheduling state.
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
		provider.Status = providercore.StatusDisabled
		provider.Schedulable = false
		resetAt := time.Now().Add(30 * time.Minute)
		provider.RateLimitResetAt = &resetAt
	}
	if provider == nil || provider.Platform != capability.PlatformGrok ||
		provider.Type != capability.ProviderTypeOAuth || !reflect.DeepEqual(provider.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.updateCalls++
	r.updateCredentialsCalls++
	provider.Credentials = maps.Clone(credentials)
	r.lastProvider = provider
	if r.cancelOnUpdate != nil {
		r.cancelOnUpdate()
	}
	return true, nil
}

// SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged models a transient
// failure racing with identity repair. It counts the conditional attempt,
// handles a configured write error, and requires a stored record before
// proceeding.
//
// One-shot credential or proxy repair runs before active status and identity
// comparison. A mismatch returns false without recording the stale cooldown,
// while a match sets its deadline and reason. The supplied deadline pointer
// escapes into the fixture record, and operations remain sequential so tests
// control exactly when that comparison becomes stale.
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
		provider.Credentials = map[string]any{
			"access_token":   "fresh-access",
			"refresh_token":  "fresh-refresh",
			"_token_version": int64(2),
		}
		provider.Status = providercore.StatusActive
		provider.Schedulable = true
	}
	if r.repairProxyOnTempCAS {
		r.repairProxyOnTempCAS = false
		proxyID := int64(902)
		provider.ProxyID = &proxyID
	}
	if provider.Status != providercore.StatusActive || provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(provider.Credentials, expectedCredentials) || !reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	provider.TempUnschedulableUntil = &until
	provider.TempUnschedulableReason = reason
	return true, nil
}

// UpdateExtra captures a shallow patch copy and merges values into a matching
// stored record. It initializes missing Extra maps, leaves unmatched storage
// untouched, and reports success for these sequential assertions.
func (r *tokenRefreshProviderRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.updateExtraCalls++
	r.lastExtraUpdates = maps.Clone(updates)
	if r.providersByID != nil {
		if acc, ok := r.providersByID[id]; ok && acc != nil {
			if acc.Extra == nil {
				acc.Extra = make(map[string]any, len(updates))
			}
			maps.Copy(acc.Extra, updates)
		}
	}
	return nil
}

// CanRefresh accepts every record for the scripted refresh exchange fixture.
func (r *tokenRefresherStub) CanRefresh(provider *providercore.Record) bool {
	return true
}

// NeedsRefresh requests an exchange for every scripted fixture record.
func (r *tokenRefresherStub) NeedsRefresh(provider *providercore.Record, refreshWindowDuration time.Duration) bool {
	return true
}

// Refresh counts the exchange and returns the configured error or shared
// credential map. Tests keep configured values stable while callers consume
// them; successful maps are not copied by this stub.
func (r *tokenRefresherStub) Refresh(ctx context.Context, provider *providercore.Record) (map[string]any, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.credentials, nil
}

// CacheKey returns the fixture token namespace containing the record's
// platform.
func (r *tokenRefresherStub) CacheKey(provider *providercore.Record) string {
	return "test:stub:" + provider.Platform
}

// newRefreshAPI constructs production refresh coordination with the supplied
// repository and optional cache. It installs platform refresh policy, leaving
// locking, credential comparison, and exchange lifecycle to that coordinator.
func newRefreshAPI(repo providercore.RefreshRepository, cache providercore.RefreshCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(repo, cache, providercore.RefreshOptions{Platform: providercore.ProviderRefreshPlatformPolicy()})
}

// GetByID returns the shared nonnil provider pointer stored under id. Missing
// or nil entries return a fixture error, and sequential callers observe
// mutations to that retained record.
func (r *refreshRecordFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	if value := r.providersByID[id]; value != nil {
		return value, nil
	}
	return nil, errors.New("provider not found")
}

// UpdateSessionWindow retains the supplied start and end pointers with provider
// ID and status for assertions. The fixture does not clone timestamps, so
// callers leave those values stable while recorded writes are inspected.
func (m *sessionWindowMockRepo) UpdateSessionWindow(_ context.Context, id int64, start, end *time.Time, status string) error {
	m.sessionWindowCalls = append(m.sessionWindowCalls, swCall{ID: id, Start: start, End: end, Status: status})
	return nil
}

// UpdateSessionWindowEnd accepts a deadline update without retaining additional
// fixture state.
func (m *sessionWindowMockRepo) UpdateSessionWindowEnd(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

// UpdateExtra retains the supplied patch map with its provider ID for ordered
// session assertions. Map values remain shared with the caller, who keeps them
// stable through inspection.
func (m *sessionWindowMockRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	m.updateExtraCalls = append(m.updateExtraCalls, ueCall{ID: id, Updates: updates})
	return nil
}

// ClearRateLimit records the provider ID requested for session-window recovery.
func (m *sessionWindowMockRepo) ClearRateLimit(_ context.Context, id int64) error {
	m.clearRateLimitIDs = append(m.clearRateLimitIDs, id)
	return nil
}

// ClearAntigravityQuotaScopes accepts recovery cleanup without recording
// additional fixture state.
func (m *sessionWindowMockRepo) ClearAntigravityQuotaScopes(_ context.Context, _ int64) error {
	return nil
}

// ClearModelRateLimits accepts recovery cleanup without recording additional
// fixture state.
func (m *sessionWindowMockRepo) ClearModelRateLimits(_ context.Context, _ int64) error {
	return nil
}

// ClearTempUnschedulable accepts recovery cleanup without recording additional
// fixture state.
func (m *sessionWindowMockRepo) ClearTempUnschedulable(_ context.Context, _ int64) error {
	return nil
}

// GetByID panics when session-window tests unexpectedly request a provider
// reread.
func (m *sessionWindowMockRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	panic("unexpected")
}

// ClearError panics when session-window recovery unexpectedly clears provider
// error state.
func (m *sessionWindowMockRepo) ClearError(context.Context, int64) error { panic("unexpected") }

// SetError panics when session-window observation unexpectedly writes provider
// error state.
func (m *sessionWindowMockRepo) SetError(context.Context, int64, string) error { panic("unexpected") }

// SetOverloaded panics when session-window observation unexpectedly writes
// overload state.
func (m *sessionWindowMockRepo) SetOverloaded(context.Context, int64, time.Time) error {
	panic("unexpected")
}

// SetRateLimited panics when session-window observation unexpectedly writes a
// provider cooldown.
func (m *sessionWindowMockRepo) SetRateLimited(context.Context, int64, time.Time) error {
	panic("unexpected")
}

// SetTempUnschedulable panics when session-window observation unexpectedly
// writes temporary scheduling state.
func (m *sessionWindowMockRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	panic("unexpected")
}

// SetModelRateLimit panics when session-window observation unexpectedly writes
// model cooldown state.
func (m *sessionWindowMockRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	panic("unexpected")
}

// UpdateOAuthCredentialsIfUnchanged compares the current non-Grok row with the
// exchange credential version before invoking UpdateCredentials. Missing rows
// return false, and nil stored credentials are normalized to an empty map for
// comparison.
//
// Identity includes record ID, platform, type, status, credentials, and proxy
// ID. A mismatch keeps the delayed exchange result out of storage; a match
// delegates to the configured writer and reports acceptance only when that
// writer succeeds. The sequential fixture exposes write failure and postwrite
// cancellation for cleanup tests.
func (r *tokenRefreshProviderRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	current := r.providersByID[version.ID]
	if current == nil {
		return false, nil
	}
	expected := current.Credentials
	if expected == nil {
		expected = map[string]any{}
	}
	if current.ID != version.ID || current.Platform != version.Platform || current.Type != version.Type || current.Status != version.Status || !reflect.DeepEqual(current.ProxyID, version.ProxyID) || !reflect.DeepEqual(expected, version.Credentials) {
		return false, nil
	}
	err := r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

// ClearRefreshCooldownIfUnchanged rejects a stored cooldown version mismatch
// before capturing a clear call. Missing stored rows still accept the fixture
// clear, and the method returns its delegated error with acceptance true.
func (r *tokenRefreshProviderRepo) ClearRefreshCooldownIfUnchanged(ctx context.Context, v providercore.RefreshCooldownVersion) (bool, error) {
	if current := r.providersByID[v.ID]; current != nil && !reflect.DeepEqual(providercore.ObserveRefreshCooldown(current), v) {
		return false, nil
	}
	return true, r.ClearTempUnschedulable(ctx, v.ID)
}

// ApplyOAuthRefreshFailure checks caller cancellation and any configured stored
// identity before recording health failure. When providersByID is nil, it
// permits capture-only scenarios without a current row.
//
// Permanent failures call SetError and transient failures call
// SetTempUnschedulable. A successful permanent write also updates the stored
// status, scheduling flag, and message; failed writes leave those stored fields
// untouched. This ordering lets tests distinguish attempted persistence from
// accepted state while keeping a stale failure out of a replacement identity.
func (r *tokenRefreshProviderRepo) ApplyOAuthRefreshFailure(ctx context.Context, version providercore.RefreshFailureVersion, failure providercore.RefreshFailure) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.providersByID != nil && !refreshFailureMatchesFixture(r.providersByID[version.ID], version) {
		return false, nil
	}
	// Capture persistence before updating fixture row fields. A failed error
	// writer remains an attempted call and must not turn the stored record into
	// an accepted permanent failure.
	var err error
	if failure.Kind == providercore.RefreshFailurePermanent {
		err = r.SetError(ctx, version.ID, failure.Message)
	} else {
		err = r.SetTempUnschedulable(ctx, version.ID, failure.Until, failure.Message)
	}
	if err == nil && failure.Kind == providercore.RefreshFailurePermanent && r.providersByID != nil {
		if value := r.providersByID[version.ID]; value != nil {
			value.Status = providercore.StatusError
			value.Schedulable = false
			value.ErrorMessage = failure.Message
		}
	}
	return err == nil, err
}

// ClearAntigravityRefreshRequest compares a configured current identity before
// clearing the force-refresh patch. Capture-only fixtures without records
// proceed directly, and acceptance reflects whether the Extra write succeeded.
func (r *tokenRefreshProviderRepo) ClearAntigravityRefreshRequest(ctx context.Context, version providercore.CredentialVersion) (bool, error) {
	if r.providersByID != nil {
		value := r.providersByID[version.ID]
		if value == nil || !refreshFailureMatchesFixture(value, providercore.RefreshFailureVersion{CredentialVersion: version, Schedulable: value.Schedulable}) {
			return false, nil
		}
	}
	err := r.UpdateExtra(ctx, version.ID, providercore.ClearedAntigravityRefreshRequest())
	return err == nil, err
}

// matches compares current ID, platform, type, status, credentials, and proxy
// ID with a usage observation. The fixture requires current to be initialized
// and uses exact map and pointer-value equality for identity.
func (r *qoderObservationIdentityRepo) matches(value providercore.UsageObservationVersion) bool {
	current := r.current
	return current.ID == value.ID && current.Platform == value.Platform && current.Type == value.Type && current.Status == value.Status && reflect.DeepEqual(current.Credentials, value.Credentials) && reflect.DeepEqual(current.ProxyID, value.ProxyID)
}

// UpdateUsageExtraIfUnchanged compares the delayed observation before counting
// an Extra write. A mismatch returns false without mutation, while a match
// delegates using the observed provider ID.
func (r *qoderObservationIdentityRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, value providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	if !r.matches(value) {
		return false, nil
	}
	return true, r.UpdateExtra(ctx, value.ID, updates)
}

// SetUsageRateLimitIfUnchanged compares the delayed observation before counting
// a cooldown write. Mismatched credentials or proxy identity return false, and
// matching observations delegate their provider ID and deadline.
func (r *qoderObservationIdentityRepo) SetUsageRateLimitIfUnchanged(ctx context.Context, value providercore.UsageObservationVersion, reset time.Time) (bool, error) {
	if !r.matches(value) {
		return false, nil
	}
	return true, r.SetRateLimited(ctx, value.ID, reset)
}

// ClearUsageRateLimitIfUnchanged checks current identity before counting
// recovery. A mismatch returns false without clearing, while matching
// observations delegate the captured provider ID to ClearRateLimit.
func (r *qoderObservationIdentityRepo) ClearUsageRateLimitIfUnchanged(ctx context.Context, value providercore.UsageObservationVersion) (bool, error) {
	if !r.matches(value) {
		return false, nil
	}
	return true, r.ClearRateLimit(ctx, value.ID)
}
