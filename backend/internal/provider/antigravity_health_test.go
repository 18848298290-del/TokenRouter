package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// antigravityHealthStoreFixture captures serial model/Extra writes with
// unexpected embedded operations unused.
type antigravityHealthStoreFixture struct {
	AntigravityHealthStore // unused embedded health operations
	// modelRateLimitCalls and extraUpdateCalls retain serial request captures
	// without locking. Scalar limit arguments are copied, while Extra patches
	// keep their supplied nested objects borrowed for later assertions.
	// captured model rate-limit arguments
	modelRateLimitCalls []struct {
		providerID int64     // captured provider row identifier
		modelKey   string    // captured exact model key
		resetAt    time.Time // captured model reset deadline
	}
	// captured metadata patch arguments
	extraUpdateCalls []struct {
		providerID int64 // captured provider row identifier
		// updates remains a borrowed patch retained by the serial capture.
		// Keep its nested values stable while the test inspects this observation.
		updates map[string]any
	}
}

// mockInternal500Cache records serial counter calls and returns configured
// count/errors.
type mockInternal500Cache struct {
	incrementCount int64 // configured increment return count
	incrementErr   error // configured increment failure result
	resetErr       error // configured reset failure result

	incrementCalls []int64 // captured increment row identifiers
	resetCalls     []int64 // captured reset row identifiers
}

// internal500ProviderRepoStub records temporary pause and error writes for
// serial penalty tests.
type internal500ProviderRepoStub struct {
	// AntigravityHealthStore leaves other operations unset. Tests only call the
	// overridden methods; dispatch through this nil embedded dependency panics.
	AntigravityHealthStore

	// captured temporary scheduling pauses
	tempUnschedCalls []tempUnschedCall
	setErrorCalls    []setErrorCall // captured persistent error writes
}

// tempUnschedCall captures provider identity, pause deadline, and its
// diagnostic reason.
type tempUnschedCall struct {
	providerID int64     // captured provider row identifier
	until      time.Time // captured temporary pause deadline
	reason     string    // captured health diagnostic text
}

// setErrorCall captures provider identity and accepted error text for
// assertions.
type setErrorCall struct {
	providerID int64  // captured provider row identifier
	reason     string // captured health diagnostic text
}

// antigravityPublicationFixture retains borrowed record pointers passed to
// serial publication calls.
type antigravityPublicationFixture struct {
	// setProviderCalls retains borrowed record pointers without locking or
	// copying.
	// Tests serialize publication and inspect them while their records stay
	// stable.
	setProviderCalls []*Record
}

