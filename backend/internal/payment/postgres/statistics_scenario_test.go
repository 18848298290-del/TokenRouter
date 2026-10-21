package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	sqlitetest "github.com/TokenFlux/TokenRouter/internal/testutil/sqlite"
)

// Payment statistics keep amounts grouped by currency and classify paid-status
// orders within a half-open paid-time range. SQLite fixtures distinguish
// credited points, payable money and subscription quota when deriving purchase
// unit price, and pure fixtures inspect per-currency totals, daily grouping,
// method breakdown and user ranking. Saved plan names provide display data
// when a current plan is absent.

// paymentStatsOrderSeed supplies explicit stored order observations for SQLite
// aggregate scenario fixtures.
type paymentStatsOrderSeed struct {
	userID      int64  // stored buyer numeric identity
	userEmail   string // stored buyer email snapshot
	userName    string // stored buyer name snapshot
	paymentType string // stored visible payment method
	orderType   string // stored purchase classification label
	status      string // stored order lifecycle state
	// amount supplies the order value and snapshot price; its fixture meaning
	// follows balance points or subscription purchase price.
	amount float64
	// payAmount supplies payable money; zero selects amount in
	// createPaidPaymentStatsOrder, so that helper cannot seed an explicit zero
	// override.
	payAmount float64
	feeAmount float64 // stored payable fee money
	// paidAt is written even for a pending fixture and also determines expiry one
	// hour later for statistics row construction.
	paidAt  time.Time
	tradeNo string // merchant fixture reference label
	// planID is stored when nonnil and also requests plan snapshot construction,
	// even when orderType is not subscription.
	planID *int64
	// planName becomes saved plan display text and independently requests a
	// snapshot whenever its string is nonempty.
	planName string
	// monthlyLimit supplies snapshot monthly points and requests a snapshot
	// whenever its pointer exists, including a pointed-to zero value.
	monthlyLimit *float64
}

// TestPaymentDashboardStatsWithRangeBuildsPurchaseDistribution seeds
// completed, paid, recharging, pending and out-of-range SQLite orders. It
// counts the three qualifying purchases, combines the two subscription entries
// and prefers the current plan display name.
func TestPaymentDashboardStatsWithRangeBuildsPurchaseDistribution(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	user, err := client.User.Create().
		SetEmail("buyer@example.com").
		SetPasswordHash("hash").
		SetUsername("buyer").
		Save(ctx)
	require.NoError(t, err)

	paidAt := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	plan, err := client.SubscriptionPlan.Create().
		SetName("Pro 版").
		SetPrice(30).
		Save(ctx)
	require.NoError(t, err)
	planID := plan.ID
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeAlipay,
		orderType:   payment.OrderTypeBalance,
		status:      payment.OrderStatusCompleted,
		amount:      50,
		paidAt:      paidAt,
		tradeNo:     "balance-in-range",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeStripe,
		orderType:   payment.OrderTypeSubscription,
		status:      payment.OrderStatusPaid,
		amount:      30,
		paidAt:      paidAt.Add(time.Hour),
		tradeNo:     "plan-one",
		planID:      &planID,
		planName:    "专业版",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeStripe,
		orderType:   payment.OrderTypeSubscription,
		status:      payment.OrderStatusRecharging,
		amount:      20,
		paidAt:      paidAt.Add(2 * time.Hour),
		tradeNo:     "plan-two",
		planID:      &planID,
		planName:    "专业版",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeWxpay,
		orderType:   payment.OrderTypeBalance,
		status:      payment.OrderStatusPending,
		amount:      999,
		paidAt:      paidAt,
		tradeNo:     "pending-ignored",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeWxpay,
		orderType:   payment.OrderTypeBalance,
		status:      payment.OrderStatusCompleted,
		amount:      999,
		paidAt:      paidAt.AddDate(0, 0, 5),
		tradeNo:     "outside-range",
	})

	svc := payment.NewOrderQueries(NewOrderStore(client), time.Now, nil)
	stats, err := svc.GetDashboardStatsWithRange(
		ctx,
		time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)

	require.Equal(t, payment.CurrencyAmounts{"CNY": 100}, stats.TotalAmount)
	require.Equal(t, 3, stats.TotalCount)
	require.Len(t, stats.DailySeries, 3)

	require.Len(t, stats.PurchaseDistribution, 2)
	balanceStat := findPurchaseDistributionStat(t, stats.PurchaseDistribution, payment.OrderTypeBalance)
	require.Equal(t, payment.OrderTypeBalance, balanceStat.Label)
	require.Equal(t, 50.0, balanceStat.Amount)
	require.Equal(t, 1, balanceStat.Count)
	subscriptionStat := findPurchaseDistributionStat(t, stats.PurchaseDistribution, payment.OrderTypeSubscription)
	require.Equal(t, "Pro 版", subscriptionStat.Label)
	require.Equal(t, planID, *subscriptionStat.PlanID)
	require.Equal(t, 50.0, subscriptionStat.Amount)
	require.Equal(t, 2, subscriptionStat.Count)
}

