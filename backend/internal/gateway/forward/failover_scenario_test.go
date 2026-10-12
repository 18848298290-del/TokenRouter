package forward

// These scenarios check failure values with the gateway/failover state machine and telemetry observers.

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// mockTempUnscheduler records temporary-exclusion calls.
type mockTempUnscheduler struct {
	calls []tempUnscheduleCall
}

type tempUnscheduleCall struct {
	providerID  int64
	failoverErr *UpstreamFailoverError
}

// TestSameProviderRetryDelayFor checks transient-capacity backoff and fixed waits for other failures.
func TestSameProviderRetryDelayFor(t *testing.T) {
	capacityErr := &UpstreamFailoverError{RequestScopedTransient: true}

	for _, tt := range []struct {
		name       string
		retryCount int
		want       time.Duration
	}{
		{name: "first retry", retryCount: 1, want: 500 * time.Millisecond},
		{name: "second retry", retryCount: 2, want: time.Second},
		{name: "third retry", retryCount: 3, want: 2 * time.Second},
		{name: "fourth retry", retryCount: 4, want: 4 * time.Second},
		{name: "fifth retry", retryCount: 5, want: 8 * time.Second},
		{name: "capped retry", retryCount: 10, want: 8 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				require.Equal(t, tt.want, failover.SameProviderRetryDelayFor(capacityErr.RetryFailure(), tt.retryCount))
			})
		})
	}

	t.Run("ordinary error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			require.Equal(t, failover.SameProviderRetryDelay, failover.SameProviderRetryDelayFor((&UpstreamFailoverError{}).RetryFailure(), 10))
		})
	})
	t.Run("nil error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			require.Equal(t, failover.SameProviderRetryDelay, failover.SameProviderRetryDelayFor((*UpstreamFailoverError)(nil).RetryFailure(), 10))
		})
	})

	t.Run("explicit oauth delay wins", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			err := &UpstreamFailoverError{SameProviderRetryDelay: 3 * time.Second}
			require.Equal(t, 3*time.Second, failover.SameProviderRetryDelayFor(err.RetryFailure(), 1))
		})
	})
}

func TestSameProviderRetryAllowedUsesDeadlineInsteadOfPoolCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		err := &UpstreamFailoverError{
			RetryableOnSameProvider:   true,
			SameProviderRetryDeadline: time.Now().Add(time.Minute),
		}
		require.True(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 100, 0))
		require.True(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 100, failover.MaxSameProviderRetries))
		err.SameProviderRetryDeadline = time.Now().Add(-time.Second)
		require.False(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 0, 100))
	})
}

func TestSameProviderRetryAllowedRequiresOptInAndDefaultsToCountLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		err := &UpstreamFailoverError{SameProviderRetryDeadline: time.Now().Add(time.Minute)}
		require.False(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 0, failover.MaxSameProviderRetries))

		err.RetryableOnSameProvider = true
		err.SameProviderRetryDeadline = time.Time{}
		require.True(t, failover.SameProviderRetryAllowed(err.RetryFailure(), failover.MaxSameProviderRetries-1, failover.MaxSameProviderRetries))
		require.False(t, failover.SameProviderRetryAllowed(err.RetryFailure(), failover.MaxSameProviderRetries, failover.MaxSameProviderRetries))
	})
}

func TestSameProviderRetryAllowedHonorsErrorMaxBeforeDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		err := &UpstreamFailoverError{
			RetryableOnSameProvider:   true,
			SameProviderRetryDeadline: time.Now().Add(time.Minute),
			SameProviderRetryMax:      1,
		}
		require.True(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 0, failover.MaxSameProviderRetries))
		require.False(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 1, failover.MaxSameProviderRetries))
		require.False(t, failover.SameProviderRetryAllowed(err.RetryFailure(), 0, 0), "an explicit zero retry budget remains disabled")
	})
}

func TestSameProviderRetryDeadlineAllows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require.True(t, failover.SameProviderRetryDeadlineAllows((&UpstreamFailoverError{}).RetryFailure()))
		require.True(t, failover.SameProviderRetryDeadlineAllows((&UpstreamFailoverError{
			SameProviderRetryDeadline: time.Now().Add(time.Second),
		}).RetryFailure()))
		require.False(t, failover.SameProviderRetryDeadlineAllows((&UpstreamFailoverError{
			SameProviderRetryDeadline: time.Now().Add(-time.Second),
		}).RetryFailure()))
	})
}

