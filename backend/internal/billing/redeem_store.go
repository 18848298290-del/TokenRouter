package billing

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Redemption coordinates an advisory cache with durable credential and usage
// records. Repository locks and per-user usage checks protect grant-state
// decisions, while history queries use individual recipient/time observations
// even when a multi-use code's summary points to a later recipient.
//
// Batch changes retain field presence independently of values.
// NullableTimeUpdate distinguishes omitted expiry from a supplied clear, and
// core entitlement pointers remain represented so the service can reject them
// before the ordinary batch write. The repository's locked read methods
// require caller transaction scope; complete entitlement issuance stays with
// RedeemService, which combines those reads with fund and usage writes.

// RedeemCache supplies advisory user failure counts and exact-code exclusion
// for redemption.
type RedeemCache interface {
	// GetRedeemAttemptCount returns saved failure count or zero for an absent
	// counter. Cache errors remain available so RedeemService can apply its
	// best-effort admission policy for this user.
	GetRedeemAttemptCount(ctx context.Context, userID int64) (int, error)
	// IncrementRedeemAttemptCount records a user failure and renews its counter
	// expiry. The cache implementation determines partial/retry effects, and
	// callers use the operation as advisory evidence rather than an exactly-once
	// fund record.
	IncrementRedeemAttemptCount(ctx context.Context, userID int64) error

	// AcquireRedeemLock attempts exclusion for the exact code with the supplied
	// lease duration. False with no error means held exclusion; callers retain
	// database validation/locking and use the implementation's release ownership
	// rule.
	AcquireRedeemLock(ctx context.Context, code string, ttl time.Duration) (bool, error)
	// ReleaseRedeemLock ends advisory exclusion for the supplied code. Callers
	// coordinate it with acquisition lifetime and the concrete cache's ownership
	// checks; cache errors do not replace authoritative database grant state.
	ReleaseRedeemLock(ctx context.Context, code string) error
}

// RedeemCodeRepository persists grant credentials, usage observations and
// administrator/history queries.
type RedeemCodeRepository interface {
	// Create persists a non-nil code and writes its generated ID/creation time
	// back on success. Callers validate business eligibility and uniqueness
	// inputs before persistence, while supplied transaction context determines
	// participation.
	Create(ctx context.Context, code *RedeemCode) error
	// CreateBatch persists the supplied code records, with empty input
	// succeeding. The PostgreSQL implementation does not populate each slice
	// record's generated ID; callers own input validation and transaction
	// selection for bulk creation.
	CreateBatch(ctx context.Context, codes []RedeemCode) error
	// GetByID reads a code with its hydrated user/plan data for administrator or
	// response mapping. Missing records return code-not-found; consumers retain
	// the returned record's optional sharing rules during use.
	GetByID(ctx context.Context, id int64) (*RedeemCode, error)
	// GetByIDForUpdate reads and locks the code row, returning code-not-found
	// when absent. Callers supply a transaction so the lock survives the read,
	// and obtain any hydrated edges separately when needed.
	GetByIDForUpdate(ctx context.Context, id int64) (*RedeemCode, error)
	// GetByCode reads the exact code spelling with hydrated user/plan data for
	// response mapping and existing-code recovery. Missing records return
	// code-not-found, and callers normalize credential text before this lookup
	// when their entry point requires trimming.
	GetByCode(ctx context.Context, code string) (*RedeemCode, error)
	// GetByCodeForUpdate reads and locks the exact code row, returning
	// code-not-found when absent. Callers supply a transaction to retain the lock
	// through grant/update work and obtain hydrated edges separately.
	GetByCodeForUpdate(ctx context.Context, code string) (*RedeemCode, error)
	// Update replaces the supplied non-nil code snapshot, including clearing nil
	// optional fields. Callers own authorization and used-value consistency
	// checks; the PostgreSQL implementation refreshes CreatedAt from the
	// persisted row and maps absence to code-not-found.
	Update(ctx context.Context, code *RedeemCode) error
	// BatchUpdate applies supplied status/notes/expiry changes to distinct target
	// IDs and returns the changed count. The service validates positive IDs and
	// excludes core changes; the PostgreSQL adapter checks target presence and
	// sensitive used-code changes in its selected transaction.
	BatchUpdate(ctx context.Context, ids []int64, fields RedeemCodeBatchUpdateFields) (int64, error)
	// Delete removes the code row through the selected persistence connection.
	// Callers check delete eligibility before invoking it; the PostgreSQL delete
	// does not interpret zero affected rows as a missing-code error.
	Delete(ctx context.Context, id int64) error
	// Use records one user usage and advances the credential snapshot in a locked
	// transaction. It rejects unavailable or already-used-by-this-user codes
	// before inserting their receipt and updating the count and latest recipient.
	//
	// Caller-owned transactions retain commit and recovery responsibility; an
	// owned transaction completes both writes together. A receipt failure stops
	// before the summary update. Entitlement grants and paid-order eligibility
	// belong to RedeemService, so invoking this method alone records usage rather
	// than issuing the credential's benefit.
	Use(ctx context.Context, id, userID int64) error
	// CreateUsage persists the supplied recipient/time observation and fills its
	// generated ID on success. Callers coordinate it with entitlement and
	// credential updates in the same transaction when a complete redemption is
	// required.
	CreateUsage(ctx context.Context, usage *RedeemCodeUsage) error
	// GetUsageByRedeemCodeAndUser returns the user's saved observation for the
	// code, or nil with no error when absent. Other errors remain available
	// before the service decides whether the recipient has already redeemed.
	GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*RedeemCodeUsage, error)

	// List returns credential rows and page metadata with the repository's default
	// ordering. Callers supply pagination limits and interpret the total as all
	// matches, with page and count reads governed by the adapter's consistency rules.
	List(ctx context.Context, params pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error)
	// ListWithFilters returns paginated credentials using supplied type, status
	// and code/recipient search filters. Empty filters omit conditions;
	// PostgreSQL status predicates use database time, and the expiry predicate
	// can include an expired disabled code even when its returned display label
	// remains disabled. Ordering supplies deterministic ID tie-breaking.
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, codeType, status, search string) ([]RedeemCode, *pagination.PaginationResult, error)
	// ListByUserPaginated returns the user's redemption/adjustment history with
	// an optional type filter. The history maps each usage's recipient/time into
	// its code view, retaining the individual event rather than the code's latest
	// recipient.
	ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]RedeemCode, *pagination.PaginationResult, error)
	// SumPositiveBalanceByUser sums positive balance and administrator-balance
	// credential values referenced by the user's usage history. It is a history
	// query distinct from the user's stored total_recharged column, and
	// persistence failures propagate to the history caller.
	SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error)
}

