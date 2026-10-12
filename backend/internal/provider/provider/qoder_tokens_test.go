package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

type qoderCenterHTTPUpstreamStub struct {
	proxyURL            string
	providerID          int64
	providerConcurrency int
	statusCode          int
	body                string
	profileSet          bool
	requests            []*http.Request
}

// qoderInvalidationCache records cache deletions for assertions.
type qoderInvalidationCache struct {
	providercore.AccessTokenCache
	deletedKeys []string
}

// TestQoderTokenProviderConcurrent exercises caching during concurrent reads and invalidations.
func TestQoderTokenProviderConcurrent(t *testing.T) {
	tokenSource := &QoderTokenProvider{Core: &qoderSessionState{Sessions: make(map[int64]qoderSessionCacheEntry)}}

	provider := &providercore.Record{
		ID:       12345,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "test_oauth_token",
			"machine_id":           "test_machine_id",
			"uid":                  "test_uid",
		},
	}

	const numGoroutines = 50
	const numRequestsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	ctx := context.Background()
	successCount := sync.Map{}

	for i := range numGoroutines {
		go func(workerID int) {
			defer wg.Done()
			for j := range numRequestsPerGoroutine {
				session, err := tokenSource.GetSession(ctx, provider)
				if err != nil {
					// Invalidation or credential updates discard builds from an earlier generation.
					if errors.Is(err, errQoderSessionBuildInvalidated) {
						successCount.Store(workerID*1000+j, true)
						continue
					}
					t.Logf("worker %d request %d failed: %v", workerID, j, err)
					continue
				}
				if session == nil {
					t.Errorf("worker %d request %d: got nil session", workerID, j)
					continue
				}
				successCount.Store(workerID*1000+j, true)

				// Invalidate while other workers are reading sessions.
				if workerID%3 == 0 && j%5 == 0 {
					tokenSource.Invalidate(provider.ID)
				}
			}
		}(i)
	}

	wg.Wait()

	count := 0
	successCount.Range(func(key, value any) bool {
		count++
		return true
	})

	expectedTotal := numGoroutines * numRequestsPerGoroutine
	successRate := float64(count) / float64(expectedTotal)
	t.Logf("Success rate: %d/%d (%.2f%%)", count, expectedTotal, successRate*100)

	if successRate < 0.8 {
		t.Errorf("success rate too low: %.2f%%, expected >= 80%%", successRate*100)
	}
}

// TestQoderTokenProviderInvalidateRace exercises concurrent GetSession and Invalidate calls.
func TestQoderTokenProviderInvalidateRace(t *testing.T) {
	tokenSource := &QoderTokenProvider{Core: &qoderSessionState{Sessions: make(map[int64]qoderSessionCacheEntry)}}

	provider := &providercore.Record{
		ID:       999,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "token_999",
			"machine_id":           "machine_999",
			"aid":                  "aid_999",
		},
	}

	ctx := context.Background()
	const numIterations = 1000

	// Run repeated session reads concurrently with invalidations.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range numIterations {
			_, _ = tokenSource.GetSession(ctx, provider)
		}
	}()

	go func() {
		defer wg.Done()
		for range numIterations {
			tokenSource.Invalidate(provider.ID)
		}
	}()

	wg.Wait()

	// Completion checks that neither goroutine panics or deadlocks.
	t.Log("GetSession/Invalidate race test completed without panic")
}

func TestQoderTokenProviderBuildsAndCachesDirectSession(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	provider := &providercore.Record{
		ID:       101,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
			"organization_id":      "org-1",
			"organization_name":    "Org 1",
		},
	}

	session1, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.NotNil(t, session1)
	require.Equal(t, "dt-token", session1.Identity.SecurityOauthToken)
	require.Equal(t, "uid-1", session1.Identity.UID)
	require.Equal(t, "uid-1", session1.Identity.AID)
	require.Equal(t, "org-1", session1.Identity.OrganizationID)
	require.Equal(t, "Org 1", session1.Identity.OrganizationName)
	require.Equal(t, "machine-1", session1.Machine.MachineID)

	session2, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Same(t, session1, session2, "session should be cached per provider credentials")

	provider.Credentials["organization_id"] = "org-2"
	session3, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.NotSame(t, session1, session3)
	require.Equal(t, "org-2", session3.Identity.OrganizationID)
}

func TestQoderTokenProviderRejectsUnsupportedCredentialShape(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	provider := &providercore.Record{
		ID:          102,
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"security_oauth_token": "dt-token"},
	}

	_, err := tokenSource.GetSession(context.Background(), provider)
	require.ErrorContains(t, err, "machine_id")
}