func TestEffectiveSameProviderRetryLimitHonorsErrorCapAndDisabledProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &providercore.Record{Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_count": float64(3)}}
		require.Equal(t, 1, failover.EffectiveSameProviderRetryLimit((&UpstreamFailoverError{SameProviderRetryMax: 1}).RetryFailure(), provider.GetPoolModeRetryCount()))
		provider.Credentials["pool_mode_retry_count"] = float64(0)
		require.Equal(t, 0, failover.EffectiveSameProviderRetryLimit((&UpstreamFailoverError{SameProviderRetryMax: 1}).RetryFailure(), provider.GetPoolModeRetryCount()))
	})
}

func TestNewFailoverState(t *testing.T) {
	t.Run("初始化字段正确", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](5, true, gatewaytelemetry.Failover)
			require.Equal(t, 5, fs.MaxSwitches)
			require.Equal(t, 0, fs.SwitchCount)
			require.NotNil(t, fs.FailedProviderIDs)
			require.Empty(t, fs.FailedProviderIDs)
			require.NotNil(t, fs.SameProviderRetryCount)
			require.Empty(t, fs.SameProviderRetryCount)
			require.Nil(t, fs.LastFailoverErr)
			require.False(t, fs.ForceCacheBilling)
			require.True(t, fs.HasBoundSession)
		})
	})

	t.Run("无绑定会话", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			require.Equal(t, 3, fs.MaxSwitches)
			require.False(t, fs.HasBoundSession)
		})
	})

	t.Run("零最大切换次数", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](0, false, gatewaytelemetry.Failover)
			require.Equal(t, 0, fs.MaxSwitches)
		})
	})
}

func TestHandleFailoverError_BasicSwitch(t *testing.T) {
	t.Run("显式停止不切换提供商且旧错误默认仍切换", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			stopErr := &UpstreamFailoverError{
				Stage:              GatewayFailureStageProviderAuth,
				Scope:              GatewayFailureScopeShared,
				NextProviderAction: NextProviderStop,
			}

			action := fs.HandleFailoverError(context.Background(), mock, 100, capability.PlatformGrok, failover.MaxSameProviderRetries, stopErr)

			require.Equal(t, failover.FailoverExhausted, action)
			require.Zero(t, fs.SwitchCount)
			require.Empty(t, fs.FailedProviderIDs)
			require.Equal(t, stopErr, fs.LastFailoverErr)

			legacyErr := newTestFailoverErr(429, false, false)
			action = fs.HandleFailoverError(context.Background(), mock, 100, capability.PlatformGrok, failover.MaxSameProviderRetries, legacyErr)

			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SwitchCount)
			require.Contains(t, fs.FailedProviderIDs, int64(100))
		})
	})

	t.Run("已取消的认证失败不改变切换状态", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := &UpstreamFailoverError{
				Stage:              GatewayFailureStageProviderAuth,
				Scope:              GatewayFailureScopeProvider,
				NextProviderAction: NextProviderRetry,
			}

			action := fs.HandleFailoverError(ctx, mock, 101, capability.PlatformGrok, failover.MaxSameProviderRetries, err)

			require.Equal(t, failover.FailoverCanceled, action)
			require.Zero(t, fs.SwitchCount)
			require.Empty(t, fs.FailedProviderIDs)
			require.Nil(t, fs.LastFailoverErr)
			require.Empty(t, mock.calls)
		})
	})

	t.Run("非重试错误_非Antigravity_直接切换", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, false, false)

			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)

			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SwitchCount)
			require.Contains(t, fs.FailedProviderIDs, int64(100))
			require.Equal(t, err, fs.LastFailoverErr)
			require.False(t, fs.ForceCacheBilling)
			require.Empty(t, mock.calls, "不应调用 TempUnschedule")
		})
	})

	t.Run("非重试错误_Antigravity_第一次切换无延迟", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// The first Antigravity switch has no delay.
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, false, false)

			start := time.Now()
			action := fs.HandleFailoverError(context.Background(), mock, 100, capability.PlatformAntigravity, failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SwitchCount)
			require.Less(t, elapsed, 200*time.Millisecond, "第一次切换延迟应为 0")
		})
	})

	t.Run("非重试错误_Antigravity_第二次切换有1秒延迟", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// The second Antigravity switch waits one second.
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.SwitchCount = 1 // One previous switch.

			err := newTestFailoverErr(500, false, false)
			start := time.Now()
			action := fs.HandleFailoverError(context.Background(), mock, 200, capability.PlatformAntigravity, failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 2, fs.SwitchCount)
			require.GreaterOrEqual(t, elapsed, 800*time.Millisecond, "第二次切换延迟应约 1s")
			require.Less(t, elapsed, 3*time.Second)
		})
	})

	t.Run("连续切换直到耗尽", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](2, false, gatewaytelemetry.Failover)

			// First switch.
			err1 := newTestFailoverErr(500, false, false)
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err1)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SwitchCount)

			// Second switch.
			err2 := newTestFailoverErr(502, false, false)
			action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, err2)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 2, fs.SwitchCount)

			// The third failure exhausts the two-switch budget.
			err3 := newTestFailoverErr(503, false, false)
			action = fs.HandleFailoverError(context.Background(), mock, 300, "openai", failover.MaxSameProviderRetries, err3)
			require.Equal(t, failover.FailoverExhausted, action)
			require.Equal(t, 2, fs.SwitchCount, "耗尽时不应继续递增")

			require.Len(t, fs.FailedProviderIDs, 3)
			require.Contains(t, fs.FailedProviderIDs, int64(100))
			require.Contains(t, fs.FailedProviderIDs, int64(200))
			require.Contains(t, fs.FailedProviderIDs, int64(300))

			require.Equal(t, err3, fs.LastFailoverErr)
		})
	})

	t.Run("MaxSwitches为0时首次即耗尽", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](0, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, false, false)

			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverExhausted, action)
			require.Equal(t, 0, fs.SwitchCount)
			require.Contains(t, fs.FailedProviderIDs, int64(100))
		})
	})
}

