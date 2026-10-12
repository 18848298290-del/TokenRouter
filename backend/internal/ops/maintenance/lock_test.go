package maintenance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// The failure-state alias lets controlled fixture records enter the same retryable
// status used by persisted maintenance claims during busy and recovery branch assertions.
const (
	IdempotencyStatusFailedRetryable = idempotency.IdempotencyStatusFailedRetryable
)

// flakySystemLockRenewRepo fails its first renewal before delegating later lease updates.
type flakySystemLockRenewRepo struct {
	// inMemoryIdempotencyRepo supplies captured ownership and deadline state for the
	// scenario. RenewOperation changes only the first call's result before using this store.
	*inMemoryIdempotencyRepo
	extendCalls int32 // atomically observed renewal attempt count
}

// systemLockRepoStub selects lease acquisition and completion outcomes for branch tests.
type systemLockRepoStub struct {
	// createOwner, createErr, getErr, reclaimOK, and reclaimErr select ClaimOperation
	// outcomes in that method's fixed precedence. Controls are set between sequential test calls.
	createOwner bool  // report an owner without ID
	createErr   error // injected initial acquisition error
	// existing supplies the cloned busy record when no acquisition error is selected.
	// Tests replace it between calls instead of modelling a concurrently changing database row.
	existing   *IdempotencyRecord
	getErr     error // injected existing-record read error
	reclaimOK  bool  // canned acquisition ownership result
	reclaimErr error // injected reclaim comparison error
	// markSuccErr and markFailErr select completion errors by the caller's success
	// flag, allowing manually stopped lock fixtures to exercise both release result branches.
	markSuccErr error // injected successful completion error
	markFailErr error // injected failed completion error
}

// IdempotencyRecord names the shared persisted record shape used by maintenance fixtures.
type IdempotencyRecord = idempotency.IdempotencyRecord

// inMemoryIdempotencyRepo models synchronized maintenance claim identity and deadline changes.
type inMemoryIdempotencyRepo struct {
	// mu protects nextID and data during fixture lookups and updates. Claim results
	// copy the record shallowly; lookup helpers clone nullable fields for assertions under the lock.
	mu     sync.Mutex                    // stored maintenance fixture lock
	nextID int64                         // next generated lease identifier
	data   map[string]*IdempotencyRecord // scope/key-indexed maintenance fixture records
}

// TestSystemOperationLockService_AcquireBusyAndRelease competes two operation labels
// for the shared fixture lease. The second must receive busy identity and delay
// metadata, and successful release must allow its subsequent acquisition.
func TestSystemOperationLockService_AcquireBusyAndRelease(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})

	lock1, err := svc.Acquire(context.Background(), "op-1")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	_, err = svc.Acquire(context.Background(), "op-2")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrSystemOperationBusy), errorCode(err))
	appErr := apperror.FromError(err)
	require.Equal(t, "op-1", appErr.Metadata["operation_id"])
	require.NotEmpty(t, appErr.Metadata["retry_after"])

	require.NoError(t, svc.Release(context.Background(), lock1, true, ""))

	lock2, err := svc.Acquire(context.Background(), "op-2")
	require.NoError(t, err)
	require.NotNil(t, lock2)
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

// TestSystemOperationLockService_RenewLease waits across a renewal tick while a
// fixture lock remains held. Reloaded locked_until must exceed its initial value,
// protecting renewal of the captured acquisition before deferred release.
func TestSystemOperationLockService_RenewLease(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  1200 * time.Millisecond,
	})

	lock, err := svc.Acquire(context.Background(), "op-renew")
	require.NoError(t, err)
	require.NotNil(t, lock)
	// End the renewal lifetime after assertions even if they fail, keeping these
	// timing tests from leaving an active background fixture worker behind during later scenarios.
	defer func() {
		_ = svc.Release(context.Background(), lock, true, "")
	}()

	keyHash := HashIdempotencyKey(systemOperationLockKey)
	// Fixture lookups produce no storage error; inspect their returned record and
	// deadline directly so these renewal assertions remain focused on held-lock state changes.
	initial, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, initial)
	require.NotNil(t, initial.LockedUntil)
	initialLockedUntil := *initial.LockedUntil

	// Wait beyond the constructor's minimum one-second tick before observing the
	// lease again, allowing this held-lock scenario to inspect a scheduled renewal result.
	time.Sleep(1500 * time.Millisecond)

	// This synchronized fixture lookup has no storage error; its record and deadline
	// are the observations used to establish whether scheduled renewal changed held state.
	updated, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, updated)
	require.NotNil(t, updated.LockedUntil)
	require.True(t, updated.LockedUntil.After(initialLockedUntil), "locked_until should be renewed while lock is held")
}