// TestClearCreditsExhausted varies absent records, Extra, limits, and AI
// Credits entries. It checks no writes for missing markers and deletion plus
// one Extra write while the configured ordinary model entry remains.
func TestClearCreditsExhausted(t *testing.T) {
	t.Run("provider 为 nil 不操作", func(t *testing.T) {
		repo := &antigravityHealthStoreFixture{}
		svc := &AntigravityHealth{Store: repo, Logf: func(string, ...any) {}}
		svc.ClearCreditsExhausted(context.Background(), nil)
		require.Empty(t, repo.extraUpdateCalls)
	})

	t.Run("Extra 为 nil 不操作", func(t *testing.T) {
		repo := &antigravityHealthStoreFixture{}
		svc := &AntigravityHealth{Store: repo, Logf: func(string, ...any) {}}
		svc.ClearCreditsExhausted(context.Background(), &Record{ID: 1})
		require.Empty(t, repo.extraUpdateCalls)
	})

	t.Run("无 modelRateLimitsKey 不操作", func(t *testing.T) {
		repo := &antigravityHealthStoreFixture{}
		svc := &AntigravityHealth{Store: repo, Logf: func(string, ...any) {}}
		svc.ClearCreditsExhausted(context.Background(), &Record{
			ID:    1,
			Extra: map[string]any{"some_key": "value"},
		})
		require.Empty(t, repo.extraUpdateCalls)
	})

	t.Run("无 AICredits key 不操作", func(t *testing.T) {
		repo := &antigravityHealthStoreFixture{}
		svc := &AntigravityHealth{Store: repo, Logf: func(string, ...any) {}}
		svc.ClearCreditsExhausted(context.Background(), &Record{
			ID: 1,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"claude-sonnet-4-5": map[string]any{
						"rate_limited_at":     "2026-03-15T00:00:00Z",
						"rate_limit_reset_at": "2099-03-15T00:00:00Z",
					},
				},
			},
		})
		require.Empty(t, repo.extraUpdateCalls)
	})

	t.Run("有 AICredits key 时删除并调用 UpdateExtra", func(t *testing.T) {
		repo := &antigravityHealthStoreFixture{}
		svc := &AntigravityHealth{Store: repo, Logf: func(string, ...any) {}}
		provider := &Record{
			ID: 1,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"claude-sonnet-4-5": map[string]any{
						"rate_limited_at":     "2026-03-15T00:00:00Z",
						"rate_limit_reset_at": "2099-03-15T00:00:00Z",
					},
					CreditsExhaustedKey: map[string]any{
						"rate_limited_at":     "2026-03-15T00:00:00Z",
						"rate_limit_reset_at": time.Now().Add(5 * time.Hour).UTC().Format(time.RFC3339),
					},
				},
			},
		}
		svc.ClearCreditsExhausted(context.Background(), provider)
		require.Len(t, repo.extraUpdateCalls, 1)
		// Remove the AICredits key.
		rawLimits := assertion.MustType[map[string]any](provider.Extra["model_rate_limits"])
		_, exists := rawLimits[CreditsExhaustedKey]
		require.False(t, exists, "AICredits key 应被删除")
		// Retain ordinary model rate limits.
		_, exists = rawLimits["claude-sonnet-4-5"]
		require.True(t, exists, "普通模型限流应保留")
	})
}

// SetModelRateLimit appends scalar arguments to an unlocked call slice and
// returns success. Context and optional sources are ignored; tests serialize
// access before inspecting captured model keys and deadlines.
func (s *antigravityHealthStoreFixture) SetModelRateLimit(_ context.Context, id int64, key string, at time.Time, _ ...string) error {
	s.modelRateLimitCalls = append(s.modelRateLimitCalls, struct {
		providerID int64     // captured provider row identifier
		modelKey   string    // captured exact model key
		resetAt    time.Time // captured model reset deadline
	}{id, key, at})
	return nil
}

// UpdateExtra records row ID and the borrowed patch map in an unlocked slice.
// It returns success without copying nested values, so tests serialize calls
// and keep retained patches stable while inspecting them.
func (s *antigravityHealthStoreFixture) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	s.extraUpdateCalls = append(s.extraUpdateCalls, struct {
		providerID int64 // captured provider row identifier
		// updates remains a borrowed patch retained by the serial capture.
		// Keep its nested values stable while the test inspects this observation.
		updates map[string]any
	}{id, updates})
	return nil
}

// IncrementInternal500Count records its row and returns the configured
// count/error. The fixture does not increment a stored count or simulate
// atomic storage; tests configure and inspect its unlocked slices serially.
func (m *mockInternal500Cache) IncrementInternal500Count(_ context.Context, providerID int64) (int64, error) {
	m.incrementCalls = append(m.incrementCalls, providerID)
	return m.incrementCount, m.incrementErr
}

// ResetInternal500Count records the requested row and returns its configured
// failure. It changes no count state and ignores context; the serial fixture
// retains captured IDs for assertions after return.
func (m *mockInternal500Cache) ResetInternal500Count(_ context.Context, providerID int64) error {
	m.resetCalls = append(m.resetCalls, providerID)
	return m.resetErr
}

// SetTempUnschedulable appends the supplied row, deadline, and reason for
// serial assertions. It returns success without durable storage, and tests
// serialize access to its unlocked capture slice.
func (r *internal500ProviderRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls = append(r.tempUnschedCalls, tempUnschedCall{providerID: id, until: until, reason: reason})
	return nil
}