func TestHandleFailoverError_CacheBilling(t *testing.T) {
	t.Run("hasBoundSession为true且实际切换时设置ForceCacheBilling", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, true, gatewaytelemetry.Failover) // hasBoundSession=true
			err := newTestFailoverErr(500, false, false)

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.True(t, fs.ForceCacheBilling)
		})
	})

	t.Run("同提供商重试时仅凭hasBoundSession不设置ForceCacheBilling", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, true, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)

			require.False(t, fs.ForceCacheBilling)
			require.Zero(t, fs.SwitchCount)
		})
	})

	t.Run("OAuth deadline存在时不按普通计数切换", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, true, gatewaytelemetry.Failover)
			fs.SameProviderRetryCount[100] = failover.MaxSameProviderRetries

			err := newTestFailoverErr(429, true, false)
			err.SameProviderRetryDeadline = time.Now().Add(time.Minute)
			err.SameProviderRetryDelay = time.Nanosecond

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)

			require.False(t, fs.ForceCacheBilling)
			require.Zero(t, fs.SwitchCount)
			require.Equal(t, failover.MaxSameProviderRetries+
				1, fs.SameProviderRetryCount[100])
			require.Empty(t, mock.calls)
		})
	})
	t.Run("同提供商重试耗尽并实际切换时设置ForceCacheBilling", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, true, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			for range failover.MaxSameProviderRetries {
				fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
				require.False(t, fs.ForceCacheBilling)
			}
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)

			require.True(t, fs.ForceCacheBilling)
			require.Equal(t, 1, fs.SwitchCount)
		})
	})

	t.Run("failoverErr.ForceCacheBilling为true时设置", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, false, true) // ForceCacheBilling=true

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.True(t, fs.ForceCacheBilling)
		})
	})

	t.Run("同提供商重试保留显式ForceCacheBilling", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, true, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, true)

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)

			require.True(t, fs.ForceCacheBilling)
			require.Zero(t, fs.SwitchCount)
		})
	})

	t.Run("两者均为false时不设置", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, false, false)

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.False(t, fs.ForceCacheBilling)
		})
	})

	t.Run("一旦设置不会被后续错误重置", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)

			// The first failure enables forced cache billing.
			err1 := newTestFailoverErr(500, false, true)
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err1)
			require.True(t, fs.ForceCacheBilling)

			// Later failures cannot reset forced cache billing.
			err2 := newTestFailoverErr(502, false, false)
			fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, err2)
			require.True(t, fs.ForceCacheBilling, "ForceCacheBilling 一旦设置不应被重置")
		})
	})
}

