package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// oldFailureRefresher installs an administrator's new credentials before returning an older exchange failure.
type oldFailureRefresher struct {
	row     *providercore.Record
	failure error
}

type failureBlocker struct{ calls int }

type grokReconcileRepo struct {
	mu                      sync.Mutex
	providers               []providercore.Record
	requests                []providercore.OAuthRefreshPageOptions
	setErrorIDs             []int64
	updatedCredIDs          []int64
	setErrorMessage         []string
	getByIDOverrides        map[int64]providercore.Record
	pageOverride            *providercore.OAuthRefreshCandidatePage
	reauthorizeOnCAS        bool
	reauthorizeOnRefreshCAS bool
	conditionalCalls        int
}

type reconcileInvalidator struct {
	mu  sync.Mutex
	ids []int64
	err error
}

type reconcileRuntimeBlocker struct {
	mu      sync.Mutex
	blocked []int64
	cleared []int64
}

type tokenCacheInvalidatorStub struct {
	calls        int
	err          error
	ctxErr       error
	lastProvider *providercore.Record
}

type tokenRefreshRuntimeBlocker struct {
	blockCalls int
	clearCalls int
}

type tokenRefreshSchedulerCache struct {
	setProviderCalls int
	ctxErr           error
	lastProvider     *providercore.Record
}

type tempUnschedCacheStub struct {
	deleteCalls int
	setCalls    int
	lastState   *providercore.TempUnschedState
}

// mockTokenCacheForRefreshAPI records token-cache and lock operations for coordinated refresh tests.
type mockTokenCacheForRefreshAPI struct {
	lockResult   bool
	lockErr      error
	releaseCalls int
	deleteCalls  int
	deleteCtxErr error
}

// alwaysFreshRefresherStub models a second check finding credentials already refreshed.
type alwaysFreshRefresherStub struct{}

// refreshAttemptFixture binds refresh attempts, cleanup, and failure blocking for tests.
type refreshAttemptFixture struct {
	Attempts providercore.RefreshAttempts
	Post     *providercore.RefreshPostActions
}

type poolHealthProviderRepo struct {
	mu                   sync.Mutex
	pages                map[int64][]providercore.Record
	requests             []providercore.OAuthRefreshPageOptions
	updatedCredentialIDs []int64
	setErrorCalls        int
	setTempUnschedCalls  int
	getByIDErr           error
}

type poolHealthRefresher struct {
	err            error
	delay          time.Duration
	startDelays    []time.Duration
	ignoreContext  bool
	cancel         context.CancelFunc
	newCredentials map[string]any
	calls          atomic.Int64
	active         atomic.Int64
	maxActive      atomic.Int64
	startMu        sync.Mutex
	startTimes     []time.Time
}

type countingRefreshAttemptGate struct {
	calls atomic.Int64
}

type rejectedRefreshAttemptGate struct {
	err error
}

type poolHealthTokenCacheStub struct {
	providercore.AccessTokenCache
}

type tripBeforeRateAdmissionGate struct {
	state *providercore.RefreshProviderState
}

type breakerTripProviderRepo struct {
	*productionPathRateRepo
	setErrorCalls atomic.Int64
	setTempCalls  atomic.Int64
}

type productionPathRateRepo struct {
	mu        sync.Mutex
	providers map[int64]*providercore.Record
}

type productionPathRefreshStart struct {
	providerID int64
	at         time.Time
}

type productionPathRateExecutor struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	calls        atomic.Int64
	startMu      sync.Mutex
	starts       []productionPathRefreshStart
}

type tokenRefreshCandidateRepo struct {
	mu                    sync.Mutex
	providers             []providercore.Record
	updatedCredentialIDs  []int64
	setErrorCalls         int
	setTempUnschedCalls   int
	clearTempCalls        int
	lastTempUnschedReason string
	listActiveCalls       int
}

type tokenRefreshTestRefresher struct {
	err error
}

func TestBackgroundFailureCannotBlockNewCredentials(t *testing.T) {
	for _, engine := range []string{"fallback", "unified"} {
		for _, platform := range []string{capability.PlatformAnthropic, capability.PlatformOpenAI, capability.PlatformGemini, capability.PlatformAntigravity, capability.PlatformQoder} {
			for _, mode := range []string{"permanent", "retry_exhausted"} {
				t.Run(platform+"/"+mode+"/"+engine, func(t *testing.T) {
					row := &providercore.Record{ID: 1, Platform: platform, Type: capability.ProviderTypeOAuth, Status: providercore.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old-fixture", "refresh_token": "old-refresh-fixture"}}
					if platform == capability.PlatformQoder {
						row.Type = capability.ProviderTypeCosy
					}
					if platform == capability.PlatformAntigravity {
						row.Extra = providercore.AntigravityForceTokenRefreshExtra("fixture 401")
					}
					snapshot := *row
					snapshot.Credentials = maps.Clone(row.Credentials)
					repo := &tokenRefreshProviderRepo{}
					repo.providersByID = map[int64]*providercore.Record{1: row}
					blocker := &failureBlocker{}
					s := newRefreshAttemptFixture(repo, &providercore.RefreshTuning{MaxRetries: 1}, nil, nil, nil)
					s.Attempts.PrepareFailure = func(v *providercore.Record) func(time.Time, string) {
						return providercore.PrepareRefreshFailureNotice(blocker, v)
					}
					if engine == "unified" {
						s.Attempts.API = newRefreshAPI(repo, nil)
					}
					message := "invalid_grant fixture"
					if mode == "retry_exhausted" {
						message = "temporary upstream fixture"
					}
					refresher := &oldFailureRefresher{row: row, failure: errors.New(message)}
					require.Error(t, s.Attempts.Run(context.Background(), &snapshot, refresher, refresher, time.Hour, nil))
					require.Zero(t, repo.setErrorCalls+repo.setTempUnschedCalls, "旧失败不能修改新身份的健康状态")
					require.Zero(t, blocker.calls, "旧失败不能阻断新身份的内存调度")
					require.Zero(t, repo.updateExtraCalls, "旧失败不能退休新身份的强制刷新标记")
				})
			}
		}
	}
}

func TestTokenRefreshService_ReconcileGrokOAuthDefaultsToDryRunAndSanitizedPlan(t *testing.T) {
	repo := &grokReconcileRepo{providers: grokReconcileFixtures()}
	refresher := &poolHealthRefresher{}
	svc := newGrokReconcileService(repo, refresher, nil)

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{})

	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.Equal(t, 4, result.Scanned, "Grok API-key rows must not enter the OAuth reconciliation page")
	require.Equal(t, 3, result.Actionable)
	require.Equal(t, 1, result.WouldBlock)
	require.Equal(t, 2, result.WouldRefresh)
	require.Zero(t, result.Blocked)
	require.Zero(t, result.Refreshed)
	require.Zero(t, refresher.calls.Load())
	_, setErrorIDs, updatedIDs, _ := repo.snapshot()
	require.Empty(t, setErrorIDs)
	require.Empty(t, updatedIDs)

	payload, err := json.Marshal(result)
	require.NoError(t, err)
	text := string(payload)
	require.NotContains(t, text, "access-secret")
	require.NotContains(t, text, "refresh-secret")
	require.NotContains(t, text, "api-key-secret")
	require.NotContains(t, text, `"credentials":`)
}

func TestGrokTokenRefresher_NeedsRefreshWhenAccessTokenMissingDespiteFarFutureExpiry(t *testing.T) {
	refresher := providercore.NewGrokTokenRefresher(nil)
	provider := grokPoolProvider(99)
	delete(provider.Credentials, "access_token")
	provider.Credentials["expires_at"] = time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339)

	require.True(t, refresher.NeedsRefresh(&provider, time.Hour))
}

func TestTokenRefreshService_ReconcileGrokOAuthApplyIsIdempotent(t *testing.T) {
	repo := &grokReconcileRepo{providers: grokReconcileFixtures()}
	invalidator := &reconcileInvalidator{}
	refresher := &poolHealthRefresher{newCredentials: map[string]any{
		"access_token":  "rotated-access-secret",
		"refresh_token": "rotated-refresh-secret",
		"expires_at":    time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
	}}
	svc := newGrokReconcileService(repo, refresher, invalidator)

	first, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})
	require.NoError(t, err)
	require.False(t, first.DryRun)
	require.Equal(t, 1, first.Blocked)
	require.Equal(t, 2, first.Refreshed)
	require.Zero(t, first.Failed)
	requests, setErrorIDs, updatedIDs, messages := repo.snapshot()
	require.Equal(t, []int64{1}, setErrorIDs)
	slices.Sort(updatedIDs)
	require.Equal(t, []int64{2, 3}, updatedIDs)
	require.Len(t, messages, 1)
	require.NotContains(t, messages[0], "secret")
	require.False(t, requests[0].RequireRefreshToken, "structurally invalid rows must remain discoverable")
	require.False(t, requests[0].IncludeSetupToken)
	require.Equal(t, 3, invalidator.count(), "block and refresh actions must invalidate token cache state")

	second, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})
	require.NoError(t, err)
	require.Zero(t, second.Actionable)
	require.Equal(t, int64(2), refresher.calls.Load(), "already refreshed rows must not be refreshed again")
	_, setErrorIDs, updatedIDs, _ = repo.snapshot()
	require.Equal(t, []int64{1}, setErrorIDs, "already blocked invalid rows must not transition twice")
	require.Len(t, updatedIDs, 2)
}

func TestTokenRefreshService_ReconcileGrokOAuthCursorResumesWithoutDuplicates(t *testing.T) {
	fixtures := grokReconcileFixtures()[:3]
	repo := &grokReconcileRepo{providers: fixtures}
	svc := newGrokReconcileService(repo, &poolHealthRefresher{}, nil)

	first, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Limit: 2})
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.Equal(t, int64(2), first.NextAfterID)
	require.Len(t, first.Items, 2)

	second, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{AfterID: first.NextAfterID, Limit: 2})
	require.NoError(t, err)
	require.False(t, second.HasMore)
	require.Zero(t, second.NextAfterID)
	require.Len(t, second.Items, 1)
	require.NotEqual(t, first.Items[0].ProviderID, second.Items[0].ProviderID)
	require.NotEqual(t, first.Items[1].ProviderID, second.Items[0].ProviderID)
}

func TestTokenRefreshService_ReconcileGrokOAuthCursorUsesRawPageAfterHydrationGap(t *testing.T) {
	provider := grokReconcileFixtures()[0]
	repo := &grokReconcileRepo{pageOverride: &providercore.OAuthRefreshCandidatePage{
		Providers:   []providercore.Record{provider},
		NextAfterID: provider.ID + 1,
		HasMore:     true,
	}}
	svc := newGrokReconcileService(repo, &poolHealthRefresher{}, nil)

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Limit: 2})

	require.NoError(t, err)
	require.True(t, result.HasMore)
	require.Equal(t, provider.ID+1, result.NextAfterID,
		"cursor must advance past a raw selected ID that disappeared during hydration")
}

func TestTokenRefreshService_ReconcileGrokOAuthRejectsConflictingApplyMode(t *testing.T) {
	svc := newGrokReconcileService(&grokReconcileRepo{}, &poolHealthRefresher{}, nil)

	_, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, DryRun: true})

	require.ErrorIs(t, err, providercore.ErrGrokOAuthReconcileMode)
}

func TestTokenRefreshService_ReconcileGrokOAuthSkipsStaleBlockAfterConcurrentReauthorization(t *testing.T) {
	stale := grokReconcileFixtures()[0]
	latest := stale
	latest.Credentials = map[string]any{
		"access_token":  "fresh-access",
		"refresh_token": "fresh-refresh",
		"expires_at":    time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
	}
	repo := &grokReconcileRepo{
		providers:        []providercore.Record{stale},
		getByIDOverrides: map[int64]providercore.Record{stale.ID: latest},
	}
	svc := newGrokReconcileService(repo, &poolHealthRefresher{}, &reconcileInvalidator{})

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})

	require.NoError(t, err)
	require.Zero(t, result.Blocked)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, providercore.GrokOAuthReconcileOutcomeSkipped, result.Items[0].Outcome)
	_, setErrorIDs, _, _ := repo.snapshot()
	require.Empty(t, setErrorIDs, "a concurrently reauthorized provider must not be disabled from stale page state")
}

func TestTokenRefreshService_ReconcileGrokOAuthDoesNotRuntimeBlockWhenReauthorizationWinsConditionalMutation(t *testing.T) {
	provider := grokReconcileFixtures()[0]
	provider.Credentials["_token_version"] = int64(1)
	repo := &grokReconcileRepo{
		providers:        []providercore.Record{provider},
		reauthorizeOnCAS: true,
	}
	invalidator := &reconcileInvalidator{}
	blocker := &reconcileRuntimeBlocker{}
	svc := newGrokReconcileService(repo, &poolHealthRefresher{}, invalidator, blocker)

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})

	require.NoError(t, err)
	require.Zero(t, result.Blocked)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, providercore.GrokOAuthReconcileOutcomeSkipped, result.Items[0].Outcome)
	require.Zero(t, invalidator.count(), "a lost compare-and-set race must not invalidate fresh credentials")
	_, setErrorIDs, _, _ := repo.snapshot()
	require.Empty(t, setErrorIDs)
	require.Equal(t, 1, repo.conditionalCalls)
	blocked, cleared := blocker.snapshot()
	require.Empty(t, blocked, "a lost compare-and-set race must never install a runtime block")
	require.Empty(t, cleared, "reconciliation must not clear a block it does not own")

	latest, getErr := repo.GetByID(context.Background(), provider.ID)
	require.NoError(t, getErr)
	require.Equal(t, providercore.StatusActive, latest.Status)
	require.True(t, latest.Schedulable)
	require.Equal(t, "fresh-refresh", latest.GetGrokRefreshToken())
}