// TestSystemOperationLockService_RenewLeaseContinuesAfterTransientFailure injects
// one failed renewal. A later tick must make another call and extend the recorded
// deadline, showing that this fixture error does not permanently stop renewal.
func TestSystemOperationLockService_RenewLeaseContinuesAfterTransientFailure(t *testing.T) {
	repo := &flakySystemLockRenewRepo{inMemoryIdempotencyRepo: newInMemoryIdempotencyRepo()}
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  2400 * time.Millisecond,
	})

	lock, err := svc.Acquire(context.Background(), "op-renew-transient")
	require.NoError(t, err)
	require.NotNil(t, lock)
	// End the renewal lifetime after assertions even if they fail, keeping these
	// timing tests from leaving an active background fixture worker behind during later scenarios.
	defer func() {
		_ = svc.Release(context.Background(), lock, true, "")
	}()

	keyHash := HashIdempotencyKey(systemOperationLockKey)
	// Fixture lookups produce no storage error; inspect their returned record and
	// deadline directly so these renewal assertions remain focused on held-lock state changes.
	initial, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, initial)
	require.NotNil(t, initial.LockedUntil)
	initialLockedUntil := *initial.LockedUntil

	// Observe the retry attempt and extended deadline together across later ticks,
	// so the injected first failure does not masquerade as a finished renewal lifetime.
	require.Eventually(t, func() bool {
		// This synchronized fixture lookup has no storage error; its record and deadline
		// are the observations used to establish whether scheduled renewal changed held state.
		updated, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
		if updated == nil || updated.LockedUntil == nil {
			return false
		}
		return atomic.LoadInt32(&repo.extendCalls) >= 2 && updated.LockedUntil.After(initialLockedUntil)
	}, 4*time.Second, 100*time.Millisecond, "renew loop should continue after transient error")
}

// TestSystemOperationLockService_SameOperationIDRetryWhileRunning repeats the same
// label while its first lease is live. It must receive busy metadata for that
// label until release, then acquire a new lock for the repeated operation ID.
func TestSystemOperationLockService_SameOperationIDRetryWhileRunning(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})

	lock1, err := svc.Acquire(context.Background(), "op-same")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	_, err = svc.Acquire(context.Background(), "op-same")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrSystemOperationBusy), errorCode(err))
	appErr := apperror.FromError(err)
	require.Equal(t, "op-same", appErr.Metadata["operation_id"])

	require.NoError(t, svc.Release(context.Background(), lock1, true, ""))

	lock2, err := svc.Acquire(context.Background(), "op-same")
	require.NoError(t, err)
	require.NotNil(t, lock2)
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

// TestSystemOperationLockService_RecoverAfterLeaseExpired stops the first lock
// renewal to model a vanished process. After its short deadline passes, another
// operation must acquire and release the retained fixture row.
func TestSystemOperationLockService_RecoverAfterLeaseExpired(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  300 * time.Millisecond,
	})

	lock1, err := svc.Acquire(context.Background(), "op-crashed")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	// Stop further renewal while retaining the captured fixture row, modelling the
	// stored lease left behind after a process stops maintaining ownership.
	lock1.stopOnce.Do(func() {
		close(lock1.stopCh)
	})

	// Let the retained short deadline pass after stopping renewal, keeping the
	// next acquisition focused on expiration-based takeover rather than explicit successful release.
	time.Sleep(450 * time.Millisecond)

	lock2, err := svc.Acquire(context.Background(), "op-recovered")
	require.NoError(t, err, "expired lease should allow a new operation to reclaim lock")
	require.NotNil(t, lock2)
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