// SetError records the row and supplied error text in an unlocked fixture
// slice. It returns success, ignores context, and requires serial test access
// before the captured arguments are inspected.
func (r *internal500ProviderRepoStub) SetError(_ context.Context, id int64, errorMsg string) error {
	r.setErrorCalls = append(r.setErrorCalls, setErrorCall{providerID: id, reason: errorMsg})
	return nil
}

// TestApplyInternal500Penalty supplies zero, first, second, and later
// exhausted-round counts. It checks selected write kind, row/reason, and
// constant-based pause deadlines for the two temporary penalty tiers.
func TestApplyInternal500Penalty(t *testing.T) {
	t.Run("count=1 → SetTempUnschedulable 10 分钟", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		svc := newInternal500Health(repo, nil)
		provider := &Record{ID: 1, Name: "acc-1"}

		before := time.Now()
		svc.ApplyInternal500Penalty(context.Background(), "[test]", provider, 1)
		after := time.Now()

		require.Len(t, repo.tempUnschedCalls, 1)
		require.Empty(t, repo.setErrorCalls)

		call := repo.tempUnschedCalls[0]
		require.Equal(t, int64(1), call.providerID)
		require.Contains(t, call.reason, "INTERNAL 500")
		// Compare against the configured first-tier duration with one-second timing
		// tolerance, allowing execution delay while rejecting a different penalty
		// duration from the current thirty-minute policy.
		require.True(t, call.until.After(before.Add(Internal500PenaltyTier1Duration).Add(-time.Second)))
		require.True(t, call.until.Before(after.Add(Internal500PenaltyTier1Duration).Add(time.Second)))
	})

	t.Run("count=2 → SetTempUnschedulable 10 小时", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		svc := newInternal500Health(repo, nil)
		provider := &Record{ID: 2, Name: "acc-2"}

		before := time.Now()
		svc.ApplyInternal500Penalty(context.Background(), "[test]", provider, 2)
		after := time.Now()

		require.Len(t, repo.tempUnschedCalls, 1)
		require.Empty(t, repo.setErrorCalls)

		call := repo.tempUnschedCalls[0]
		require.Equal(t, int64(2), call.providerID)
		require.Contains(t, call.reason, "INTERNAL 500")
		// Allow one second of execution delay around the configured penalty deadline.
		require.True(t, call.until.After(before.Add(Internal500PenaltyTier2Duration).Add(-time.Second)))
		require.True(t, call.until.Before(after.Add(Internal500PenaltyTier2Duration).Add(time.Second)))
	})

	t.Run("count=3 → SetError 永久禁用", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		svc := newInternal500Health(repo, nil)
		provider := &Record{ID: 3, Name: "acc-3"}

		svc.ApplyInternal500Penalty(context.Background(), "[test]", provider, 3)

		require.Empty(t, repo.tempUnschedCalls)
		require.Len(t, repo.setErrorCalls, 1)

		call := repo.setErrorCalls[0]
		require.Equal(t, int64(3), call.providerID)
		require.Contains(t, call.reason, "INTERNAL 500 consecutive failures: 3")
	})

	t.Run("count=5 → SetError 永久禁用（>=3 都走永久禁用）", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		svc := newInternal500Health(repo, nil)
		provider := &Record{ID: 5, Name: "acc-5"}

		svc.ApplyInternal500Penalty(context.Background(), "[test]", provider, 5)

		require.Empty(t, repo.tempUnschedCalls)
		require.Len(t, repo.setErrorCalls, 1)

		call := repo.setErrorCalls[0]
		require.Equal(t, int64(5), call.providerID)
		require.Contains(t, call.reason, "INTERNAL 500 consecutive failures: 5")
	})

	t.Run("count=0 → 不调用任何方法", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		svc := newInternal500Health(repo, nil)
		provider := &Record{ID: 10, Name: "acc-10"}

		svc.ApplyInternal500Penalty(context.Background(), "[test]", provider, 0)

		require.Empty(t, repo.tempUnschedCalls)
		require.Empty(t, repo.setErrorCalls)
	})
}