func TestTokenRefreshService_ReconcileGrokOAuthReportsPermanentRefreshMutationAsBlocked(t *testing.T) {
	provider := grokReconcileFixtures()[2]
	repo := &grokReconcileRepo{providers: []providercore.Record{provider}}
	refresher := &poolHealthRefresher{err: errors.New(`GROK_OAUTH_ENTITLEMENT_DENIED: subscription required`)}
	svc := newGrokReconcileService(repo, refresher, &reconcileInvalidator{})

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})

	require.NoError(t, err)
	require.Equal(t, 1, result.Blocked)
	require.Zero(t, result.Failed)
	require.Zero(t, result.Partial)
	require.Equal(t, providercore.GrokOAuthReconcileActionBlock, result.Items[0].Action)
	require.Equal(t, providercore.GrokOAuthReconcileReasonCredentialRejected, result.Items[0].Reason)
	require.Equal(t, providercore.GrokOAuthReconcileOutcomeApplied, result.Items[0].Outcome)
	_, setErrorIDs, _, _ := repo.snapshot()
	require.Equal(t, []int64{provider.ID}, setErrorIDs)
}

func TestTokenRefreshService_ReconcileGrokOAuthReportsConcurrentRefreshReauthorizationAsSkipped(t *testing.T) {
	provider := grokReconcileFixtures()[2]
	repo := &grokReconcileRepo{
		providers:               []providercore.Record{provider},
		reauthorizeOnRefreshCAS: true,
	}
	invalidator := &reconcileInvalidator{}
	refresher := &poolHealthRefresher{err: errors.New("invalid_grant: revoked")}
	svc := newGrokReconcileService(repo, refresher, invalidator)

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})

	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, result.Failed)
	require.Zero(t, result.Blocked)
	require.Equal(t, providercore.GrokOAuthReconcileOutcomeSkipped, result.Items[0].Outcome)
	require.Zero(t, invalidator.count())
	_, setErrorIDs, _, _ := repo.snapshot()
	require.Empty(t, setErrorIDs)
	latest, getErr := repo.GetByID(context.Background(), provider.ID)
	require.NoError(t, getErr)
	require.Equal(t, providercore.StatusActive, latest.Status)
	require.Equal(t, "fresh-refresh", latest.GetGrokRefreshToken())
}

func TestTokenRefreshService_ReconcileGrokOAuthReportsInvalidationFailureAsPartial(t *testing.T) {
	provider := grokReconcileFixtures()[0]
	repo := &grokReconcileRepo{providers: []providercore.Record{provider}}
	svc := newGrokReconcileService(repo, &poolHealthRefresher{}, &reconcileInvalidator{err: errors.New("cache unavailable")})

	result, err := svc.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 50})

	require.NoError(t, err)
	require.Equal(t, 1, result.Blocked)
	require.Equal(t, 1, result.Partial)
	require.Zero(t, result.Failed)
	require.Equal(t, providercore.GrokOAuthReconcileOutcomePartial, result.Items[0].Outcome)
}

func TestTokenRefreshService_RefreshWithRetry_InvalidatesCache(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       5,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "new-token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 0, repo.fullUpdateCalls)
	require.Equal(t, 1, invalidator.calls)
	require.Equal(t, "new-token", provider.GetCredential("access_token"))
}

func TestTokenRefreshService_RefreshWithRetry_InvalidatorErrorIgnored(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{err: errors.New("invalidate failed")}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       6,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls)
}

func TestTokenRefreshService_RefreshWithRetry_NilInvalidator(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, nil, nil, nil)
	provider := &providercore.Record{
		ID:       7,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
}

func TestTokenRefreshService_RefreshWithRetry_QoderInvalidatesCache(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       17,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"security_oauth_token": "new-token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)

	require.NoError(t, err)
	require.Equal(t, 1, invalidator.calls)
}

// TestTokenRefreshService_RefreshWithRetry_Antigravity checks cache invalidation after refresh.
func TestTokenRefreshService_RefreshWithRetry_Antigravity(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       8,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "ag-token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls) // Antigravity invalidates its token cache too.
}

func TestAntigravityTokenRefresher_NeedsRefresh_ForceRefreshMarker(t *testing.T) {
	refresher := &providercore.AntigravityRefreshRules{Printf: func(string, ...any) {}}
	provider := &providercore.Record{
		ID:       3675,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		},
		Extra: map[string]any{
			providercore.AntigravityForceTokenRefreshExtraKey: true,
		},
	}

	require.True(t, refresher.NeedsRefresh(provider, 0), "server-invalidated token must refresh even before expires_at")
}

func TestAntigravityTokenRefresher_NeedsRefresh_NormalExpiryRulesUnchanged(t *testing.T) {
	refresher := &providercore.AntigravityRefreshRules{Printf: func(string, ...any) {}}

	t.Run("normal_unexpired_without_marker_does_not_refresh", func(t *testing.T) {
		provider := &providercore.Record{
			ID:       3707,
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}

		require.False(t, refresher.NeedsRefresh(provider, 0))
	})

	t.Run("normal_expiring_refreshes", func(t *testing.T) {
		provider := &providercore.Record{
			ID:       3708,
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"expires_at": time.Now().Add(5 * time.Minute).Format(time.RFC3339),
			},
		}

		require.True(t, refresher.NeedsRefresh(provider, 0))
	})
}

func TestTokenRefreshService_RefreshWithRetry_AntigravityClearsForceRefreshOnSuccess(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, nil, nil, nil)
	until := time.Now().Add(10 * time.Minute)
	provider := &providercore.Record{
		ID:                     3709,
		Platform:               capability.PlatformAntigravity,
		Type:                   capability.ProviderTypeOAuth,
		TempUnschedulableUntil: &until,
		Extra: map[string]any{
			providercore.AntigravityForceTokenRefreshExtraKey:       true,
			providercore.AntigravityForceTokenRefreshReasonExtraKey: "401_invalid",
			"privacy_mode": providercore.AntigravityPrivacySet,
		},
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "new-ag-token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 1, repo.updateExtraCalls)
	require.Equal(t, false, repo.lastExtraUpdates[providercore.AntigravityForceTokenRefreshExtraKey])
	require.Equal(t, "", repo.lastExtraUpdates[providercore.AntigravityForceTokenRefreshReasonExtraKey])
	require.Equal(t, false, provider.Extra[providercore.AntigravityForceTokenRefreshExtraKey])
	require.Equal(t, 1, repo.clearTempCalls, "successful refresh should restore schedulability")
}

func TestTokenRefreshService_RefreshWithRetry_AntigravityForceRefreshInvalidGrantSetsError(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          3,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, nil, nil, nil)
	provider := &providercore.Record{
		ID:       3710,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			providercore.AntigravityForceTokenRefreshExtraKey:       true,
			providercore.AntigravityForceTokenRefreshReasonExtraKey: "401_invalid",
		},
	}
	refresher := &tokenRefresherStub{
		err: errors.New("invalid_grant: token revoked"),
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.setTempUnschedCalls)
	require.Equal(t, 1, repo.updateExtraCalls)
	require.Equal(t, false, repo.lastExtraUpdates[providercore.AntigravityForceTokenRefreshExtraKey])
	require.Contains(t, repo.lastErrorMessage, "non-retryable")
}

// TestTokenRefreshService_RefreshWithRetry_NonOAuthProvider checks that API key providers skip OAuth cache
// invalidation.
func TestTokenRefreshService_RefreshWithRetry_NonOAuthProvider(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       9,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeAPIKey, // API key provider
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls) // API key refresh leaves the OAuth cache untouched.
}

// TestTokenRefreshService_RefreshWithRetry_OtherPlatformOAuth checks OpenAI OAuth cache invalidation.
func TestTokenRefreshService_RefreshWithRetry_OtherPlatformOAuth(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       10,
		Platform: capability.PlatformOpenAI, // OpenAI OAuth fixture
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 1, invalidator.calls) // Invalidate the OAuth token after persisting credentials.
}

func TestTokenRefreshService_RefreshWithRetry_UsesCredentialsUpdater(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, nil, nil, nil)
	resetAt := time.Now().Add(30 * time.Minute)
	provider := &providercore.Record{
		ID:               17,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		RateLimitResetAt: &resetAt,
		Credentials: map[string]any{
			"access_token": "old-token",
		},
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "new-token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 0, repo.fullUpdateCalls)
	require.NotNil(t, provider.RateLimitResetAt)
	require.WithinDuration(t, resetAt, *provider.RateLimitResetAt, time.Second)
}

// TestTokenRefreshService_RefreshWithRetry_UpdateFailed checks that persistence failure skips cache invalidation.
func TestTokenRefreshService_RefreshWithRetry_UpdateFailed(t *testing.T) {
	repo := &tokenRefreshProviderRepo{updateErr: errors.New("update failed")}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       11,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to save credentials")
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls) // Persistence failed, so keep the cached token.
}

// TestTokenRefreshService_RefreshWithRetry_RefreshFailed leaves retryable failures eligible for a later cycle.
func TestTokenRefreshService_RefreshWithRetry_RefreshFailed(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          2,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       12,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("refresh failed"),
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)   // Failed exchange leaves credentials unchanged.
	require.Equal(t, 0, invalidator.calls)  // Failed exchange leaves the cached token unchanged.
	require.Equal(t, 0, repo.setErrorCalls) // Exhausted retries can run again next cycle.
}

// TestTokenRefreshService_RefreshWithRetry_AntigravityRefreshFailed leaves retryable failures out of permanent
// error state.
func TestTokenRefreshService_RefreshWithRetry_AntigravityRefreshFailed(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       13,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("network error"), // retryable network failure
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls)
	require.Equal(t, 0, repo.setErrorCalls) // Retryable failure does not mark a permanent error.
}

// TestTokenRefreshService_RefreshWithRetry_AntigravityNonRetryableError checks permanent credential failures.
func TestTokenRefreshService_RefreshWithRetry_AntigravityNonRetryableError(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          3,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	provider := &providercore.Record{
		ID:       14,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("invalid_grant: token revoked"), // permanent credential failure
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls)
	require.Equal(t, 1, repo.setErrorCalls) // Permanent failure marks the provider as erroneous.
}

// TestTokenRefreshService_RefreshWithRetry_ClearsTempUnschedulable clears database and Redis suspension after
// success.
func TestTokenRefreshService_RefreshWithRetry_ClearsTempUnschedulable(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	tempCache := &tempUnschedCacheStub{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, tempCache)
	until := time.Now().Add(10 * time.Minute)
	provider := &providercore.Record{
		ID:                     15,
		Platform:               capability.PlatformGemini,
		Type:                   capability.ProviderTypeOAuth,
		TempUnschedulableUntil: &until,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "new-token",
		},
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, repo.clearTempCalls)   // database suspension cleared
	require.Equal(t, 1, tempCache.deleteCalls) // Redis suspension cleared
}

// TestTokenRefreshService_RefreshWithRetry_NonRetryableErrorAllPlatforms checks permanent failure handling
// across platforms.
func TestTokenRefreshService_RefreshWithRetry_NonRetryableErrorAllPlatforms(t *testing.T) {
	tests := []struct {
		name     string
		platform string
	}{
		{name: "gemini", platform: capability.PlatformGemini},
		{name: "anthropic", platform: capability.PlatformAnthropic},
		{name: "openai", platform: capability.PlatformOpenAI},
		{name: "antigravity", platform: capability.PlatformAntigravity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &tokenRefreshProviderRepo{}
			invalidator := &tokenCacheInvalidatorStub{}
			cfg := &providercore.RefreshTuning{
				MaxRetries:          3,
				RetryBackoffSeconds: 0,
			}
			service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
			provider := &providercore.Record{
				ID:       16,
				Platform: tt.platform,
				Type:     capability.ProviderTypeOAuth,
			}
			refresher := &tokenRefresherStub{
				err: errors.New("invalid_grant: token revoked"),
			}

			err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
			require.Error(t, err)
			require.Equal(t, 1, repo.setErrorCalls) // Permanent errors apply across platforms.
		})
	}
}

func TestTokenRefreshService_RefreshWithRetry_NoRefreshTokenDoesNotTempUnschedule(t *testing.T) {
	repo := &tokenRefreshProviderRepo{}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          2,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, nil, nil, nil)
	provider := &providercore.Record{
		ID:       18,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("no refresh token available"),
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, repo.setTempUnschedCalls, "missing refresh token should not mark the provider temp unschedulable")
	require.Equal(t, 1, repo.setErrorCalls, "missing refresh token should be treated as a non-retryable credential state")
}

// TestPathA_Success checks exchange, persistence, and post-refresh cleanup through the coordinator.
func TestPathA_Success(t *testing.T) {
	provider := &providercore.Record{
		ID:       100,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}

	service, refresher := buildPathAService(repo, cache, invalidator)

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)   // credentials persisted
	require.Equal(t, 1, invalidator.calls)  // cached token invalidated
	require.Equal(t, 1, cache.releaseCalls) // refresh lock released
}

