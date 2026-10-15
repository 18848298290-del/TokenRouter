package billing

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// subscription_store.go defines subscription persistence operations used by
// grant and maintenance services.
// Subscription services use repository reads to build grant timelines and
// persisted quota observations. Mutations share the caller-selected
// persistence context, while lock and transaction ownership come from
// SubscriptionTransactions. Conditional resets can succeed after another
// request changed the anchor, so admission rereads stored counters before
// using locally cleared usage.
//
// Ordinary PostgreSQL reads use soft-delete query scope, while restoration
// loads deleted records with their stored labels for service decisions.
// Snapshot updates leave nil optional columns unchanged through nillable
// setters; dedicated reset/restoration operations carry their own clearing
// rules. Services compute eligible lifetimes, chain changes and status before
// invoking these persistence methods, and returned row/count observations need
// not come from one atomic database snapshot.

// UserSubscriptionRepository supplies issued grant persistence, timelines and
// quota-window mutation for services.
type UserSubscriptionRepository interface {
	// Create persists a nonnil issued grant through the selected persistence
	// context. The PostgreSQL adapter fills ID/creation/update metadata on
	// success; database defaults for other omitted fields are not fully copied
	// back into the input.
	Create(ctx context.Context, sub *UserSubscription) error
	// GetByID returns a hydrated grant or the subscription not-found error for a
	// missing row. The PostgreSQL adapter uses ordinary soft-delete query scope
	// and maps included deletion markers to the revoked display label.
	GetByID(ctx context.Context, id int64) (*UserSubscription, error)
	// GetByIDIncludeDeleted returns the hydrated grant including deleted rows
	// with its stored status retained. Restoration uses that label separately
	// from the deletion-derived revoked display state; missing rows return the
	// subscription not-found error.
	GetByIDIncludeDeleted(ctx context.Context, id int64) (*UserSubscription, error)
	// GetLatestByUserIDAndPlanID selects the owner's plan grant with latest
	// expiry and then creation time. It does not require current eligibility, so
	// issuance uses the returned lifetime to decide whether a new grant queues
	// afterward.
	GetLatestByUserIDAndPlanID(ctx context.Context, userID, planID int64) (*UserSubscription, error)
	// Update persists a nonnil grant snapshot in the selected context and fills
	// returned metadata on success. PostgreSQL nillable setters leave nil
	// optional fields unchanged rather than clearing their columns; services own
	// prior locking and eligibility checks.
	Update(ctx context.Context, sub *UserSubscription) error
	// Delete removes the selected grant according to persistence context.
	// Ordinary PostgreSQL scope soft-deletes through the Ent hook; the service
	// owns revocation eligibility and chain changes, and a missing target can be
	// a successful no-op.
	Delete(ctx context.Context, id int64) error
	// Restore clears deletion and writes the caller-computed stored status, then
	// returns the hydrated grant. PostgreSQL rereading can fail after its write;
	// transaction ownership and renewal-chain positioning remain with the service
	// caller.
	Restore(ctx context.Context, subscriptionID int64, restoredStatus string) (*UserSubscription, error)

	// ListByUserID returns the owner's ordinarily visible grant rows with plan
	// views. PostgreSQL orders expiry then creation descending; service callers
	// normalize effective status and stale window display after this read.
	ListByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	// ListByUserIDAndPlanID returns the ordinarily visible timeline for one
	// owner/plan pair. PostgreSQL orders starts then creation ascending, and the
	// service uses that sequence for later-chain shifts inside its selected
	// transaction.
	ListByUserIDAndPlanID(ctx context.Context, userID, planID int64) ([]UserSubscription, error)
	// ListActiveByUserID returns grants whose start has arrived and expiry is
	// future under current wall-clock observation. PostgreSQL accepts stored
	// active/pending labels and orders expiry then start ascending, with quota
	// admission left to the service.
	ListActiveByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	// ListByPlanID returns a grant page for the plan with user/plan views and
	// count metadata. PostgreSQL counts and reads separately, ordering creation
	// descending; callers do not receive an unconditional atomic page/count
	// snapshot.
	ListByPlanID(ctx context.Context, planID int64, params pagination.PaginationParams) ([]UserSubscription, *pagination.PaginationResult, error)
	// List returns administrator grants filtered by optional IDs and requested
	// status with pagination. PostgreSQL includes deletion history for
	// empty/revoked filters, ignores platform and uses an allowed sort field;
	// count and row reads are separate observations.
	List(ctx context.Context, params pagination.PaginationParams, userID, planID *int64, status, platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error)
	// ListBySourceOrderID returns ordinarily visible grants linked to the exact
	// payment order ID. PostgreSQL includes plan views and orders starts then
	// creation ascending; issuance uses these records for its order replay check.
	ListBySourceOrderID(ctx context.Context, sourceOrderID int64) ([]UserSubscription, error)

	// ExtendExpiry writes the supplied absolute deadline for the selected grant
	// and reports missing-row or persistence failure. It does not add days or
	// shift a renewal chain; the service computes and coordinates those changes.
	ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error
	// UpdateStatus writes the supplied stored label and reports missing-row or
	// persistence failure. The repository does not calculate effective status
	// here; services decide the label from lifetime, deletion and operation
	// rules.
	UpdateStatus(ctx context.Context, subscriptionID int64, status string) error
	// UpdateNotes writes the supplied grant reason text and reports missing-row
	// or persistence failure. It performs a direct selected-context mutation,
	// while caller service operations determine authorization, locking and
	// surrounding changes.
	UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error

	// ActivateWindows writes the supplied anchor for selected first-activation
	// flags without clearing counters. No selected flag is a no-op; the caller
	// decides which periods qualify, and PostgreSQL does not condition these
	// writes on an anchor remaining nil.
	ActivateWindows(ctx context.Context, id int64, start time.Time, activation SubscriptionWindowActivation) error
	// ResetUsageWindows clears selected period counters and sets their supplied
	// common anchor. The service validates at least one selected flag and owns
	// maintenance locking; PostgreSQL can still execute an update when all flags
	// are false.
	ResetUsageWindows(ctx context.Context, id int64, resetDaily, resetWeekly, resetMonthly bool, newWindowStart time.Time) error
	// ResetDailyUsage conditionally clears daily use and advances its anchor when
	// stored start matches expectedWindowStart. Nil expected start matches SQL
	// NULL, while a supplied time compares that exact stored timestamp. Service
	// eligibility chooses the calendar-day candidate before invoking this
	// mutation.
	//
	// A changed anchor can produce no affected row but still return success when
	// the grant exists. The caller rereads stored counters after maintenance
	// rather than assuming that success means its reset won. Persistence errors
	// and missing grants remain errors, and transaction/lock ownership comes from
	// the caller.
	ResetDailyUsage(ctx context.Context, id int64, expectedWindowStart *time.Time, newWindowStart time.Time) error
	// ResetWeeklyUsage clears weekly use and advances its anchor only for the
	// expected stored start. Nil expected input matches SQL NULL; a supplied
	// timestamp checks the previously observed period, so another reset need not
	// be overwritten. The service first evaluates its elapsed weekly and
	// expiry-tail rules.
	//
	// PostgreSQL reports success for a stale-start no-op if the grant still
	// exists, while missing grants and storage errors fail. Admission therefore
	// rereads the actual anchor and usage after maintenance, preserving
	// consumption recorded by a request that won the reset first. Caller
	// transaction ownership governs these writes.
	ResetWeeklyUsage(ctx context.Context, id int64, expectedWindowStart *time.Time, newWindowStart time.Time) error
	// ResetMonthlyUsage clears monthly use and advances its anchor when the
	// stored timestamp equals the expected start, using SQL NULL for nil
	// expectation. The service supplies a candidate whose full monthly duration
	// fits before expiry. Repository mutation itself does not evaluate that
	// lifetime policy.
	//
	// A stale expected anchor can leave the row unchanged with nil error when it
	// still exists. The caller then rereads current quota rather than treating
	// its locally cleared usage as committed. Missing-row or persistence errors
	// propagate, and the caller owns transaction/lock scope around related period
	// changes.
	ResetMonthlyUsage(ctx context.Context, id int64, expectedWindowStart *time.Time, newWindowStart time.Time) error
	// IncrementUsage adds the supplied USD amount to all three period counters
	// and updates the row timestamp. PostgreSQL excludes deleted rows and returns
	// not-found for zero affected rows; it validates neither amount sign nor
	// quota capacity.
	IncrementUsage(ctx context.Context, id int64, costUSD float64) error

	// BatchUpdateExpiredStatus marks stored active/pending grants expired when
	// their deadlines have reached the wall clock. It returns affected count and
	// error; the PostgreSQL update adds no separate deleted-row predicate to this
	// mutation.
	BatchUpdateExpiredStatus(ctx context.Context) (int64, error)
}