// TestSystemOperationLockService_InputAndStoreErrorBranches supplies absent services,
// absent stores, empty operation labels, and an injected acquisition failure. Error
// categories must distinguish required identity from unavailable lease storage.
func TestSystemOperationLockService_InputAndStoreErrorBranches(t *testing.T) {
	var nilSvc *SystemOperationLockService
	_, err := nilSvc.Acquire(context.Background(), "x")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrIdempotencyStoreUnavail), errorCode(err))

	svc := &SystemOperationLockService{repo: nil}
	_, err = svc.Acquire(context.Background(), "x")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrIdempotencyStoreUnavail), errorCode(err))

	svc = NewSystemOperationLockService(newInMemoryIdempotencyRepo(), Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	_, err = svc.Acquire(context.Background(), "")
	require.Error(t, err)
	require.Equal(t, "SYSTEM_OPERATION_ID_REQUIRED", errorReason(err))

	badStore := &systemLockRepoStub{createErr: errors.New("db down")}
	svc = NewSystemOperationLockService(badStore, Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	_, err = svc.Acquire(context.Background(), "x")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrIdempotencyStoreUnavail), errorCode(err))
}

// TestSystemOperationLockService_ExistingNilAndReclaimBranches selects a missing
// busy record, injected reclaim error, and a losing ownership result. Acquisition
// must classify storage uncertainty separately from a live busy-operation response.
func TestSystemOperationLockService_ExistingNilAndReclaimBranches(t *testing.T) {
	now := time.Now()
	repo := &systemLockRepoStub{
		createOwner: false,
	}
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})

	_, err := svc.Acquire(context.Background(), "op")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrIdempotencyStoreUnavail), errorCode(err))

	repo.existing = &IdempotencyRecord{
		ID:                 1,
		Scope:              systemOperationLockScope,
		IdempotencyKeyHash: HashIdempotencyKey(systemOperationLockKey),
		RequestFingerprint: "other-op",
		Status:             IdempotencyStatusFailedRetryable,
		LockedUntil:        ptrTime(now.Add(-time.Second)),
		ExpiresAt:          now.Add(time.Hour),
	}
	repo.reclaimErr = errors.New("reclaim failed")
	_, err = svc.Acquire(context.Background(), "op")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrIdempotencyStoreUnavail), errorCode(err))

	repo.reclaimErr = nil
	repo.reclaimOK = false
	_, err = svc.Acquire(context.Background(), "op")
	require.Error(t, err)
	require.Equal(t, errorCode(ErrSystemOperationBusy), errorCode(err))
}

// TestSystemOperationLockService_ReleaseBranchesAndOperationID checks nil diagnostics,
// repeated completion, and injected success/failure persistence errors with stopped
// renewal fixtures. Release results must remain cached and retain the tested error or no-op handling.
func TestSystemOperationLockService_ReleaseBranchesAndOperationID(t *testing.T) {
	require.Equal(t, "", (*SystemOperationLock)(nil).OperationID())

	svc := NewSystemOperationLockService(newInMemoryIdempotencyRepo(), Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	lock, err := svc.Acquire(context.Background(), "op")
	require.NoError(t, err)
	require.NotNil(t, lock)

	require.NoError(t, svc.Release(context.Background(), lock, false, ""))
	require.NoError(t, svc.Release(context.Background(), lock, true, ""))

	repo := &systemLockRepoStub{
		createOwner: true,
		markSuccErr: errors.New("mark succeeded failed"),
		markFailErr: errors.New("mark failed failed"),
	}
	svc = NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 10 * time.Second,
		ProcessingTimeout:  2 * time.Second,
	})
	lock = &SystemOperationLock{recordID: 1, operationID: "op2", stopCh: make(chan struct{}), cancel: func() {}, done: closedTestChannel()}
	require.Error(t, svc.Release(context.Background(), lock, true, ""))
	lock = &SystemOperationLock{recordID: 1, operationID: "op3", stopCh: make(chan struct{}), cancel: func() {}, done: closedTestChannel()}
	require.Error(t, svc.Release(context.Background(), lock, false, "BAD"))

	var nilLockSvc *SystemOperationLockService
	require.NoError(t, nilLockSvc.Release(context.Background(), nil, true, ""))

	err = svc.busyError("", nil, time.Now())
	require.Equal(t, errorCode(ErrSystemOperationBusy), errorCode(err))
}