func TestPathA_GrokSuccessPersistenceFailureContainsProviderWithoutRetryOrMutation(t *testing.T) {
	provider := &providercore.Record{
		ID:       110,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
		Credentials: map[string]any{
			"access_token":  "attempted-access",
			"refresh_token": "attempted-refresh",
		},
	}
	repo := &tokenRefreshProviderRepo{
		conditionalSuccessErr: errors.New("database unavailable after provider success"),
	}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          3,
		RetryBackoffSeconds: 0,
	}
	svc := newRefreshAttemptFixture(repo, cfg, nil, nil, nil)
	svc.Attempts.API = newRefreshAPI(repo, nil)
	refresher := &tokenRefresherStub{credentials: map[string]any{
		"access_token":  "provider-access",
		"refresh_token": "provider-refresh",
	}}

	err := svc.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)

	var containmentErr *providercore.ProviderCycleContainmentRefreshError
	require.ErrorAs(t, err, &containmentErr)
	require.Equal(t, 1, refresher.calls, "a provider-issued rotated token must never be retried after persistence fails")
	require.Equal(t, 1, repo.conditionalSuccessCalls)
	require.Zero(t, repo.conditionalErrorCalls)
	require.Zero(t, repo.conditionalTempCalls)
	require.Equal(t, providercore.StatusActive, provider.Status)
	require.Equal(t, "attempted-refresh", provider.GetGrokRefreshToken())
}

func TestPathA_GrokSuccessPublishesDurableSchedulingState(t *testing.T) {
	provider := &providercore.Record{
		ID:          111,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      providercore.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "attempted-access",
			"refresh_token": "attempted-refresh",
		},
	}
	repo := &tokenRefreshProviderRepo{
		snapshotReads:                true,
		mutateSchedulingOnSuccessCAS: true,
	}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	scheduler := &tokenRefreshSchedulerCache{}
	cfg := &providercore.RefreshTuning{MaxRetries: 1}
	svc := newRefreshAttemptFixture(repo, cfg, nil, scheduler, nil)
	svc.Attempts.API = newRefreshAPI(repo, nil)
	refresher := &tokenRefresherStub{credentials: map[string]any{
		"access_token":  "provider-access",
		"refresh_token": "provider-refresh",
	}}

	err := svc.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)

	require.NoError(t, err)
	require.Equal(t, providercore.StatusDisabled, repo.providersByID[provider.ID].Status)
	require.False(t, repo.providersByID[provider.ID].Schedulable)
	require.NotNil(t, repo.providersByID[provider.ID].RateLimitResetAt)
	require.Equal(t, 1, scheduler.setProviderCalls)
	require.NotNil(t, scheduler.lastProvider)
	require.Equal(t, providercore.StatusDisabled, scheduler.lastProvider.Status)
	require.False(t, scheduler.lastProvider.Schedulable)
	require.NotNil(t, scheduler.lastProvider.RateLimitResetAt,
		"post-refresh cache publication must preserve the durable concurrent exclusion state")
}

func TestPathA_GrokCancelAfterSuccessCASUsesDetachedDurableStateAndInvalidatesCache(t *testing.T) {
	provider := &providercore.Record{
		ID:          112,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      providercore.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "attempted-access",
			"refresh_token": "attempted-refresh",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	repo := &tokenRefreshProviderRepo{
		cancelOnUpdate:               cancel,
		snapshotReads:                true,
		respectReadContext:           true,
		mutateSchedulingOnSuccessCAS: true,
	}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	scheduler := &tokenRefreshSchedulerCache{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}
	cfg := &providercore.RefreshTuning{MaxRetries: 1}
	svc := newRefreshAttemptFixture(repo, cfg, invalidator, scheduler, nil)
	svc.Attempts.API = newRefreshAPI(repo, cache)
	refresher := &tokenRefresherStub{credentials: map[string]any{
		"access_token":  "provider-access",
		"refresh_token": "provider-refresh",
	}}

	err := svc.Attempts.Run(ctx, provider, refresher, refresher, time.Hour, nil)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, repo.conditionalSuccessCalls)
	require.Equal(t, "provider-refresh", repo.providersByID[provider.ID].GetGrokRefreshToken())
	require.Equal(t, 1, cache.deleteCalls)
	require.NoError(t, cache.deleteCtxErr)
	require.Equal(t, 1, invalidator.calls, "the pre-rotation access-token cache must be invalidated after committed CAS")
	require.NoError(t, invalidator.ctxErr)
	require.NotNil(t, invalidator.lastProvider)
	require.Equal(t, "provider-refresh", invalidator.lastProvider.GetGrokRefreshToken())
	require.Equal(t, providercore.StatusDisabled, invalidator.lastProvider.Status)
	require.Equal(t, 1, scheduler.setProviderCalls)
	require.NoError(t, scheduler.ctxErr)
	require.NotNil(t, scheduler.lastProvider)
	require.Equal(t, providercore.StatusDisabled, scheduler.lastProvider.Status)
	require.False(t, scheduler.lastProvider.Schedulable)
	require.NotNil(t, scheduler.lastProvider.RateLimitResetAt)
}

func TestTokenRefreshService_PersistedSuccessCrossingAttemptDeadlineStaysSuccessful(t *testing.T) {
	provider := &providercore.Record{
		ID:          113,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      providercore.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "attempted-access",
			"refresh_token": "attempted-refresh",
		},
	}
	repo := &tokenRefreshProviderRepo{
		snapshotReads:    true,
		durableReadDelay: 30 * time.Millisecond,
	}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	scheduler := &tokenRefreshSchedulerCache{}
	svc := newRefreshAttemptFixture(repo, &providercore.RefreshTuning{MaxRetries: 1, ProviderFailureThreshold: 1}, nil, scheduler, nil)
	svc.Attempts.API = newRefreshAPI(repo, nil)
	svc.Attempts.AttemptTimeout = 10 * time.Millisecond
	refresher := &tokenRefresherStub{credentials: map[string]any{
		"access_token":  "provider-access",
		"refresh_token": "provider-refresh",
	}}
	state := providercore.NewRefreshProviderState(providercore.NewRefreshRateGate(10000), providercore.NewRefreshConcurrencyGate(1), 1, IsNonRetryableRefreshError)

	err := svc.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, state)
	state.RecordResult(err)

	require.NoError(t, err)
	require.Equal(t, 1, refresher.calls, "durably persisted success must not retry after only the internal attempt deadline elapsed")
	require.Equal(t, 1, repo.conditionalSuccessCalls)
	require.Zero(t, repo.conditionalTempCalls)
	require.Zero(t, repo.setTempUnschedCalls)
	require.False(t, state.IsTripped(), "a durable success must not count toward the provider breaker")
	require.Equal(t, "provider-refresh", repo.providersByID[provider.ID].GetGrokRefreshToken())
	require.Equal(t, 1, scheduler.setProviderCalls)
}

func TestPathA_ParentCancellationAfterPersistStillSynchronizesCacheState(t *testing.T) {
	provider := &providercore.Record{
		ID:       109,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	ctx, cancel := context.WithCancel(context.Background())
	repo := &tokenRefreshProviderRepo{cancelOnUpdate: cancel}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	scheduler := &tokenRefreshSchedulerCache{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}
	service, refresher := buildPathAService(repo, cache, invalidator)
	service.Post.SyncProvider = scheduler.SetProvider

	err := service.Attempts.Run(ctx, provider, refresher, refresher, time.Hour, nil)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, repo.updateCredentialsCalls, "credentials were durably persisted before cancellation")
	require.Equal(t, 1, invalidator.calls)
	require.NoError(t, invalidator.ctxErr, "post-persist invalidation must use bounded cleanup context")
	require.Equal(t, 1, scheduler.setProviderCalls)
	require.NoError(t, scheduler.ctxErr, "scheduler sync must use bounded cleanup context")
}

// TestPathA_LockHeld returns ErrRefreshSkipped when another worker owns the lock.
func TestPathA_LockHeld(t *testing.T) {
	provider := &providercore.Record{
		ID:       101,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	repo := &tokenRefreshProviderRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: false} // lock held by another worker

	service, refresher := buildPathAService(repo, cache, invalidator)

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.ErrorIs(t, err, providercore.ErrRefreshSkipped)
	require.Equal(t, 0, repo.updateCalls)  // No credentials to persist after a skipped refresh.
	require.Equal(t, 0, invalidator.calls) // A skipped refresh leaves the cached token unchanged.
}

// TestPathA_AlreadyRefreshed returns ErrRefreshSkipped when the second freshness check passes.
func TestPathA_AlreadyRefreshed(t *testing.T) {
	// A false NeedsRefresh result produces Refreshed=false.
	provider := &providercore.Record{
		ID:       102,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}

	service, _ := buildPathAService(repo, cache, invalidator)

	noRefreshNeeded := &tokenRefresherStub{
		credentials: map[string]any{"access_token": "token"},
	}
	// Use a separate freshness policy from the exchange stub.
	alwaysFreshStub := &alwaysFreshRefresherStub{}

	err := service.Attempts.Run(context.Background(), provider, noRefreshNeeded, alwaysFreshStub, time.Hour, nil)
	require.ErrorIs(t, err, providercore.ErrRefreshSkipped)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls)
}

// TestPathA_NonRetryableError checks that the coordinator marks permanent failures.
func TestPathA_NonRetryableError(t *testing.T) {
	provider := &providercore.Record{
		ID:       103,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}

	service, _ := buildPathAService(repo, cache, invalidator)

	refresher := &tokenRefresherStub{
		err: errors.New("invalid_grant: token revoked"),
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 1, repo.setErrorCalls) // provider marked erroneous
	require.Equal(t, 0, repo.updateCalls)   // credentials unchanged
	require.Equal(t, 1, invalidator.calls)  // Invalidate the old cached token after permanent credential failure.
}

// TestPathA_RetryableErrorExhausted keeps retryable failures out of permanent error state.
func TestPathA_RetryableErrorExhausted(t *testing.T) {
	provider := &providercore.Record{
		ID:       104,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}

	cfg := &providercore.RefreshTuning{
		MaxRetries:          2,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	refreshAPI := newRefreshAPI(repo, cache)
	service.Attempts.API = refreshAPI

	refresher := &tokenRefresherStub{
		err: errors.New("network timeout"),
	}

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.Equal(t, 0, repo.setErrorCalls) // retryable failure
	require.Equal(t, 0, repo.updateCalls)   // credentials unchanged
	require.Equal(t, 0, invalidator.calls)  // cached token unchanged
}

func TestPathA_GrokPermanentFailureCASLetsConcurrentProviderRepairWin(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*tokenRefreshProviderRepo)
		assert    func(*testing.T, *providercore.Record)
	}{
		{
			name: "credential reauthorization",
			configure: func(repo *tokenRefreshProviderRepo) {
				repo.reauthorizeOnErrorCAS = true
			},
			assert: func(t *testing.T, provider *providercore.Record) {
				require.Equal(t, "fresh-refresh", provider.GetGrokRefreshToken())
			},
		},
		{
			name: "proxy repair",
			configure: func(repo *tokenRefreshProviderRepo) {
				repo.repairProxyOnErrorCAS = true
			},
			assert: func(t *testing.T, provider *providercore.Record) {
				require.NotNil(t, provider.ProxyID)
				require.Equal(t, int64(902), *provider.ProxyID)
				require.Equal(t, "attempted-refresh", provider.GetGrokRefreshToken(),
					"proxy-only repair must prove the proxy fingerprint independently of credentials")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyID := int64(901)
			provider := &providercore.Record{
				ID:          120,
				Platform:    capability.PlatformGrok,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				ProxyID:     &proxyID,
				Credentials: map[string]any{
					"access_token":   "attempted-access",
					"refresh_token":  "attempted-refresh",
					"_token_version": int64(1),
				},
			}
			repo := &tokenRefreshProviderRepo{}
			repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
			tt.configure(repo)
			invalidator := &tokenCacheInvalidatorStub{}
			cache := &mockTokenCacheForRefreshAPI{lockResult: true}
			service, _ := buildPathAService(repo, cache, invalidator)
			blocker := &tokenRefreshRuntimeBlocker{}
			service.Attempts.PrepareFailure = func(v *providercore.Record) func(time.Time, string) {
				return providercore.PrepareRefreshFailureNotice(blocker, v)
			}
			refresher := &tokenRefresherStub{err: errors.New("invalid_grant: revoked")}

			err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)

			require.ErrorIs(t, err, providercore.ErrRefreshSkipped)
			require.Equal(t, 1, repo.conditionalErrorCalls)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, blocker.blockCalls)
			require.Zero(t, invalidator.calls, "a stale permanent failure must not invalidate newly repaired credentials")
			require.Equal(t, providercore.StatusActive, provider.Status)
			require.True(t, provider.Schedulable)
			tt.assert(t, provider)
		})
	}
}

func TestPathA_GrokTransientFailureCASLetsConcurrentProviderRepairWin(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*tokenRefreshProviderRepo)
		assert    func(*testing.T, *providercore.Record)
	}{
		{
			name: "credential reauthorization",
			configure: func(repo *tokenRefreshProviderRepo) {
				repo.reauthorizeOnTempCAS = true
			},
			assert: func(t *testing.T, provider *providercore.Record) {
				require.Equal(t, "fresh-refresh", provider.GetGrokRefreshToken())
			},
		},
		{
			name: "proxy repair",
			configure: func(repo *tokenRefreshProviderRepo) {
				repo.repairProxyOnTempCAS = true
			},
			assert: func(t *testing.T, provider *providercore.Record) {
				require.NotNil(t, provider.ProxyID)
				require.Equal(t, int64(902), *provider.ProxyID)
				require.Equal(t, "attempted-refresh", provider.GetGrokRefreshToken(),
					"proxy-only repair must prove the proxy fingerprint independently of credentials")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyID := int64(901)
			provider := &providercore.Record{
				ID:          121,
				Platform:    capability.PlatformGrok,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				ProxyID:     &proxyID,
				Credentials: map[string]any{
					"access_token":   "attempted-access",
					"refresh_token":  "attempted-refresh",
					"_token_version": int64(1),
				},
			}
			repo := &tokenRefreshProviderRepo{}
			repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
			tt.configure(repo)
			invalidator := &tokenCacheInvalidatorStub{}
			cache := &mockTokenCacheForRefreshAPI{lockResult: true}
			service, _ := buildPathAService(repo, cache, invalidator)
			blocker := &tokenRefreshRuntimeBlocker{}
			service.Attempts.PrepareFailure = func(v *providercore.Record) func(time.Time, string) {
				return providercore.PrepareRefreshFailureNotice(blocker, v)
			}
			refresher := &tokenRefresherStub{err: errors.New("temporary provider timeout")}

			err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)

			require.ErrorIs(t, err, providercore.ErrRefreshSkipped)
			require.Equal(t, 1, repo.conditionalTempCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			require.Zero(t, blocker.blockCalls)
			require.Equal(t, providercore.StatusActive, provider.Status)
			require.True(t, provider.Schedulable)
			require.Nil(t, provider.TempUnschedulableUntil)
			tt.assert(t, provider)
		})
	}
}

