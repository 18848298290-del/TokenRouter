package provider

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An imported provider can remain disabled or errored while its supplied
// token-version metadata is refreshed. The import path therefore reuses
// coordinator locking and stop registration but leaves status qualification to
// the selected import. These fixtures check retained status and metadata,
// discarded conflicts after one exchange, and canceled responses that cannot
// write credentials.
//
// The single-row store holds comparison and replacement under one mutex, with
// exchange callbacks using that mutex to inject administrator changes. Reads
// copy supported containers, while unsupported credential values retain their
// references. Write counts distinguish rejected comparisons from applied
// results; the fixture omits ID comparison and database transaction behavior,
// so those guarantees remain with production storage.

// importRefreshStore models one provider's reads and conditional credential
// writes for import fixtures.
type importRefreshStore struct {
	// mu protects current, reads, writes and failure during store calls; fixture
	// callbacks take the same lock when replacing authentication state during
	// exchange.
	mu      sync.Mutex // fixture provider synchronization
	current *Record    // current fixture provider row
	// reads: attempted fixture row lookups; writes: matched credential replacements.
	reads, writes int
	failure       error // injected conditional write failure
}

// GetByID counts a read and returns a snapshot of the current fixture row under
// its mutex. The context and ID are ignored, and unsupported values retain
// snapshotRefreshRecord's copy limits.
func (s *importRefreshStore) GetByID(context.Context, int64) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return snapshotRefreshRecord(s.current), nil
}

// UpdateOAuthCredentialsIfUnchanged compares the current fixture's status,
// platform, type, credentials and proxy before copying replacement credentials.
// The fixture requires a current row, and a configured failure returns before
// comparison or mutation.
//
// Its mutex keeps comparison and replacement together while interleaving
// callbacks use the same lock. A mismatch returns false and leaves write count
// unchanged; a match increments the count and copies supported credential
// containers. This single-row fixture omits ID validation and models logical
// identity conflict rather than PostgreSQL transaction behavior.
func (s *importRefreshStore) UpdateOAuthCredentialsIfUnchanged(_ context.Context, v CredentialVersion, credentials map[string]any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return false, s.failure
	}
	c := s.current
	if c.Status != v.Status || c.Platform != v.Platform || c.Type != v.Type || !reflect.DeepEqual(c.ProxyID, v.ProxyID) || !reflect.DeepEqual(c.Credentials, v.Credentials) {
		return false, nil
	}
	// Keep the accepted replacement and write count inside the comparison lock
	// so an administrator callback cannot interleave between the match and
	// update.
	s.writes++
	c.Credentials = CloneValues(credentials)
	return true, nil
}

// newImportRefreshStore creates the active OpenAI fixture with an initial
// refresh token.
func newImportRefreshStore() *importRefreshStore {
	return &importRefreshStore{current: &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Status: StatusActive, Credentials: map[string]any{"refresh_token": "old"}}}
}

// TestRefreshImportedPreservesExplicitStatusAndTokenVersion refreshes active,
// inactive and errored imports with supplied token metadata. It checks one
// write, retained status and version, and isolation of the tested nested
// credential map.
func TestRefreshImportedPreservesExplicitStatusAndTokenVersion(t *testing.T) {
	for _, status := range []string{StatusActive, "inactive", StatusError} {
		t.Run(status, func(t *testing.T) {
			s := newImportRefreshStore()
			s.current.Status = status
			api := NewOAuthRefreshAPI(s, nil, RefreshOptions{})
			source, _ := s.GetByID(context.Background(), 1)
			credentials := map[string]any{"refresh_token": "new", "_token_version": float64(123), "nested": map[string]any{"keep": true}}
			require.NoError(t, api.RefreshImported(context.Background(), source, "shared", func(context.Context, *Record) map[string]any { return credentials }))
			require.Equal(t, 1, s.writes)
			require.Equal(t, status, s.current.Status)
			require.Equal(t, credentials, s.current.Credentials)
			stored, ok := s.current.Credentials["nested"].(map[string]any)
			require.True(t, ok)
			stored["keep"] = false
			original, ok := credentials["nested"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, true, original["keep"])
		})
	}
}