// TestPaymentDashboardStatsWithRangeFallsBackToPlanSnapshotWhenPlanDeleted
// seeds an order referring to a plan absent from the database. It expects
// saved plan name, identity, amount and count in the distribution.
func TestPaymentDashboardStatsWithRangeFallsBackToPlanSnapshotWhenPlanDeleted(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	user, err := client.User.Create().
		SetEmail("deleted-plan@example.com").
		SetPasswordHash("hash").
		SetUsername("deleted-plan-user").
		Save(ctx)
	require.NoError(t, err)

	planID := int64(22)
	paidAt := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeStripe,
		orderType:   payment.OrderTypeSubscription,
		status:      payment.OrderStatusCompleted,
		amount:      66,
		paidAt:      paidAt,
		tradeNo:     "deleted-plan-order",
		planID:      &planID,
		planName:    "历史套餐",
	})

	svc := payment.NewOrderQueries(NewOrderStore(client), time.Now, nil)
	stats, err := svc.GetDashboardStatsWithRange(
		ctx,
		time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)

	require.Len(t, stats.PurchaseDistribution, 1)
	require.Equal(t, "历史套餐", stats.PurchaseDistribution[0].Label)
	require.Equal(t, planID, *stats.PurchaseDistribution[0].PlanID)
	require.Equal(t, 66.0, stats.PurchaseDistribution[0].Amount)
	require.Equal(t, 1, stats.PurchaseDistribution[0].Count)
}

// TestPaymentDashboardStatsWithRangeComputesReasoningPointPurchaseUnitPrice
// combines a fee-bearing balance purchase with a quota-bearing subscription
// and excluded fixtures. It expects two contributing orders and aggregate
// price 0.5 from their eligible money and points.
func TestPaymentDashboardStatsWithRangeComputesReasoningPointPurchaseUnitPrice(t *testing.T) {
	ctx := context.Background()
	client := sqlitetest.NewClient(t)

	user, err := client.User.Create().
		SetEmail("unit-price@example.com").
		SetPasswordHash("hash").
		SetUsername("unit-price-user").
		Save(ctx)
	require.NoError(t, err)

	paidAt := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	monthlyLimit := 120.0
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeAlipay,
		orderType:   payment.OrderTypeBalance,
		status:      payment.OrderStatusCompleted,
		amount:      200,
		payAmount:   103,
		feeAmount:   3,
		paidAt:      paidAt,
		tradeNo:     "balance-with-fee",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:       user.ID,
		userEmail:    user.Email,
		userName:     user.Username,
		paymentType:  payment.TypeStripe,
		orderType:    payment.OrderTypeSubscription,
		status:       payment.OrderStatusPaid,
		amount:       60,
		paidAt:       paidAt.Add(time.Hour),
		tradeNo:      "subscription-monthly-limit",
		monthlyLimit: &monthlyLimit,
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeStripe,
		orderType:   payment.OrderTypeSubscription,
		status:      payment.OrderStatusCompleted,
		amount:      88,
		paidAt:      paidAt.Add(2 * time.Hour),
		tradeNo:     "subscription-no-monthly-limit",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeWxpay,
		orderType:   payment.OrderTypeBalance,
		status:      payment.OrderStatusPending,
		amount:      999,
		payAmount:   999,
		paidAt:      paidAt,
		tradeNo:     "pending-ignored-by-status",
	})
	createPaidPaymentStatsOrder(t, ctx, client, paymentStatsOrderSeed{
		userID:      user.ID,
		userEmail:   user.Email,
		userName:    user.Username,
		paymentType: payment.TypeWxpay,
		orderType:   payment.OrderTypeBalance,
		status:      payment.OrderStatusCompleted,
		amount:      999,
		payAmount:   999,
		paidAt:      paidAt.AddDate(0, 0, 4),
		tradeNo:     "outside-range-unit-price",
	})

	svc := payment.NewOrderQueries(NewOrderStore(client), time.Now, nil)
	stats, err := svc.GetDashboardStatsWithRange(
		ctx,
		time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)

	require.Equal(t, 2, stats.ReasoningPointPurchaseOrderCount)
	require.Equal(t, payment.CurrencyAmounts{"CNY": 0.5}, stats.AvgReasoningPointPurchaseUnitPrice)
}