func TestTokenRefreshService_GrokMissingConditionalMutationContractContainsProviderCycle(t *testing.T) {
	tests := []struct {
		name       string
		refreshErr error
	}{
		{name: "permanent failure", refreshErr: errors.New("invalid_grant: revoked")},
		{name: "transient failure", refreshErr: errors.New("temporary provider timeout")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newRefreshAttemptFixture(&tokenRefreshProviderRepo{}, &providercore.RefreshTuning{MaxRetries: 1}, nil, nil, nil)
			svc.Attempts.GrokMutation = nil
			provider := &providercore.Record{
				ID:          122,
				Platform:    capability.PlatformGrok,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "attempted"},
			}
			refresher := &tokenRefresherStub{err: tt.refreshErr}

			err := svc.Attempts.Run(context.Background(), provider, refresher, nil, time.Hour, nil)

			var providerErr *providercore.ProviderConfigurationRefreshError
			require.ErrorAs(t, err, &providerErr)
			state := providercore.NewRefreshProviderState(nil, nil, svc.Attempts.Tuning.FailureThreshold(), IsNonRetryableRefreshError)
			state.RecordResult(err)
			require.True(t, state.IsTripped(), "a missing safety contract must stop the provider cycle")
			require.Equal(t, providercore.StatusActive, provider.Status)
			require.True(t, provider.Schedulable)
		})
	}
}

func TestTokenRefreshService_GrokConditionalMutationErrorsContainProviderCycle(t *testing.T) {
	tests := []struct {
		name             string
		upstreamErr      error
		configureRepo    func(*tokenRefreshProviderRepo, error)
		expectedCASCalls func(*tokenRefreshProviderRepo) int
	}{
		{
			name:        "permanent failure",
			upstreamErr: errors.New("invalid_grant: revoked"),
			configureRepo: func(repo *tokenRefreshProviderRepo, casErr error) {
				repo.conditionalErrorErr = casErr
			},
			expectedCASCalls: func(repo *tokenRefreshProviderRepo) int { return repo.conditionalErrorCalls },
		},
		{
			name:        "transient failure",
			upstreamErr: errors.New("temporary provider timeout"),
			configureRepo: func(repo *tokenRefreshProviderRepo, casErr error) {
				repo.conditionalTempErr = casErr
			},
			expectedCASCalls: func(repo *tokenRefreshProviderRepo) int { return repo.conditionalTempCalls },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &providercore.Record{
				ID:          123,
				Platform:    capability.PlatformGrok,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "attempted"},
			}
			casErr := errors.New("conditional provider mutation unavailable")
			repo := &tokenRefreshProviderRepo{}
			repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
			tt.configureRepo(repo, casErr)
			invalidator := &tokenCacheInvalidatorStub{}
			blocker := &tokenRefreshRuntimeBlocker{}
			svc := newRefreshAttemptFixture(repo, &providercore.RefreshTuning{MaxRetries: 1}, invalidator, nil, nil)
			svc.Attempts.PrepareFailure = func(v *providercore.Record) func(time.Time, string) {
				return providercore.PrepareRefreshFailureNotice(blocker, v)
			}
			refresher := &tokenRefresherStub{err: tt.upstreamErr}

			err := svc.Attempts.Run(context.Background(), provider, refresher, nil, time.Hour, nil)

			var containmentErr *providercore.ProviderCycleContainmentRefreshError
			require.ErrorAs(t, err, &containmentErr)
			require.ErrorIs(t, err, casErr)
			require.NotErrorIs(t, err, tt.upstreamErr, "a CAS execution failure must replace the stale upstream classification")
			var permanentErr *providercore.ProviderPermanentRefreshError
			require.False(t, errors.As(err, &permanentErr))
			require.Equal(t, 1, tt.expectedCASCalls(repo))

			state := providercore.NewRefreshProviderState(nil, nil, svc.Attempts.Tuning.FailureThreshold(), IsNonRetryableRefreshError)
			state.RecordResult(err)
			require.True(t, state.IsTripped(), "an unsafe mutation result must stop the provider cycle immediately")
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setTempUnschedCalls)
			require.Zero(t, blocker.blockCalls)
			require.Zero(t, invalidator.calls)
			require.Equal(t, providercore.StatusActive, provider.Status)
			require.True(t, provider.Schedulable)
		})
	}
}

// TestPathA_DBUpdateFailed returns a persistence error and skips post-refresh actions.
func TestPathA_DBUpdateFailed(t *testing.T) {
	provider := &providercore.Record{
		ID:       105,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
	}
	repo := &tokenRefreshProviderRepo{updateErr: errors.New("db connection lost")}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	invalidator := &tokenCacheInvalidatorStub{}
	cache := &mockTokenCacheForRefreshAPI{lockResult: true}

	service, refresher := buildPathAService(repo, cache, invalidator)

	err := service.Attempts.Run(context.Background(), provider, refresher, refresher, time.Hour, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, providercore.ErrRefreshCredentialPersist)
	require.Equal(t, 1, repo.updateCalls)  // persistence attempted
	require.Equal(t, 0, invalidator.calls) // Keep the cached token after persistence fails.
}

// TestIsNonRetryableRefreshError checks permanent refresh error classification.
func TestIsNonRetryableRefreshError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "nil_error", err: nil, expected: false},
		{name: "network_error", err: errors.New("network timeout"), expected: false},
		{name: "invalid_grant", err: errors.New("invalid_grant"), expected: true},
		{name: "invalid_client", err: errors.New("invalid_client"), expected: true},
		{name: "invalid_refresh_token", err: errors.New(`OPENAI_OAUTH_TOKEN_REFRESH_FAILED: token refresh failed: status 401, body: {"error":{"code":"invalid_refresh_token"}}`), expected: true},
		{name: "token_expired", err: errors.New(`OPENAI_OAUTH_TOKEN_REFRESH_FAILED: token refresh failed: status 401, body: {"error":{"code":"token_expired"}}`), expected: true},
		{name: "refresh_token_reused", err: errors.New(`OPENAI_OAUTH_TOKEN_REFRESH_FAILED: token refresh failed: status 401, body: {"error":{"code":"refresh_token_reused"}}`), expected: true},
		{name: "refresh_token_invalidated", err: errors.New(`OPENAI_OAUTH_TOKEN_REFRESH_FAILED: token refresh failed: status 401, body: {"error":{"code":"refresh_token_invalidated"}}`), expected: true},
		{name: "app_session_terminated", err: errors.New(`OPENAI_OAUTH_TOKEN_REFRESH_FAILED: token refresh failed: status 401, body: {"error":{"code":"app_session_terminated"}}`), expected: true},
		{name: "unauthorized_client", err: errors.New("unauthorized_client"), expected: true},
		{name: "access_denied", err: errors.New("access_denied"), expected: true},
		{name: "no_refresh_token", err: errors.New("no refresh token available"), expected: true},
		{name: "grok_entitlement_denied", err: errors.New("GROK_OAUTH_ENTITLEMENT_DENIED: subscription required"), expected: true},
		{name: "invalid_scope", err: errors.New("invalid_scope: requested scope is not allowed"), expected: true},
		{name: "invalid_grant_with_desc", err: errors.New("Error: invalid_grant - token revoked"), expected: true},
		{name: "case_insensitive", err: errors.New("INVALID_GRANT"), expected: true},
		{name: "qoder_refresh_unauthorized", err: fmt.Errorf("refresh failed: %w", &qoder.OpenAPIError{Operation: "token refresh", StatusCode: 401}), expected: true},
		{name: "qoder_refresh_upstream_failure", err: &qoder.OpenAPIError{Operation: "token refresh", StatusCode: 503}, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsNonRetryableRefreshError(tt.err)
			require.Equal(t, tt.expected, result)
		})
	}
	require.False(t, IsSharedProviderRefreshError(&qoder.OpenAPIError{
		Operation:  "token refresh",
		StatusCode: 400,
		Message:    "invalid_scope",
	}))
}

func TestTokenRefreshService_ProcessRefreshPagesByStableCursor(t *testing.T) {
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{
		0: {grokPoolProvider(1), grokPoolProvider(2)},
		2: {grokPoolProvider(3)},
	}}
	refresher := &poolHealthRefresher{}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		RefreshBeforeExpiryHours: 1,
		MaxRetries:               1,
		CandidatePageSize:        2,
		ProviderConcurrency:      4,
		ProviderQPS:              10000,
		AttemptTimeoutSeconds:    1,
		CycleTimeoutSeconds:      2,
	})

	runtime := providercore.NewBackgroundRefreshService(*svc)
	runtime.ScanCycle(context.Background())

	requests, updatedIDs, _, _ := repo.snapshot()
	require.Len(t, requests, 2)
	require.Equal(t, int64(0), requests[0].AfterID)
	require.Equal(t, int64(2), requests[1].AfterID)
	require.Equal(t, []string{capability.PlatformGrok}, requests[0].Platforms)
	require.True(t, requests[0].ActiveOnly)
	require.True(t, requests[0].RequireRefreshToken)
	require.True(t, requests[0].ExcludeRetryCooldown)
	slices.Sort(updatedIDs)
	require.Equal(t, []int64{1, 2, 3}, updatedIDs)
	require.Zero(t, runtime.CandidatePosition(), "a short final page must wrap the next cycle to the beginning")
}

func TestTokenRefreshService_BoundsPerProviderConcurrency(t *testing.T) {
	providers := make([]providercore.Record, 0, 8)
	for id := int64(1); id <= 8; id++ {
		providers = append(providers, grokPoolProvider(id))
	}
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{0: providers}}
	refresher := &poolHealthRefresher{delay: 20 * time.Millisecond}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:            1,
		CandidatePageSize:     20,
		ProviderConcurrency:   2,
		ProviderQPS:           10000,
		AttemptTimeoutSeconds: 1,
		CycleTimeoutSeconds:   2,
	})

	providercore.NewBackgroundRefreshService(*svc).ScanCycle(context.Background())

	require.Equal(t, int64(8), refresher.calls.Load())
	require.Equal(t, int64(2), refresher.maxActive.Load())
}

func TestTokenRefreshRateGate_ReservesSpacedSlotsAndHonorsCancellation(t *testing.T) {
	const interval = 25 * time.Millisecond
	gate := providercore.NewRefreshRateGateWithInterval(interval)
	base := time.Unix(1_700_000_000, 0)

	require.Equal(t, base, gate.ReserveSlot(base))
	require.Equal(t, base.Add(interval), gate.ReserveSlot(base))
	require.Equal(t, base.Add(2*interval), gate.ReserveSlot(base))
	jumped := base.Add(time.Second)
	require.Equal(t, jumped, gate.ReserveSlot(jumped), "an idle gate should not retain stale delay")

	cancelGate := providercore.NewRefreshRateGateWithInterval(time.Hour)
	require.NoError(t, cancelGate.Wait(context.Background()), "the first slot is immediately available")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	require.ErrorIs(t, cancelGate.Wait(ctx), context.Canceled)
	require.Less(t, time.Since(started), 100*time.Millisecond, "cancellation must not wait for the reserved slot")
}

func TestTokenRefreshService_RetriesAcquireRateSlotPerAttempt(t *testing.T) {
	repo := &poolHealthProviderRepo{}
	refresher := &poolHealthRefresher{err: errors.New("temporary provider failure")}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{MaxRetries: 3})
	gate := &countingRefreshAttemptGate{}
	provider := grokPoolProvider(44)

	err := svc.Attempts.Run(context.Background(), &provider, refresher, nil, time.Hour, gate)

	require.Error(t, err)
	require.Equal(t, int64(3), refresher.calls.Load())
	require.Equal(t, int64(3), gate.calls.Load(), "every upstream retry must consume a provider rate slot")
}

func TestTokenRefreshService_ProcessProviderProvidersLegacyNilReleaseGateIsSafe(t *testing.T) {
	repo := &poolHealthProviderRepo{}
	refresher := &poolHealthRefresher{}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:          1,
		ProviderConcurrency: 1,
	})
	// Admission rejection has no release callback; direct refresh must propagate the skip without an exchange.
	gate := providercore.NewRefreshProviderState(&rejectedRefreshAttemptGate{err: providercore.ErrRefreshSkipped}, nil, svc.Tuning.FailureThreshold(), IsNonRetryableRefreshError)
	state := &providercore.RefreshProviderExecution{
		Platform: capability.PlatformGrok, State: gate,
		CanRefresh: refresher.CanRefresh, NeedsRefresh: refresher.NeedsRefresh,
		Execute: func(ctx context.Context, value *providercore.Record, window time.Duration, state *providercore.RefreshProviderState) error {
			return svc.Attempts.Run(ctx, value, refresher, nil, window, state)
		},
	}
	provider := grokPoolProvider(45)

	refreshed, skipped, failed := (providercore.RefreshPageProcessor{Concurrency: svc.Tuning.Concurrency(), Info: func(string, ...any) {}, Warn: func(string, ...any) {}}).ProcessProvider(
		context.Background(),
		state,
		[]*providercore.Record{&provider},
		time.Hour,
	)

	require.Zero(t, refreshed)
	require.Equal(t, 1, skipped)
	require.Zero(t, failed)
	require.Zero(t, refresher.calls.Load(), "rejected rate admission must not reach the legacy upstream refresher")
}