// NullableTimeUpdate represents a supplied expiry change separately from an
// optional replacement timestamp.
type NullableTimeUpdate struct {
	// Set and Value distinguish omission from clearing or replacement. Set false
	// leaves expiry unchanged; with Set true, nil Value requests clearing the
	// saved expiry.
	Set   bool       // expiry replacement was supplied
	Value *time.Time // optional replacement expiry timestamp
}

// RedeemCodeBatchUpdateFields carries supplied common changes and attempted
// entitlement fields for validation.
type RedeemCodeBatchUpdateFields struct {
	Status    *string            // nil retains current status
	ExpiresAt NullableTimeUpdate // presence-aware expiry replacement request
	Notes     *string            // nil retains current notes

	// Type, Value, MaxUses and PlanID retain attempted entitlement changes for
	// service rejection. Batch mutation accepts common status/notes/expiry
	// fields; individual entitlement edits use their separate consistency checks.
	Type    *string  // attempted entitlement type change
	Value   *float64 // attempted entitlement amount change
	MaxUses *int     // attempted lifetime capacity change
	PlanID  *int64   // attempted subscription plan change
}

// RedeemCodeBatchUpdateInput supplies target code identities and their common
// requested changes.
type RedeemCodeBatchUpdateInput struct {
	IDs    []int64                     // requested target code identifiers
	Fields RedeemCodeBatchUpdateFields // common requested field changes
}

// RedeemCodeBatchUpdateResult reports the number of rows changed by a batch
// operation.
type RedeemCodeBatchUpdateResult struct {
	Updated int64 `json:"updated"` // reported changed row count
}

// HasChanges reports whether any pointer or expiry-presence flag requests a
// change.
func (f RedeemCodeBatchUpdateFields) HasChanges() bool {
	return f.Status != nil ||
		f.ExpiresAt.Set ||
		f.Notes != nil ||
		f.Type != nil ||
		f.Value != nil ||
		f.MaxUses != nil ||
		f.PlanID != nil
}

// HasCoreFieldChanges reports attempted type, amount, lifetime capacity or
// subscription-plan changes.
func (f RedeemCodeBatchUpdateFields) HasCoreFieldChanges() bool {
	return f.Type != nil || f.Value != nil || f.MaxUses != nil || f.PlanID != nil
}

// TouchesUsedSensitiveFields reports status/expiry changes requiring the batch
// used-code guard.
func (f RedeemCodeBatchUpdateFields) TouchesUsedSensitiveFields() bool {
	return f.Status != nil || f.ExpiresAt.Set
}
