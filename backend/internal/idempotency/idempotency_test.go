package idempotency

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// inMemoryIdempotencyRepo supplies synchronized claims, timestamps, and outcome
// updates for coordinator execution and replay scenarios.
type inMemoryIdempotencyRepo struct {
	// mu protects nextID and data during reads and mutations. Record clones are
	// produced before releasing the lock so coordinator calls can inspect their own pointer fields.
	mu     sync.Mutex                    // fixture record access lock
	nextID int64                         // next generated fixture identifier
	data   map[string]*IdempotencyRecord // indexed fixture command records
}

// failingIdempotencyRepo injects storage errors for every coordinator persistence operation.
type failingIdempotencyRepo struct{}

// utf8RejectingIdempotencyRepo models PostgreSQL text rejection for invalid response encoding.
type utf8RejectingIdempotencyRepo struct {
	// inMemoryIdempotencyRepo supplies the successful fixture operations. MarkSucceeded
	// overrides its result-storage method to reject invalid UTF-8 before updating the record.
	inMemoryIdempotencyRepo
}

// noIDOwnerRepo reports initial ownership without populating the required record identifier.
type noIDOwnerRepo struct{}

// conflictBranchRepo controls existing-record and reclaim outcomes for coordinator error branches.
type conflictBranchRepo struct {
	// existing is cloned for every lookup, regardless of its supplied scope and hash.
	// Tests replace this record between calls to exercise controlled persisted states.
	existing *IdempotencyRecord
	// tryReclaimErr takes precedence over tryReclaimOK. Together they select a storage
	// failure or ownership result without changing the record returned by fixture lookups.
	tryReclaimErr error // injected conditional reclaim error
	tryReclaimOK  bool  // canned reclaim ownership result
}

// markBehaviorRepo injects completion-write failures after the business callback has run.
type markBehaviorRepo struct {
	// inMemoryIdempotencyRepo owns fixture state for successful operations; the two
	// completion overrides can inject errors before invoking its normal update methods.
	inMemoryIdempotencyRepo
	// failMarkSucceeded and failMarkFailed independently select the two completion
	// write errors. Disabled flags delegate their corresponding update to the embedded
	// repository, while enabled flags return before its record mutation occurs.
	failMarkSucceeded bool // inject successful outcome write failure
	failMarkFailed    bool // inject retry-state write failure
}

// newInMemoryIdempotencyRepo creates an empty fixture with its identifier sequence initialized.
func newInMemoryIdempotencyRepo() *inMemoryIdempotencyRepo {
	return &inMemoryIdempotencyRepo{
		nextID: 1,
		data:   make(map[string]*IdempotencyRecord),
	}
}

// key joins controlled fixture scope and hash values for record indexing.
// The literal separator is sufficient for these test inputs; callers constructing
// arbitrary separator-bearing components would need an encoding that distinguishes them.
func (r *inMemoryIdempotencyRepo) key(scope, hash string) string {
	return scope + "|" + hash
}

// cloneRecord copies the supplied record and independently allocates its nullable
// pointer fields. Nil input stays nil; callers copying shared repository records
// hold their owning lock, while conflict fixtures supply sequentially accessed test-owned values.
func cloneRecord(in *IdempotencyRecord) *IdempotencyRecord {
	if in == nil {
		return nil
	}
	out := *in
	if in.ResponseStatus != nil {
		v := *in.ResponseStatus
		out.ResponseStatus = &v
	}
	if in.ResponseBody != nil {
		v := *in.ResponseBody
		out.ResponseBody = &v
	}
	if in.ErrorReason != nil {
		v := *in.ErrorReason
		out.ErrorReason = &v
	}
	if in.LockedUntil != nil {
		v := *in.LockedUntil
		out.LockedUntil = &v
	}
	return &out
}