func TestTokenRefreshService_ProviderRateGateIsSharedAcrossRuns(t *testing.T) {
	runtime := providercore.NewBackgroundRefreshService(providercore.BackgroundRefreshOptions{Tuning: &providercore.RefreshTuning{ProviderQPS: 40}})
	first := runtime.ProviderRateGate(capability.PlatformGrok)
	second := runtime.ProviderRateGate(capability.PlatformGrok)
	require.Same(t, first, second, "background cycles and reconciliation must share the process-local provider limiter")

	base := time.Unix(1_700_000_000, 0)
	require.Equal(t, base, first.ReserveSlot(base))
	require.Equal(t, base.Add(25*time.Millisecond), second.ReserveSlot(base))
}

func TestTokenRefreshService_ProviderConcurrencyGateIsSharedAcrossBackgroundAndConcurrentAdminReconciliation(t *testing.T) {
	providers := []providercore.Record{
		grokPoolProvider(1),
		grokPoolProvider(2),
		grokPoolProvider(3),
		grokPoolProvider(4),
	}
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{0: providers}}
	refresher := &poolHealthRefresher{delay: 80 * time.Millisecond}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		RefreshBeforeExpiryHours: 1,
		MaxRetries:               1,
		CandidatePageSize:        20,
		ProviderConcurrency:      2,
		ProviderQPS:              100,
		ProviderFailureThreshold: 20,
		AttemptTimeoutSeconds:    1,
		CycleTimeoutSeconds:      3,
	})

	runtime := providercore.NewBackgroundRefreshService(*svc)
	firstGate := runtime.ProviderConcurrencyGate(capability.PlatformGrok)
	require.Same(t, firstGate, runtime.ProviderConcurrencyGate(capability.PlatformGrok))

	start := make(chan struct{})
	adminErrors := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		runtime.ScanCycle(context.Background())
	}()
	for range 2 {
		go func() {
			defer wg.Done()
			<-start
			_, err := runtime.ReconcileGrokOAuth(context.Background(), providercore.GrokOAuthReconcileInput{Apply: true, Limit: 20})
			adminErrors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(adminErrors)

	for err := range adminErrors {
		require.NoError(t, err)
	}
	require.Equal(t, int64(12), refresher.calls.Load(), "background and both admin calls must all execute")
	require.Equal(t, int64(2), refresher.maxActive.Load(),
		"all entry points must share the configured per-provider upstream concurrency cap")
}

func TestTokenRefreshService_SaturatedProviderPreservesConcurrencyAndActualQPSStartSpacing(t *testing.T) {
	const (
		providerConcurrency = 2
		providerQPS         = 20
		attemptCount        = 8
	)
	repo := &poolHealthProviderRepo{}
	refresher := &poolHealthRefresher{
		// The first two QPS-spaced attempts finish together, exposing stale rate reservations made before capacity
		// becomes available.
		startDelays: []time.Duration{
			220 * time.Millisecond,
			170 * time.Millisecond,
			20 * time.Millisecond,
			20 * time.Millisecond,
			20 * time.Millisecond,
			20 * time.Millisecond,
			20 * time.Millisecond,
			20 * time.Millisecond,
		},
	}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:            1,
		ProviderConcurrency:   providerConcurrency,
		ProviderQPS:           providerQPS,
		AttemptTimeoutSeconds: 1,
	})
	runtime := providercore.NewBackgroundRefreshService(*svc)
	sharedRateGate := runtime.ProviderRateGate(capability.PlatformGrok)
	sharedPoolGate := runtime.ProviderConcurrencyGate(capability.PlatformGrok)

	start := make(chan struct{})
	errorsCh := make(chan error, attemptCount)
	var wg sync.WaitGroup
	for i := range attemptCount {
		provider := grokPoolProvider(int64(i + 1))
		state := providercore.NewRefreshProviderState(sharedRateGate, sharedPoolGate, svc.Tuning.FailureThreshold(), IsNonRetryableRefreshError)
		wg.Go(func() {
			<-start
			errorsCh <- svc.Attempts.Run(context.Background(), &provider, refresher, nil, time.Hour, state)
		})
	}
	close(start)
	wg.Wait()
	close(errorsCh)

	for err := range errorsCh {
		require.NoError(t, err)
	}
	require.Equal(t, int64(providerConcurrency), refresher.maxActive.Load(),
		"the scripted attempts must actually saturate the provider semaphore")
	starts := refresher.startsSnapshot()
	require.Len(t, starts, attemptCount)
	configuredSpacing := time.Second / time.Duration(providerQPS)

	// Timestamps are recorded after admission; scheduling delays can compress observed intervals.
	// One tenth of the configured interval distinguishes 50ms throttling from microsecond starts
	// while tolerating scheduling jitter. Total elapsed time would be a weak assertion because
	// refresh duration under providerConcurrency can make it pass even with rate limiting disabled.
	minimumObservedSpacing := configuredSpacing / 10
	actualMinimumSpacing := starts[1].Sub(starts[0])
	for i := 1; i < len(starts); i++ {
		spacing := starts[i].Sub(starts[i-1])
		if spacing < actualMinimumSpacing {
			actualMinimumSpacing = spacing
		}
		require.GreaterOrEqualf(t, spacing, minimumObservedSpacing,
			"upstream starts %d and %d violated configured QPS spacing", i-1, i)
	}
	t.Logf("max_active=%d configured_concurrency=%d minimum_start_spacing=%s configured_spacing=%s",
		refresher.maxActive.Load(), providerConcurrency, actualMinimumSpacing, configuredSpacing)
}

func TestTokenRefreshService_ProductionPathRatesOnlyActualRefreshAfterSameProviderContention(t *testing.T) {
	const interval = 200 * time.Millisecond
	providerOne := grokPoolProvider(71)
	providerOne.Credentials["needs_refresh"] = true
	providerTwo := grokPoolProvider(72)
	providerTwo.Credentials["needs_refresh"] = true
	firstSelection := providercore.CloneRecord(&providerOne)
	contendingSelection := providercore.CloneRecord(&providerOne)
	differentSelection := providercore.CloneRecord(&providerTwo)
	repo := &productionPathRateRepo{providers: map[int64]*providercore.Record{
		providerOne.ID: providercore.CloneRecord(&providerOne),
		providerTwo.ID: providercore.CloneRecord(&providerTwo),
	}}
	executor := &productionPathRateExecutor{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	tuning := &providercore.RefreshTuning{MaxRetries: 1}
	svc := &providercore.BackgroundRefreshOptions{Tuning: tuning, Attempts: backgroundAttemptOptions(repo, tuning)}
	svc.Attempts.API = refreshAPIForFixture(repo, nil)
	svc.Attempts.AttemptTimeout = 2 * time.Second
	poolGate := providercore.NewRefreshConcurrencyGate(2)
	state := providercore.NewRefreshProviderState(providercore.NewRefreshRateGateWithInterval(interval), poolGate, svc.Tuning.FailureThreshold(), IsNonRetryableRefreshError)

	errorsCh := make(chan error, 3)
	go func() {
		errorsCh <- svc.Attempts.Run(context.Background(), firstSelection, executor, executor, time.Hour, state)
	}()
	select {
	case <-executor.firstStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "first production-path refresh did not reach the upstream executor")
	}

	go func() {
		errorsCh <- svc.Attempts.Run(context.Background(), contendingSelection, executor, executor, time.Hour, state)
	}()
	require.Eventually(t, func() bool {
		return poolGate.InFlight() == 2
	}, time.Second, time.Millisecond, "same-provider contender must hold the second provider slot while waiting on the local refresh lock")

	go func() {
		errorsCh <- svc.Attempts.Run(context.Background(), differentSelection, executor, executor, time.Hour, state)
	}()
	close(executor.releaseFirst)

	skipped := 0
	for range 3 {
		err := <-errorsCh
		if errors.Is(err, providercore.ErrRefreshSkipped) {
			skipped++
			continue
		}
		require.NoError(t, err)
	}
	require.Equal(t, 1, skipped, "the same-provider contender must reread the refreshed row and skip without upstream admission")

	starts := executor.startsSnapshot()
	require.Len(t, starts, 2, "only the two providers that actually refresh may consume QPS admission")
	require.Equal(t, int64(71), starts[0].providerID)
	require.Equal(t, int64(72), starts[1].providerID)
	spacing := starts[1].at.Sub(starts[0].at)
	require.GreaterOrEqual(t, spacing, interval-30*time.Millisecond)
	require.Less(t, spacing, 350*time.Millisecond,
		"a same-provider lock waiter must not consume a rate slot and push the different-provider refresh to the second interval")
	t.Logf("actual_refresh_calls=%d actual_start_spacing=%s configured_spacing=%s", executor.calls.Load(), spacing, interval)
}

func TestTokenRefreshService_ProviderTripBeforeRateAdmissionSkipsWithoutProviderMutation(t *testing.T) {
	provider := grokPoolProvider(73)
	stored := providercore.CloneRecord(&provider)
	repo := &breakerTripProviderRepo{productionPathRateRepo: &productionPathRateRepo{
		providers: map[int64]*providercore.Record{provider.ID: stored},
	}}
	refresher := &poolHealthRefresher{}
	tuning := &providercore.RefreshTuning{MaxRetries: 1}
	svc := &providercore.BackgroundRefreshOptions{Tuning: tuning, Attempts: backgroundAttemptOptions(repo, tuning)}
	svc.Attempts.API = refreshAPIForFixture(repo, nil)
	state := providercore.NewRefreshProviderState(providercore.NewRefreshRateGate(1), providercore.NewRefreshConcurrencyGate(1), svc.Tuning.FailureThreshold(), IsNonRetryableRefreshError)
	gate := &tripBeforeRateAdmissionGate{state: state}

	err := svc.Attempts.Run(context.Background(), &provider, refresher, refresher, time.Hour, gate)

	require.ErrorIs(t, err, providercore.ErrRefreshSkipped)
	require.Zero(t, refresher.calls.Load(), "a tripped provider must not reach upstream rate admission")
	require.Zero(t, repo.setErrorCalls.Load())
	require.Zero(t, repo.setTempCalls.Load(), "provider skip must never fall through to per-provider cooldown")
}

func TestTokenRefreshService_ConfigBounds(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tuning := &providercore.RefreshTuning{
		MaxRetries:               maxInt,
		RetryBackoffSeconds:      maxInt,
		ProviderFailureThreshold: maxInt,
		AttemptTimeoutSeconds:    maxInt,
		CycleTimeoutSeconds:      maxInt,
	}

	require.Equal(t, providercore.MaxTokenRefreshMaxRetries, tuning.Retries())
	require.Equal(t, providercore.MaxTokenRefreshProviderFailureThreshold, tuning.FailureThreshold())
	require.Equal(t, providercore.MaxTokenRefreshAttemptTimeout, tuning.AttemptTimeout(0, 0, false))
	require.Equal(t, providercore.MaxTokenRefreshCycleTimeout, tuning.CycleTimeout())
	require.LessOrEqual(t, tuning.RetryBackoff(1, providercore.MaxTokenRefreshMaxRetries), providercore.MaxTokenRefreshRetryBackoff)
	require.Equal(t, 500, providercore.MaxGrokOAuthReconcilePageSize)
}

func TestTokenRefreshService_AttemptTimeoutStaysInsideDistributedLockLease(t *testing.T) {
	cache := &poolHealthTokenCacheStub{}
	tuning := &providercore.RefreshTuning{AttemptTimeoutSeconds: int(providercore.MaxTokenRefreshAttemptTimeout / time.Second)}
	lease, configured := refreshAPIForFixture(&poolHealthProviderRepo{}, cache).LockLease()
	require.Equal(t, 55*time.Second, tuning.AttemptTimeout(0, lease, configured))
	require.Less(t, tuning.AttemptTimeout(0, lease, configured), time.Minute)
}

func TestTokenRefreshService_SharedProviderFailureContainsCycleWithoutProviderMutation(t *testing.T) {
	providers := make([]providercore.Record, 0, 5)
	for id := int64(1); id <= 5; id++ {
		providers = append(providers, grokPoolProvider(id))
	}
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{0: providers}}
	refresher := &poolHealthRefresher{err: errors.New("invalid_client: provider configuration rejected")}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      4,
		ProviderQPS:              10000,
		ProviderFailureThreshold: 3,
		AttemptTimeoutSeconds:    1,
		CycleTimeoutSeconds:      2,
	})

	providercore.NewBackgroundRefreshService(*svc).ScanCycle(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, int64(1), refresher.calls.Load(), "shared provider configuration failures must open the in-cycle breaker immediately")
	require.Zero(t, setErrorCalls, "shared provider failures must not mass-disable providers")
	require.Zero(t, setTempUnschedCalls, "shared provider failures must not mutate per-provider scheduling state")
}

