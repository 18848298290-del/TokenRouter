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

// antigravityHealthStoreFixture records writes; unimplemented embedded methods panic.
type antigravityHealthStoreFixture struct {
	AntigravityHealthStore
	modelRateLimitCalls []struct {
		providerID int64
		modelKey   string
		resetAt    time.Time
	}
	extraUpdateCalls []struct {
		providerID int64
		updates    map[string]any
	}
}

type mockInternal500Cache struct {
	incrementCount int64
	incrementErr   error
	resetErr       error

	incrementCalls []int64 // Provider IDs passed to IncrementInternal500Count.
	resetCalls     []int64 // Provider IDs passed to ResetInternal500Count.
}

type internal500ProviderRepoStub struct {
	AntigravityHealthStore // Unexpected calls to embedded methods panic.

	tempUnschedCalls []tempUnschedCall
	setErrorCalls    []setErrorCall
}

type tempUnschedCall struct {
	providerID int64
	until      time.Time
	reason     string
}

type setErrorCall struct {
	providerID int64
	reason     string
}

// The publisher records the provider it receives.
type antigravityPublicationFixture struct{ setProviderCalls []*Record }

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

func (s *antigravityHealthStoreFixture) SetModelRateLimit(_ context.Context, id int64, key string, at time.Time, _ ...string) error {
	s.modelRateLimitCalls = append(s.modelRateLimitCalls, struct {
		providerID int64
		modelKey   string
		resetAt    time.Time
	}{id, key, at})
	return nil
}

func (s *antigravityHealthStoreFixture) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	s.extraUpdateCalls = append(s.extraUpdateCalls, struct {
		providerID int64
		updates    map[string]any
	}{id, updates})
	return nil
}

func (m *mockInternal500Cache) IncrementInternal500Count(_ context.Context, providerID int64) (int64, error) {
	m.incrementCalls = append(m.incrementCalls, providerID)
	return m.incrementCount, m.incrementErr
}

func (m *mockInternal500Cache) ResetInternal500Count(_ context.Context, providerID int64) error {
	m.resetCalls = append(m.resetCalls, providerID)
	return m.resetErr
}

func (r *internal500ProviderRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls = append(r.tempUnschedCalls, tempUnschedCall{providerID: id, until: until, reason: reason})
	return nil
}

func (r *internal500ProviderRepoStub) SetError(_ context.Context, id int64, errorMsg string) error {
	r.setErrorCalls = append(r.setErrorCalls, setErrorCall{providerID: id, reason: errorMsg})
	return nil
}

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
		// The deadline is between before+10m and after+10m.
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

// newInternal500Health combines counter and storage fixtures for health assertions.
func newInternal500Health(store AntigravityHealthStore, counter Internal500CounterCache) *AntigravityHealth {
	noop := func(string, ...any) {}
	return &AntigravityHealth{Store: store, Counter: counter, Error: noop, Warn: noop, Info: noop, Logf: noop}
}

// TestSetModelRateLimitByModelName_UsesOfficialModelID checks the stored upstream model ID.
func TestSetModelRateLimitByModelName_UsesOfficialModelID(t *testing.T) {
	tests := []struct {
		name             string
		modelName        string
		expectedModelKey string
		expectedSuccess  bool
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

// TestSetModelRateLimitByModelName_NotConvertToScope protects model IDs from scope conversion.
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

// TestUpdateProviderModelRateLimitInCache_UpdatesExtraAndCallsCache checks record mutation and publication.
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

// TestUpdateProviderModelRateLimitInCache_NilSchedulerSnapshot checks that a nil publisher is safe.
func TestUpdateProviderModelRateLimitInCache_NilSchedulerSnapshot(t *testing.T) {
	svc := &AntigravityHealth{}

	provider := &Record{ID: 1, Name: "test"}

	// A nil publisher is safe.
	svc.UpdateProviderModelRateLimitInCache(context.Background(), provider, "claude-sonnet-4-5", time.Now().Add(30*time.Second))

	// Returning before publication also leaves Extra unchanged.
	require.Nil(t, provider.Extra)
}

// TestUpdateProviderModelRateLimitInCache_PreservesExistingExtra protects unrelated Extra fields.
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

func (s *antigravityPublicationFixture) publish(_ context.Context, v *Record) error {
	s.setProviderCalls = append(s.setProviderCalls, v)
	return nil
}
