package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
)

// HTTP idempotency scenarios need competing requests to see one record per scope
// and key, while assertion reads should not mutate stored nullable fields. The
// store serializes lookups and state updates with a mutex and copies every pointer
// field when inserting or reading a record. IDs are assigned monotonically within
// each newly constructed store.
//
// Reclaim checks the expected status and lock window under the same mutex as
// mutation, matching the coordinator's conditional claim decisions. Outcome writes
// search by record ID and retain the same response and failure field behavior as
// the SQL command adapter. Expiry cleanup is inert here; database visibility,
// transaction behavior, and retention deletion remain integration-test concerns.

// MemoryStore stores records for HTTP idempotency tests; PostgreSQL transaction behavior needs
// integration tests.
type MemoryStore struct {
	// mu protects nextID and data during every lookup and update. Returned records
	// are cloned under that lock before tests or coordinator calls can inspect them.
	mu     sync.Mutex                                // stored record mutation lock
	nextID int64                                     // next assigned record identifier
	data   map[string]*idempotency.IdempotencyRecord // scope/key-indexed command records
}

// NewMemoryStore creates an independent in-memory record set for a test.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		nextID: 1,
		data:   make(map[string]*idempotency.IdempotencyRecord),
	}
}

// key combines the supplied scope and key hash for fixture map indexing.
// Callers use controlled scopes and hashes; the separator is not escaped, so this
// helper does not provide collision-free encoding for arbitrary delimiter-bearing components.
func (r *MemoryStore) key(scope, keyHash string) string {
	return scope + "|" + keyHash
}

// clone copies a fixture record and all of its nullable pointer fields.
// A nil source returns nil, and callers hold the store lock while copying
// mutable stored records so subsequent fixture updates leave the returned values independent.
func (r *MemoryStore) clone(in *idempotency.IdempotencyRecord) *idempotency.IdempotencyRecord {
	if in == nil {
		return nil
	}
	out := *in
	if in.LockedUntil != nil {
		v := *in.LockedUntil
		out.LockedUntil = &v
	}
	if in.ResponseBody != nil {
		v := *in.ResponseBody
		out.ResponseBody = &v
	}
	if in.ResponseStatus != nil {
		v := *in.ResponseStatus
		out.ResponseStatus = &v
	}
	if in.ErrorReason != nil {
		v := *in.ErrorReason
		out.ErrorReason = &v
	}
	return &out
}

// CreateProcessing stores a clone of a nonnil record when its fixture key is
// absent and reports ownership. It assigns the caller's ID before returning;
// an existing key returns false and leaves that caller record unchanged.
//
// The fixture mutex spans key admission, identifier allocation, and publication
// of the clone, letting concurrent HTTP requests select one initial owner.
// Nullable fields are copied before publication so later storage updates and
// assertion reads can use their values independently of the submitted record.
func (r *MemoryStore) CreateProcessing(_ context.Context, record *idempotency.IdempotencyRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Check the key, allocate its ID, and publish its cloned record under one lock,
	// so simultaneous HTTP requests cannot both receive initial execution ownership from this fixture.
	k := r.key(record.Scope, record.IdempotencyKeyHash)
	if _, ok := r.data[k]; ok {
		return false, nil
	}
	cp := r.clone(record)
	cp.ID = r.nextID
	r.nextID++
	r.data[k] = cp
	record.ID = cp.ID
	return true, nil
}

// GetByScopeAndKeyHash returns an independent clone of the matching fixture record.
// A missing key returns nil without an error, mirroring the coordinator's lookup
// decision while allowing assertions to inspect nullable fields without mutating storage.
func (r *MemoryStore) GetByScopeAndKeyHash(_ context.Context, scope, keyHash string) (*idempotency.IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clone(r.data[r.key(scope, keyHash)]), nil
}

// TryReclaim finds a record by ID and changes the expected status to processing
// after its lock window ends. It installs both supplied deadlines and clears the
// error reason, returning false for missing or ineligible records.
//
// The lookup, eligibility checks, and mutation share the store mutex so a
// competing fixture request cannot install another active lock between them.
// Existing response fields remain available in storage until a later outcome
// replaces them, matching the SQL adapter's reclaim field updates.
func (r *MemoryStore) TryReclaim(_ context.Context, id int64, fromStatus string, now, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep status and lock-window checks with their mutation under the same lock,
	// so the fixture models one reclaim winner rather than two independently accepted retry claims.
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
		rec.Status = idempotency.IdempotencyStatusProcessing
		rec.LockedUntil = &newLockedUntil
		rec.ExpiresAt = newExpiresAt
		rec.ErrorReason = nil
		return true, nil
	}
	return false, nil
}

// ExtendProcessingLock updates both deadlines for the requested fixture record
// when its processing state and fingerprint still match. False identifies missing,
// finished, or differently fingerprinted records, and prior lock expiry is not checked.
//
// The fingerprint describes a request rather than one acquisition of its record.
// Its comparison matches the SQL command adapter, including accepting another
// acquisition with that same fingerprint. Fixtures requiring owner-token renewal
// use the maintenance lease store instead of this HTTP command store.
func (r *MemoryStore) ExtendProcessingLock(_ context.Context, id int64, requestFingerprint string, newLockedUntil, newExpiresAt time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Check processing identity before mutating deadlines under the store lock,
	// retaining the SQL adapter's conditional renewal behavior across competing HTTP fixture calls.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		if rec.Status != idempotency.IdempotencyStatusProcessing || rec.RequestFingerprint != requestFingerprint {
			return false, nil
		}
		rec.LockedUntil = &newLockedUntil
		rec.ExpiresAt = newExpiresAt
		return true, nil
	}
	return false, nil
}

// MarkSucceeded replaces outcome fields for the fixture record identified by ID,
// clearing its lock and failure reason. Missing IDs succeed silently, matching
// the SQL adapter's lack of an affected-row check for ordinary command completion.
func (r *MemoryStore) MarkSucceeded(_ context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Update response, retention, status, and lock fields in one locked pass so
	// concurrent fixture lookups observe either the earlier record or this completed outcome.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		rec.Status = idempotency.IdempotencyStatusSucceeded
		rec.LockedUntil = nil
		rec.ExpiresAt = expiresAt
		rec.ResponseStatus = &responseStatus
		rec.ResponseBody = &responseBody
		rec.ErrorReason = nil
		return nil
	}
	return nil
}

// MarkFailedRetryable records the reason and retry deadline for a fixture record
// by ID while leaving response fields in place. Missing IDs succeed silently;
// the coordinator receives the same completion behavior as the ordinary SQL adapter.
func (r *MemoryStore) MarkFailedRetryable(_ context.Context, id int64, errorReason string, lockedUntil, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Publish the retry state and its backoff deadline together under the mutex,
	// so a concurrent duplicate cannot read a failed status with the previous lock window.
	for _, rec := range r.data {
		if rec.ID != id {
			continue
		}
		rec.Status = idempotency.IdempotencyStatusFailedRetryable
		rec.LockedUntil = &lockedUntil
		rec.ExpiresAt = expiresAt
		rec.ErrorReason = &errorReason
		return nil
	}
	return nil
}

// DeleteExpired leaves fixture records intact because HTTP tests do not exercise retention.
func (r *MemoryStore) DeleteExpired(_ context.Context, _ time.Time, _ int) (int64, error) {
	return 0, nil
}