// CreateProcessing clones a nonnil record into an absent fixture scope/key pair
// and reports ownership. It populates the caller's ID and timestamps on success;
// duplicate keys leave the caller record untouched and return false.
//
// Key admission, identifier allocation, and cloned-record publication share the
// fixture mutex so competing coordinator calls select one initial owner. Copying
// nullable fields before publication keeps later outcome updates independent of
// the submitted record, while the assigned timestamps expose the successful claim.
func (r *inMemoryIdempotencyRepo) CreateProcessing(_ context.Context, record *IdempotencyRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Couple key admission, identifier assignment, and record publication under one
	// mutex so concurrent test requests cannot both acquire the same initial fixture claim.
	k := r.key(record.Scope, record.IdempotencyKeyHash)
	if _, ok := r.data[k]; ok {
		return false, nil
	}
	rec := cloneRecord(record)
	rec.ID = r.nextID
	rec.CreatedAt = time.Now()
	rec.UpdatedAt = rec.CreatedAt
	r.nextID++
	r.data[k] = rec
	record.ID = rec.ID
	record.CreatedAt = rec.CreatedAt
	record.UpdatedAt = rec.UpdatedAt
	return true, nil
}

// GetByScopeAndKeyHash returns a cloned fixture record for the supplied pair.
// Missing keys return nil without an error, and returned nullable values can
// be inspected independently of later fixture outcome updates.
func (r *inMemoryIdempotencyRepo) GetByScopeAndKeyHash(_ context.Context, scope, keyHash string) (*IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneRecord(r.data[r.key(scope, keyHash)]), nil
}

// TryReclaim returns a matching fixture record to processing when its lock is
// absent or expired, replacing both deadlines and clearing its failure reason. Missing IDs,
// changed status, and active locks return false.
//
// The expected status and deadline are checked while holding the same lock
// used for mutation, so concurrent test retries can observe a single new claim.
// Timestamp updates expose that mutation to later fixture reads while response
// fields remain unchanged until an outcome write replaces them.
func (r *inMemoryIdempotencyRepo) TryReclaim(_ context.Context, id int64, fromStatus string, now, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep eligibility checks and the processing transition within one locked pass,
	// matching the conditional repository update used to arbitrate competing production retry claims.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != fromStatus {
			return false, nil
		}
		if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
			return false, nil
		}
		rec.Status = IdempotencyStatusProcessing
		rec.LockedUntil = &newLockedUntil
		rec.ExpiresAt = newExpiresAt
		rec.ErrorReason = nil
		rec.UpdatedAt = time.Now()
		return true, nil
	}
	return false, nil
}

// ExtendProcessingLock changes deadlines for a fixture record whose processing
// state and request fingerprint match, returning whether it changed. Missing,
// finished, or differently fingerprinted records return false.
//
// Lookup and mutation share the fixture mutex so a concurrent finish cannot
// interleave between eligibility and deadline replacement. The fingerprint represents
// a request rather than an acquisition owner; this helper follows the ordinary
// command repository predicate and does not add maintenance owner-token fencing.
func (r *inMemoryIdempotencyRepo) ExtendProcessingLock(_ context.Context, id int64, requestFingerprint string, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check processing identity while holding the update lock, so fixture snapshots
	// cannot observe either deadline before the other one has been replaced for this request.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != IdempotencyStatusProcessing || rec.RequestFingerprint != requestFingerprint {
			return false, nil
		}
		rec.LockedUntil = &newLockedUntil
		rec.ExpiresAt = newExpiresAt
		rec.UpdatedAt = time.Now()
		return true, nil
	}
	return false, nil
}

// MarkSucceeded stores success, response fields, and retention for a fixture
// record by ID, clearing the failure reason and lock. A missing record returns
// a test error, making accidental completion of an unclaimed record observable.
func (r *inMemoryIdempotencyRepo) MarkSucceeded(_ context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Publish every success field under the record lock so later test retries
	// can replay a complete response instead of observing a partially completed fixture transition.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		rec.Status = IdempotencyStatusSucceeded
		rec.LockedUntil = nil
		rec.ExpiresAt = expiresAt
		rec.UpdatedAt = time.Now()
		rec.ErrorReason = nil
		rec.ResponseStatus = &responseStatus
		rec.ResponseBody = &responseBody
		return nil
	}
	return errors.New("record not found")
}

// MarkFailedRetryable records a fixture failure and backoff deadline by ID while
// leaving response fields unchanged. Missing records return a test error, and
// successful updates refresh the record timestamp for subsequent inspection.
func (r *inMemoryIdempotencyRepo) MarkFailedRetryable(_ context.Context, id int64, errorReason string, lockedUntil, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep failed status, reason, and retry deadline in one synchronized mutation,
	// so overlapping test retries cannot observe failed state paired with the previous lock window.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		rec.Status = IdempotencyStatusFailedRetryable
		rec.LockedUntil = &lockedUntil
		rec.ExpiresAt = expiresAt
		rec.UpdatedAt = time.Now()
		rec.ErrorReason = &errorReason
		return nil
	}
	return errors.New("record not found")
}