func TestTokenRefreshService_SharedDBRereadFailureContainsCycleWithoutProviderMutation(t *testing.T) {
	providers := []providercore.Record{grokPoolProvider(1), grokPoolProvider(2), grokPoolProvider(3)}
	repo := &poolHealthProviderRepo{
		pages:      map[int64][]providercore.Record{0: providers},
		getByIDErr: errors.New("database unavailable"),
	}
	refresher := &poolHealthRefresher{}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:            3,
		CandidatePageSize:     10,
		ProviderConcurrency:   4,
		ProviderQPS:           10000,
		AttemptTimeoutSeconds: 1,
		CycleTimeoutSeconds:   2,
	})
	svc.Attempts.API = refreshAPIForFixture(repo, nil)

	providercore.NewBackgroundRefreshService(*svc).ScanCycle(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Zero(t, refresher.calls.Load(), "refresh must fail closed before using stale provider credentials")
	require.Zero(t, setErrorCalls)
	require.Zero(t, setTempUnschedCalls, "a shared DB outage must not mutate the selected provider")
}

func TestTokenRefreshService_GenericGrokForbiddenContainsCycleWithoutProviderMutation(t *testing.T) {
	providers := []providercore.Record{grokPoolProvider(1), grokPoolProvider(2), grokPoolProvider(3)}
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{0: providers}}
	refresher := &poolHealthRefresher{err: errors.New(`GROK_OAUTH_ENTITLEMENT_DENIED: token refresh failed: status 403, body: <html>request blocked</html>`)}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      4,
		ProviderQPS:              10000,
		ProviderFailureThreshold: 3,
		AttemptTimeoutSeconds:    1,
		CycleTimeoutSeconds:      2,
	})

	providercore.NewBackgroundRefreshService(*svc).ScanCycle(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, int64(1), refresher.calls.Load(), "an ambiguous Grok 403 must contain the provider immediately")
	require.Zero(t, setErrorCalls, "a generic 403 is not evidence that a provider credential is permanently invalid")
	require.Zero(t, setTempUnschedCalls, "provider containment must not mutate provider scheduling state")
}

func TestTokenRefreshService_ExplicitGrokEntitlementDenialIsPermanent(t *testing.T) {
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{0: {grokPoolProvider(1)}}}
	refresher := &poolHealthRefresher{err: errors.New(`GROK_OAUTH_ENTITLEMENT_DENIED: token refresh failed: status 403, body: {"error":"subscription required"}`)}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:            1,
		CandidatePageSize:     10,
		ProviderConcurrency:   1,
		ProviderQPS:           10000,
		AttemptTimeoutSeconds: 1,
		CycleTimeoutSeconds:   2,
	})

	providercore.NewBackgroundRefreshService(*svc).ScanCycle(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, int64(1), refresher.calls.Load())
	require.Equal(t, 1, setErrorCalls, "explicit entitlement evidence is a provider-permanent failure")
	require.Zero(t, setTempUnschedCalls)
}

func TestTokenRefreshService_AttemptTimeoutTripsRetryableProviderThreshold(t *testing.T) {
	providers := []providercore.Record{grokPoolProvider(1), grokPoolProvider(2), grokPoolProvider(3)}
	repo := &poolHealthProviderRepo{pages: map[int64][]providercore.Record{0: providers}}
	refresher := &poolHealthRefresher{delay: time.Second}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      1,
		ProviderQPS:              10000,
		ProviderFailureThreshold: 2,
		CycleTimeoutSeconds:      2,
	})
	svc.Attempts.AttemptTimeout = 20 * time.Millisecond

	providercore.NewBackgroundRefreshService(*svc).ScanCycle(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, int64(2), refresher.calls.Load(), "two attempt timeouts should trip the retryable provider threshold")
	require.Zero(t, setErrorCalls)
	require.Equal(t, 2, setTempUnschedCalls, "attempt timeouts remain provider-transient failures before containment opens")
}

func TestTokenRefreshService_ParentCancellationStopsRetryWithoutProviderMutation(t *testing.T) {
	repo := &poolHealthProviderRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	refresher := &poolHealthRefresher{
		err:    errors.New("temporary provider failure"),
		cancel: cancel,
	}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{
		MaxRetries:            3,
		RetryBackoffSeconds:   1,
		AttemptTimeoutSeconds: 1,
	})
	provider := grokPoolProvider(42)

	err := svc.Attempts.Run(ctx, &provider, refresher, nil, time.Hour, nil)

	require.ErrorIs(t, err, context.Canceled)
	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Zero(t, setErrorCalls)
	require.Zero(t, setTempUnschedCalls)
}

func TestTokenRefreshService_LateSuccessPastAttemptDeadlineIsRejected(t *testing.T) {
	repo := &poolHealthProviderRepo{}
	refresher := &poolHealthRefresher{
		delay:         30 * time.Millisecond,
		ignoreContext: true,
	}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{MaxRetries: 1})
	svc.Attempts.AttemptTimeout = 10 * time.Millisecond
	provider := grokPoolProvider(43)

	err := svc.Attempts.Run(context.Background(), &provider, refresher, nil, time.Hour, nil)

	var timeoutErr *providercore.RefreshAttemptTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	_, updatedIDs, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Empty(t, updatedIDs, "credentials returned after the deadline must not be persisted")
	require.Zero(t, setErrorCalls)
	require.Equal(t, 1, setTempUnschedCalls)
}

func TestTokenRefreshService_NonRetryableGrokFailureInvalidatesTokenCache(t *testing.T) {
	repo := &poolHealthProviderRepo{}
	invalidator := &reconcileInvalidator{}
	refresher := &poolHealthRefresher{err: errors.New("invalid_grant: revoked")}
	svc := newPoolHealthService(repo, refresher, providercore.RefreshTuning{MaxRetries: 1})
	svc.Attempts.Invalidate = invalidator.InvalidateToken
	provider := grokPoolProvider(77)

	err := svc.Attempts.Run(context.Background(), &provider, refresher, nil, time.Hour, nil)

	require.Error(t, err)
	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, 1, setErrorCalls)
	require.Zero(t, setTempUnschedCalls)
	require.Equal(t, 1, invalidator.count())
}

func TestTokenRefreshService_ProcessRefreshUsesOAuthRefreshCandidates(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	repo := &tokenRefreshCandidateRepo{
		providers: []providercore.Record{
			{
				ID:          1,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:          2,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{},
			},
			{
				ID:          3,
				Platform:    capability.PlatformGemini,
				Type:        capability.ProviderTypeAPIKey,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:                      4,
				Platform:                capability.PlatformAntigravity,
				Type:                    capability.ProviderTypeOAuth,
				Status:                  providercore.StatusActive,
				Schedulable:             true,
				Credentials:             map[string]any{"refresh_token": "refresh-token"},
				TempUnschedulableUntil:  &future,
				TempUnschedulableReason: "token refresh retry exhausted: network timeout",
			},
			{
				ID:          5,
				Platform:    "other",
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:          6,
				Platform:    capability.PlatformQoder,
				Type:        capability.ProviderTypeCosy,
				Status:      providercore.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:                      7,
				Platform:                capability.PlatformAntigravity,
				Type:                    capability.ProviderTypeOAuth,
				Status:                  providercore.StatusActive,
				Schedulable:             true,
				Credentials:             map[string]any{"refresh_token": "refresh-token"},
				Extra:                   map[string]any{"privacy_mode": providercore.AntigravityPrivacySet},
				TempUnschedulableUntil:  &future,
				TempUnschedulableReason: "OAuth 401: unauthorized",
			},
			{
				ID:          8,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeOAuth,
				Status:      providercore.StatusActive,
				Schedulable: false,
				Credentials: map[string]any{"refresh_token": "permanently-rejected-token"},
			},
		},
	}
	tuning := &providercore.RefreshTuning{RefreshBeforeExpiryHours: 1, MaxRetries: 1}
	attempts := backgroundAttemptOptions(repo, tuning)
	post := candidatePostActions(repo)
	attempts.PostActions = post.Run
	svc := providercore.NewBackgroundRefreshService(providercore.BackgroundRefreshOptions{
		Tuning: tuning, Pager: repo, Attempts: attempts,
		Registrations: []providercore.RefreshRegistration{
			{Platform: capability.PlatformOpenAI, Refresher: &tokenRefreshTestRefresher{}},
			{Platform: capability.PlatformGemini, Refresher: &tokenRefreshTestRefresher{}},
			{Platform: capability.PlatformAntigravity, Refresher: &tokenRefreshTestRefresher{}},
			{Platform: capability.PlatformQoder, Refresher: &tokenRefreshTestRefresher{}},
		},
	})

	svc.ScanCycle(context.Background())

	require.Zero(t, repo.listActiveCalls, "TokenRefreshService should not use the broad active-provider query")
	require.ElementsMatch(t, []int64{1, 6, 7}, repo.updatedCredentialIDs)
	require.Equal(t, 1, repo.clearTempCalls, "successful refresh should clear the OAuth 401 temp-unschedulable state")
}

func TestTokenRefreshService_RefreshFailureDoesNotCallPrivacy(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "retry exhausted", err: errors.New("temporary upstream timeout")},
		{name: "non retryable", err: errors.New("invalid_grant: token revoked")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &tokenRefreshCandidateRepo{}
			attempts := backgroundAttemptOptions(repo, &providercore.RefreshTuning{MaxRetries: 1, RetryBackoffSeconds: 0})
			post := candidatePostActions(repo)
			post.Privacy = providercore.NewPrivacyService(nil, nil, PrivacyOptions(func(string) (*req.Client, error) {
				t.Fatalf("privacy client factory must not be called on refresh failure")
				return nil, errors.New("unexpected privacy call")
			}, openai.PrivacyEndpoints{}))
			attempts.PostActions = post.Run
			provider := &providercore.Record{
				ID:       11,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					"access_token":  "old-access-token",
					"refresh_token": "refresh-token",
				},
			}

			err := attempts.Run(context.Background(), provider, &tokenRefreshTestRefresher{err: tt.err}, nil, time.Hour, nil)

			require.Error(t, err)
			if IsNonRetryableRefreshError(tt.err) {
				require.Equal(t, 1, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
			} else {
				require.Zero(t, repo.setErrorCalls)
				require.Equal(t, 1, repo.setTempUnschedCalls)
				require.True(t, strings.HasPrefix(repo.lastTempUnschedReason, "token refresh retry exhausted:"))
			}
		})
	}
}

func (*oldFailureRefresher) CanRefresh(*providercore.Record) bool { return true }

func (*oldFailureRefresher) NeedsRefresh(*providercore.Record, time.Duration) bool { return true }

func (*oldFailureRefresher) CacheKey(*providercore.Record) string { return "fixture:old-failure" }

func (r *oldFailureRefresher) Refresh(context.Context, *providercore.Record) (map[string]any, error) {
	r.row.Credentials = map[string]any{"access_token": "fresh-admin-fixture", "refresh_token": "fresh-refresh-fixture"}
	return nil, r.failure
}

func (b *failureBlocker) PrepareRefreshFailure(int64) func(providercore.RefreshFailureNotice) {
	return func(providercore.RefreshFailureNotice) { b.calls++ }
}

// backgroundAttemptOptions binds test dependencies to production refresh attempts and post-refresh cleanup.
func backgroundAttemptOptions(repo providercore.CredentialUpdateStore, tuning *providercore.RefreshTuning) providercore.RefreshAttempts {
	post := &providercore.RefreshPostActions{
		Now: time.Now, Info: slog.Info, Warn: slog.Warn, Debug: slog.Debug,
		ClearBlock: func(int64) {}, NeedsReauth: providercore.GrokNeedsReauth,
		ClearReauth: func(context.Context, *providercore.Record) {},
	}
	failure, _ := repo.(providercore.RefreshFailureWriter)
	grok, _ := repo.(providercore.GrokRefreshMutationWriter)
	return providercore.RefreshAttempts{
		Tuning: tuning, Policy: providercore.DefaultBackgroundRefreshPolicy(),
		AttemptTimeout: tuning.AttemptTimeout(0, 0, false), Now: time.Now,
		Info: slog.Info, Warn: slog.Warn, Error: slog.Error,
		NonRetryable: IsNonRetryableRefreshError, SharedProviderError: IsSharedProviderRefreshError,
		AmbiguousEntitlement: IsAmbiguousGrokEntitlementRefreshError,
		FailureWriter:        failure, GrokMutation: grok,
		PrepareFailure:      func(*providercore.Record) func(time.Time, string) { return func(time.Time, string) {} },
		ClearRefreshRequest: post.ClearRefreshRequest, PostActions: post.Run, SyncCleanup: post.SyncWithCleanup,
		Persist: func(ctx context.Context, value *providercore.Record, credentials map[string]any) error {
			_, err := providercore.PersistCredentials(ctx, repo, value, credentials, slog.Warn)
			return err
		},
	}
}

func refreshAPIForFixture(repo providercore.RefreshRepository, cache providercore.RefreshCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(repo, cache, providercore.RefreshOptions{Platform: providercore.ProviderRefreshPlatformPolicy()})
}

// Update records full-row writes and delegates to the credential-update stub.
func (r *poolHealthProviderRepo) Update(ctx context.Context, value *providercore.Record) error {
	return r.UpdateCredentials(ctx, value.ID, value.Credentials)
}

func (r *productionPathRateRepo) Update(ctx context.Context, value *providercore.Record) error {
	return r.UpdateCredentials(ctx, value.ID, value.Credentials)
}

func (r *grokReconcileRepo) Update(ctx context.Context, value *providercore.Record) error {
	return r.UpdateCredentials(ctx, value.ID, value.Credentials)
}

func (b *reconcileRuntimeBlocker) PrepareRefreshFailure(int64) func(providercore.RefreshFailureNotice) {
	return func(value providercore.RefreshFailureNotice) {
		b.BlockProviderScheduling(&providercore.Record{ID: value.ProviderID}, value.Until, value.Reason)
	}
}