// TestSameOperationReclaimed expires a live fixture row and reacquires the same
// operation label. The new token must differ, an earlier owner must fail release,
// and the new owner's repeated release must retain its first successful result.
func TestSameOperationReclaimed(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	s := NewSystemOperationLockService(repo, Options{ProcessingTimeout: time.Hour, SystemOperationTTL: 2 * time.Hour})
	first, err := s.Acquire(context.Background(), "same")
	require.NoError(t, err)
	// Expire the stored deadline while keeping the first token and operation label,
	// so reacquiring the same ID exercises new ownership rather than another namespace or action.
	repo.mu.Lock()
	for _, v := range repo.data {
		past := time.Now().Add(-time.Second)
		v.LockedUntil = &past
	}
	repo.mu.Unlock()
	second, err := s.Acquire(context.Background(), "same")
	require.NoError(t, err)
	require.NotEqual(t, first.ownership, second.ownership)
	require.ErrorIs(t, s.Release(context.Background(), first, true, ""), ErrOperationOwnershipLost)
	require.NoError(t, s.Release(context.Background(), second, true, ""))
	require.NoError(t, s.Release(context.Background(), second, false, "late"))
}

// RenewOperation counts attempts atomically and injects the first renewal error.
// Later calls delegate the supplied ownership and deadlines to the synchronized
// fixture, allowing the retry test to observe both a subsequent call and extended lease.
func (r *flakySystemLockRenewRepo) RenewOperation(ctx context.Context, id int64, requestFingerprint, ownership string, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	call := atomic.AddInt32(&r.extendCalls, 1)
	if call == 1 {
		return false, errors.New("transient extend failure")
	}
	return r.inMemoryIdempotencyRepo.RenewOperation(ctx, id, requestFingerprint, ownership, newLockedUntil, newExpiresAt)
}

// HashIdempotencyKey delegates fixture lock-key hashing to the shared idempotency implementation.
func HashIdempotencyKey(s string) string { return idempotency.HashIdempotencyKey(s) }

// ptrTime returns a pointer holding the supplied fixture lease timestamp.
func ptrTime(t time.Time) *time.Time { return &t }

// errorCode returns the application category code for a nonnil fixture error.
func errorCode(err error) int { return int(apperror.FromError(err).Code) }

// errorReason returns the application reason text for a nonnil fixture error.
func errorReason(err error) string { return apperror.FromError(err).Reason }

// closedTestChannel returns an already completed renewal signal for manual release fixtures.
func closedTestChannel() chan struct{} { ch := make(chan struct{}); close(ch); return ch }

// ClaimOperation acquires an absent or unlocked fixture row and returns a shallow
// record copy plus ownership. A live deadline returns a shallow busy copy; nullable
// pointers remain shared, so consumers treat pointed-to values as immutable until fields are replaced.
//
// Lookup, expiry comparison, identifier selection, and publication share the fixture
// mutex so competing calls observe one current token and operation label together.
// Reclaim retains the row ID but replaces its record and captured ownership.
// The helper ignores ctx, using synchronized memory to isolate lease decisions
// from external database cancellation and connection behavior.
func (r *inMemoryIdempotencyRepo) ClaimOperation(ctx context.Context, c idempotency.OperationClaim) (*idempotency.IdempotencyRecord, bool, error) {
	// Couple availability and replacement under one lock so a competing fixture
	// acquisition cannot publish a different token between the deadline check and this claim's record update.
	r.mu.Lock()
	defer r.mu.Unlock()
	key := r.key(c.Scope, c.KeyHash)
	record := r.data[key]
	if record != nil && record.LockedUntil != nil && record.LockedUntil.After(c.Now) {
		copy := *record
		return &copy, false, nil
	}
	id := r.nextID
	if record != nil {
		id = record.ID
	} else {
		r.nextID++
	}
	record = &idempotency.IdempotencyRecord{ID: id, Scope: c.Scope, IdempotencyKeyHash: c.KeyHash, RequestFingerprint: c.OperationID, Status: idempotency.IdempotencyStatusProcessing, ResponseBody: &c.Ownership, LockedUntil: &c.LockedUntil, ExpiresAt: c.ExpiresAt}
	r.data[key] = record
	copy := *record
	return &copy, true, nil
}