// DeleteExpired removes all fixture records expired at now and returns their
// count. This fixture ignores the requested batch limit; use the SQL repository
// when a scenario needs to exercise bounded expiry deletion behavior.
func (r *inMemoryIdempotencyRepo) DeleteExpired(_ context.Context, now time.Time, _ int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Delete expired fixture entries while excluding concurrent claims and lookups,
	// so later test requests see either the retained record or its completed removal.
	var deleted int64
	for k, rec := range r.data {
		if !rec.ExpiresAt.After(now) {
			delete(r.data, k)
			deleted++
		}
	}
	return deleted, nil
}

// TestIdempotencyCoordinator_RequireKey enforces key policy for a command whose
// key is absent. Disabling observe-only mode must return the required-key error
// category for this request, as checked through the shared application-error classifier.
func TestIdempotencyCoordinator_RequireKey(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:      "test.scope",
		Method:     "POST",
		Route:      "/test",
		ActorScope: "admin:1",
		RequireKey: true,
		Payload:    map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(err), infraerrors.CategoryOf(ErrIdempotencyKeyRequired))
}

// TestIdempotencyCoordinator_ReplaySucceededResult repeats one successful command
// with the same identity. The second result must be replayed, the callback count
// must stay at one, and claim/replay counters must describe those two requests.
func TestIdempotencyCoordinator_ReplaySucceededResult(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	execCount := 0
	exec := func(ctx context.Context) (any, error) {
		execCount++
		return map[string]any{"count": execCount}, nil
	}

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-1",
		Payload:        map[string]any{"a": 1},
	}

	first, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	second, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, 1, execCount, "second request should replay without executing business logic")

	metrics := GetIdempotencyMetricsSnapshot()
	require.Equal(t, uint64(1), metrics.ClaimTotal)
	require.Equal(t, uint64(1), metrics.ReplayTotal)
}

// TestIdempotencyCoordinator_ReclaimExpiredSucceededRecord expires a completed
// fixture record while retaining its request fingerprint. The next call must
// execute again and publish a new result that a subsequent retry can replay.
func TestIdempotencyCoordinator_ReclaimExpiredSucceededRecord(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope.expired",
		Method:         "POST",
		Route:          "/test/expired",
		ActorScope:     "user:99",
		RequireKey:     true,
		IdempotencyKey: "expired-case",
		Payload:        map[string]any{"k": "v"},
	}

	execCount := 0
	exec := func(ctx context.Context) (any, error) {
		execCount++
		return map[string]any{"count": execCount}, nil
	}

	first, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.False(t, first.Replayed)
	require.Equal(t, 1, execCount)

	keyHash := HashIdempotencyKey(opts.IdempotencyKey)
	// Change only retention under the fixture lock, keeping the successful response
	// and matching fingerprint available so this retry specifically exercises expired-record reclaim.
	repo.mu.Lock()
	existing := repo.data[repo.key(opts.Scope, keyHash)]
	require.NotNil(t, existing)
	existing.ExpiresAt = time.Now().Add(-time.Second)
	repo.mu.Unlock()

	second, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.False(t, second.Replayed, "expired record should be reclaimed and execute business logic again")
	require.Equal(t, 2, execCount)

	third, err := coordinator.Execute(context.Background(), opts, exec)
	require.NoError(t, err)
	require.NotNil(t, third)
	require.True(t, third.Replayed)
	payload, ok := third.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2), payload["count"])

	metrics := GetIdempotencyMetricsSnapshot()
	require.GreaterOrEqual(t, metrics.ClaimTotal, uint64(2))
	require.GreaterOrEqual(t, metrics.ReplayTotal, uint64(1))
}

// TestIdempotencyCoordinator_SameKeyDifferentPayloadConflict reuses a recorded key
// with changed payload data. The retry must return the conflict category and
// increment the conflict counter to one for these two sequential requests.
func TestIdempotencyCoordinator_SameKeyDifferentPayloadConflict(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-2",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)

	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-2",
		Payload:        map[string]any{"a": 2},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(err), infraerrors.CategoryOf(ErrIdempotencyKeyConflict))

	metrics := GetIdempotencyMetricsSnapshot()
	require.Equal(t, uint64(1), metrics.ConflictTotal)
}

