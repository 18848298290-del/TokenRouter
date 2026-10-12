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

type paymentOrderLifecycleRedeemRepo struct {
	codesByCode map[string]*billing.RedeemCode
	// Usage indexed by code and user for duplicate-redemption checks.
	usageByRedeemCodeID map[int64]map[int64]*billing.RedeemCodeUsage
	useCalls            []struct {
		id     int64
		userID int64
	}
}

// fulfillmentBalance supplies configurable balance reads and writes. Calls through its nil embedded
// interface panic.
type fulfillmentBalance struct {
	billingpostgres.RedeemUserWriter
	getByIDUser     *billing.UserSummary
	updateBalanceFn func(context.Context, int64, float64) error
}

func ensurePaymentAuditOrderActionUniqueIndex(t *testing.T, ctx context.Context, client *dbent.Client) {
	t.Helper()
	_, err := client.ExecContext(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_audit_logs_order_action_uniq ON payment_audit_logs(order_id, action)")
	require.NoError(t, err)
}

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

func (r *paymentOrderLifecycleRedeemRepo) Create(context.Context, *billing.RedeemCode) error {
	panic("unexpected call")
}

func (r *paymentOrderLifecycleRedeemRepo) CreateBatch(context.Context, []billing.RedeemCode) error {
	panic("unexpected call")
}

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

func (r *paymentOrderLifecycleRedeemRepo) GetByIDForUpdate(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	return r.GetByID(ctx, id)
}

func (r *paymentOrderLifecycleRedeemRepo) GetByCode(_ context.Context, code string) (*billing.RedeemCode, error) {
	redeemCode, ok := r.codesByCode[code]
	if !ok {
		return nil, billing.ErrRedeemCodeNotFound
	}
	cloned := *redeemCode
	return &cloned, nil
}

func (r *paymentOrderLifecycleRedeemRepo) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return r.GetByCode(ctx, code)
}

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

func (r *paymentOrderLifecycleRedeemRepo) BatchUpdate(context.Context, []int64, billing.RedeemCodeBatchUpdateFields) (int64, error) {
	panic("unexpected call")
}

func (r *paymentOrderLifecycleRedeemRepo) Delete(context.Context, int64) error {
	panic("unexpected call")
}

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
			id     int64
			userID int64
		}{id: id, userID: userID})
		return nil
	}
	return billing.ErrRedeemCodeNotFound
}

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
		id     int64
		userID int64
	}{
		id:     cloned.RedeemCodeID,
		userID: cloned.UserID,
	})
	return nil
}

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

func (r *paymentOrderLifecycleRedeemRepo) List(context.Context, pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected call")
}

func (r *paymentOrderLifecycleRedeemRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected call")
}

func (r *paymentOrderLifecycleRedeemRepo) ListByUser(context.Context, int64, int) ([]billing.RedeemCode, error) {
	panic("unexpected call")
}

func (r *paymentOrderLifecycleRedeemRepo) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected call")
}

func (r *paymentOrderLifecycleRedeemRepo) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	panic("unexpected call")
}

func newPaymentOrderLifecycleTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", "file:payment_order_lifecycle?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func createPaymentOrderLifecycleOrder(t *testing.T, ctx context.Context, client *dbent.Client, status string, expiresAt time.Time) *dbent.PaymentOrder {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	user, err := client.User.Create().
		SetEmail("payment-lifecycle-" + suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername("payment-lifecycle-" + suffix).
		Save(ctx)
	require.NoError(t, err)

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

func (r *fulfillmentBalance) GetByID(context.Context, int64) (*billing.UserSummary, error) {
	user := *r.getByIDUser
	return &user, nil
}

func (r *fulfillmentBalance) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	if r.updateBalanceFn != nil {
		return r.updateBalanceFn(ctx, id, amount)
	}
	return nil
}

func newFulfillmentRedeemService(repo billing.RedeemCodeRepository, users *fulfillmentBalance, client *dbent.Client) *billing.RedeemService {
	return billing.NewRedeemService(repo, users, nil, nil, nil, billingpostgres.NewRedeemMutations(client, users), nil, nil, billing.RedeemRuntime{})
}