func TestHandleFailoverError_SameProviderRetry(t *testing.T) {
	t.Run("第一次重试返回FailoverContinue", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			start := time.Now()
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[100])
			require.Equal(t, 0, fs.SwitchCount, "同提供商重试不应增加切换计数")
			require.NotContains(t, fs.FailedProviderIDs, int64(100), "同提供商重试不应加入失败列表")
			require.Empty(t, mock.calls, "同提供商重试期间不应调用 TempUnschedule")
			// Check the default 500 ms same-provider delay.
			require.GreaterOrEqual(t, elapsed, 400*time.Millisecond)
			require.Less(t, elapsed, 2*time.Second)
		})
	})

	t.Run("达到最大重试次数前均返回FailoverContinue", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			for i := 1; i <=
				failover.MaxSameProviderRetries; i++ {
				action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
				require.Equal(t, failover.FailoverContinue, action)
				require.Equal(t, i, fs.SameProviderRetryCount[100])
			}

			require.Empty(t, mock.calls, "达到最大重试次数前均不应调用 TempUnschedule")
		})
	})

	t.Run("超过最大重试次数后触发TempUnschedule并切换", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			for range failover.MaxSameProviderRetries {
				fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			}
			require.Equal(t, failover.MaxSameProviderRetries, fs.SameProviderRetryCount[100])

			// After the same-provider retry budget is spent, switch providers.
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SwitchCount)
			require.Contains(t, fs.FailedProviderIDs, int64(100))

			require.Len(t, mock.calls, 1)
			require.Equal(t, int64(100), mock.calls[0].providerID)
			require.Equal(t, err, mock.calls[0].failoverErr)
		})
	})

	t.Run("不同提供商独立跟踪重试次数", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](5, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			// Provider 100 uses its first retry.
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[100])

			// Provider 200 has an independent retry count.
			action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[200])
			require.Equal(t, 1, fs.SameProviderRetryCount[100], "提供商 100 的计数不应受影响")
		})
	})

	t.Run("重试耗尽后再次遇到同提供商_直接切换", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](5, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			// Exhaust provider 100 retries.
			for range failover.MaxSameProviderRetries {
				fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			}
			// The next failure switches providers.
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)

			// Revisiting provider 100 retains its exhausted retry count and switches immediately.
			action = fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Len(t, mock.calls, 2, "第二次耗尽也应调用 TempUnschedule")
		})
	})

	t.Run("尊重提供商级retryLimit_配置1次只重试1次", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// A limit of one permits one same-provider retry.
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](5, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(403, true, false)
			const retryLimit = 1

			// First failure retries the same provider.
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", retryLimit, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[100])
			require.Equal(t, 0, fs.SwitchCount, "首次重试不应切换提供商")
			require.Empty(t, mock.calls, "未耗尽前不应 TempUnschedule")

			// The next failure excludes the provider and switches.
			action = fs.HandleFailoverError(context.Background(), mock, 100, "openai", retryLimit, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[100], "重试计数不应超过 retryLimit")
			require.Equal(t, 1, fs.SwitchCount, "重试耗尽应切换提供商")
			require.Contains(t, fs.FailedProviderIDs, int64(100))
			require.Len(t, mock.calls, 1, "重试耗尽应触发 TempUnschedule")
		})
	})

	t.Run("retryLimit为0时立即切换不重试", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// A zero pool retry limit disables same-provider retries.
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](5, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(403, true, false)

			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 0, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 0, fs.SameProviderRetryCount[100], "retryLimit=0 不应发生同提供商重试")
			require.Equal(t, 1, fs.SwitchCount, "应立即切换提供商")
			require.Len(t, mock.calls, 1, "应立即 TempUnschedule")
		})
	})
}

func TestHandleFailoverError_TempUnschedule(t *testing.T) {
	t.Run("非重试错误不调用TempUnschedule", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, false, false) // RetryableOnSameProvider=false

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Empty(t, mock.calls)
		})
	})

	t.Run("重试错误耗尽后调用TempUnschedule_传入正确参数", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(502, true, false)

			for range failover.MaxSameProviderRetries {
				fs.HandleFailoverError(context.Background(), mock, 42, "openai", failover.MaxSameProviderRetries, err)
			}
			// The next failure excludes and switches after the retry budget is spent.
			fs.HandleFailoverError(context.Background(), mock, 42, "openai", failover.MaxSameProviderRetries, err)

			require.Len(t, mock.calls, 1)
			require.Equal(t, int64(42), mock.calls[0].providerID)
			require.Equal(t, 502, mock.calls[0].failoverErr.StatusCode)
			require.True(t, mock.calls[0].failoverErr.RetryableOnSameProvider)
		})
	})
}