// TestIdempotencyCoordinator_BackoffAfterRetryableFailure retries immediately after
// a callback reports an application error. The active backoff must produce a
// conflict with a positive retry delay and the corresponding failure observations.
func TestIdempotencyCoordinator_BackoffAfterRetryableFailure(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.FailedRetryBackoff = 2 * time.Second
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope",
		Method:         "POST",
		Route:          "/test",
		ActorScope:     "user:1",
		RequireKey:     true,
		IdempotencyKey: "case-3",
		Payload:        map[string]any{"a": 1},
	}

	_, err := coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		return nil, infraerrors.InternalServer("UPSTREAM_ERROR", "upstream error")
	})
	require.Error(t, err)

	_, err = coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(err), infraerrors.CategoryOf(ErrIdempotencyRetryBackoff))
	require.Greater(t, RetryAfterSecondsFromError(err), 0)

	metrics := GetIdempotencyMetricsSnapshot()
	require.GreaterOrEqual(t, metrics.RetryBackoffTotal, uint64(2))
	require.GreaterOrEqual(t, metrics.ConflictTotal, uint64(1))
	require.GreaterOrEqual(t, metrics.ProcessingDurationCount, uint64(1))
}

// TestIdempotencyCoordinator_ConcurrentSameKeySingleSideEffect launches eight
// matching calls around a delayed callback. The callback count must remain one,
// and a later request must replay while metrics retain the claim and observed conflicts.
func TestIdempotencyCoordinator_ConcurrentSameKeySingleSideEffect(t *testing.T) {
	resetIdempotencyMetricsForTest()
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ProcessingTimeout = 2 * time.Second
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope.concurrent",
		Method:         "POST",
		Route:          "/test/concurrent",
		ActorScope:     "user:7",
		RequireKey:     true,
		IdempotencyKey: "concurrent-case",
		Payload:        map[string]any{"v": 1},
	}

	var execCount int32
	var wg sync.WaitGroup
	// Launch matching requests together and wait before the final replay assertion;
	// the shared atomic count separates callback execution from duplicate conflict or replay handling.
	for range 8 {
		wg.Go(func() {
			// These concurrent return values are intentionally ignored because this scenario
			// asserts aggregate callback and observation counts; the later replay is checked directly.
			_, _ = coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
				atomic.AddInt32(&execCount, 1)
				// Keep the winning callback active briefly so other test goroutines can
				// inspect its processing record before the successful response becomes available for replay.
				time.Sleep(80 * time.Millisecond)
				return map[string]any{"ok": true}, nil
			})
		})
	}
	wg.Wait()

	replayed, err := coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		atomic.AddInt32(&execCount, 1)
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, int32(1), atomic.LoadInt32(&execCount), "concurrent same-key requests should execute business side-effect once")

	metrics := GetIdempotencyMetricsSnapshot()
	require.Equal(t, uint64(1), metrics.ClaimTotal)
	require.Equal(t, uint64(1), metrics.ReplayTotal)
	require.GreaterOrEqual(t, metrics.ConflictTotal, uint64(1))
}

// CreateProcessing fails claim persistence to exercise initial storage unavailability handling.
func (failingIdempotencyRepo) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return false, errors.New("store unavailable")
}

// GetByScopeAndKeyHash injects a read failure for existing-record storage branches.
func (failingIdempotencyRepo) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return nil, errors.New("store unavailable")
}

// TryReclaim fails conditional ownership recovery with the fixture's storage outage error.
func (failingIdempotencyRepo) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}

// ExtendProcessingLock fails deadline extension with the same injected repository outage.
func (failingIdempotencyRepo) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}

// MarkSucceeded fails result persistence if a scenario reaches callback completion.
func (failingIdempotencyRepo) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return errors.New("store unavailable")
}

// MarkFailedRetryable fails retry-state persistence with an injected store outage error.
func (failingIdempotencyRepo) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return errors.New("store unavailable")
}

// DeleteExpired fails cleanup with the outage used by other fixture methods.
func (failingIdempotencyRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("store unavailable")
}

