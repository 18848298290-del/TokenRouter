package provider

import (
	"context"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// privacyLifecycleStore counts conditional write attempts after upstream privacy
// callbacks return, allowing lifecycle tests to detect canceled observations.
type privacyLifecycleStore struct {
	writes atomic.Int32 // conditional write attempt count
}

// TestForceOpenAIPrivacy_SkipsShadow supplies a credential shadow to a service
// with no privacy callbacks or stores configured. It checks that the force call
// returns an empty result for that fixture.
func TestForceOpenAIPrivacy_SkipsShadow(t *testing.T) {
	svc := NewPrivacyService(nil, nil, PrivacyOptions{})
	pid := int64(1)
	shadow := &Record{ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ParentProviderID: &pid}
	require.Equal(t, "", svc.ForceOpenAIPrivacy(context.Background(), shadow), "影子隐私设置应跳过")
}

// applyAntigravitySubscriptionResult builds shallow credential and Extra copies
// from a nonnil test record, then applies plan and subscription observations.
// Empty status or error strings remove those fields; nested values remain shared.
func applyAntigravitySubscriptionResult(provider *Record, result AntigravitySubscriptionResult) (map[string]any, map[string]any) {
	credentials := make(map[string]any)
	maps.Copy(credentials, provider.Credentials)
	credentials["plan_type"] = result.PlanType

	extra := make(map[string]any)
	maps.Copy(extra, provider.Extra)
	// Apply status and error together from this subscription result. Empty values
	// remove stale observations while unrelated Extra entries, including
	// privacy_mode, remain copied.
	if result.SubscriptionStatus != "" {
		extra["subscription_status"] = result.SubscriptionStatus
	} else {
		delete(extra, "subscription_status")
	}
	if result.SubscriptionError != "" {
		extra["subscription_error"] = result.SubscriptionError
	} else {
		delete(extra, "subscription_error")
	}
	return credentials, extra
}

// TestApplyAntigravityPrivacyMode_SetsInMemoryExtra begins with a record lacking
// Extra and applies privacy_set. It checks that Extra is initialized and contains
// the requested mode after local application.
func TestApplyAntigravityPrivacyMode_SetsInMemoryExtra(t *testing.T) {
	provider := &Record{}

	ApplyAntigravityPrivacyMode(provider, AntigravityPrivacySet)

	if provider.Extra == nil {
		t.Fatal("expected provider.Extra to be initialized")
	}
	if got := provider.Extra["privacy_mode"]; got != AntigravityPrivacySet {
		t.Fatalf("expected privacy_mode %q, got %v", AntigravityPrivacySet, got)
	}
}

// TestApplyAntigravityPrivacyMode_PreservedBySubscriptionResult applies privacy
// before building a subscription update. It checks that the update keeps both
// privacy_set and an unrelated existing Extra entry.
func TestApplyAntigravityPrivacyMode_PreservedBySubscriptionResult(t *testing.T) {
	provider := &Record{
		Credentials: map[string]any{
			"access_token": "token",
		},
		Extra: map[string]any{
			"existing": "value",
		},
	}
	ApplyAntigravityPrivacyMode(provider, AntigravityPrivacySet)

	_, extra := applyAntigravitySubscriptionResult(provider, AntigravitySubscriptionResult{
		PlanType: "Pro",
	})

	if got := extra["privacy_mode"]; got != AntigravityPrivacySet {
		t.Fatalf("expected subscription writeback to keep privacy_mode %q, got %v", AntigravityPrivacySet, got)
	}
	if got := extra["existing"]; got != "value" {
		t.Fatalf("expected existing extra fields to be preserved, got %v", got)
	}
}

// UpdatePrivacyModeIfUnchanged counts the fixture's write attempt and reports
// successful persistence for any supplied version and privacy mode.
func (s *privacyLifecycleStore) UpdatePrivacyModeIfUnchanged(context.Context, UsageObservationVersion, string) (bool, error) {
	// Parallel maintenance may reach this fixture. Count each attempted write
	// atomically so lifecycle assertions observe the total after joining the operation.
	s.writes.Add(1)
	return true, nil
}

// TestPrivacyLifecycleCancelsAndPreventsLateWrites stops an entered upstream
// callback that either honors cancellation or ignores it until released. It checks
// shutdown results, empty late observations, zero writes, rejected later requests,
// and reuse of the first shutdown result.
func TestPrivacyLifecycleCancelsAndPreventsLateWrites(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[ignore], func(t *testing.T) {
			store := &privacyLifecycleStore{}
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan string, 1)
			var calls atomic.Int32
			core := NewPrivacyService(store, nil, PrivacyOptions{OpenAI: func(ctx context.Context, _, _ string) string {
				calls.Add(1)
				close(entered)
				if ignore {
					<-release
				} else {
					<-ctx.Done()
				}
				return "disabled"
			}})
			value := &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}}
			require.Zero(t, calls.Load())
			// Wait for the callback's entry before shutdown so cancellation and deadline
			// assertions concern registered work rather than a request rejected before starting.
			go func() { done <- core.ForceOpenAIPrivacy(context.Background(), value) }()
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := core.StopContext(ctx)
			if ignore {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorContains(t, err, "unfinished")
			} else {
				require.NoError(t, err)
			}
			// Release the cancellation-ignoring branch after StopContext returns, then
			// join its result to verify the late mode was discarded before persistence.
			close(release)
			require.Empty(t, <-done)
			require.Zero(t, store.writes.Load())
			require.Empty(t, core.ForceOpenAIPrivacy(context.Background(), value))
			require.Equal(t, int32(1), calls.Load())
			if ignore {
				require.Same(t, err, core.StopContext(context.Background()))
			} else {
				require.NoError(t, core.StopContext(context.Background()))
			}
		})
	}
}
