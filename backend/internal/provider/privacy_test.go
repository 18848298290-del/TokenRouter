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

// privacyLifecycleStore counts writes to detect persistence after a canceled operation returns late.
type privacyLifecycleStore struct{ writes atomic.Int32 }

// TestForceOpenAIPrivacy_SkipsShadow checks that shadow privacy is left to the parent.
// The service has no dependencies configured and returns an empty result.
func TestForceOpenAIPrivacy_SkipsShadow(t *testing.T) {
	svc := NewPrivacyService(nil, nil, PrivacyOptions{})
	pid := int64(1)
	shadow := &Record{ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ParentProviderID: &pid}
	require.Equal(t, "", svc.ForceOpenAIPrivacy(context.Background(), shadow), "影子隐私设置应跳过")
}

func applyAntigravitySubscriptionResult(provider *Record, result AntigravitySubscriptionResult) (map[string]any, map[string]any) {
	credentials := make(map[string]any)
	maps.Copy(credentials, provider.Credentials)
	credentials["plan_type"] = result.PlanType

	extra := make(map[string]any)
	maps.Copy(extra, provider.Extra)
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

func (s *privacyLifecycleStore) UpdatePrivacyModeIfUnchanged(context.Context, UsageObservationVersion, string) (bool, error) {
	s.writes.Add(1)
	return true, nil
}

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