// TestIdempotencyCoordinator_StoreUnavailableMetrics fails storage during initial
// claim creation. Execute must return the store-unavailable category and increment
// process diagnostics so this failed persistence attempt remains observable.
func TestIdempotencyCoordinator_StoreUnavailableMetrics(t *testing.T) {
	resetIdempotencyMetricsForTest()
	coordinator := NewIdempotencyCoordinator(failingIdempotencyRepo{}, DefaultIdempotencyConfig())

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "test.scope.unavailable",
		Method:         "POST",
		Route:          "/test/unavailable",
		ActorScope:     "admin:1",
		RequireKey:     true,
		IdempotencyKey: "case-unavailable",
		Payload:        map[string]any{"v": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))
	require.GreaterOrEqual(t, GetIdempotencyMetricsSnapshot().StoreUnavailableTotal, uint64(1))
}

// newUTF8RejectingIdempotencyRepo creates a synchronized fixture that validates response text encoding.
func newUTF8RejectingIdempotencyRepo() *utf8RejectingIdempotencyRepo {
	return &utf8RejectingIdempotencyRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo()}
}

// MarkSucceeded rejects invalid UTF-8 with a database-shaped fixture error before
// storing the response. Valid text delegates to the synchronized record update,
// letting truncation tests exercise the success path with realistic encoding acceptance.
func (r *utf8RejectingIdempotencyRepo) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	// Reject invalid text before modifying fixture state so Execute observes the
	// encoding-shaped persistence failure while its earlier processing claim remains available for inspection.
	if !utf8.ValidString(responseBody) {
		return errors.New(`pq: invalid byte sequence for encoding "UTF8": 0xe8 0xb4 0x2e`)
	}
	return r.inMemoryIdempotencyRepo.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

// TestIdempotencyCoordinator_TruncatedStoredResponseRemainsUTF8 places the response
// cutoff inside a multibyte character. Execution must persist a valid UTF-8 prefix
// with the truncation marker, rather than trigger the fixture's encoding rejection.
func TestIdempotencyCoordinator_TruncatedStoredResponseRemainsUTF8(t *testing.T) {
	repo := newUTF8RejectingIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	// Place the byte budget two bytes into a three-byte rune after the JSON prefix,
	// forcing truncation to retreat to a complete preceding character before storing text.
	cfg.MaxStoredResponseLen = len(`{"message":"`) + 2
	coordinator := NewIdempotencyCoordinator(repo, cfg)

	opts := IdempotencyExecuteOptions{
		Scope:          "test.scope.truncate_utf8",
		Method:         "POST",
		Route:          "/api/v1/providers/import/codex-session",
		ActorScope:     "admin:1",
		RequireKey:     true,
		IdempotencyKey: "truncate-utf8",
		Payload:        map[string]any{"content": "codex-session"},
	}

	result, err := coordinator.Execute(context.Background(), opts, func(ctx context.Context) (any, error) {
		return map[string]any{"message": strings.Repeat("\u8d26", 8)}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	stored, err := repo.GetByScopeAndKeyHash(context.Background(), opts.Scope, HashIdempotencyKey(opts.IdempotencyKey))
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.ResponseBody)
	require.True(t, utf8.ValidString(*stored.ResponseBody))
	require.Contains(t, *stored.ResponseBody, "...(truncated)")
}

// TestIdempotencyCoordinatorTTLs compares unbound defaults with a coordinator's
// configured write and maintenance periods. Accessors must tolerate a nil receiver
// while returning the positive values belonging to a bound instance.
func TestIdempotencyCoordinatorTTLs(t *testing.T) {
	var empty *IdempotencyCoordinator
	require.Equal(t, DefaultIdempotencyConfig().DefaultTTL, empty.DefaultWriteIdempotencyTTL())
	require.Equal(t, DefaultIdempotencyConfig().SystemOperationTTL, empty.DefaultSystemOperationIdempotencyTTL())
	coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		DefaultTTL: 2 * time.Hour, SystemOperationTTL: 15 * time.Minute,
		ProcessingTimeout: 10 * time.Second, FailedRetryBackoff: 3 * time.Second,
	})
	require.Equal(t, 2*time.Hour, coordinator.DefaultWriteIdempotencyTTL())
	require.Equal(t, 15*time.Minute, coordinator.DefaultSystemOperationIdempotencyTTL())
	require.Equal(t, DefaultIdempotencyConfig().DefaultTTL, empty.DefaultWriteIdempotencyTTL())
}

