package payment_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Payment lifecycle cases combine SQLite order storage with map-backed
// redemption and configurable balance callbacks. The redemption fixture records
// code use and usage rows separately, returns shallow copies, and adds no lock
// in its ForUpdate methods, so these callers exercise use-case decisions rather
// than PostgreSQL transaction interleavings.
//
// The fixed shared-memory SQLite name can be shared by overlapping helper
// lifetimes, and setup issues a foreign-key PRAGMA before creating the Ent
// schema. Balance reads require a configured user pointer and ignore the
// requested ID, while an absent write callback reports success without changing
// balance. Unsupported promoted methods remain nil-backed, making the fixture's
// implemented method set part of each scenario's setup.

// paymentOrderLifecycleRedeemRepo records unprotected code and usage state for
// payment redemption scenarios.
type paymentOrderLifecycleRedeemRepo struct {
	// codesByCode contains nonnil borrowed code pointers. ID reads scan
	// these entries, credential reads select exact keys, and returned
	// copies retain nested references.
	codesByCode map[string]*billing.RedeemCode
	// usageByRedeemCodeID stores shallow usage copies by code and user. Nil
	// or absent entries mean no recorded use, and readers return another
	// shallow copy.
	usageByRedeemCodeID map[int64]map[int64]*billing.RedeemCodeUsage
	// useCalls records both Use and CreateUsage pairs in call order. The
	// fixture has no synchronization, so tests serialize access to
	// observations and stored records.
	useCalls []struct {
		id     int64 // recorded redeem code ID
		userID int64 // recorded redeeming user ID
	}
}

// fulfillmentBalance supplies shallow user reads and optional balance writes
// for redemption scenarios.
type fulfillmentBalance struct {
	// RedeemUserWriter supplies promoted methods beyond the two overrides.
	// Tests leave it nil, so a flow reaching those promoted methods would
	// panic.
	billingpostgres.RedeemUserWriter
	// getByIDUser is required by GetByID, which dereferences it regardless
	// of the requested user ID and returns a shallow copy with shared
	// nested references.
	getByIDUser *billing.UserSummary
	// updateBalanceFn owns the observed write effect when configured. Nil
	// lets UpdateBalance report success without changing a user or touching
	// persistent storage.
	updateBalanceFn func(context.Context, int64, float64) error
}

