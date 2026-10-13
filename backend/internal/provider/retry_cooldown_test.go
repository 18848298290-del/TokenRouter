package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Retry-exhaustion fixtures count persistent pause calls while changing pool
// membership or request-scoped eligibility. The assertions distinguish a shared
// provider cooldown from a transient attempt, checking pool suppression and the
// ordinary non-pool 502 path independently.

// retryExhaustedCooldownRepoStub supplies current pool configuration and
// records retry-exhaustion pause calls.
type retryExhaustedCooldownRepoStub struct {
	// RetryCooldownStore remains unset because both reached operations are
	// overridden; unexpected inherited methods fail through the nil interface and
	// expose extra fixture dependencies.
	RetryCooldownStore // unused cooldown methods

	// provider is returned directly by GetByID. Tests replace it between
	// synchronous Apply calls to make the next lookup observe changed pool
	// membership.
	provider  *Record
	tempCalls int // persistent pause calls
}

// capacityShedProviderRepoStub supplies a normal provider and counts pauses for
// request-scope tests.
type capacityShedProviderRepoStub struct {
	// RetryCooldownStore remains unset because both reached operations are
	// overridden; unexpected inherited methods fail through the nil interface and
	// expose extra fixture dependencies.
	RetryCooldownStore // unused cooldown methods

	tempUnschedCalls int // persistent pause calls
}

// GetByID returns the fixture's current provider pointer with no error. The
// pointer remains shared with the fixture, so tests replace or mutate it
// between synchronous Apply calls.
func (r *retryExhaustedCooldownRepoStub) GetByID(context.Context, int64) (*Record, error) {
	return r.provider, nil
}

// SetTempUnschedulable records one persistent pause call for the
// retry-exhaustion fixture.
func (r *retryExhaustedCooldownRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

// TestTempUnscheduleRetryableError_PoolModeSkipsLegacyCooldown applies the same
// retryable 502 to a pool and then an ordinary provider. It checks the current
// pool record skips persistence and the non-pool record writes once.
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

	// Change the lookup result between synchronous calls so pool suppression and
	// ordinary status-based persistence are checked against current provider
	// configuration in the same fixture.
	repo.provider = &Record{LoadLocation: time.LoadLocation, ID: 82, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformAntigravity}
	svc.Apply(context.Background(), RetryCooldownInput{ProviderID: repo.provider.ID, Status: 502, Retryable: true})
	require.Equal(t, 1, repo.tempCalls)
}

// SetTempUnschedulable counts a persistent pause selected by the request-scope
// fixture.
func (r *capacityShedProviderRepoStub) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

// GetByID returns a fresh ordinary OpenAI OAuth fixture with the requested ID
// and no error. It lets status selection reach pause storage when the
// request-scoped transient flag is absent.
func (r *capacityShedProviderRepoStub) GetByID(_ context.Context, id int64) (*Record, error) {
	return &Record{LoadLocation: time.LoadLocation, ID: id, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}, nil
}

// TestTempUnscheduleRetryableErrorSkipsRequestScopedTransient compares a
// retryable 502 with and without the request-scoped flag. It checks the flagged
// attempt writes no shared provider pause while the ordinary attempt writes
// once.
func TestTempUnscheduleRetryableErrorSkipsRequestScopedTransient(t *testing.T) {
	t.Run("请求级瞬时故障不写提供商状态", func(t *testing.T) {
		repo := &capacityShedProviderRepoStub{}
		svc := NewRetryCooldown(repo, RetryCooldownOptions{})

		svc.Apply(context.Background(), RetryCooldownInput{ProviderID: 1, Status: 502, Retryable: true, RequestScopedTransient: true})

		require.Zero(t, repo.tempUnschedCalls)
	})

	// Use the same failure status with the scope flag absent to establish that
	// persistence suppression comes from request scope, while ordinary retry
	// exhaustion still reaches storage.
	t.Run("未标记时保持原有临时摘号语义", func(t *testing.T) {
		repo := &capacityShedProviderRepoStub{}
		svc := NewRetryCooldown(repo, RetryCooldownOptions{})

		svc.Apply(context.Background(), RetryCooldownInput{ProviderID: 1, Status: 502, Retryable: true})

		require.Equal(t, 1, repo.tempUnschedCalls)
	})
}