// TestHandleInternal500RetryExhausted varies missing counter, increment
// failure, and configured first/later counts. It checks counter call
// identities and selected penalty captures, including suppression of penalties
// after failed increment.
func TestHandleInternal500RetryExhausted(t *testing.T) {
	t.Run("internal500Cache 为 nil → 不 panic，不调用任何方法", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		svc := newInternal500Health(repo, nil)
		provider := &Record{ID: 1, Name: "acc-1"}

		// A missing counter is safe.
		require.NotPanics(t, func() {
			svc.HandleInternal500RetryExhausted(context.Background(), "[test]", provider)
		})
		require.Empty(t, repo.tempUnschedCalls)
		require.Empty(t, repo.setErrorCalls)
	})

	t.Run("IncrementInternal500Count 返回 error → 不调用惩罚方法", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		cache := &mockInternal500Cache{
			incrementErr: errors.New("redis connection error"),
		}
		svc := newInternal500Health(repo, cache)
		provider := &Record{ID: 2, Name: "acc-2"}

		svc.HandleInternal500RetryExhausted(context.Background(), "[test]", provider)

		require.Len(t, cache.incrementCalls, 1)
		require.Equal(t, int64(2), cache.incrementCalls[0])
		require.Empty(t, repo.tempUnschedCalls)
		require.Empty(t, repo.setErrorCalls)
	})

	t.Run("IncrementInternal500Count 返回 count=1 → 触发 tier1 惩罚", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		cache := &mockInternal500Cache{
			incrementCount: 1,
		}
		svc := newInternal500Health(repo, cache)
		provider := &Record{ID: 3, Name: "acc-3"}

		svc.HandleInternal500RetryExhausted(context.Background(), "[test]", provider)

		require.Len(t, cache.incrementCalls, 1)
		require.Equal(t, int64(3), cache.incrementCalls[0])
		// The first penalty tier writes a temporary scheduling pause.
		require.Len(t, repo.tempUnschedCalls, 1)
		require.Equal(t, int64(3), repo.tempUnschedCalls[0].providerID)
		require.Empty(t, repo.setErrorCalls)
	})

	t.Run("IncrementInternal500Count 返回 count=3 → 触发 tier3 永久禁用", func(t *testing.T) {
		repo := &internal500ProviderRepoStub{}
		cache := &mockInternal500Cache{
			incrementCount: 3,
		}
		svc := newInternal500Health(repo, cache)
		provider := &Record{ID: 4, Name: "acc-4"}

		svc.HandleInternal500RetryExhausted(context.Background(), "[test]", provider)

		require.Len(t, cache.incrementCalls, 1)
		require.Empty(t, repo.tempUnschedCalls)
		require.Len(t, repo.setErrorCalls, 1)
		require.Equal(t, int64(4), repo.setErrorCalls[0].providerID)
	})
}

// TestResetInternal500Counter supplies missing, failed, and successful reset
// fixtures. It checks normal return plus the captured provider IDs for reached
// callbacks, without asserting actual stored-count deletion.
func TestResetInternal500Counter(t *testing.T) {
	t.Run("internal500Cache 为 nil → 不 panic", func(t *testing.T) {
		svc := newInternal500Health(nil, nil)

		require.NotPanics(t, func() {
			svc.ResetInternal500Counter(context.Background(), "[test]", 1)
		})
	})

	t.Run("ResetInternal500Count 返回 error → 不 panic（仅日志）", func(t *testing.T) {
		cache := &mockInternal500Cache{
			resetErr: errors.New("redis timeout"),
		}
		svc := newInternal500Health(nil, cache)

		require.NotPanics(t, func() {
			svc.ResetInternal500Counter(context.Background(), "[test]", 42)
		})
		require.Len(t, cache.resetCalls, 1)
		require.Equal(t, int64(42), cache.resetCalls[0])
	})

	t.Run("正常调用 → 调用 ResetInternal500Count", func(t *testing.T) {
		cache := &mockInternal500Cache{}
		svc := newInternal500Health(nil, cache)

		svc.ResetInternal500Counter(context.Background(), "[test]", 99)

		require.Len(t, cache.resetCalls, 1)
		require.Equal(t, int64(99), cache.resetCalls[0])
	})
}