// ensurePaymentAuditOrderActionUniqueIndex creates the order/action unique
// audit index when absent using the supplied client and context. SQL failures
// fail the test before its duplicate-audit or recovery scenario begins.
func ensurePaymentAuditOrderActionUniqueIndex(t *testing.T, ctx context.Context, client *dbent.Client) {
	t.Helper()
	_, err := client.ExecContext(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_audit_logs_order_action_uniq ON payment_audit_logs(order_id, action)")
	require.NoError(t, err)
}

// createPaymentFulfillmentSubscriptionOrder stores a user and a subscription
// order with a paid timestamp and plan ID one hundred. The supplied status and
// updatedAt select the fulfillment lease state used by recovery cases.
//
// The user is saved first because the order retains its user ID, email, and
// name. A failed order save fails the test after the user exists. The helper
// uses the supplied client and context for both writes; callers own that
// client's transaction completion or fixture cleanup, including partially
// constructed rows.
func createPaymentFulfillmentSubscriptionOrder(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	status string,
	updatedAt time.Time,
) *dbent.PaymentOrder {
	t.Helper()
	user, err := client.User.Create().
		SetEmail("fulfillment-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.com").
		SetPasswordHash("hash").
		SetUsername("payment-fulfillment-user").
		Save(ctx)
	require.NoError(t, err)

	// Save the order after its user so the stored identity and display
	// snapshot refer to that fixture; a rejected order write leaves user
	// cleanup to the owner of the supplied client.
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(80).
		SetPayAmount(80).
		SetFeeRate(0).
		SetRechargeCode("PAY-SUB-" + strconv.FormatInt(time.Now().UnixNano(), 10)).
		SetOutTradeNo("sub2_fulfillment_" + strconv.FormatInt(time.Now().UnixNano(), 10)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-fulfillment").
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(100).
		SetStatus(status).
		SetPaidAt(time.Now().Add(-time.Hour)).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetUpdatedAt(updatedAt).
		Save(ctx)
	require.NoError(t, err)
	return order
}

// Create panics on unsupported code creation through the lifecycle redemption
// fixture.
func (r *paymentOrderLifecycleRedeemRepo) Create(context.Context, *billing.RedeemCode) error {
	panic("unexpected call")
}

// CreateBatch panics on unsupported batch code creation through the lifecycle
// redemption fixture.
func (r *paymentOrderLifecycleRedeemRepo) CreateBatch(context.Context, []billing.RedeemCode) error {
	panic("unexpected call")
}

// GetByID scans configured code pointers for id and returns a shallow struct
// copy. Missing IDs return RedeemCodeNotFound; context is ignored, nested
// references stay shared, and callers configure nonnil records with distinct
// IDs.
func (r *paymentOrderLifecycleRedeemRepo) GetByID(_ context.Context, id int64) (*billing.RedeemCode, error) {
	for _, code := range r.codesByCode {
		if code.ID != id {
			continue
		}
		cloned := *code
		return &cloned, nil
	}
	return nil, billing.ErrRedeemCodeNotFound
}

// GetByIDForUpdate delegates the same map scan and shallow-copy behavior as
// GetByID. This fixture acquires no database or mutex lock, so callers
// serialize access to its stored code pointers.
func (r *paymentOrderLifecycleRedeemRepo) GetByIDForUpdate(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	return r.GetByID(ctx, id)
}

// GetByCode returns a shallow copy of the exact configured credential entry and
// ignores ctx. Missing map entries return RedeemCodeNotFound; stored pointers
// must be nonnil, and their nested references remain shared.
func (r *paymentOrderLifecycleRedeemRepo) GetByCode(_ context.Context, code string) (*billing.RedeemCode, error) {
	redeemCode, ok := r.codesByCode[code]
	if !ok {
		return nil, billing.ErrRedeemCodeNotFound
	}
	cloned := *redeemCode
	return &cloned, nil
}

// GetByCodeForUpdate delegates the same credential lookup as GetByCode and adds
// no locking. Callers serialize this map fixture's reads and writes rather than
// using it as row-lock evidence.
func (r *paymentOrderLifecycleRedeemRepo) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return r.GetByCode(ctx, code)
}

// Update stores a shallow copy under the supplied code string, allocating the
// map when needed. Nil input succeeds unchanged; changing a code string leaves
// any preceding map entry intact, and ctx is ignored.
func (r *paymentOrderLifecycleRedeemRepo) Update(_ context.Context, code *billing.RedeemCode) error {
	if code == nil {
		return nil
	}
	cloned := *code
	if r.codesByCode == nil {
		r.codesByCode = make(map[string]*billing.RedeemCode)
	}
	r.codesByCode[cloned.Code] = &cloned
	return nil
}

// BatchUpdate panics on unsupported bulk editing through the lifecycle code
// fixture.
func (r *paymentOrderLifecycleRedeemRepo) BatchUpdate(context.Context, []int64, billing.RedeemCodeBatchUpdateFields) (int64, error) {
	panic("unexpected call")
}

// Delete panics on unsupported code removal through the lifecycle redemption
// fixture.
func (r *paymentOrderLifecycleRedeemRepo) Delete(context.Context, int64) error {
	panic("unexpected call")
}

// Use marks the first matching configured code used, overwrites its user and
// timestamp, and appends a call observation. Missing IDs return
// RedeemCodeNotFound; it enforces no usage limits, and callers serialize its
// pointer mutation.
func (r *paymentOrderLifecycleRedeemRepo) Use(_ context.Context, id, userID int64) error {
	for code, redeemCode := range r.codesByCode {
		if redeemCode.ID != id {
			continue
		}
		now := time.Now().UTC()
		redeemCode.Status = billing.StatusUsed
		redeemCode.UsedBy = &userID
		redeemCode.UsedAt = &now
		r.codesByCode[code] = redeemCode
		r.useCalls = append(r.useCalls, struct {
			id     int64 // recorded redeem code ID
			userID int64 // recorded redeeming user ID
		}{id: id, userID: userID})
		return nil
	}
	return billing.ErrRedeemCodeNotFound
}

// CreateUsage stores a shallow usage copy indexed by code and user and records
// that pair in useCalls. Nil input succeeds unchanged; nested references remain
// shared, existing pairs are replaced, and ctx is ignored.
func (r *paymentOrderLifecycleRedeemRepo) CreateUsage(_ context.Context, usage *billing.RedeemCodeUsage) error {
	if usage == nil {
		return nil
	}
	cloned := *usage
	if r.usageByRedeemCodeID == nil {
		r.usageByRedeemCodeID = make(map[int64]map[int64]*billing.RedeemCodeUsage)
	}
	if r.usageByRedeemCodeID[cloned.RedeemCodeID] == nil {
		r.usageByRedeemCodeID[cloned.RedeemCodeID] = make(map[int64]*billing.RedeemCodeUsage)
	}
	r.usageByRedeemCodeID[cloned.RedeemCodeID][cloned.UserID] = &cloned
	r.useCalls = append(r.useCalls, struct {
		id     int64 // recorded redeem code ID
		userID int64 // recorded redeeming user ID
	}{
		id:     cloned.RedeemCodeID,
		userID: cloned.UserID,
	})
	return nil
}

// GetUsageByRedeemCodeAndUser returns a shallow copy of the configured
// code/user usage record and ignores ctx. An absent map, code, or user returns
// nil with no error, allowing fixture callers to detect no recorded use.
func (r *paymentOrderLifecycleRedeemRepo) GetUsageByRedeemCodeAndUser(_ context.Context, redeemCodeID, userID int64) (*billing.RedeemCodeUsage, error) {
	if r.usageByRedeemCodeID == nil {
		return nil, nil
	}
	usagesByUser, ok := r.usageByRedeemCodeID[redeemCodeID]
	if !ok {
		return nil, nil
	}
	usage, ok := usagesByUser[userID]
	if !ok {
		return nil, nil
	}
	cloned := *usage
	return &cloned, nil
}

// List panics on unsupported code listing through the lifecycle redemption
// fixture.
func (r *paymentOrderLifecycleRedeemRepo) List(context.Context, pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected call")
}

// ListWithFilters panics on unsupported filtered listing through the lifecycle
// redemption fixture.
func (r *paymentOrderLifecycleRedeemRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected call")
}

// ListByUser panics on unsupported user history reads through the lifecycle
// redemption fixture.
func (r *paymentOrderLifecycleRedeemRepo) ListByUser(context.Context, int64, int) ([]billing.RedeemCode, error) {
	panic("unexpected call")
}

// ListByUserPaginated panics on unsupported paginated history reads through the
// lifecycle redemption fixture.
func (r *paymentOrderLifecycleRedeemRepo) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected call")
}