// RenewOperation replaces fixture deadlines when row ID, processing state,
// operation label, and exact owner text still match, returning whether it changed.
// A missing or replaced processing owner returns false.
//
// The comparison and update share the fixture mutex, so a takeover cannot
// interleave between them. This models the maintained store predicate, including
// renewal after deadline expiry while captured identity still matches; it does
// not add a separate cancellation check to the memory fixture.
func (r *inMemoryIdempotencyRepo) RenewOperation(ctx context.Context, id int64, operation, ownership string, until, expires time.Time) (bool, error) {
	// Keep identity checks and both deadline changes under the same fixture lock,
	// so competing acquisition and assertion reads cannot interleave with an accepted renewal transition.
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.data {
		if v.ID == id && v.Status == idempotency.IdempotencyStatusProcessing && v.RequestFingerprint == operation && v.ResponseBody != nil && *v.ResponseBody == ownership {
			v.LockedUntil = &until
			v.ExpiresAt = expires
			return true, nil
		}
	}
	return false, nil
}

// FinishOperation records success or retryable failure for the matching processing
// fixture owner and reports whether it changed a row. Both outcomes clear the
// lock and update retention, while response token and reason fields remain untouched here.
//
// Ownership comparison and mutation share the fixture mutex so an earlier worker
// cannot finish a replacement token's processing row. Finished state then rejects
// later renewal despite retaining response_body. The helper models ownership
// rejection and release eligibility, while the SQL store's detailed response and
// failure-deadline encoding are outside this fixture's field updates.
func (r *inMemoryIdempotencyRepo) FinishOperation(ctx context.Context, id int64, operation, ownership string, success bool, reason string, expires time.Time) (bool, error) {
	// Compare the captured token and finish its state while holding the mutation
	// lock, making stale-owner rejection observable before a different acquisition can be marked finished.
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.data {
		if v.ID == id && v.Status == idempotency.IdempotencyStatusProcessing && v.RequestFingerprint == operation && v.ResponseBody != nil && *v.ResponseBody == ownership {
			v.Status = idempotency.IdempotencyStatusFailedRetryable
			if success {
				v.Status = idempotency.IdempotencyStatusSucceeded
			}
			v.LockedUntil = nil
			v.ExpiresAt = expires
			return true, nil
		}
	}
	return false, nil
}

// ClaimOperation returns the selected acquisition error, then missing-ID ownership,
// or the cloned configured record with reclaimOK. Error precedence is createErr,
// getErr, and reclaimErr, allowing sequential tests to target each service result branch.
func (s *systemLockRepoStub) ClaimOperation(ctx context.Context, c idempotency.OperationClaim) (*idempotency.IdempotencyRecord, bool, error) {
	if s.createErr != nil {
		return nil, false, s.createErr
	}
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	if s.reclaimErr != nil {
		return nil, false, s.reclaimErr
	}
	if s.createOwner {
		return &idempotency.IdempotencyRecord{}, true, nil
	}
	return cloneRecord(s.existing), s.reclaimOK, nil
}

// RenewOperation reports success inertly for this branch-only acquisition fixture store.
func (s *systemLockRepoStub) RenewOperation(context.Context, int64, string, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

// FinishOperation reports a matched owner with the configured success or failure
// persistence error. It selects the error using success without mutating fixture
// data, so manually stopped locks can isolate Release's completion error handling.
func (s *systemLockRepoStub) FinishOperation(ctx context.Context, id int64, op, owner string, success bool, reason string, expires time.Time) (bool, error) {
	if success {
		return true, s.markSuccErr
	}
	return true, s.markFailErr
}

// newInMemoryIdempotencyRepo initializes independent fixture records and its first lease identifier.
func newInMemoryIdempotencyRepo() *inMemoryIdempotencyRepo {
	return &inMemoryIdempotencyRepo{
		nextID: 1,
		data:   make(map[string]*IdempotencyRecord),
	}
}

// key joins controlled scope and hash components for fixture record lookup.
// Inputs use the fixed maintenance scope and hashed key, so this test index
// relies on those components not introducing ambiguous delimiter-bearing pairs.
func (r *inMemoryIdempotencyRepo) key(scope, hash string) string {
	return scope + "|" + hash
}

// cloneRecord copies fixture record values and independently allocates its nullable
// pointer fields, returning nil for absent input. Shared memory-store callers hold
// their mutex while cloning; branch fixtures access their configured record sequentially.
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

// GetByScopeAndKeyHash returns a cloned matching fixture record or nil when absent.
// Nullable fields are copied under the record lock for assertion reads; the
// helper ignores context and produces no external storage error.
func (r *inMemoryIdempotencyRepo) GetByScopeAndKeyHash(_ context.Context, scope, keyHash string) (*IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneRecord(r.data[r.key(scope, keyHash)]), nil
}