func TestHandleFailoverError_ContextCanceled(t *testing.T) {
	t.Run("同提供商重试sleep期间context取消", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(400, true, false)

			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				time.Sleep(30 * time.Millisecond)
				cancel() // Cancel during the wait, after the entry check.
			}()

			start := time.Now()
			action := fs.HandleFailoverError(ctx, mock, 100, "openai", failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverCanceled, action)
			require.Less(t, elapsed, 400*time.Millisecond, "sleep 应被取消打断")
			// Cancellation during retry waiting occurs after the retry counter increments.
			require.Equal(t, 1, fs.SameProviderRetryCount[100])
		})
	})

	t.Run("入口即已取消_不改动任何failover状态", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(520, false, false)

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Cancel before the call.

			start := time.Now()
			action := fs.HandleFailoverError(ctx, mock, 100, "openai", failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverCanceled, action)
			require.Less(t, elapsed, 100*time.Millisecond, "应立即返回")
			// Cancellation at entry leaves failover state unchanged.
			require.Equal(t, 0, fs.SwitchCount, "取消的请求不应计入切换")
			require.Equal(t, 0, fs.SameProviderRetryCount[100], "取消的请求不应改动重试计数")
			require.NotContains(t, fs.FailedProviderIDs, int64(100))
			require.Nil(t, fs.LastFailoverErr)
			require.Empty(t, mock.calls, "不应触发 TempUnschedule")
		})
	})

	t.Run("Antigravity延迟期间context取消", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.SwitchCount = 1 // The next switch would wait one second.
			err := newTestFailoverErr(500, false, false)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			start := time.Now()
			action := fs.HandleFailoverError(ctx, mock, 100, capability.PlatformAntigravity, failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverCanceled, action)
			require.Less(t, elapsed, 100*time.Millisecond, "应立即返回而非等待 1s")
		})
	})
}

func TestHandleFailoverError_FailedProviderIDs(t *testing.T) {
	t.Run("切换时添加到失败列表", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, newTestFailoverErr(500, false, false))
			require.Contains(t, fs.FailedProviderIDs, int64(100))

			fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, newTestFailoverErr(502, false, false))
			require.Contains(t, fs.FailedProviderIDs, int64(200))
			require.Len(t, fs.FailedProviderIDs, 2)
		})
	})

	t.Run("耗尽时也添加到失败列表", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](0, false, gatewaytelemetry.Failover)

			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, newTestFailoverErr(500, false, false))
			require.Equal(t, failover.FailoverExhausted, action)
			require.Contains(t, fs.FailedProviderIDs, int64(100))
		})
	})

	t.Run("同提供商重试期间不添加到失败列表", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)

			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, newTestFailoverErr(400, true, false))
			require.Equal(t, failover.FailoverContinue, action)
			require.NotContains(t, fs.FailedProviderIDs, int64(100))
		})
	})

	t.Run("同一提供商多次切换不重复添加", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](5, false, gatewaytelemetry.Failover)

			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, newTestFailoverErr(500, false, false))
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, newTestFailoverErr(500, false, false))
			require.Len(t, fs.FailedProviderIDs, 1, "map 天然去重")
		})
	})
}

func TestHandleFailoverError_LastFailoverErr(t *testing.T) {
	t.Run("每次调用都更新LastFailoverErr", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)

			err1 := newTestFailoverErr(500, false, false)
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err1)
			require.Equal(t, err1, fs.LastFailoverErr)

			err2 := newTestFailoverErr(502, false, false)
			fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, err2)
			require.Equal(t, err2, fs.LastFailoverErr)
		})
	})

	t.Run("同提供商重试时也更新LastFailoverErr", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)

			err := newTestFailoverErr(400, true, false)
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, err, fs.LastFailoverErr)
		})
	})
}