// newInternal500Health retains the supplied storage/counter fixtures and
// installs no-op diagnostics. Publisher and model expansion remain unset, so
// callers use this helper only for counter and penalty paths.
func newInternal500Health(store AntigravityHealthStore, counter Internal500CounterCache) *AntigravityHealth {
	noop := func(string, ...any) {}
	return &AntigravityHealth{Store: store, Counter: counter, Error: noop, Warn: noop, Info: noop, Logf: noop}
}

// TestSetModelRateLimitByModelName_UsesOfficialModelID supplies three model
// IDs and empty input. It checks exact stored key, row/deadline, success
// result, and no capture for the empty-name case.
func TestSetModelRateLimitByModelName_UsesOfficialModelID(t *testing.T) {
	tests := []struct {
		name             string // model storage case name
		modelName        string // input upstream model identifier
		expectedModelKey string // expected exact stored key
		expectedSuccess  bool   // expected storage success result
	}{
		{
			name:             "claude-sonnet-4-5 should be stored as-is",
			modelName:        "claude-sonnet-4-5",
			expectedModelKey: "claude-sonnet-4-5",
			expectedSuccess:  true,
		},
		{
			name:             "gemini-3-pro-high should be stored as-is",
			modelName:        "gemini-3-pro-high",
			expectedModelKey: "gemini-3-pro-high",
			expectedSuccess:  true,
		},
		{
			name:             "gemini-3-flash should be stored as-is",
			modelName:        "gemini-3-flash",
			expectedModelKey: "gemini-3-flash",
			expectedSuccess:  true,
		},
		{
			name:             "empty model name should fail",
			modelName:        "",
			expectedModelKey: "",
			expectedSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &antigravityHealthStoreFixture{}
			resetAt := time.Now().Add(30 * time.Second)

			success := SetModelRateLimitByModelName(
				context.Background(),
				repo,
				123, // providerID
				tt.modelName,
				"[test]",
				429,
				resetAt,
				false, // afterSmartRetry
				func(string, ...any) {},
			)

			require.Equal(t, tt.expectedSuccess, success)

			if tt.expectedSuccess {
				require.Len(t, repo.modelRateLimitCalls, 1)
				call := repo.modelRateLimitCalls[0]
				require.Equal(t, int64(123), call.providerID)
				// The storage key is the upstream model ID.
				require.Equal(t, tt.expectedModelKey, call.modelKey, "should store official model ID, not scope")
				require.WithinDuration(t, resetAt, call.resetAt, time.Second)
			} else {
				require.Empty(t, repo.modelRateLimitCalls)
			}
		})
	}
}

// TestSetModelRateLimitByModelName_NotConvertToScope submits the configured
// upstream model ID after smart retry. It checks accepted storage under that
// exact key and rejection of the separate scope spelling in the capture.
func TestSetModelRateLimitByModelName_NotConvertToScope(t *testing.T) {
	repo := &antigravityHealthStoreFixture{}
	resetAt := time.Now().Add(30 * time.Second)

	// Pass the upstream model ID directly.
	success := SetModelRateLimitByModelName(
		context.Background(),
		repo,
		456,
		"claude-sonnet-4-5", // Upstream model ID.
		"[test]",
		429,
		resetAt,
		true, // afterSmartRetry
		func(string, ...any) {},
	)

	require.True(t, success)
	require.Len(t, repo.modelRateLimitCalls, 1)

	call := repo.modelRateLimitCalls[0]
	// The storage key remains claude-sonnet-4-5.
	require.Equal(t, "claude-sonnet-4-5", call.modelKey, "should NOT convert to scope like claude_sonnet")
	require.NotEqual(t, "claude_sonnet", call.modelKey, "should NOT be scope")
}