func TestQoderTokenProviderRejectsDirectTokenWithoutIdentity(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	provider := &providercore.Record{
		ID:       105,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
		},
	}

	_, err := tokenSource.GetSession(context.Background(), provider)
	require.ErrorContains(t, err, "uid or aid")
}

func TestQoderTokenProviderRejectsCN20RefreshModeOnGlobalSite(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	provider := &providercore.Record{
		ID:       32,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":                 "global",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "token",
			"machine_id":           "machine",
			"uid":                  "uid",
		},
	}

	_, err := tokenSource.GetSession(context.Background(), provider)
	require.ErrorContains(t, err, "require cn site")
}

func TestQoderTokenProviderRejectsMachineIDOnlyWithoutReadingLocalAuth(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	provider := &providercore.Record{
		ID:       106,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"machine_id": "machine-1",
		},
	}

	_, err := tokenSource.GetSession(context.Background(), provider)
	require.ErrorContains(t, err, "pat or security_oauth_token")
}

func TestQoderTokenProviderRejectsExplicitAuthDirWithoutReadingLocalAuth(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	provider := &providercore.Record{
		ID:       106,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"machine_id": "machine-1",
			"auth_dir":   "/tmp/qoder-auth",
		},
	}

	_, err := tokenSource.GetSession(context.Background(), provider)
	require.ErrorContains(t, err, "pat or security_oauth_token")
}

func TestQoderTokenProviderSupportsInjectedPATExchange(t *testing.T) {
	calls := 0
	orgCalls := 0
	var exchangedPATs []string
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.exchangePAT = func(_ context.Context, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		calls++
		exchangedPATs = append(exchangedPATs, pat)
		require.NotEmpty(t, machine.MachineID)
		return &qoder.AuthIdentity{
			Name:               "PAT User",
			UID:                "uid-1",
			AID:                "uid-1",
			UserType:           "personal_standard",
			SecurityOauthToken: "dt-from-pat",
			RefreshToken:       "rt-from-pat",
		}, nil
	}
	tokenSource.getOrgTags = func(context.Context, string, string) (*qoder.OrganizationTags, error) {
		orgCalls++
		return nil, nil
	}

	provider := &providercore.Record{
		ID:       103,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"pat":               "pat-123",
			"organization_id":   "org-from-provider",
			"organization_name": "Org From providercore.Record",
		},
	}

	session1, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "dt-from-pat", session1.Identity.SecurityOauthToken)
	require.Equal(t, "org-from-provider", session1.Identity.OrganizationID)
	require.Equal(t, "Org From providercore.Record", session1.Identity.OrganizationName)

	session2, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Same(t, session1, session2)
	require.Equal(t, 1, calls, "PAT exchange should not run after cache hit")
	require.Equal(t, 0, orgCalls, "provider-provided organization metadata should skip org lookup")

	provider.Credentials["pat"] = "pat-456"
	session3, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.NotSame(t, session1, session3)
	require.Equal(t, []string{"pat-123", "pat-456"}, exchangedPATs)
}

func TestQoderRefreshCredentialsHashTracksAuthenticationContext(t *testing.T) {
	base := map[string]any{
		"pat":             "pat-123",
		"site":            "global",
		"refresh_mode":    "cosy",
		"organization_id": "org-1",
		"model_mapping":   map[string]any{"auto": "auto"},
	}
	baseHash := providercore.QoderRefreshCredentialsHash(base)
	for _, change := range []map[string]any{
		{"pat": "pat-456"},
		{"site": "cn"},
		{"refresh_mode": qoder.RefreshModeQoderCN20},
		{"organization_id": "org-2"},
	} {
		credentials := providercore.MergeCredentials(base, change)
		require.NotEqual(t, baseHash, providercore.QoderRefreshCredentialsHash(credentials))
	}
	unrelated := providercore.MergeCredentials(base, map[string]any{
		"model_mapping": map[string]any{"custom": "auto"},
	})
	require.Equal(t, baseHash, providercore.QoderRefreshCredentialsHash(unrelated))
}

