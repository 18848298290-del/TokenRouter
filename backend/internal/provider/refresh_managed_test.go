package provider

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Administrative refresh needs the current credentials while retaining
// eligibility for an explicitly disabled provider. These fixtures cover its
// shared stop/lock path, copied callback input, shadow rejection before reading,
// and omission of internal credential comparisons from JSON serialization.

// managedRefreshReader counts current-row reads and supplies copied
// administrative fixture records.
type managedRefreshReader struct {
	reads atomic.Int32 // observed current-row read count
	// current stays stable during each fixture read; the reader uses CloneRecord
	// so supported mutable values in callback input are detached from this row.
	current *Record
}

// TestManagedRefreshSharesLockAndStopWithBackground holds the administrative
// callback while normal refresh queues on the same key. Shutdown joins both
// canceled results, with no normal exchange call or credential write.
func TestManagedRefreshSharesLockAndStopWithBackground(t *testing.T) {
	repo := &lifecycleRefreshRepository{}
	api := NewOAuthRefreshAPI(repo, nil, RefreshOptions{})
	entered := make(chan struct{})
	done := make(chan error, 2)
	observed := &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Status: StatusActive}
	executor := &lifecycleRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
	// Hold the administrator callback after entry so normal refresh queues on its
	// lock; stop cancellation then releases that owner before both results are
	// joined.
	go func() {
		_, _, err := api.WithManagedRefresh(context.Background(), observed, executor.CacheKey(observed), func(ctx context.Context, _ *Record) (*Record, string, error) {
			close(entered)
			<-ctx.Done()
			return nil, "", ctx.Err()
		})
		done <- err
	}()
	<-entered
	go func() {
		_, err := api.RefreshIfNeeded(context.Background(), observed, executor, time.Minute)
		done <- err
	}()
	require.Eventually(t, func() bool {
		api.activity.mu.Lock()
		defer api.activity.mu.Unlock()
		return len(api.activity.active) == 2
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, api.StopContext(ctx))
	for range 2 {
		require.ErrorIs(t, <-done, context.Canceled)
	}
	require.Zero(t, executor.calls.Load())
	require.Zero(t, repo.writes.Load())
}

// GetByID counts a read and returns CloneRecord's copy of the current fixture.
// The ID and context are ignored, and the test keeps the source record stable
// during cloning.
func (r *managedRefreshReader) GetByID(context.Context, int64) (*Record, error) {
	r.reads.Add(1)
	return CloneRecord(r.current), nil
}

// TestManagedRefreshUsesLockTimeCredentialSnapshot changes current credentials
// after selecting a disabled provider. The callback sees the current token,
// returned status stays disabled, and the selected record retains its earlier
// token.
func TestManagedRefreshUsesLockTimeCredentialSnapshot(t *testing.T) {
	observed := &Record{ID: 5, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Status: StatusDisabled, Credentials: map[string]any{"refresh_token": "selected"}}
	current := CloneRecord(observed)
	current.Credentials["refresh_token"] = "current"
	reader := &managedRefreshReader{current: current}
	api := NewOAuthRefreshAPI(reader, nil, RefreshOptions{})
	out, warning, err := api.WithManagedRefresh(context.Background(), observed, "openai:5", func(_ context.Context, v *Record) (*Record, string, error) {
		require.Equal(t, "current", v.Credentials["refresh_token"])
		return v, "", nil
	})
	require.NoError(t, err)
	require.Empty(t, warning)
	require.Equal(t, StatusDisabled, out.Status, "显式管理刷新保持禁用提供商资格")
	require.Equal(t, "selected", observed.Credentials["refresh_token"])
	require.Equal(t, int32(1), reader.reads.Load())
}

// TestManagedRefreshRejectsShadowBeforeReading supplies a Spark shadow and a nil
// administrative callback. Validation returns the dedicated shadow error before
// any store read or callback access can occur.
func TestManagedRefreshRejectsShadowBeforeReading(t *testing.T) {
	parent := int64(1)
	reader := &managedRefreshReader{}
	api := NewOAuthRefreshAPI(reader, nil, RefreshOptions{})
	_, _, err := api.WithManagedRefresh(context.Background(), &Record{ID: 2, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, ParentProviderID: &parent, QuotaDimension: QuotaDimensionSpark}, "unused", nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "SPARK_SHADOW_NO_REFRESH")
	require.Zero(t, reader.reads.Load())
}

// TestManagedRefreshConditionIsNotHTTPInput marshals an update carrying the
// internal expected credential object. The JSON omits both its secret marker and
// ExpectedCredentials, protecting their omission from serialized update values.
func TestManagedRefreshConditionIsNotHTTPInput(t *testing.T) {
	payload, err := json.Marshal(UpdateProviderInput{ExpectedCredentials: &CredentialVersion{Credentials: map[string]any{"refresh_token": "internal-secret"}}})
	require.NoError(t, err)
	require.NotContains(t, string(payload), "internal-secret")
	require.NotContains(t, string(payload), "ExpectedCredentials")
}