// TestNormalizeIdempotencyKeyAndFingerprint covers trimmed keys, empty input,
// oversized or control-character keys, and fingerprint defaults. Equivalent default
// inputs must hash alike, while unsupported JSON payloads must return the payload error category.
func TestNormalizeIdempotencyKeyAndFingerprint(t *testing.T) {
	key, err := NormalizeIdempotencyKey("  abc-123  ")
	require.NoError(t, err)
	require.Equal(t, "abc-123", key)

	key, err = NormalizeIdempotencyKey("")
	require.NoError(t, err)
	require.Equal(t, "", key)

	_, err = NormalizeIdempotencyKey(string(make([]byte, 129)))
	require.Error(t, err)

	_, err = NormalizeIdempotencyKey("bad\nkey")
	require.Error(t, err)

	fp1, err := BuildIdempotencyFingerprint("", "", "", map[string]any{"a": 1})
	require.NoError(t, err)
	require.NotEmpty(t, fp1)
	fp2, err := BuildIdempotencyFingerprint("POST", "/", "anonymous", map[string]any{"a": 1})
	require.NoError(t, err)
	require.Equal(t, fp1, fp2)

	_, err = BuildIdempotencyFingerprint("POST", "/x", "u:1", map[string]any{"bad": make(chan int)})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyInvalidPayload), infraerrors.CategoryOf(err))
}

// TestRetryAfterSecondsFromErrorBranches supplies unrelated errors and valid or
// malformed retry metadata. The helper must return the positive declared delay
// for valid application metadata and zero when no usable delay is available.
func TestRetryAfterSecondsFromErrorBranches(t *testing.T) {
	require.Equal(t, 0, RetryAfterSecondsFromError(nil))
	require.Equal(t, 0, RetryAfterSecondsFromError(errors.New("plain")))

	err := ErrIdempotencyInProgress.WithMetadata(map[string]string{"retry_after": "12"})
	require.Equal(t, 12, RetryAfterSecondsFromError(err))

	err = ErrIdempotencyInProgress.WithMetadata(map[string]string{"retry_after": "bad"})
	require.Equal(t, 0, RetryAfterSecondsFromError(err))
}

// TestIdempotencyCoordinator_ExecuteNilExecutorAndNoKeyPassThrough checks missing
// callbacks and keyless observe-only commands. Nil callbacks must return their
// structured error, while an admitted keyless call executes once and reports a live result.
func TestIdempotencyCoordinator_ExecuteNilExecutorAndNoKeyPassThrough(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Payload:        map[string]any{"a": 1},
	}, nil)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_EXECUTOR_NIL", infraerrors.Reason(err))

	called := 0
	result, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:      "scope",
		RequireKey: true,
		Payload:    map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		called++
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)
	require.NotNil(t, result)
	require.False(t, result.Replayed)
}

// CreateProcessing reports ownership while leaving the caller's record ID unset.
func (noIDOwnerRepo) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return true, nil
}

// GetByScopeAndKeyHash returns no record for this missing-identifier ownership fixture.
func (noIDOwnerRepo) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return nil, nil
}

// TryReclaim returns false because this fixture supplies no existing record ownership.
func (noIDOwnerRepo) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, nil
}

// ExtendProcessingLock returns false because this fixture stores no renewable record.
func (noIDOwnerRepo) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return false, nil
}

// MarkSucceeded succeeds inertly because missing-ID claims stop before storing a result.
func (noIDOwnerRepo) MarkSucceeded(context.Context, int64, int, string, time.Time) error { return nil }

// MarkFailedRetryable succeeds inertly because this fixture executes no claimed business work.
func (noIDOwnerRepo) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return nil
}

// DeleteExpired returns zero because the missing-ID fixture retains no stored records.
func (noIDOwnerRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) { return 0, nil }

// TestIdempotencyCoordinator_RepoNilScopeRequiredAndRecordIDMissing supplies keyed
// commands with missing persistence, missing scope, and an incomplete ownership result.
// Execute must classify repository or ID absence as storage unavailability and reject the absent scope.
func TestIdempotencyCoordinator_RepoNilScopeRequiredAndRecordIDMissing(t *testing.T) {
	cfg := DefaultIdempotencyConfig()
	coordinator := NewIdempotencyCoordinator(nil, cfg)

	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))

	coordinator = NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), cfg)
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		IdempotencyKey: "k2",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_SCOPE_REQUIRED", infraerrors.Reason(err))

	coordinator = NewIdempotencyCoordinator(noIDOwnerRepo{}, cfg)
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-no-id",
		IdempotencyKey: "k3",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))
}

