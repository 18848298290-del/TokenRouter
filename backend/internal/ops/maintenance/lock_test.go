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

const (
	IdempotencyStatusFailedRetryable = idempotency.IdempotencyStatusFailedRetryable
)

type flakySystemLockRenewRepo struct {
	*inMemoryIdempotencyRepo
	extendCalls int32
}

type systemLockRepoStub struct {
	createOwner bool
	createErr   error
	existing    *IdempotencyRecord
	getErr      error
	reclaimOK   bool
	reclaimErr  error
	markSuccErr error
	markFailErr error
}

type IdempotencyRecord = idempotency.IdempotencyRecord

// inMemoryIdempotencyRepo stores idempotency records for maintenance lock tests.
type inMemoryIdempotencyRepo struct {
	mu     sync.Mutex
	nextID int64
	data   map[string]*IdempotencyRecord
}

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

func TestSystemOperationLockService_RenewLease(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  1200 * time.Millisecond,
	})

	lock, err := svc.Acquire(context.Background(), "op-renew")
	require.NoError(t, err)
	require.NotNil(t, lock)
	defer func() {
		_ = svc.Release(context.Background(), lock, true, "")
	}()

	keyHash := HashIdempotencyKey(systemOperationLockKey)
	initial, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, initial)
	require.NotNil(t, initial.LockedUntil)
	initialLockedUntil := *initial.LockedUntil

	time.Sleep(1500 * time.Millisecond)

	updated, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, updated)
	require.NotNil(t, updated.LockedUntil)
	require.True(t, updated.LockedUntil.After(initialLockedUntil), "locked_until should be renewed while lock is held")
}

func TestSystemOperationLockService_RenewLeaseContinuesAfterTransientFailure(t *testing.T) {
	repo := &flakySystemLockRenewRepo{inMemoryIdempotencyRepo: newInMemoryIdempotencyRepo()}
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  2400 * time.Millisecond,
	})

	lock, err := svc.Acquire(context.Background(), "op-renew-transient")
	require.NoError(t, err)
	require.NotNil(t, lock)
	defer func() {
		_ = svc.Release(context.Background(), lock, true, "")
	}()

	keyHash := HashIdempotencyKey(systemOperationLockKey)
	initial, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
	require.NotNil(t, initial)
	require.NotNil(t, initial.LockedUntil)
	initialLockedUntil := *initial.LockedUntil

	// After the first renewal fails, the next tick must retry and extend the lease.
	require.Eventually(t, func() bool {
		updated, _ := repo.GetByScopeAndKeyHash(context.Background(), systemOperationLockScope, keyHash)
		if updated == nil || updated.LockedUntil == nil {
			return false
		}
		return atomic.LoadInt32(&repo.extendCalls) >= 2 && updated.LockedUntil.After(initialLockedUntil)
	}, 4*time.Second, 100*time.Millisecond, "renew loop should continue after transient error")
}

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

func TestSystemOperationLockService_RecoverAfterLeaseExpired(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	svc := NewSystemOperationLockService(repo, Options{
		SystemOperationTTL: 5 * time.Second,
		ProcessingTimeout:  300 * time.Millisecond,
	})

	lock1, err := svc.Acquire(context.Background(), "op-crashed")
	require.NoError(t, err)
	require.NotNil(t, lock1)

	// Simulate a process crash after lease renewal stops.
	lock1.stopOnce.Do(func() {
		close(lock1.stopCh)
	})

	time.Sleep(450 * time.Millisecond)

	lock2, err := svc.Acquire(context.Background(), "op-recovered")
	require.NoError(t, err, "expired lease should allow a new operation to reclaim lock")
	require.NotNil(t, lock2)
	require.NoError(t, svc.Release(context.Background(), lock2, true, ""))
}

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

// TestSameOperationReclaimed checks that reclaiming the same operation ID assigns a new owner token.
func TestSameOperationReclaimed(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	s := NewSystemOperationLockService(repo, Options{ProcessingTimeout: time.Hour, SystemOperationTTL: 2 * time.Hour})
	first, err := s.Acquire(context.Background(), "same")
	require.NoError(t, err)
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

func (r *flakySystemLockRenewRepo) RenewOperation(ctx context.Context, id int64, requestFingerprint, ownership string, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	call := atomic.AddInt32(&r.extendCalls, 1)
	if call == 1 {
		return false, errors.New("transient extend failure")
	}
	return r.inMemoryIdempotencyRepo.RenewOperation(ctx, id, requestFingerprint, ownership, newLockedUntil, newExpiresAt)
}

func HashIdempotencyKey(s string) string { return idempotency.HashIdempotencyKey(s) }

func ptrTime(t time.Time) *time.Time { return &t }

func errorCode(err error) int { return int(apperror.FromError(err).Code) }

func errorReason(err error) string { return apperror.FromError(err).Reason }

func closedTestChannel() chan struct{} { ch := make(chan struct{}); close(ch); return ch }

// ClaimOperation checks lease expiration and records the new owner token.
func (r *inMemoryIdempotencyRepo) ClaimOperation(ctx context.Context, c idempotency.OperationClaim) (*idempotency.IdempotencyRecord, bool, error) {
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

func (r *inMemoryIdempotencyRepo) RenewOperation(ctx context.Context, id int64, operation, ownership string, until, expires time.Time) (bool, error) {
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

func (r *inMemoryIdempotencyRepo) FinishOperation(ctx context.Context, id int64, operation, ownership string, success bool, reason string, expires time.Time) (bool, error) {
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

func (s *systemLockRepoStub) RenewOperation(context.Context, int64, string, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (s *systemLockRepoStub) FinishOperation(ctx context.Context, id int64, op, owner string, success bool, reason string, expires time.Time) (bool, error) {
	if success {
		return true, s.markSuccErr
	}
	return true, s.markFailErr
}

func newInMemoryIdempotencyRepo() *inMemoryIdempotencyRepo {
	return &inMemoryIdempotencyRepo{
		nextID: 1,
		data:   make(map[string]*IdempotencyRecord),
	}
}

func (r *inMemoryIdempotencyRepo) key(scope, hash string) string {
	return scope + "|" + hash
}

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

func (r *inMemoryIdempotencyRepo) GetByScopeAndKeyHash(_ context.Context, scope, keyHash string) (*IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneRecord(r.data[r.key(scope, keyHash)]), nil
}