func TestQoderTokenProviderPATExchangePopulatesOrganizationFromAPI(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.exchangePAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return &qoder.AuthIdentity{
			Name:               "PAT User",
			UID:                "uid-1",
			AID:                "aid-1",
			UserType:           "personal_standard",
			SecurityOauthToken: "dt-from-pat",
		}, nil
	}
	tokenSource.getOrgTags = func(_ context.Context, token, uid string) (*qoder.OrganizationTags, error) {
		require.Equal(t, "dt-from-pat", token)
		require.Equal(t, "uid-1", uid)
		return &qoder.OrganizationTags{
			OrganizationID:   "org-from-api",
			OrganizationName: "Org From API",
		}, nil
	}

	provider := &providercore.Record{
		ID:       104,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"pat": "pat-123",
		},
	}

	session, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "org-from-api", session.Identity.OrganizationID)
	require.Equal(t, "Org From API", session.Identity.OrganizationName)
}

func TestQoderTokenProviderRoutesCNPATAndBuildsCNSession(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		require.Equal(t, "cn-pat", pat)
		require.Equal(t, "machine-cn", machine.MachineID)
		require.Empty(t, machine.MachineToken)
		require.Empty(t, machine.MachineType)
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "aid-cn",
			OrganizationID:     "org-cn",
			SecurityOauthToken: "cosy-cn",
			RefreshToken:       "refresh-cn",
		}, time.Now().Add(time.Hour), nil
	}
	provider := &providercore.Record{
		ID:       31,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":          "cn",
			"pat":           "cn-pat",
			"machine_id":    "machine-cn",
			"machine_token": "machine-token-cn",
			"machine_type":  "machine-type-cn",
		},
	}

	session, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, qoder.SiteCN, session.Site)
	require.Equal(t, qoder.CNClientVersion, session.ClientVersion)
	require.Equal(t, "cosy-cn", session.Identity.SecurityOauthToken)
	require.Empty(t, session.Machine.MachineToken)
	require.Empty(t, session.Machine.MachineType)
}

func TestQoderTokenProviderRebuildsExpiredCNPATSession(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	expiresAt := time.Now().Add(time.Hour)
	exchangeCalls := 0
	tokenSource.exchangeCNPAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls++
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: fmt.Sprintf("cosy-cn-%d", exchangeCalls),
			RefreshToken:       "refresh-cn",
		}, expiresAt, nil
	}
	provider := &providercore.Record{
		ID:       32,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":       "cn",
			"pat":        "cn-pat",
			"machine_id": "machine-cn",
		},
	}

	first, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, expiresAt, tokenSource.qoderState().Sessions[provider.ID].ExpiresAt)

	// Expire the cached OpenAPI token so the next read must exchange the PAT again.
	entry := tokenSource.qoderState().Sessions[provider.ID]
	entry.ExpiresAt = time.Now().Add(-time.Second)
	tokenSource.qoderState().Sessions[provider.ID] = entry
	second, err := tokenSource.GetSession(context.Background(), provider)

	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Equal(t, "cosy-cn-2", second.Identity.SecurityOauthToken)
	require.Equal(t, 2, exchangeCalls)
}

func TestQoderTokenProviderSingleflightsConcurrentExpiredCNPATSession(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	var blockRebuild atomic.Bool
	rebuildStarted := make(chan struct{})
	releaseRebuild := make(chan struct{})
	tokenSource.exchangeCNPAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		call := exchangeCalls.Add(1)
		if blockRebuild.Load() {
			if call == 2 {
				close(rebuildStarted)
			}
			<-releaseRebuild
		}
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: fmt.Sprintf("cosy-cn-%d", call),
		}, time.Now().Add(time.Hour), nil
	}
	provider := &providercore.Record{
		ID:       3201,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":       "cn",
			"pat":        "cn-pat",
			"machine_id": "machine-cn",
		},
	}

	_, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	tokenSource.qoderState().Mu.Lock()
	entry := tokenSource.qoderState().Sessions[provider.ID]
	entry.ExpiresAt = time.Now().Add(-time.Second)
	tokenSource.qoderState().Sessions[provider.ID] = entry
	tokenSource.qoderState().Mu.Unlock()
	blockRebuild.Store(true)

	const workers = 32
	start := make(chan struct{})
	results := make(chan *qoder.SessionContext, workers)
	errorsCh := make(chan error, workers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(workers)
	done.Add(workers)
	for range workers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			session, getErr := tokenSource.GetSession(context.Background(), provider)
			results <- session
			errorsCh <- getErr
		}()
	}
	ready.Wait()
	close(start)
	<-rebuildStarted
	// Hold the first exchange so other callers can join its singleflight.
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(2), exchangeCalls.Load())
	close(releaseRebuild)
	done.Wait()
	close(results)
	close(errorsCh)

	for getErr := range errorsCh {
		require.NoError(t, getErr)
	}
	var shared *qoder.SessionContext
	for session := range results {
		require.NotNil(t, session)
		if shared == nil {
			shared = session
		}
		require.Same(t, shared, session)
	}
	require.Equal(t, int32(2), exchangeCalls.Load(), "初次构建加一次过期重建应只交换两次")
}