// SumPositiveBalanceByUser panics on unsupported credit aggregation through the
// lifecycle redemption fixture.
func (r *paymentOrderLifecycleRedeemRepo) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	panic("unexpected call")
}

// newPaymentOrderLifecycleTestClient opens the fixed shared-memory SQLite
// database and issues its foreign-key PRAGMA before creating the Ent schema.
// SQL and Ent cleanup close the shared pool; callers keep overlapping fixture
// lifetimes serialized.
func newPaymentOrderLifecycleTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", "file:payment_order_lifecycle?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	// Register pool closure before Ent cleanup so the later wrapper
	// callback closes its shared driver first; repeated pool closure errors
	// are discarded by this fixture.
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	// Close the Ent wrapper before the pool callback runs, discarding its
	// error; both callbacks share the same named SQLite pool and its
	// fixture connection lifetime.
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// createPaymentOrderLifecycleOrder stores a fixture user followed by a balance
// order using one nanosecond suffix. The caller selects status and expiresAt
// and supplies the context and client used by both writes.
//
// The saved user provides the order's owner ID, email, and name, so order
// creation follows successful user persistence. A failed order save fails the
// test after the user row exists. Transaction completion and cleanup of
// complete or partial fixtures belong to the client owner; this helper
// registers no separate row cleanup.
func createPaymentOrderLifecycleOrder(t *testing.T, ctx context.Context, client *dbent.Client, status string, expiresAt time.Time) *dbent.PaymentOrder {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	user, err := client.User.Create().
		SetEmail("payment-lifecycle-" + suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername("payment-lifecycle-" + suffix).
		Save(ctx)
	require.NoError(t, err)

	// Keep the order snapshot tied to the user already saved with this
	// suffix; the caller-owned client scope also determines recovery of an
	// incomplete user-and-order fixture after a failed save.
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("LIFECYCLE-" + suffix).
		SetOutTradeNo("sub2_lifecycle_" + suffix).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetExpiresAt(expiresAt).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}

// GetByID shallow-copies the configured user summary and reports success for
// any requested ID or context. Its configured pointer must be nonnil, and
// copied nested references remain shared with that fixture user.
func (r *fulfillmentBalance) GetByID(context.Context, int64) (*billing.UserSummary, error) {
	user := *r.getByIDUser
	return &user, nil
}

// UpdateBalance invokes updateBalanceFn with the supplied context, ID, and
// amount when configured. A nil callback reports success with no write, leaving
// the observable balance effect to the scenario's callback.
func (r *fulfillmentBalance) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	if r.updateBalanceFn != nil {
		return r.updateBalanceFn(ctx, id, amount)
	}
	return nil
}

// newFulfillmentRedeemService combines the supplied code and user fixtures with
// Ent redemption mutations using client. Subscription, cache, and invalidation
// dependencies remain nil; the empty runtime selects time.Now in
// NewRedeemService. Callers provide the methods reached by their selected
// redemption scenario.
func newFulfillmentRedeemService(repo billing.RedeemCodeRepository, users *fulfillmentBalance, client *dbent.Client) *billing.RedeemService {
	return billing.NewRedeemService(repo, users, nil, nil, nil, billingpostgres.NewRedeemMutations(client, users), nil, nil, billing.RedeemRuntime{})
}