// TestComputeBasicStatsGroupsAmountsByCurrency supplies CNY and USD pure order
// observations across the today cutoff. It checks separate totals, today
// amounts, per-currency averages and overall counts.
func TestComputeBasicStatsGroupsAmountsByCurrency(t *testing.T) {
	t.Parallel()

	todayStart := time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)
	yesterday := todayStart.Add(-time.Hour)
	today := todayStart.Add(time.Hour)
	orders := []*payment.Order{
		paymentStatsTestOrder(1, "alice@example.com", "CNY", 10, &today),
		paymentStatsTestOrder(2, "bob@example.com", "USD", 10, &today),
		paymentStatsTestOrder(1, "alice@example.com", "CNY", 5, &yesterday),
	}

	stats := &payment.DashboardStats{}
	payment.StatsComputeBasicStats(stats, orders, todayStart)

	require.Equal(t, payment.CurrencyAmounts{"CNY": 15, "USD": 10}, stats.TotalAmount)
	require.Equal(t, payment.CurrencyAmounts{"CNY": 10, "USD": 10}, stats.TodayAmount)
	require.Equal(t, payment.CurrencyAmounts{"CNY": 7.5, "USD": 10}, stats.AvgAmount)
	require.Equal(t, 3, stats.TotalCount)
	require.Equal(t, 2, stats.TodayCount)
}

// TestPaymentDashboardBreakdownsGroupAmountsAndRankingsByCurrency supplies two
// currencies, days, users and payment methods with fractional CNY money. It
// checks rounding, daily and method grouping, and independently ordered user
// rankings per currency.
func TestPaymentDashboardBreakdownsGroupAmountsAndRankingsByCurrency(t *testing.T) {
	t.Parallel()

	firstDay := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	secondDay := firstDay.AddDate(0, 0, 1)
	orders := []*payment.Order{
		paymentStatsTestOrder(1, "alice@example.com", "CNY", 5.555, &firstDay),
		paymentStatsTestOrder(2, "bob@example.com", "CNY", 10, &firstDay),
		paymentStatsTestOrder(1, "alice@example.com", "USD", 20, &secondDay),
		paymentStatsTestOrder(2, "bob@example.com", "USD", 10, &secondDay),
	}
	orders[0].PaymentType = "stripe"
	orders[1].PaymentType = "stripe"
	orders[2].PaymentType = "stripe"
	orders[3].PaymentType = "alipay"

	dailyEnd := time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)
	daily := payment.StatsBuildDailySeries(orders, firstDay, dailyEnd)
	require.Equal(t, []payment.DailyStats{
		{Date: "2026-07-24", Amount: payment.CurrencyAmounts{"CNY": 15.56}, Count: 2},
		{Date: "2026-07-25", Amount: payment.CurrencyAmounts{"USD": 30}, Count: 2},
	}, daily)

	methods := payment.StatsBuildMethodDistribution(orders)
	require.Equal(t, []payment.PaymentMethodStat{
		{Type: "alipay", Amount: payment.CurrencyAmounts{"USD": 10}, Count: 1},
		{Type: "stripe", Amount: payment.CurrencyAmounts{"CNY": 15.56, "USD": 20}, Count: 3},
	}, methods)

	users := payment.StatsBuildTopUsers(orders)
	require.Equal(t, payment.TopUsersByCurrency{
		"CNY": {
			{UserID: 2, Email: "bob@example.com", Amount: 10},
			{UserID: 1, Email: "alice@example.com", Amount: 5.56},
		},
		"USD": {
			{UserID: 1, Email: "alice@example.com", Amount: 20},
			{UserID: 2, Email: "bob@example.com", Amount: 10},
		},
	}, users)
}