// CreateProcessing declines ownership so Execute inspects the configured existing record.
func (r *conflictBranchRepo) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return false, nil
}

// GetByScopeAndKeyHash returns a clone of this scenario's configured existing record.
// The supplied scope and hash are ignored so the test can select each persisted
// state directly before the next sequential coordinator call.
func (r *conflictBranchRepo) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return cloneRecord(r.existing), nil
}

// TryReclaim returns the injected error or configured ownership result for the
// reclaim branch. It leaves existing unchanged, allowing the same fixture record
// to remain available if Execute reloads after a lost reclaim.
func (r *conflictBranchRepo) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	if r.tryReclaimErr != nil {
		return false, r.tryReclaimErr
	}
	return r.tryReclaimOK, nil
}

// ExtendProcessingLock returns false because these conflict scenarios perform no renewal.
func (r *conflictBranchRepo) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return false, nil
}

// MarkSucceeded succeeds inertly for the branch fixture's unused outcome storage method.
func (r *conflictBranchRepo) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return nil
}

// MarkFailedRetryable succeeds inertly because controlled conflict calls execute no callback work.
func (r *conflictBranchRepo) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return nil
}

// DeleteExpired leaves configured branch records intact for subsequent coordinator assertions.
func (r *conflictBranchRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

// TestIdempotencyCoordinator_ConflictBranchesAndDecodeError supplies malformed
// successful response data, unknown state, reclaim failure, and losing retry ownership.
// Errors must distinguish unusable storage from unsupported or still-processing request conflicts.
func TestIdempotencyCoordinator_ConflictBranchesAndDecodeError(t *testing.T) {
	now := time.Now()
	fp, err := BuildIdempotencyFingerprint("POST", "/x", "u:1", map[string]any{"a": 1})
	require.NoError(t, err)
	badBody := "{bad-json"
	repo := &conflictBranchRepo{
		existing: &IdempotencyRecord{
			ID:                 1,
			Scope:              "scope",
			IdempotencyKeyHash: HashIdempotencyKey("k"),
			RequestFingerprint: fp,
			Status:             IdempotencyStatusSucceeded,
			ResponseBody:       &badBody,
			ExpiresAt:          now.Add(time.Hour),
		},
	}
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))

	repo.existing = &IdempotencyRecord{
		ID:                 2,
		Scope:              "scope",
		IdempotencyKeyHash: HashIdempotencyKey("k"),
		RequestFingerprint: fp,
		Status:             "unknown",
		ExpiresAt:          now.Add(time.Hour),
	}
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyKeyConflict), infraerrors.CategoryOf(err))

	repo.existing = &IdempotencyRecord{
		ID:                 3,
		Scope:              "scope",
		IdempotencyKeyHash: HashIdempotencyKey("k"),
		RequestFingerprint: fp,
		Status:             IdempotencyStatusFailedRetryable,
		LockedUntil:        ptrTime(now.Add(-time.Second)),
		ExpiresAt:          now.Add(time.Hour),
	}
	repo.tryReclaimErr = errors.New("reclaim down")
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))

	repo.tryReclaimErr = nil
	repo.tryReclaimOK = false
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope",
		IdempotencyKey: "k",
		Method:         "POST",
		Route:          "/x",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyInProgress), infraerrors.CategoryOf(err))
}

// MarkSucceeded injects an outcome write error when the scenario enables it.
// Otherwise it delegates to the synchronized fixture update, allowing tests to
// isolate persistence failure after successful callback execution.
func (r *markBehaviorRepo) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	if r.failMarkSucceeded {
		return errors.New("mark succeeded failed")
	}
	return r.inMemoryIdempotencyRepo.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

// MarkFailedRetryable injects a retry-state write failure when enabled by the
// scenario. Other calls update the synchronized fixture, letting Execute tests
// check that callback errors remain visible when persistence also fails.
func (r *markBehaviorRepo) MarkFailedRetryable(ctx context.Context, id int64, errorReason string, lockedUntil, expiresAt time.Time) error {
	if r.failMarkFailed {
		return errors.New("mark failed retryable failed")
	}
	return r.inMemoryIdempotencyRepo.MarkFailedRetryable(ctx, id, errorReason, lockedUntil, expiresAt)
}