func TestQoderTokenProviderSingleflightSeparatesWaiterCancellation(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	tokenSource.exchangeCNPAT = func(ctx context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		close(buildStarted)
		select {
		case <-ctx.Done():
			return nil, time.Time{}, ctx.Err()
		case <-releaseBuild:
			return &qoder.AuthIdentity{
				UID:                "uid-cn",
				AID:                "uid-cn",
				SecurityOauthToken: "cosy-cn-shared",
			}, time.Now().Add(time.Hour), nil
		}
	}
	provider := &providercore.Record{
		ID:       3204,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":       "cn",
			"pat":        "cn-pat",
			"machine_id": "machine-cn",
		},
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, getErr := tokenSource.GetSession(firstCtx, provider)
		firstResult <- getErr
	}()
	<-buildStarted
	cancelFirst()
	require.ErrorIs(t, <-firstResult, context.Canceled)

	secondResult := make(chan struct {
		session *qoder.SessionContext
		err     error
	}, 1)
	go func() {
		session, getErr := tokenSource.GetSession(context.Background(), provider)
		secondResult <- struct {
			session *qoder.SessionContext
			err     error
		}{session: session, err: getErr}
	}()
	require.Eventually(t, func() bool {
		return exchangeCalls.Load() == 1
	}, time.Second, 10*time.Millisecond)
	close(releaseBuild)

	second := <-secondResult
	require.NoError(t, second.err)
	require.NotNil(t, second.session)
	require.Equal(t, "cosy-cn-shared", second.session.Identity.SecurityOauthToken)
	require.Equal(t, int32(1), exchangeCalls.Load())
}

func TestQoderTokenProviderDetachedBuildHasHardTimeout(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.qoderState().SessionBuildTimeout = 20 * time.Millisecond
	tokenSource.exchangeCNPAT = func(ctx context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		<-ctx.Done()
		return nil, time.Time{}, ctx.Err()
	}
	provider := &providercore.Record{ID: 3205, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "cn-pat", "machine_id": "machine-cn",
	}}

	startedAt := time.Now()
	_, err := tokenSource.GetSession(context.Background(), provider)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
}

func TestQoderTokenProviderBuildUsesProviderSnapshot(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	tokenSource.exchangePAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		close(buildStarted)
		<-releaseBuild
		return &qoder.AuthIdentity{
			UID:                "uid-global",
			AID:                "uid-global",
			SecurityOauthToken: "cosy-global",
		}, nil
	}
	provider := &providercore.Record{ID: 3206, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "global", "pat": "global-pat", "machine_id": "machine-global", "organization_id": "org-original",
	}}

	type sessionResult struct {
		session *qoder.SessionContext
		err     error
	}
	resultCh := make(chan sessionResult, 1)
	go func() {
		session, getErr := tokenSource.GetSession(context.Background(), provider)
		resultCh <- sessionResult{session: session, err: getErr}
	}()
	<-buildStarted
	provider.Credentials["organization_id"] = "org-mutated"
	close(releaseBuild)

	result := <-resultCh
	require.NoError(t, result.err)
	require.Equal(t, "org-original", result.session.Identity.OrganizationID)
}

func TestQoderTokenProviderInvalidateDoesNotReturnOrCacheInflightSession(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	tokenSource.exchangeCNPAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		call := exchangeCalls.Add(1)
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: fmt.Sprintf("cosy-cn-%d", call),
		}, time.Now().Add(time.Hour), nil
	}
	provider := &providercore.Record{
		ID:       3202,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site":       "cn",
			"pat":        "cn-pat",
			"machine_id": "machine-cn",
		},
	}

	type sessionResult struct {
		session *qoder.SessionContext
		err     error
	}
	firstResult := make(chan sessionResult, 1)
	go func() {
		session, getErr := tokenSource.GetSession(context.Background(), provider)
		firstResult <- sessionResult{session: session, err: getErr}
	}()
	<-firstStarted
	tokenSource.Invalidate(provider.ID)

	second, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "cosy-cn-2", second.Identity.SecurityOauthToken)
	close(releaseFirst)
	stale := <-firstResult
	require.Nil(t, stale.session)
	require.ErrorIs(t, stale.err, errQoderSessionBuildInvalidated)

	tokenSource.qoderState().Mu.Lock()
	cached := tokenSource.qoderState().Sessions[provider.ID].Session
	tokenSource.qoderState().Mu.Unlock()
	require.Same(t, second, cached)
	require.Equal(t, int32(2), exchangeCalls.Load())
}