// paymentStatsTestOrder returns a minimal pure statistics order with a fresh
// currency snapshot map. PaidAt retains the supplied pointer, so callers keep
// that time unchanged while statistics read it.
func paymentStatsTestOrder(userID int64, email, currency string, amount float64, paidAt *time.Time) *payment.Order {
	return &payment.Order{
		UserID:           userID,
		UserEmail:        email,
		PayAmount:        amount,
		PaidAt:           paidAt,
		PaymentType:      "stripe",
		ProviderSnapshot: map[string]any{"currency": currency},
	}
}

// findPurchaseDistributionStat returns the first distribution item matching
// the requested type and fails the test when none exists. Scenario assertions
// use the returned value to inspect grouping and plan display decisions.
func findPurchaseDistributionStat(t *testing.T, items []payment.PurchaseDistributionStat, itemType string) payment.PurchaseDistributionStat {
	t.Helper()
	for _, item := range items {
		if item.Type == itemType {
			return item
		}
	}
	t.Fatalf("purchase distribution item %q not found", itemType)
	return payment.PurchaseDistributionStat{}
}

// createPaidPaymentStatsOrder persists the supplied fixture status and paid
// time on SQLite and fails the test on save error. Zero payAmount uses amount;
// optional plan observations request a thirty-day snapshot, while merchant,
// channel and recharge labels derive from tradeNo.
func createPaidPaymentStatsOrder(t *testing.T, ctx context.Context, client *dbent.Client, seed paymentStatsOrderSeed) {
	t.Helper()
	payAmount := seed.payAmount
	if payAmount == 0 {
		payAmount = seed.amount
	}
	create := client.PaymentOrder.Create().
		SetUserID(seed.userID).
		SetUserEmail(seed.userEmail).
		SetUserName(seed.userName).
		SetAmount(seed.amount).
		SetPayAmount(payAmount).
		SetFeeRate(0).
		SetFeeAmount(seed.feeAmount).
		SetRechargeCode("PAY-" + seed.tradeNo).
		SetOutTradeNo(seed.tradeNo).
		SetPaymentType(seed.paymentType).
		SetPaymentTradeNo("trade-" + seed.tradeNo).
		SetOrderType(seed.orderType).
		SetStatus(seed.status).
		SetExpiresAt(seed.paidAt.Add(time.Hour)).
		SetPaidAt(seed.paidAt).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com")
	if seed.planID != nil {
		create.SetPlanID(*seed.planID)
	}
	if seed.orderType == payment.OrderTypeSubscription || seed.planID != nil || seed.planName != "" || seed.monthlyLimit != nil {
		create.SetPlanSnapshot(billing.SubscriptionPlanSnapshot{
			Name:            seed.planName,
			Price:           seed.amount,
			ValidityDays:    30,
			MonthlyLimitUSD: seed.monthlyLimit,
		})
	}
	_, err := create.Save(ctx)
	require.NoError(t, err)
}