// TestUpdateProviderModelRateLimitInCache_UpdatesExtraAndCallsCache starts
// with absent Extra and a configured publisher. It checks created model
// timestamps and one captured publication carrying the expected row ID.
func TestUpdateProviderModelRateLimitInCache_UpdatesExtraAndCallsCache(t *testing.T) {
	cache := &antigravityPublicationFixture{}
	svc := &AntigravityHealth{Publish: cache.publish}

	provider := &Record{
		ID:       100,
		Name:     "test-provider",
		Platform: capability.PlatformAntigravity,
	}
	modelKey := "claude-sonnet-4-5"
	resetAt := time.Now().Add(30 * time.Second)

	svc.UpdateProviderModelRateLimitInCache(context.Background(), provider, modelKey, resetAt)

	// The record contains the model rate limit.
	require.NotNil(t, provider.Extra)
	limits, ok := provider.Extra["model_rate_limits"].(map[string]any)
	require.True(t, ok)
	modelLimit, ok := limits[modelKey].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, modelLimit["rate_limited_at"])
	require.NotEmpty(t, modelLimit["rate_limit_reset_at"])

	// The publisher receives the updated provider.
	require.Len(t, cache.setProviderCalls, 1)
	require.Equal(t, provider.ID, cache.setProviderCalls[0].ID)
}

// TestUpdateProviderModelRateLimitInCache_NilSchedulerSnapshot supplies no
// publisher with absent Extra. It checks that the method returns normally and
// leaves Extra nil, covering the pre-mutation publication gate.
func TestUpdateProviderModelRateLimitInCache_NilSchedulerSnapshot(t *testing.T) {
	svc := &AntigravityHealth{}

	provider := &Record{ID: 1, Name: "test"}

	// A nil publisher is safe.
	svc.UpdateProviderModelRateLimitInCache(context.Background(), provider, "claude-sonnet-4-5", time.Now().Add(30*time.Second))

	// Returning before publication also leaves Extra unchanged.
	require.Nil(t, provider.Extra)
}

// TestUpdateProviderModelRateLimitInCache_PreservesExistingExtra adds a model
// limit beside an existing map entry. It checks the unrelated value and both
// model keys remain present in the caller record.
func TestUpdateProviderModelRateLimitInCache_PreservesExistingExtra(t *testing.T) {
	cache := &antigravityPublicationFixture{}
	svc := &AntigravityHealth{Publish: cache.publish}

	provider := &Record{
		ID:       200,
		Name:     "test-provider",
		Platform: capability.PlatformAntigravity,
		Extra: map[string]any{
			"existing_key": "existing_value",
			"model_rate_limits": map[string]any{
				"gemini-3-flash": map[string]any{
					"rate_limited_at":     "2024-01-01T00:00:00Z",
					"rate_limit_reset_at": "2024-01-01T00:05:00Z",
				},
			},
		},
	}

	svc.UpdateProviderModelRateLimitInCache(context.Background(), provider, "claude-sonnet-4-5", time.Now().Add(30*time.Second))

	// Existing Extra fields remain unchanged.
	require.Equal(t, "existing_value", provider.Extra["existing_key"])
	limits := assertion.MustType[map[string]any](provider.Extra["model_rate_limits"])
	require.NotNil(t, limits["gemini-3-flash"])
	require.NotNil(t, limits["claude-sonnet-4-5"])
}

// publish appends the supplied borrowed record pointer and returns success.
// Tests serialize capture access and keep records stable while inspecting
// them; this fixture copies no publication snapshot.
func (s *antigravityPublicationFixture) publish(_ context.Context, v *Record) error {
	s.setProviderCalls = append(s.setProviderCalls, v)
	return nil
}