func TestQoderTokenProviderDoesNotMergeDifferentCredentialHashes(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		if pat == "old-pat" {
			close(oldStarted)
			<-releaseOld
		}
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	oldProvider := &providercore.Record{ID: 3203, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "old-pat", "machine_id": "machine-cn", "_token_version": int64(1),
	}}
	newProvider := &providercore.Record{ID: 3203, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "new-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}

	oldResult := make(chan error, 1)
	go func() {
		_, getErr := tokenSource.GetSession(context.Background(), oldProvider)
		oldResult <- getErr
	}()
	<-oldStarted
	newSession, err := tokenSource.GetSession(context.Background(), newProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-new-pat", newSession.Identity.SecurityOauthToken)
	close(releaseOld)
	require.ErrorIs(t, <-oldResult, errQoderSessionBuildInvalidated)

	tokenSource.qoderState().Mu.Lock()
	cached := tokenSource.qoderState().Sessions[newProvider.ID]
	tokenSource.qoderState().Mu.Unlock()
	require.Equal(t, providercore.QoderCredentialsHash(newProvider.Credentials), cached.CredentialsHash)
	require.Same(t, newSession, cached.Session)
	require.Equal(t, int32(2), exchangeCalls.Load())
}

func TestQoderTokenProviderDoesNotRegressToOlderCredentialVersion(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	newProvider := &providercore.Record{ID: 3207, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "new-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}
	staleProvider := &providercore.Record{ID: 3207, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "old-pat", "machine_id": "machine-cn", "_token_version": int64(1),
	}}

	latest, err := tokenSource.GetSession(context.Background(), newProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-new-pat", latest.Identity.SecurityOauthToken)

	fromStaleSnapshot, err := tokenSource.GetSession(context.Background(), staleProvider)
	require.NoError(t, err)
	require.Same(t, latest, fromStaleSnapshot)
	require.Equal(t, int32(1), exchangeCalls.Load())

	tokenSource.qoderState().Mu.Lock()
	cached := tokenSource.qoderState().Sessions[newProvider.ID]
	currentState := tokenSource.qoderState().ProviderStates[newProvider.ID]
	tokenSource.qoderState().Mu.Unlock()
	require.Equal(t, providercore.QoderCredentialsHash(newProvider.Credentials), currentState.CredentialsHash)
	require.Equal(t, int64(2), currentState.CredentialVersion)
	require.Same(t, latest, cached.Session)
}

func TestQoderTokenProviderAuthoritativeInvalidationBlocksUnobservedStaleVersion(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	staleProvider := &providercore.Record{ID: 3210, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "old-pat", "machine_id": "machine-cn", "_token_version": int64(1),
	}}
	refreshedProvider := &providercore.Record{ID: 3210, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "new-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}

	oldSession, err := tokenSource.GetSession(context.Background(), staleProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-old-pat", oldSession.Identity.SecurityOauthToken)
	tokenSource.InvalidateProvider(refreshedProvider)

	fromStaleSnapshot, err := tokenSource.GetSession(context.Background(), staleProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-new-pat", fromStaleSnapshot.Identity.SecurityOauthToken)
	require.Equal(t, int32(2), exchangeCalls.Load())

	tokenSource.qoderState().Mu.Lock()
	currentState := tokenSource.qoderState().ProviderStates[staleProvider.ID]
	tokenSource.qoderState().Mu.Unlock()
	require.Equal(t, int64(2), currentState.CredentialVersion)
	require.Equal(t, providercore.QoderCredentialsHash(refreshedProvider.Credentials), currentState.CredentialsHash)
}

func TestQoderTokenProviderAuthoritativeInvalidationRejectsOlderSnapshot(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	now := time.Now()
	latestProvider := &providercore.Record{ID: 3211, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, UpdatedAt: now, Credentials: map[string]any{
		"site": "cn", "pat": "v3-pat", "machine_id": "machine-cn", "_token_version": int64(3),
	}}
	olderProvider := &providercore.Record{ID: 3211, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, UpdatedAt: now.Add(-time.Minute), Credentials: map[string]any{
		"site": "cn", "pat": "v2-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}

	tokenSource.InvalidateProvider(latestProvider)
	latestSession, err := tokenSource.GetSession(context.Background(), latestProvider)
	require.NoError(t, err)
	tokenSource.InvalidateProvider(olderProvider)

	tokenSource.qoderState().Mu.Lock()
	cached := tokenSource.qoderState().Sessions[latestProvider.ID]
	currentState := tokenSource.qoderState().ProviderStates[latestProvider.ID]
	tokenSource.qoderState().Mu.Unlock()
	require.Same(t, latestSession, cached.Session)
	require.Equal(t, int64(3), currentState.CredentialVersion)
	require.Equal(t, providercore.QoderCredentialsHash(latestProvider.Credentials), currentState.CredentialsHash)

	fromOlderSnapshot, err := tokenSource.GetSession(context.Background(), olderProvider)
	require.NoError(t, err)
	require.Same(t, latestSession, fromOlderSnapshot)
	require.Equal(t, int32(1), exchangeCalls.Load())
}

func TestQoderTokenProviderNewerVersionOverridesOlderUpdatedAt(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	now := time.Now()
	currentProvider := &providercore.Record{ID: 3212, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, UpdatedAt: now, Credentials: map[string]any{
		"site": "cn", "pat": "v2-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}
	refreshedProvider := &providercore.Record{ID: 3212, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, UpdatedAt: now.Add(-time.Minute), Credentials: map[string]any{
		"site": "cn", "pat": "v3-pat", "machine_id": "machine-cn", "_token_version": int64(3),
	}}

	currentSession, err := tokenSource.GetSession(context.Background(), currentProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-v2-pat", currentSession.Identity.SecurityOauthToken)
	refreshedSession, err := tokenSource.GetSession(context.Background(), refreshedProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-v3-pat", refreshedSession.Identity.SecurityOauthToken)
	require.Equal(t, int32(2), exchangeCalls.Load())
}

func TestQoderTokenProviderAuthoritativeNewerVersionOverridesOlderUpdatedAt(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	now := time.Now()
	currentProvider := &providercore.Record{ID: 3213, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, UpdatedAt: now, Credentials: map[string]any{
		"site": "cn", "pat": "v2-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}
	refreshedProvider := &providercore.Record{ID: 3213, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, UpdatedAt: now.Add(-time.Minute), Credentials: map[string]any{
		"site": "cn", "pat": "v3-pat", "machine_id": "machine-cn", "_token_version": int64(3),
	}}

	_, err := tokenSource.GetSession(context.Background(), currentProvider)
	require.NoError(t, err)
	tokenSource.InvalidateProvider(refreshedProvider)
	fromStaleSnapshot, err := tokenSource.GetSession(context.Background(), currentProvider)
	require.NoError(t, err)
	require.Equal(t, "cosy-v3-pat", fromStaleSnapshot.Identity.SecurityOauthToken)
	require.Equal(t, int32(2), exchangeCalls.Load())

	tokenSource.qoderState().Mu.Lock()
	currentState := tokenSource.qoderState().ProviderStates[currentProvider.ID]
	tokenSource.qoderState().Mu.Unlock()
	require.Equal(t, int64(3), currentState.CredentialVersion)
	require.Equal(t, providercore.QoderCredentialsHash(refreshedProvider.Credentials), currentState.CredentialsHash)
}

func TestQoderTokenProviderOlderCredentialVersionJoinsNewerInflightBuild(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	var exchangeCalls atomic.Int32
	newBuildStarted := make(chan struct{})
	releaseNewBuild := make(chan struct{})
	tokenSource.exchangeCNPAT = func(_ context.Context, pat string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		exchangeCalls.Add(1)
		if pat == "new-pat" {
			close(newBuildStarted)
			<-releaseNewBuild
		}
		return &qoder.AuthIdentity{
			UID:                "uid-cn",
			AID:                "uid-cn",
			SecurityOauthToken: "cosy-" + pat,
		}, time.Now().Add(time.Hour), nil
	}
	newProvider := &providercore.Record{ID: 3208, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "new-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}}
	staleProvider := &providercore.Record{ID: 3208, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: map[string]any{
		"site": "cn", "pat": "old-pat", "machine_id": "machine-cn", "_token_version": int64(1),
	}}

	type sessionResult struct {
		session *qoder.SessionContext
		err     error
	}
	newResult := make(chan sessionResult, 1)
	go func() {
		session, getErr := tokenSource.GetSession(context.Background(), newProvider)
		newResult <- sessionResult{session: session, err: getErr}
	}()
	<-newBuildStarted

	staleResult := make(chan sessionResult, 1)
	go func() {
		session, getErr := tokenSource.GetSession(context.Background(), staleProvider)
		staleResult <- sessionResult{session: session, err: getErr}
	}()
	// The old-credential caller must wait for the new version's exchange; a separate exchange would return immediately.
	select {
	case result := <-staleResult:
		close(releaseNewBuild)
		require.FailNowf(t, "stale build returned early", "session=%v err=%v", result.session, result.err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseNewBuild)

	latest := <-newResult
	fromStaleSnapshot := <-staleResult
	require.NoError(t, latest.err)
	require.NoError(t, fromStaleSnapshot.err)
	require.Same(t, latest.session, fromStaleSnapshot.session)
	require.Equal(t, "cosy-new-pat", latest.session.Identity.SecurityOauthToken)
	require.Equal(t, int32(1), exchangeCalls.Load())
}

func TestQoderTokenProviderUpdatesSnapshotForSameCredentials(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.exchangeCNPAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		return &qoder.AuthIdentity{UID: "uid-cn", AID: "uid-cn", SecurityOauthToken: "cosy-cn"}, time.Now().Add(time.Hour), nil
	}
	credentials := map[string]any{
		"site": "cn", "pat": "cn-pat", "machine_id": "machine-cn", "_token_version": int64(2),
	}
	oldProxy := &egress.Proxy{Protocol: "http", Host: "old-proxy.example", Port: 8080}
	newProxy := &egress.Proxy{Protocol: "http", Host: "new-proxy.example", Port: 8080}
	firstProvider := &providercore.Record{ID: 3209, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: credentials, Proxy: oldProxy}
	latestProvider := &providercore.Record{ID: 3209, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: maps.Clone(credentials), Proxy: newProxy}

	first, err := tokenSource.GetSession(context.Background(), firstProvider)
	require.NoError(t, err)
	second, err := tokenSource.GetSession(context.Background(), latestProvider)
	require.NoError(t, err)
	require.Same(t, first, second)

	tokenSource.qoderState().Mu.Lock()
	storedSnapshot := tokenSource.qoderState().ProviderStates[latestProvider.ID].ProviderSnapshot
	tokenSource.qoderState().Mu.Unlock()
	require.NotNil(t, storedSnapshot)
	require.Equal(t, "new-proxy.example", storedSnapshot.Proxy.Host)
}

func TestQoderTokenProviderDirectTokenPopulatesOrganizationFromAPI(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.getOrgTags = func(_ context.Context, token, uid string) (*qoder.OrganizationTags, error) {
		require.Equal(t, "dt-token", token)
		require.Equal(t, "uid-1", uid)
		return &qoder.OrganizationTags{
			OrganizationID:   "org-from-api",
			OrganizationName: "Org From API",
		}, nil
	}

	provider := &providercore.Record{
		ID:       110,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	}

	session, err := tokenSource.GetSession(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "org-from-api", session.Identity.OrganizationID)
	require.Equal(t, "Org From API", session.Identity.OrganizationName)
}

func TestQoderTokenProviderPATExchangeUsesProviderDoer(t *testing.T) {
	upstream := &qoderCenterHTTPUpstreamStub{}
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.SetHTTPUpstream(upstream, nil)
	provider := &providercore.Record{
		ID:          107,
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 3,
		ProxyID:     ptrInt64ForQoderTest(9),
		Proxy:       &egress.Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080},
		Credentials: map[string]any{"pat": "pat-123"},
	}

	session, err := tokenSource.GetSession(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "dt-from-center", session.Identity.SecurityOauthToken)
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(107), upstream.providerID)
	require.Equal(t, 3, upstream.providerConcurrency)
}

func TestQoderTokenProviderPATOrganizationTagsUsesProviderDoer(t *testing.T) {
	upstream := &qoderCenterHTTPUpstreamStub{
		body: `{"organization_id":"org-via-upstream","organization_name":"Org Via Upstream"}`,
	}
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.SetHTTPUpstream(upstream, &egressadapter.TLSProfiles{})
	tokenSource.exchangePAT = func(_ context.Context, _ string, _ *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return &qoder.AuthIdentity{
			Name:               "PAT User",
			UID:                "uid-1",
			AID:                "uid-1",
			UserType:           "personal_standard",
			SecurityOauthToken: "dt-from-pat",
		}, nil
	}
	provider := &providercore.Record{
		ID:          108,
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 4,
		ProxyID:     ptrInt64ForQoderTest(10),
		Proxy:       &egress.Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8081},
		Credentials: map[string]any{"pat": "pat-123"},
		Extra:       map[string]any{"enable_tls_fingerprint": true},
	}

	session, err := tokenSource.GetSession(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "org-via-upstream", session.Identity.OrganizationID)
	require.Equal(t, "Org Via Upstream", session.Identity.OrganizationName)
	require.Len(t, upstream.requests, 1)
	require.Contains(t, upstream.requests[0].URL.Path, qoder.OrganizationTagsPathPrefix+"uid-1/tags")
	require.Equal(t, "http://proxy.example.com:8081", upstream.proxyURL)
	require.Equal(t, int64(108), upstream.providerID)
	require.Equal(t, 4, upstream.providerConcurrency)
	require.True(t, upstream.profileSet)
}

func TestQoderTokenProviderOrganizationTagsErrorRedactsSensitiveBody(t *testing.T) {
	upstream := &qoderCenterHTTPUpstreamStub{
		statusCode: http.StatusInternalServerError,
		body:       `{"message":"failed","securityOauthToken":"sec-secret","refresh_token":"rt-secret","uid":"uid-secret","cookie":"sid=secret"}`,
	}
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.SetHTTPUpstream(upstream, nil)
	provider := &providercore.Record{
		ID:       109,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "sec-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	}

	_, err := tokenSource.sessionBuilder(provider).GetOrganizationTags(context.Background(), QoderCredentialInput(provider), "sec-token", "uid-1")

	require.Error(t, err)
	errText := err.Error()
	require.Contains(t, errText, "status 500")
	require.NotContains(t, errText, "sec-secret")
	require.NotContains(t, errText, "rt-secret")
	require.NotContains(t, errText, "uid-secret")
	require.NotContains(t, errText, "sid=secret")
	require.Contains(t, errText, "***")
}

func TestCompositeTokenCacheInvalidator_QoderCosy(t *testing.T) {
	cache := &qoderInvalidationCache{}
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.Core.Sessions[42] = providercore.QoderSessionCacheEntry[*qoder.SessionContext]{CredentialsHash: "old"}
	invalidator := providercore.NewCompositeTokenCacheInvalidator(cache, tokenSource, nil)
	provider := &providercore.Record{
		ID:       42,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
	}

	err := invalidator.InvalidateToken(context.Background(), provider)

	require.NoError(t, err)
	require.Contains(t, cache.deletedKeys, "qoder:provider:42")
	tokenSource.Core.Mu.Lock()
	_, cached := tokenSource.Core.Sessions[42]
	tokenSource.Core.Mu.Unlock()
	require.False(t, cached, "qoder provider session cache should be invalidated too")
}

func TestCompositeTokenCacheInvalidator_QoderCosyInvalidatesProviderWithoutExternalCache(t *testing.T) {
	tokenSource := NewQoderTokenProvider(qoder.SessionBuilder{})
	tokenSource.Core.Sessions[43] = providercore.QoderSessionCacheEntry[*qoder.SessionContext]{CredentialsHash: "old"}
	invalidator := providercore.NewCompositeTokenCacheInvalidator(nil, tokenSource, nil)
	provider := &providercore.Record{
		ID:       43,
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
	}

	err := invalidator.InvalidateToken(context.Background(), provider)

	require.NoError(t, err)
	tokenSource.Core.Mu.Lock()
	_, cached := tokenSource.Core.Sessions[43]
	tokenSource.Core.Mu.Unlock()
	require.False(t, cached)
}

func (s *qoderCenterHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (s *qoderCenterHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.proxyURL = proxyURL
	s.providerID = providerID
	s.providerConcurrency = providerConcurrency
	s.profileSet = profile != nil
	s.requests = append(s.requests, req)
	body := s.body
	if body == "" {
		body = `{
			"id":"user-1",
			"name":"User",
			"userType":"personal_standard",
			"securityOauthToken":"dt-from-center",
			"refreshToken":"rt-from-center"
		}`
	}
	statusCode := s.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func ptrInt64ForQoderTest(v int64) *int64 {
	return &v
}

func (c *qoderInvalidationCache) DeleteAccessToken(_ context.Context, key string) error {
	c.deletedKeys = append(c.deletedKeys, key)
	return nil
}