// TestIdempotencyCoordinator_MarkAndMarshalBranches fails success persistence,
// response encoding, and failure-state persistence after callbacks run. Success-side
// storage failures must return unavailability, while retry-state storage failure
// must retain the original callback error.
func TestIdempotencyCoordinator_MarkAndMarshalBranches(t *testing.T) {
	repo := &markBehaviorRepo{inMemoryIdempotencyRepo: *newInMemoryIdempotencyRepo()}
	coordinator := NewIdempotencyCoordinator(repo, DefaultIdempotencyConfig())

	repo.failMarkSucceeded = true
	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-success",
		IdempotencyKey: "k1",
		Method:         "POST",
		Route:          "/ok",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))

	repo.failMarkSucceeded = false
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-marshal",
		IdempotencyKey: "k2",
		Method:         "POST",
		Route:          "/bad",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return map[string]any{"bad": make(chan int)}, nil
	})
	require.Error(t, err)
	require.Equal(t, infraerrors.CategoryOf(ErrIdempotencyStoreUnavail), infraerrors.CategoryOf(err))

	repo.failMarkFailed = true
	_, err = coordinator.Execute(context.Background(), IdempotencyExecuteOptions{
		Scope:          "scope-fail",
		IdempotencyKey: "k3",
		Method:         "POST",
		Route:          "/fail",
		ActorScope:     "u:1",
		Payload:        map[string]any{"a": 1},
	}, func(ctx context.Context) (any, error) {
		return nil, errors.New("plain failure")
	})
	require.Error(t, err)
	require.Equal(t, "plain failure", err.Error())
}

// TestIdempotencyCoordinator_HelperBranches covers conflicts without deadlines,
// response truncation, absent stored data, and malformed replay text. Helpers must
// retain the conflict category, mark bounded responses, and distinguish object-shaped
// replay data from decode failure.
func TestIdempotencyCoordinator_HelperBranches(t *testing.T) {
	c := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), IdempotencyConfig{
		DefaultTTL:           time.Hour,
		SystemOperationTTL:   time.Hour,
		ProcessingTimeout:    time.Second,
		FailedRetryBackoff:   time.Second,
		MaxStoredResponseLen: 12,
		ObserveOnly:          false,
	})

	// A missing lock deadline leaves the conflict category unchanged.
	base := ErrIdempotencyInProgress
	err := c.conflictWithRetryAfter(base, nil, time.Now())
	require.Equal(t, infraerrors.CategoryOf(base), infraerrors.CategoryOf(err))

	// Responses exceeding the byte limit carry a truncation marker.
	body, err := c.marshalStoredResponse(map[string]any{"long": "abcdefghijklmnopqrstuvwxyz"})
	require.NoError(t, err)
	require.Contains(t, body, "...(truncated)")

	// Missing stored data decodes to an object; malformed JSON returns an error.
	out, err := c.decodeStoredResponse(nil)
	require.NoError(t, err)
	_, ok := out.(map[string]any)
	require.True(t, ok)

	invalid := "{invalid"
	_, err = c.decodeStoredResponse(&invalid)
	require.Error(t, err)
}

// ptrTime returns a pointer holding the supplied time-based fixture value.
func ptrTime(t time.Time) *time.Time { return &t }

// resetIdempotencyMetricsForTest clears every process-wide idempotency counter for
// one scenario. Callers finish earlier worker activity before resetting because
// separate atomic stores do not provide an isolated metrics generation for overlapping tests.
func resetIdempotencyMetricsForTest() {
	// Reset the shared counters before launching this scenario's coordinator work,
	// so observations from earlier completed tests do not alter the current expected event counts.
	defaultIdempotencyMetrics.claimTotal.Store(0)
	defaultIdempotencyMetrics.replayTotal.Store(0)
	defaultIdempotencyMetrics.conflictTotal.Store(0)
	defaultIdempotencyMetrics.retryBackoffTotal.Store(0)
	defaultIdempotencyMetrics.processingDurationCount.Store(0)
	defaultIdempotencyMetrics.processingDurationMicros.Store(0)
	defaultIdempotencyMetrics.storeUnavailableTotal.Store(0)
}
