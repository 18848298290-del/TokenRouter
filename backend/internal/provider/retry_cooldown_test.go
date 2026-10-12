package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// retryExhaustedCooldownRepoStub records local cooldown writes after same-provider retries are exhausted.
type retryExhaustedCooldownRepoStub struct {
	RetryCooldownStore

	provider  *Record
	tempCalls int
}

type capacityShedProviderRepoStub struct {
	RetryCooldownStore
	// Unexpected calls through the embedded nil interface panic.

	tempUnschedCalls int
}

func (r *retryExhaustedCooldownRepoStub) GetByID(context.Context, int64) (*Record, error) {
	return r.provider, nil
}

func (r *retryExhaustedCooldownRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

// TestTempUnscheduleRetryableError_PoolModeSkipsLegacyCooldown checks that exhausted
// pool-mode retries do not write a local cooldown before failover.
func TestTempUnscheduleRetryableError_PoolModeSkipsLegacyCooldown(t *testing.T) {
	poolProvider := &Record{
		LoadLocation: time.LoadLocation, ID: 81,
		Type:     capability.ProviderTypeAPIKey,
		Platform: capability.PlatformAnthropic,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
	repo := &retryExhaustedCooldownRepoStub{provider: poolProvider}
	svc := NewRetryCooldown(repo, RetryCooldownOptions{})

	svc.Apply(context.Background(), RetryCooldownInput{ProviderID: poolProvider.ID, Status: 502, Retryable: true})

	require.Zero(t, repo.tempCalls)

	// Non-pool providers retain status-specific cooldown handling.
	repo.provider = &Record{LoadLocation: time.LoadLocation, ID: 82, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformAntigravity}
	svc.Apply(context.Background(), RetryCooldownInput{ProviderID: repo.provider.ID, Status: 502, Retryable: true})
	require.Equal(t, 1, repo.tempCalls)
}

func (r *capacityShedProviderRepoStub) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

func (r *capacityShedProviderRepoStub) GetByID(_ context.Context, id int64) (*Record, error) {
	return &Record{LoadLocation: time.LoadLocation, ID: id, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}, nil
}

func TestTempUnscheduleRetryableErrorSkipsRequestScopedTransient(t *testing.T) {
	t.Run("请求级瞬时故障不写提供商状态", func(t *testing.T) {
		repo := &capacityShedProviderRepoStub{}
		svc := NewRetryCooldown(repo, RetryCooldownOptions{})

		svc.Apply(context.Background(), RetryCooldownInput{ProviderID: 1, Status: 502, Retryable: true, RequestScopedTransient: true})

		require.Zero(t, repo.tempUnschedCalls)
	})

	// A 502 without the request-scoped transient flag triggers a provider cooldown.
	t.Run("未标记时保持原有临时摘号语义", func(t *testing.T) {
		repo := &capacityShedProviderRepoStub{}
		svc := NewRetryCooldown(repo, RetryCooldownOptions{})

		svc.Apply(context.Background(), RetryCooldownInput{ProviderID: 1, Status: 502, Retryable: true})

		require.Equal(t, 1, repo.tempUnschedCalls)
	})
}