func (r *tokenRefreshCandidateRepo) Update(ctx context.Context, value *providercore.Record) error {
	return r.UpdateCredentials(ctx, value.ID, value.Credentials)
}

func candidatePostActions(repo *tokenRefreshCandidateRepo) *providercore.RefreshPostActions {
	return &providercore.RefreshPostActions{
		Now: time.Now, Info: slog.Info, Warn: slog.Warn, Debug: slog.Debug, ClearBlock: func(int64) {}, NeedsReauth: providercore.GrokNeedsReauth,
		ClearCooldown: func(ctx context.Context, value *providercore.Record) (bool, error) {
			return repo.ClearRefreshCooldownIfUnchanged(ctx, providercore.ObserveRefreshCooldown(value))
		},
	}
}

func (r *tokenRefreshCandidateRepo) ApplyOAuthRefreshFailure(ctx context.Context, version providercore.RefreshFailureVersion, failure providercore.RefreshFailure) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	matched := r.providers == nil
	for i := range r.providers {
		if r.providers[i].ID == version.ID {
			matched = refreshFailureMatchesFixture(&r.providers[i], version)
			break
		}
	}
	if !matched {
		return false, nil
	}
	if failure.Kind == providercore.RefreshFailurePermanent {
		r.setErrorCalls++
	} else {
		r.setTempUnschedCalls++
		r.lastTempUnschedReason = failure.Message
	}
	return true, nil
}

func (r *grokReconcileRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if override, ok := r.getByIDOverrides[id]; ok {
		provider := override
		return &provider, nil
	}
	for i := range r.providers {
		if r.providers[i].ID == id {
			provider := r.providers[i]
			return &provider, nil
		}
	}
	return nil, providercore.ErrProviderNotFound
}

func (r *grokReconcileRepo) ListOAuthRefreshCandidatePage(_ context.Context, options providercore.OAuthRefreshPageOptions) (*providercore.OAuthRefreshCandidatePage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, options)
	if r.pageOverride != nil {
		page := *r.pageOverride
		page.Providers = append([]providercore.Record(nil), r.pageOverride.Providers...)
		return &page, nil
	}
	providers := append([]providercore.Record(nil), r.providers...)
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	page := make([]providercore.Record, 0, options.Limit)
	for _, provider := range providers {
		if provider.ID <= options.AfterID {
			continue
		}
		platformAllowed := slices.Contains(options.Platforms, provider.Platform)
		if !platformAllowed || options.ActiveOnly && provider.Status != providercore.StatusActive {
			continue
		}
		if options.IncludeSetupToken {
			if provider.Type != capability.ProviderTypeOAuth && provider.Type != capability.ProviderTypeSetupToken {
				continue
			}
		} else if provider.Type != capability.ProviderTypeOAuth {
			continue
		}
		if options.RequireRefreshToken && strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
			continue
		}
		page = append(page, provider)
		if len(page) == options.Limit {
			break
		}
	}
	result := &providercore.OAuthRefreshCandidatePage{Providers: page, HasMore: len(page) == options.Limit}
	if len(page) > 0 {
		result.NextAfterID = page[len(page)-1].ID
	}
	return result, nil
}

func (r *grokReconcileRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedCredIDs = append(r.updatedCredIDs, id)
	for i := range r.providers {
		if r.providers[i].ID == id {
			r.providers[i].Credentials = providercore.MergeCredentials(r.providers[i].Credentials, credentials)
		}
	}
	return nil
}

func (r *grokReconcileRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.providers {
		if r.providers[i].ID != id {
			continue
		}
		if r.providers[i].Extra == nil {
			r.providers[i].Extra = make(map[string]any)
		}
		maps.Copy(r.providers[i].Extra, updates)
		break
	}
	return nil
}

func (r *grokReconcileRepo) SetError(_ context.Context, id int64, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorIDs = append(r.setErrorIDs, id)
	r.setErrorMessage = append(r.setErrorMessage, message)
	for i := range r.providers {
		if r.providers[i].ID == id {
			r.providers[i].Status = providercore.StatusError
			r.providers[i].Schedulable = false
			r.providers[i].ErrorMessage = message
		}
	}
	return nil
}

func (r *grokReconcileRepo) SetGrokOAuthErrorIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	message string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conditionalCalls++
	for i := range r.providers {
		provider := &r.providers[i]
		if provider.ID != id {
			continue
		}
		if r.reauthorizeOnCAS {
			r.reauthorizeOnCAS = false
			provider.Credentials = map[string]any{
				"access_token":   "fresh-access",
				"refresh_token":  "fresh-refresh",
				"expires_at":     time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
				"_token_version": int64(2),
			}
		}
		if provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth || provider.Status != providercore.StatusActive ||
			strings.TrimSpace(provider.GetGrokRefreshToken()) != "" || !reflect.DeepEqual(provider.Credentials, expectedCredentials) {
			return false, nil
		}
		r.setErrorIDs = append(r.setErrorIDs, id)
		r.setErrorMessage = append(r.setErrorMessage, message)
		provider.Status = providercore.StatusError
		provider.Schedulable = false
		provider.ErrorMessage = message
		return true, nil
	}
	return false, nil
}

func (r *grokReconcileRepo) SetGrokOAuthRefreshErrorIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	message string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.providers {
		provider := &r.providers[i]
		if provider.ID != id {
			continue
		}
		if r.reauthorizeOnRefreshCAS {
			r.reauthorizeOnRefreshCAS = false
			provider.Credentials = map[string]any{
				"access_token":   "fresh-access",
				"refresh_token":  "fresh-refresh",
				"expires_at":     time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
				"_token_version": int64(3),
			}
		}
		if provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth || provider.Status != providercore.StatusActive ||
			!reflect.DeepEqual(provider.ProxyID, expectedProxyID) ||
			!reflect.DeepEqual(provider.Credentials, expectedCredentials) {
			return false, nil
		}
		r.setErrorIDs = append(r.setErrorIDs, id)
		r.setErrorMessage = append(r.setErrorMessage, message)
		provider.Status = providercore.StatusError
		provider.Schedulable = false
		provider.ErrorMessage = message
		return true, nil
	}
	return false, nil
}

func (r *grokReconcileRepo) SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	until time.Time,
	reason string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.providers {
		provider := &r.providers[i]
		if provider.ID != id {
			continue
		}
		if provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth || provider.Status != providercore.StatusActive ||
			!reflect.DeepEqual(provider.ProxyID, expectedProxyID) ||
			!reflect.DeepEqual(provider.Credentials, expectedCredentials) {
			return false, nil
		}
		provider.TempUnschedulableUntil = &until
		provider.TempUnschedulableReason = reason
		return true, nil
	}
	return false, nil
}

func (r *grokReconcileRepo) snapshot() ([]providercore.OAuthRefreshPageOptions, []int64, []int64, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]providercore.OAuthRefreshPageOptions(nil), r.requests...), append([]int64(nil), r.setErrorIDs...), append([]int64(nil), r.updatedCredIDs...), append([]string(nil), r.setErrorMessage...)
}

func (b *reconcileRuntimeBlocker) BlockProviderScheduling(provider *providercore.Record, _ time.Time, _ string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if provider != nil {
		b.blocked = append(b.blocked, provider.ID)
	}
}

func (b *reconcileRuntimeBlocker) ClearProviderSchedulingBlock(providerID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleared = append(b.cleared, providerID)
}

func (b *reconcileRuntimeBlocker) snapshot() (blocked, cleared []int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.blocked...), append([]int64(nil), b.cleared...)
}

func (i *reconcileInvalidator) InvalidateToken(_ context.Context, provider *providercore.Record) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ids = append(i.ids, provider.ID)
	return i.err
}

func (i *reconcileInvalidator) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.ids)
}

func newGrokReconcileService(repo *grokReconcileRepo, refresher *poolHealthRefresher, invalidator providercore.TokenCacheInvalidator, observers ...providercore.RefreshFailureObserver) *providercore.BackgroundRefreshService {
	tuning := &providercore.RefreshTuning{
		MaxRetries:               1,
		CandidatePageSize:        50,
		ProviderConcurrency:      2,
		ProviderQPS:              100,
		ProviderFailureThreshold: 3,
		AttemptTimeoutSeconds:    1,
	}
	var observer providercore.RefreshFailureObserver
	if len(observers) > 0 {
		observer = observers[0]
	}
	prepare := func(value *providercore.Record) func(time.Time, string) {
		return providercore.PrepareRefreshFailureNotice(observer, value)
	}
	attempts := backgroundAttemptOptions(repo, tuning)
	attempts.PrepareFailure = prepare
	var invalidate func(context.Context, *providercore.Record) error
	if invalidator != nil {
		invalidate = invalidator.InvalidateToken
	}
	attempts.Invalidate = invalidate
	post := &providercore.RefreshPostActions{
		Now: time.Now, Info: func(string, ...any) {}, Warn: func(string, ...any) {}, Debug: func(string, ...any) {}, Invalidate: invalidate, ClearBlock: func(int64) {}, NeedsReauth: providercore.GrokNeedsReauth,
		ClearReauth: func(ctx context.Context, value *providercore.Record) {
			providercore.ClearGrokNeedsReauth(ctx, repo, value.ID)
		},
	}
	attempts.PostActions, attempts.SyncCleanup = post.Run, post.SyncWithCleanup
	return providercore.NewBackgroundRefreshService(providercore.BackgroundRefreshOptions{
		Tuning: tuning, Pager: repo, Attempts: attempts,
		Registrations:  []providercore.RefreshRegistration{{Platform: capability.PlatformGrok, Refresher: refresher, Executor: refresher}},
		Reconciliation: providercore.GrokReconciliationOptions{Reader: repo, ConditionalError: repo, PrepareFailure: prepare, Invalidate: invalidate, Now: time.Now, Skew: providercore.GrokTokenRefreshSkew},
	})
}

func grokReconcileFixtures() []providercore.Record {
	now := time.Now().UTC()
	return []providercore.Record{
		{
			ID:          1,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"access_token": "access-secret"},
		},
		{
			ID:          2,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"refresh_token": "refresh-secret", "expires_at": now.Add(10 * time.Minute).Format(time.RFC3339)},
		},
		{
			ID:          3,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"access_token": "access-secret", "refresh_token": "refresh-secret", "expires_at": now.Add(30 * time.Minute).Format(time.RFC3339)},
		},
		{
			ID:          4,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"access_token": "access-secret", "refresh_token": "refresh-secret", "expires_at": now.Add(4 * time.Hour).Format(time.RFC3339)},
		},
		{
			ID:          5,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"api_key": "api-key-secret"},
		},
	}
}

func (b *tokenRefreshRuntimeBlocker) BlockProviderScheduling(*providercore.Record, time.Time, string) {
	b.blockCalls++
}

func (b *tokenRefreshRuntimeBlocker) ClearProviderSchedulingBlock(int64) {
	b.clearCalls++
}

func (s *tokenCacheInvalidatorStub) InvalidateToken(ctx context.Context, provider *providercore.Record) error {
	s.calls++
	s.ctxErr = ctx.Err()
	s.lastProvider = providercore.CloneRecord(provider)
	return s.err
}

func (s *tokenRefreshSchedulerCache) SetProvider(ctx context.Context, provider *providercore.Record) error {
	s.setProviderCalls++
	s.ctxErr = ctx.Err()
	s.lastProvider = providercore.CloneRecord(provider)
	return nil
}

func (s *tempUnschedCacheStub) SetTempUnsched(ctx context.Context, providerID int64, state *providercore.TempUnschedState) error {
	s.setCalls++
	s.lastState = state
	return nil
}

func (s *tempUnschedCacheStub) GetTempUnsched(ctx context.Context, providerID int64) (*providercore.TempUnschedState, error) {
	return nil, nil
}

func (s *tempUnschedCacheStub) DeleteTempUnsched(ctx context.Context, providerID int64) error {
	s.deleteCalls++
	return nil
}

func (m *mockTokenCacheForRefreshAPI) GetAccessToken(_ context.Context, _ string) (string, error) {
	return "", errors.New("not cached")
}

func (m *mockTokenCacheForRefreshAPI) SetAccessToken(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}

func (m *mockTokenCacheForRefreshAPI) DeleteAccessToken(ctx context.Context, _ string) error {
	m.deleteCalls++
	m.deleteCtxErr = ctx.Err()
	return nil
}

func (m *mockTokenCacheForRefreshAPI) AcquireRefreshLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return m.lockResult, m.lockErr
}

func (m *mockTokenCacheForRefreshAPI) ReleaseRefreshLock(_ context.Context, _ string) error {
	m.releaseCalls++
	return nil
}

// buildPathAService builds a refresh fixture with the shared refresh coordinator.
func buildPathAService(repo *tokenRefreshProviderRepo, cache providercore.AccessTokenCache, invalidator providercore.TokenCacheInvalidator) (*refreshAttemptFixture, *tokenRefresherStub) {
	for _, provider := range repo.providersByID {
		if provider != nil && provider.Status == "" {
			provider.Status = providercore.StatusActive
		}
	}
	cfg := &providercore.RefreshTuning{
		MaxRetries:          1,
		RetryBackoffSeconds: 0,
	}
	service := newRefreshAttemptFixture(repo, cfg, invalidator, nil, nil)
	refreshAPI := newRefreshAPI(repo, cache)
	service.Attempts.API = refreshAPI

	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "refreshed-token",
		},
	}
	return service, refresher
}

func (r *alwaysFreshRefresherStub) CanRefresh(_ *providercore.Record) bool { return true }

func (r *alwaysFreshRefresherStub) NeedsRefresh(_ *providercore.Record, _ time.Duration) bool {
	return false
}