// TestRefreshImportedStaleResultRereadsWithoutSecondExchange changes
// credentials, proxy or status during the exchange callback. Each conflict
// performs one exchange and zero writes, with the read count showing the final
// existence reread.
func TestRefreshImportedStaleResultRereadsWithoutSecondExchange(t *testing.T) {
	for _, change := range []string{"credentials", "proxy", "status"} {
		t.Run(change, func(t *testing.T) {
			s := newImportRefreshStore()
			source, _ := s.GetByID(context.Background(), 1)
			api := NewOAuthRefreshAPI(s, nil, RefreshOptions{})
			calls := 0
			require.NoError(t, api.RefreshImported(context.Background(), source, "shared", func(context.Context, *Record) map[string]any {
				calls++
				// Mutate the stored identity while the
				// exchange is still active so its later
				// conditional write observes a conflict
				// with this administrator result.
				s.mu.Lock()
				defer s.mu.Unlock()
				switch change {
				case "credentials":
					s.current.Credentials = map[string]any{"refresh_token": "admin"}
				case "proxy":
					id := int64(7)
					s.current.ProxyID = &id
				case "status":
					s.current.Status = "inactive"
				}
				return map[string]any{"refresh_token": "late"}
			}))
			require.Equal(t, 1, calls)
			require.Zero(t, s.writes)
			require.Equal(t, 3, s.reads)
		})
	}
}

// TestRefreshImportedSharesBackgroundLockAndStopsWaiting holds an import
// exchange while ordinary refresh queues on its key. A bounded stop reports
// unfinished work, then joined results show cancellation, no ordinary exchange
// and no late credential write.
func TestRefreshImportedSharesBackgroundLockAndStopsWaiting(t *testing.T) {
	s := newImportRefreshStore()
	api := NewOAuthRefreshAPI(s, nil, RefreshOptions{})
	source, _ := s.GetByID(context.Background(), 1)
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 2)
	var calls atomic.Int32
	// Hold one exchange after signaling entry, leaving the ordinary coordinator
	// registered on the same key until shutdown cancels its queued lock wait.
	go func() {
		finished <- api.RefreshImported(context.Background(), source, "lifecycle:provider", func(context.Context, *Record) map[string]any {
			calls.Add(1)
			close(started)
			<-release
			return map[string]any{"refresh_token": "late"}
		})
	}()
	<-started
	// The import owns this shared key while ordinary refresh registers its wait.
	// Joining both results later proves that cancellation prevented a second
	// exchange.
	executor := &lifecycleRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
	go func() {
		_, err := api.RefreshIfNeeded(context.Background(), &Record{ID: 1}, executor, time.Minute)
		finished <- err
	}()
	require.Eventually(t, func() bool {
		api.activity.mu.Lock()
		defer api.activity.mu.Unlock()
		return len(api.activity.active) == 2
	}, time.Second, time.Millisecond)
	budget, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, api.StopContext(budget), context.DeadlineExceeded)
	require.Zero(t, executor.calls.Load())
	// Release the callback that ignored cancellation and join both operations
	// before inspecting writes, distinguishing actual exits from the stop
	// timeout.
	close(release)
	for range 2 {
		require.ErrorIs(t, <-finished, context.Canceled)
	}
	require.Zero(t, s.writes)
	require.Equal(t, int32(1), calls.Load())
	require.ErrorIs(t, api.RefreshImported(context.Background(), source, "shared", func(context.Context, *Record) map[string]any { t.Fatal("停止后不得交换"); return nil }), ErrRefreshStopped)
}

// TestRefreshImportedRejectsChangedSourceAndReportsPersistenceFailure supplies a
// stale selected status before exchange and then an injected write error. The
// stale case never exchanges, the storage error propagates, and no write is
// counted.
func TestRefreshImportedRejectsChangedSourceAndReportsPersistenceFailure(t *testing.T) {
	s := newImportRefreshStore()
	api := NewOAuthRefreshAPI(s, nil, RefreshOptions{})
	source, _ := s.GetByID(context.Background(), 1)
	source.Status = "inactive"
	require.ErrorIs(t, api.RefreshImported(context.Background(), source, "shared", func(context.Context, *Record) map[string]any { t.Fatal("旧身份不得交换"); return nil }), ErrRefreshProviderStateChanged)
	source.Status = StatusActive
	s.failure = errors.New("fixture persistence failed")
	require.ErrorIs(t, api.RefreshImported(context.Background(), source, "shared", func(context.Context, *Record) map[string]any { return map[string]any{"refresh_token": "new"} }), s.failure)
	require.Zero(t, s.writes)
}