func TestHandleFailoverError_IntegrationScenario(t *testing.T) {
	t.Run("模拟完整failover流程_多提供商混合重试与切换", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, true, gatewaytelemetry.Failover) // hasBoundSession=true

			// Provider 100 consumes its same-provider retry budget.
			retryErr := newTestFailoverErr(400, true, false)
			for range failover.MaxSameProviderRetries {
				action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, retryErr)
				require.Equal(t, failover.FailoverContinue, action)
				require.False(t, fs.ForceCacheBilling, "同提供商重试期间不应仅因绑定会话强制缓存计费")
			}

			// The next failure excludes provider 100 and switches.
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, retryErr)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SwitchCount)
			require.True(t, fs.ForceCacheBilling, "实际切换提供商时应设置 ForceCacheBilling")
			require.Len(t, mock.calls, 1)

			// Provider 200 fails without same-provider retry eligibility.
			switchErr := newTestFailoverErr(500, false, false)
			action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, switchErr)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 2, fs.SwitchCount)

			// Provider 300 also switches immediately.
			action = fs.HandleFailoverError(context.Background(), mock, 300, "openai", failover.MaxSameProviderRetries, switchErr)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 3, fs.SwitchCount)

			// Provider 400 reaches the three-switch limit.
			action = fs.HandleFailoverError(context.Background(), mock, 400, "openai", failover.MaxSameProviderRetries, switchErr)
			require.Equal(t, failover.FailoverExhausted, action)

			require.Equal(t, 3, fs.SwitchCount, "耗尽时不再递增")
			require.Len(t, fs.FailedProviderIDs, 4, "4个不同提供商都在失败列表中")
			require.True(t, fs.ForceCacheBilling)
			require.Len(t, mock.calls, 1, "只有提供商 100 触发了 TempUnschedule")
		})
	})

	t.Run("模拟Antigravity平台完整流程", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](2, false, gatewaytelemetry.Failover)

			err := newTestFailoverErr(500, false, false)

			// The first switch has no delay.
			start := time.Now()
			action := fs.HandleFailoverError(context.Background(), mock, 100, capability.PlatformAntigravity, failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)
			require.Equal(t, failover.FailoverContinue, action)
			require.Less(t, elapsed, 200*time.Millisecond, "第一次切换延迟为 0")

			// The second switch waits one second.
			start = time.Now()
			action = fs.HandleFailoverError(context.Background(), mock, 200, capability.PlatformAntigravity, failover.MaxSameProviderRetries, err)
			elapsed = time.Since(start)
			require.Equal(t, failover.FailoverContinue, action)
			require.GreaterOrEqual(t, elapsed, 800*time.Millisecond, "第二次切换延迟约 1s")

			// Exhaustion returns before the delay branch.
			start = time.Now()
			action = fs.HandleFailoverError(context.Background(), mock, 300, capability.PlatformAntigravity, failover.MaxSameProviderRetries, err)
			elapsed = time.Since(start)
			require.Equal(t, failover.FailoverExhausted, action)
			require.Less(t, elapsed, 200*time.Millisecond, "耗尽时不应有延迟")
		})
	})

	t.Run("ForceCacheBilling通过错误标志设置", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover) // hasBoundSession=false

			// The first failure leaves forced cache billing disabled.
			err1 := newTestFailoverErr(500, false, false)
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err1)
			require.False(t, fs.ForceCacheBilling)

			// An explicit failure flag enables forced cache billing.
			err2 := newTestFailoverErr(500, false, true)
			fs.HandleFailoverError(context.Background(), mock, 200, "openai", failover.MaxSameProviderRetries, err2)
			require.True(t, fs.ForceCacheBilling, "错误标志应触发 ForceCacheBilling")

			// Later failures leave the enabled flag set.
			err3 := newTestFailoverErr(500, false, false)
			fs.HandleFailoverError(context.Background(), mock, 300, "openai", failover.MaxSameProviderRetries, err3)
			require.True(t, fs.ForceCacheBilling, "不应重置")
		})
	})
}