func (r *alwaysFreshRefresherStub) Refresh(_ context.Context, _ *providercore.Record) (map[string]any, error) {
	return nil, errors.New("should not be called")
}

func (r *alwaysFreshRefresherStub) CacheKey(provider *providercore.Record) string {
	return "test:fresh:" + provider.Platform
}

func newRefreshAttemptFixture(repo *tokenRefreshProviderRepo, tuning *providercore.RefreshTuning, invalidator providercore.TokenCacheInvalidator, scheduler *tokenRefreshSchedulerCache, cooldown providercore.TempUnschedCache) *refreshAttemptFixture {
	post := &providercore.RefreshPostActions{
		Now: time.Now, Info: slog.Info, Warn: slog.Warn, Debug: slog.Debug,
		RequestClearer: repo, NeedsReauth: providercore.GrokNeedsReauth,
		ClearBlock: func(int64) {},
		ClearReauth: func(ctx context.Context, value *providercore.Record) {
			providercore.ClearGrokNeedsReauth(ctx, repo, value.ID)
		},
		ClearError: func(context.Context, *providercore.Record) (bool, error) {
			return false, errors.New("refresh error conditional writer is not configured")
		},
		ClearCooldown: func(ctx context.Context, value *providercore.Record) (bool, error) {
			return repo.ClearRefreshCooldownIfUnchanged(ctx, providercore.ObserveRefreshCooldown(value))
		},
	}
	if invalidator != nil {
		post.Invalidate = invalidator.InvalidateToken
	}
	if scheduler != nil {
		post.SyncProvider = scheduler.SetProvider
	}
	if cooldown != nil {
		post.DeleteCooldown = cooldown.DeleteTempUnsched
	}
	return &refreshAttemptFixture{Post: post, Attempts: providercore.RefreshAttempts{
		Tuning: tuning, Policy: providercore.DefaultBackgroundRefreshPolicy(), AttemptTimeout: tuning.AttemptTimeout(0, 0, false),
		Now: time.Now, Info: slog.Info, Warn: slog.Warn, Error: slog.Error,
		NonRetryable: IsNonRetryableRefreshError, SharedProviderError: IsSharedProviderRefreshError,
		AmbiguousEntitlement: IsAmbiguousGrokEntitlementRefreshError,
		FailureWriter:        repo, GrokMutation: repo, Invalidate: post.Invalidate,
		PrepareFailure:      func(*providercore.Record) func(time.Time, string) { return func(time.Time, string) {} },
		ClearRefreshRequest: post.ClearRefreshRequest, PostActions: post.Run, SyncCleanup: post.SyncWithCleanup,
		Persist: func(ctx context.Context, value *providercore.Record, credentials map[string]any) error {
			_, err := providercore.PersistCredentials(ctx, repo, value, credentials, slog.Warn)
			return err
		},
	}}
}

func (r *tokenRefreshCandidateRepo) ClearRefreshCooldownIfUnchanged(_ context.Context, v providercore.RefreshCooldownVersion) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.providers {
		if r.providers[i].ID == v.ID {
			if !reflect.DeepEqual(providercore.ObserveRefreshCooldown(&r.providers[i]), v) {
				return false, nil
			}
			r.clearTempCalls++
			r.providers[i].TempUnschedulableUntil = nil
			r.providers[i].TempUnschedulableReason = ""
			return true, nil
		}
	}
	return false, nil
}

func (b *tokenRefreshRuntimeBlocker) PrepareRefreshFailure(int64) func(providercore.RefreshFailureNotice) {
	return func(providercore.RefreshFailureNotice) { b.blockCalls++ }
}

func (r *poolHealthProviderRepo) GetByID(_ context.Context, _ int64) (*providercore.Record, error) {
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return nil, providercore.ErrProviderNotFound
}

func (r *poolHealthProviderRepo) ListOAuthRefreshCandidatePage(_ context.Context, options providercore.OAuthRefreshPageOptions) (*providercore.OAuthRefreshCandidatePage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, options)
	providers := append([]providercore.Record(nil), r.pages[options.AfterID]...)
	page := &providercore.OAuthRefreshCandidatePage{Providers: providers, HasMore: len(providers) == options.Limit}
	if len(providers) > 0 {
		page.NextAfterID = providers[len(providers)-1].ID
	}
	return page, nil
}

func (r *poolHealthProviderRepo) UpdateCredentials(_ context.Context, id int64, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedCredentialIDs = append(r.updatedCredentialIDs, id)
	return nil
}

func (r *poolHealthProviderRepo) UpdateGrokOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	_ map[string]any,
	_ *int64,
	_ map[string]any,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedCredentialIDs = append(r.updatedCredentialIDs, id)
	return true, nil
}

func (r *poolHealthProviderRepo) SetError(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	return nil
}

func (r *poolHealthProviderRepo) SetGrokOAuthErrorIfCredentialsUnchanged(context.Context, int64, map[string]any, string) (bool, error) {
	return false, nil
}

func (r *poolHealthProviderRepo) SetGrokOAuthRefreshErrorIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	return true, nil
}

func (r *poolHealthProviderRepo) SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, time.Time, string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setTempUnschedCalls++
	return true, nil
}

func (r *poolHealthProviderRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setTempUnschedCalls++
	return nil
}

func (r *poolHealthProviderRepo) snapshot() ([]providercore.OAuthRefreshPageOptions, []int64, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]providercore.OAuthRefreshPageOptions(nil), r.requests...), append([]int64(nil), r.updatedCredentialIDs...), r.setErrorCalls, r.setTempUnschedCalls
}

func (g *tripBeforeRateAdmissionGate) Acquire(ctx context.Context) (func(), error) {
	release, err := g.state.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	g.state.RecordResult(&providercore.ProviderConfigurationRefreshError{Cause: errors.New("fixture provider unavailable")})
	return release, nil
}

func (g *tripBeforeRateAdmissionGate) AcquireRate(ctx context.Context) (func(), error) {
	return g.state.AcquireRate(ctx)
}

func (r *breakerTripProviderRepo) SetGrokOAuthRefreshErrorIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, string) (bool, error) {
	r.setErrorCalls.Add(1)
	return true, nil
}

func (r *breakerTripProviderRepo) SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(context.Context, int64, map[string]any, *int64, time.Time, string) (bool, error) {
	r.setTempCalls.Add(1)
	return true, nil
}

func (g *rejectedRefreshAttemptGate) Acquire(context.Context) (func(), error) {
	return nil, g.err
}

func (r *productionPathRateRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil {
		return nil, providercore.ErrProviderNotFound
	}
	return providercore.CloneRecord(provider), nil
}

func (r *productionPathRateRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil {
		return providercore.ErrProviderNotFound
	}
	provider.Credentials = maps.Clone(credentials)
	return nil
}

func (r *productionPathRateRepo) UpdateGrokOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil || !reflect.DeepEqual(provider.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	provider.Credentials = maps.Clone(credentials)
	return true, nil
}

func (e *productionPathRateExecutor) CacheKey(provider *providercore.Record) string {
	return fmt.Sprintf("production-path-rate:%d", provider.ID)
}

func (e *productionPathRateExecutor) CanRefresh(provider *providercore.Record) bool {
	return provider != nil && provider.IsGrokOAuth()
}

func (e *productionPathRateExecutor) NeedsRefresh(provider *providercore.Record, _ time.Duration) bool {
	needsRefresh, _ := provider.Credentials["needs_refresh"].(bool)
	return needsRefresh
}

func (e *productionPathRateExecutor) Refresh(ctx context.Context, provider *providercore.Record) (map[string]any, error) {
	call := e.calls.Add(1)
	e.startMu.Lock()
	e.starts = append(e.starts, productionPathRefreshStart{providerID: provider.ID, at: time.Now()})
	e.startMu.Unlock()
	if call == 1 {
		close(e.firstStarted)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.releaseFirst:
		}
	}
	return map[string]any{
		"access_token":  fmt.Sprintf("fresh-access-%d", provider.ID),
		"refresh_token": fmt.Sprintf("fresh-refresh-%d", provider.ID),
		"needs_refresh": false,
	}, nil
}

func (e *productionPathRateExecutor) startsSnapshot() []productionPathRefreshStart {
	e.startMu.Lock()
	defer e.startMu.Unlock()
	return append([]productionPathRefreshStart(nil), e.starts...)
}

func (g *countingRefreshAttemptGate) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.calls.Add(1)
	return func() {}, nil
}

func (r *poolHealthRefresher) CacheKey(provider *providercore.Record) string {
	return fmt.Sprintf("pool-health:%d", provider.ID)
}

func (r *poolHealthRefresher) CanRefresh(provider *providercore.Record) bool {
	return provider != nil && provider.Platform == capability.PlatformGrok && provider.Type == capability.ProviderTypeOAuth
}

func (r *poolHealthRefresher) NeedsRefresh(*providercore.Record, time.Duration) bool { return true }

func (r *poolHealthRefresher) Refresh(ctx context.Context, _ *providercore.Record) (map[string]any, error) {
	r.calls.Add(1)
	active := r.active.Add(1)
	defer r.active.Add(-1)
	r.startMu.Lock()
	startIndex := len(r.startTimes)
	r.startTimes = append(r.startTimes, time.Now())
	delay := r.delay
	if startIndex < len(r.startDelays) {
		delay = r.startDelays[startIndex]
	}
	r.startMu.Unlock()
	for {
		maxActive := r.maxActive.Load()
		if active <= maxActive || r.maxActive.CompareAndSwap(maxActive, active) {
			break
		}
	}
	if r.cancel != nil {
		r.cancel()
	}
	if delay > 0 {
		if r.ignoreContext {
			time.Sleep(delay)
		} else {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.newCredentials != nil {
		credentials := make(map[string]any, len(r.newCredentials))
		maps.Copy(credentials, r.newCredentials)
		return credentials, nil
	}
	return map[string]any{"access_token": "new-token", "refresh_token": "new-refresh-token"}, nil
}

func (r *poolHealthRefresher) startsSnapshot() []time.Time {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return append([]time.Time(nil), r.startTimes...)
}

func grokPoolProvider(id int64) providercore.Record {
	return providercore.Record{
		ID:       id,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   providercore.StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
		},
	}
}

func newPoolHealthService(repo *poolHealthProviderRepo, refresher *poolHealthRefresher, cfg providercore.RefreshTuning) *providercore.BackgroundRefreshOptions {
	return &providercore.BackgroundRefreshOptions{
		Tuning: &cfg, Pager: repo,
		Registrations:  []providercore.RefreshRegistration{{Platform: capability.PlatformGrok, Refresher: refresher, Executor: refresher}},
		Attempts:       backgroundAttemptOptions(repo, &cfg),
		Reconciliation: providercore.GrokReconciliationOptions{Reader: repo, ConditionalError: repo, Now: time.Now, Skew: providercore.GrokTokenRefreshSkew},
	}
}

func (r *tokenRefreshCandidateRepo) ListActive(context.Context) ([]providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listActiveCalls++
	return r.providers, nil
}

func (r *tokenRefreshCandidateRepo) ListOAuthRefreshCandidatePage(_ context.Context, options providercore.OAuthRefreshPageOptions) (*providercore.OAuthRefreshCandidatePage, error) {
	candidates := make([]providercore.Record, 0, len(r.providers))
	now := time.Now()
	for _, provider := range r.providers {
		if provider.ID <= options.AfterID {
			continue
		}
		refreshToken, _ := provider.Credentials["refresh_token"].(string)
		inRetryCooldown := provider.TempUnschedulableUntil != nil &&
			provider.TempUnschedulableUntil.After(now) &&
			strings.HasPrefix(provider.TempUnschedulableReason, "token refresh retry exhausted:")
		platformAllowed := slices.Contains(options.Platforms, provider.Platform)
		typeAllowed := provider.Type == capability.ProviderTypeOAuth ||
			(options.IncludeSetupToken && provider.Type == capability.ProviderTypeSetupToken) ||
			provider.IsQoderCosy()
		if (options.ActiveOnly && provider.Status != providercore.StatusActive) ||
			!provider.Schedulable ||
			!typeAllowed ||
			!platformAllowed ||
			(options.RequireRefreshToken && strings.TrimSpace(refreshToken) == "") ||
			(options.ExcludeRetryCooldown && inRetryCooldown) {
			continue
		}
		candidates = append(candidates, provider)
		if len(candidates) == options.Limit {
			break
		}
	}
	page := &providercore.OAuthRefreshCandidatePage{Providers: candidates, HasMore: len(candidates) == options.Limit}
	if len(candidates) > 0 {
		page.NextAfterID = candidates[len(candidates)-1].ID
	}
	return page, nil
}

func (r *tokenRefreshCandidateRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedCredentialIDs = append(r.updatedCredentialIDs, id)
	for i := range r.providers {
		if r.providers[i].ID == id {
			r.providers[i].Credentials = maps.Clone(credentials)
		}
	}
	return nil
}

func (r *tokenRefreshCandidateRepo) SetError(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	return nil
}

func (r *tokenRefreshCandidateRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	return nil
}

func (r *tokenRefreshCandidateRepo) ClearTempUnschedulable(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearTempCalls++
	return nil
}

func (r *tokenRefreshTestRefresher) CanRefresh(*providercore.Record) bool { return true }

func (r *tokenRefreshTestRefresher) NeedsRefresh(*providercore.Record, time.Duration) bool {
	return true
}

func (r *tokenRefreshTestRefresher) Refresh(context.Context, *providercore.Record) (map[string]any, error) {
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{"access_token": "new-access-token", "refresh_token": "new-refresh-token"}, nil
}