func TestHandleFailoverError_EdgeCases(t *testing.T) {
	t.Run("StatusCode为0的错误也能正常处理", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(0, false, false)

			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
		})
	})

	t.Run("ProviderID为0也能正常跟踪", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, true, false)

			action := fs.HandleFailoverError(context.Background(), mock, 0, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[0])
		})
	})

	t.Run("负ProviderID也能正常跟踪", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			err := newTestFailoverErr(500, true, false)

			action := fs.HandleFailoverError(context.Background(), mock, -1, "openai", failover.MaxSameProviderRetries, err)
			require.Equal(t, failover.FailoverContinue, action)
			require.Equal(t, 1, fs.SameProviderRetryCount[-1])
		})
	})

	t.Run("空平台名称不触发Antigravity延迟", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockTempUnscheduler{}
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.SwitchCount = 1
			err := newTestFailoverErr(500, false, false)

			start := time.Now()
			action := fs.HandleFailoverError(context.Background(), mock, 100, "", failover.MaxSameProviderRetries, err)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverContinue, action)
			require.Less(t, elapsed, 200*time.Millisecond, "空平台不应触发 Antigravity 延迟")
		})
	})
}

func TestHandleSelectionExhausted(t *testing.T) {
	t.Run("无LastFailoverErr时返回Exhausted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			// No previous failure.

			action := fs.HandleSelectionExhausted(context.Background())
			require.Equal(t, failover.FailoverExhausted, action)
		})
	})

	t.Run("非503错误返回Exhausted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.LastFailoverErr = newTestFailoverErr(500, false, false)

			action := fs.HandleSelectionExhausted(context.Background())
			require.Equal(t, failover.FailoverExhausted, action)
		})
	})

	t.Run("503且未耗尽_等待后返回Continue并清除失败列表", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.LastFailoverErr = newTestFailoverErr(503, false, false)
			fs.FailedProviderIDs[100] = struct{}{}
			fs.SwitchCount = 1

			start := time.Now()
			action := fs.HandleSelectionExhausted(context.Background())
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverContinue, action)
			require.Empty(t, fs.FailedProviderIDs, "应清除失败提供商列表")
			require.GreaterOrEqual(t, elapsed, 1500*time.Millisecond, "应等待约 2s")
			require.Less(t, elapsed, 5*time.Second)
		})
	})

	t.Run("503但SwitchCount已超过MaxSwitches_返回Exhausted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](2, false, gatewaytelemetry.Failover)
			fs.LastFailoverErr = newTestFailoverErr(503, false, false)
			fs.SwitchCount = 3 // > MaxSwitches(2)

			start := time.Now()
			action := fs.HandleSelectionExhausted(context.Background())
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverExhausted, action)
			require.Less(t, elapsed, 100*time.Millisecond, "不应等待")
		})
	})

	t.Run("503但context已取消_返回Canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.LastFailoverErr = newTestFailoverErr(503, false, false)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			start := time.Now()
			action := fs.HandleSelectionExhausted(ctx)
			elapsed := time.Since(start)

			require.Equal(t, failover.FailoverCanceled, action)
			require.Less(t, elapsed, 100*time.Millisecond, "应立即返回")
		})
	})

	t.Run("context已取消_非503也返回Canceled而非Exhausted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// Canceled selection ends as cancellation rather than provider exhaustion (#4257).
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)
			fs.LastFailoverErr = newTestFailoverErr(520, false, false)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			action := fs.HandleSelectionExhausted(ctx)
			require.Equal(t, failover.FailoverCanceled, action)
		})
	})

	t.Run("context已取消_无LastFailoverErr也返回Canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](3, false, gatewaytelemetry.Failover)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			action := fs.HandleSelectionExhausted(ctx)
			require.Equal(t, failover.FailoverCanceled, action)
		})
	})

	t.Run("503且SwitchCount等于MaxSwitches_仍可重试", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fs := failover.NewFailoverState[*UpstreamFailoverError](2, false, gatewaytelemetry.Failover)
			fs.LastFailoverErr = newTestFailoverErr(503, false, false)
			fs.SwitchCount = 2 // Equality is allowed by the selection-retry budget.

			action := fs.HandleSelectionExhausted(context.Background())
			require.Equal(t, failover.FailoverContinue, action)
		})
	})
}

func (m *mockTempUnscheduler) TempUnscheduleRetryableError(_ context.Context, providerID int64, failoverErr *UpstreamFailoverError) {
	m.calls = append(m.calls, tempUnscheduleCall{providerID: providerID, failoverErr: failoverErr})
}

func newTestFailoverErr(statusCode int, retryable, forceBilling bool) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:              statusCode,
		RetryableOnSameProvider: retryable,
		ForceCacheBilling:       forceBilling,
	}
}
