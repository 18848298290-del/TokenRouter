package payment

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

// Payment dashboard history uses paid_at and the paid, recharging or completed
// states, while current-day cards are queried separately from the requested
// historical range. Calendar buckets use the range timezone and advance by
// dates, so a daylight-saving day can have a different elapsed duration from
// twenty-four hours.
//
// Total, average, method and user amounts keep currency groups from
// PaymentOrderCurrency. Purchase distribution and reasoning-point unit price
// use their supplied numeric quantities without currency conversion:
// subscription principal comes from Amount and its positive monthly quota,
// while balance principal comes from PayAmount minus FeeAmount. Current plan
// names can replace snapshot labels without changing plan-ID grouping. These
// rules distinguish display grouping from a consolidated monetary total.

// GetDashboardStats requests the selected number of calendar days through
// tomorrow's midnight in the injected clock's timezone. Nonpositive days
// select thirty; it delegates paid history and independent current-day cards
// to GetDashboardStatsWithRange.
func (s *OrderQueries) GetDashboardStats(ctx context.Context, days int) (*DashboardStats, error) {
	if days <= 0 {
		days = 30
	}
	now := s.now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := todayStart.AddDate(0, 0, -days+1)
	end := todayStart.AddDate(0, 0, 1)

	return s.GetDashboardStatsWithRange(ctx, start, end)
}

// GetDashboardStatsWithRange builds dashboard values from the supplied
// half-open paid range and independent current-day/pending reads. Callers
// supply the desired range and timezone; store errors abort assembly.
//
// Basic totals and distributions use historical paid, recharging and completed
// observations. Today cards are replaced by a fresh current-calendar-day query
// in start's timezone, even when that day lies outside the history. Pending
// count and current plan names are separate reads, so the result is assembled
// from multiple observations rather than one transaction snapshot. Currency
// totals remain grouped, and purchase/point calculations follow their
// dedicated numeric basis.
func (s *OrderQueries) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error) {
	paidStatuses := []string{OrderStatusCompleted, OrderStatusPaid, OrderStatusRecharging}
	todayNow := s.now().In(start.Location())
	todayStart := time.Date(todayNow.Year(), todayNow.Month(), todayNow.Day(), 0, 0, 0, 0, todayNow.Location())

	orders, err := s.store.PaidOrders(ctx, start, end, paidStatuses)
	if err != nil {
		return nil, err
	}

	st := &DashboardStats{}
	StatsComputeBasicStats(st, orders, todayStart)
	if err := s.StatsFillPaymentDashboardTodayStats(ctx, st, todayStart, paidStatuses); err != nil {
		return nil, err
	}

	st.PendingOrders, err = s.store.PendingCount(ctx)
	if err != nil {
		return nil, err
	}

	st.DailySeries = StatsBuildDailySeries(orders, start, end)
	st.PaymentMethods = StatsBuildMethodDistribution(orders)
	StatsComputeReasoningPointPurchaseUnitPrice(st, orders)
	planNames, err := s.StatsLoadPaymentDashboardPlanNames(ctx, orders)
	if err != nil {
		return nil, err
	}
	st.PurchaseDistribution = StatsBuildPurchaseDistribution(orders, planNames)
	st.TopUsers = StatsBuildTopUsers(orders)

	return st, nil
}

// StatsFillPaymentDashboardTodayStats replaces today amount/count using the
// full calendar day beginning at todayStart and the supplied paid statuses. It
// returns read errors and rounds currency-grouped PayAmount totals
// independently of the dashboard's historical range.
func (s *OrderQueries) StatsFillPaymentDashboardTodayStats(ctx context.Context, st *DashboardStats, todayStart time.Time, paidStatuses []string) error {
	// Read current-day cards separately after historical totals so a history
	// range excluding today still receives the current calendar day's amount and
	// count.
	todayOrders, err := s.store.PaidOrders(ctx, todayStart, todayStart.AddDate(0, 0, 1), paidStatuses)
	if err != nil {
		return err
	}
	todayAmounts := make(CurrencyAmounts)
	for _, o := range todayOrders {
		todayAmounts[PaymentOrderCurrency(o)] += o.PayAmount
	}
	StatsRoundCurrencyAmounts(todayAmounts)
	st.TodayAmount = todayAmounts
	st.TodayCount = len(todayOrders)
	return nil
}

// StatsComputeBasicStats fills count, currency-grouped totals/averages and
// provisional today values from the supplied order set. Orders are nonnil, and
// this helper relies on the caller's paid-status/range selection.
//
// Each currency average divides its accumulated PayAmount by that currency's
// count before two-decimal rounding. Provisional today membership requires
// PaidAt at or after todayStart and has no upper bound here.
// GetDashboardStatsWithRange later replaces those today values with its
// separate current-day query, so this intermediate classification does not
// determine final cards outside the history range.
func StatsComputeBasicStats(st *DashboardStats, orders []*Order, todayStart time.Time) {
	st.TotalAmount = make(CurrencyAmounts)
	st.TodayAmount = make(CurrencyAmounts)
	st.AvgAmount = make(CurrencyAmounts)
	currencyCounts := make(map[string]int)
	var todayCount int
	for _, o := range orders {
		currency := PaymentOrderCurrency(o)
		st.TotalAmount[currency] += o.PayAmount
		currencyCounts[currency]++
		if o.PaidAt != nil && !o.PaidAt.Before(todayStart) {
			st.TodayAmount[currency] += o.PayAmount
			todayCount++
		}
	}
	st.TotalCount = len(orders)
	st.TodayCount = todayCount
	for currency, totalAmount := range st.TotalAmount {
		st.AvgAmount[currency] = StatsRoundAmount(totalAmount / float64(currencyCounts[currency]))
	}
	StatsRoundCurrencyAmounts(st.TotalAmount)
	StatsRoundCurrencyAmounts(st.TodayAmount)
}

// StatsComputeReasoningPointPurchaseUnitPrice counts eligible point purchases
// and computes aggregate principal divided by aggregate points to four
// decimals. It uses StatsPaymentDashboardReasoningPointPurchase's raw numeric
// bases without currency conversion.
//
// Skipped orders contribute to neither aggregate nor count. A positive total
// point quantity writes the unit price; otherwise the existing field is
// retained. Subscription eligibility uses monthly quota only, while
// balance-like purchases use credited Amount and paid amount less fees. The
// calculation therefore follows purchase snapshots and their accepted
// comparisons, not an average of separately rounded per-order prices.
func StatsComputeReasoningPointPurchaseUnitPrice(st *DashboardStats, orders []*Order) {
	var totalPrincipal, totalReasoningPoints float64
	var orderCount int
	for _, o := range orders {
		principal, points, ok := StatsPaymentDashboardReasoningPointPurchase(o)
		if !ok {
			continue
		}
		totalPrincipal += principal
		totalReasoningPoints += points
		orderCount++
	}
	st.ReasoningPointPurchaseOrderCount = orderCount
	if totalReasoningPoints > 0 {
		// Round the aggregate principal-per-point ratio to four decimals.
		st.AvgReasoningPointPurchaseUnitPrice = math.Round(totalPrincipal/totalReasoningPoints*10000) / 10000
	}
}

// StatsPaymentDashboardReasoningPointPurchase returns principal, purchased
// points and eligibility for a nonnil order. Subscription input uses Amount
// and a nonnil monthly quota; other order types use PayAmount minus FeeAmount
// and credited Amount.
//
// Nonpositive compared values are excluded, and daily/weekly-only or unlimited
// subscription quotas cannot provide this metric. This helper adds no
// finite-number validation or currency conversion. Its returned raw quantities
// feed aggregate unit price before the final four-decimal rounding.
func StatsPaymentDashboardReasoningPointPurchase(o *Order) (principal float64, points float64, ok bool) {
	switch o.OrderType {
	case OrderTypeSubscription:
		// Use monthly snapshot quota as purchased points for this metric. Daily,
		// weekly and unlimited quota forms lack that quantity and leave the order
		// excluded.
		if o.PlanSnapshot.MonthlyLimitUSD == nil || *o.PlanSnapshot.MonthlyLimitUSD <= 0 || o.Amount <= 0 {
			return 0, 0, false
		}
		return o.Amount, *o.PlanSnapshot.MonthlyLimitUSD, true
	default:
		// Separate credited points from recharge principal by subtracting recorded
		// fees from PayAmount. Aggregate unit price combines that principal with
		// Amount rather than the fee-bearing gateway total.
		principal = o.PayAmount - o.FeeAmount
		if principal <= 0 || o.Amount <= 0 {
			return 0, 0, false
		}
		return principal, o.Amount, true
	}
}

// StatsBuildDailySeries groups supplied PayAmount observations by paid date in
// start's timezone and fills the requested calendar dates, including
// zero-valued days. Callers supply a paid/range-filtered set and consistent
// range timezone; nil PaidAt rows contribute no bucket.
//
// A midnight end excludes that end date, while a nonmidnight end includes its
// calendar date. Cursor advancement uses AddDate so daylight-saving changes
// retain calendar membership. Currency amounts are rounded only when a
// populated bucket is emitted. The helper creates new amount maps and records,
// and does not query or filter statuses itself.
func StatsBuildDailySeries(orders []*Order, start, end time.Time) []DailyStats {
	dailyMap := make(map[string]*DailyStats)
	for _, o := range orders {
		if o.PaidAt == nil {
			continue
		}
		date := o.PaidAt.In(start.Location()).Format("2006-01-02")
		ds, ok := dailyMap[date]
		if !ok {
			ds = &DailyStats{Date: date, Amount: make(CurrencyAmounts)}
			dailyMap[date] = ds
		}
		ds.Amount[PaymentOrderCurrency(o)] += o.PayAmount
		ds.Count++
	}
	seriesStart := StatsStartOfPaymentStatsDay(start)
	seriesEnd := StatsStartOfPaymentStatsDay(end)
	// A midnight range end belongs to the following exclusive date. Move the
	// emitted last date back once so a half-open query does not add an extra zero
	// bucket.
	if end.Equal(seriesEnd) {
		seriesEnd = seriesEnd.AddDate(0, 0, -1)
	}
	series := make([]DailyStats, 0)
	for cursor := seriesStart; !cursor.After(seriesEnd); cursor = cursor.AddDate(0, 0, 1) {
		date := cursor.Format("2006-01-02")
		if ds, ok := dailyMap[date]; ok {
			StatsRoundCurrencyAmounts(ds.Amount)
			series = append(series, *ds)
		} else {
			series = append(series, DailyStats{Date: date, Amount: make(CurrencyAmounts)})
		}
	}
	return series
}

// StatsStartOfPaymentStatsDay returns midnight on t's calendar date in its
// location.
func StatsStartOfPaymentStatsDay(t time.Time) time.Time {
	inLoc := t.In(t.Location())
	return time.Date(inLoc.Year(), inLoc.Month(), inLoc.Day(), 0, 0, 0, 0, inLoc.Location())
}

// StatsBuildMethodDistribution groups supplied orders by their stored
// PaymentType and currency, then sorts methods lexically. Callers supply
// nonnil selected orders; method labels are not normalized, and each emitted
// currency total is rounded to two decimals.
func StatsBuildMethodDistribution(orders []*Order) []PaymentMethodStat {
	methodMap := make(map[string]*PaymentMethodStat)
	for _, o := range orders {
		ms, ok := methodMap[o.PaymentType]
		if !ok {
			ms = &PaymentMethodStat{Type: o.PaymentType, Amount: make(CurrencyAmounts)}
			methodMap[o.PaymentType] = ms
		}
		ms.Amount[PaymentOrderCurrency(o)] += o.PayAmount
		ms.Count++
	}
	methods := make([]PaymentMethodStat, 0, len(methodMap))
	for _, ms := range methodMap {
		StatsRoundCurrencyAmounts(ms.Amount)
		methods = append(methods, *ms)
	}
	sort.Slice(methods, func(i, j int) bool {
		return methods[i].Type < methods[j].Type
	})
	return methods
}

// StatsLoadPaymentDashboardPlanNames reads current names for distinct nonnil
// subscription plan IDs. No qualifying IDs returns nil,nil; repository errors
// remain visible before the dashboard substitutes those names for stored
// snapshot labels.
func (s *OrderQueries) StatsLoadPaymentDashboardPlanNames(ctx context.Context, orders []*Order) (map[int64]string, error) {
	planIDSet := make(map[int64]struct{})
	for _, o := range orders {
		if o.OrderType == OrderTypeSubscription && o.PlanID != nil {
			planIDSet[*o.PlanID] = struct{}{}
		}
	}
	if len(planIDSet) == 0 {
		return nil, nil
	}
	planIDs := make([]int64, 0, len(planIDSet))
	for id := range planIDSet {
		planIDs = append(planIDs, id)
	}
	return s.store.PlanNames(ctx, planIDs)
}

// StatsBuildPurchaseDistribution groups balance-like orders together and
// subscription orders by StatsPurchaseDistributionKey, then sums raw
// PayAmount. It receives nonnil orders and optional current plan names; its
// scalar amount does not separate or convert currencies.
//
// Each group is rounded to two decimals before sorting descending by amount,
// with count as the secondary comparison. Equal amount/count ties retain no
// defined map-derived order. The first order for each key supplies its label
// metadata, while subsequent matching orders add amount and count.
func StatsBuildPurchaseDistribution(orders []*Order, planNames map[int64]string) []PurchaseDistributionStat {
	distributionMap := make(map[string]*PurchaseDistributionStat)
	for _, o := range orders {
		key, item := StatsPurchaseDistributionKey(o, planNames)
		stat, ok := distributionMap[key]
		if !ok {
			stat = item
			distributionMap[key] = stat
		}
		stat.Amount += o.PayAmount
		stat.Count++
	}
	items := make([]PurchaseDistributionStat, 0, len(distributionMap))
	for _, stat := range distributionMap {
		// Round the accumulated purchase group for two-decimal display.
		stat.Amount = math.Round(stat.Amount*100) / 100
		items = append(items, *stat)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Amount == items[j].Amount {
			return items[i].Count > items[j].Count
		}
		return items[i].Amount > items[j].Amount
	})
	return items
}

// StatsPurchaseDistributionKey returns a grouping key and label record for a
// nonnil order. Balance-like types share the balance key, while subscriptions
// prefer a nonempty current plan name, then snapshot name, decimal plan label
// and generic subscription label.
//
// A present plan ID determines the subscription key even when its display name
// changes, and is copied into a new pointer for the returned record. Without
// an ID, the chosen label determines grouping. The display fallback therefore
// remains distinct from identity for plan-bound purchases.
func StatsPurchaseDistributionKey(o *Order, planNames map[int64]string) (string, *PurchaseDistributionStat) {
	if o.OrderType != OrderTypeSubscription {
		return OrderTypeBalance, &PurchaseDistributionStat{Type: OrderTypeBalance, Label: OrderTypeBalance}
	}
	// Choose display text from the current plan name or retained snapshot, then
	// synthesize ID/generic labels when absent. The present plan ID still owns
	// grouping through those label choices.
	label := o.PlanSnapshot.Name
	if o.PlanID != nil {
		if currentName := planNames[*o.PlanID]; currentName != "" {
			label = currentName
		}
	}
	if label == "" && o.PlanID != nil {
		label = "plan #" + strconv.FormatInt(*o.PlanID, 10)
	}
	if label == "" {
		label = OrderTypeSubscription
	}
	var planID *int64
	key := label
	if o.PlanID != nil {
		id := *o.PlanID
		planID = &id
		key = strconv.FormatInt(id, 10)
	}
	return OrderTypeSubscription + ":" + key, &PurchaseDistributionStat{Type: OrderTypeSubscription, Label: label, PlanID: planID}
}

// StatsBuildTopUsers accumulates PayAmount by currency and user, rounding each
// total before descending selection. Callers supply nonnil orders; the first
// observation supplies that user's stored email for the currency.
//
// Each currency emits at most ten copied value records. Sorting compares
// rounded amounts only, so ties have no defined order from the maps. Currency
// groups remain separate and no conversion is applied; the output describes
// paid observations rather than querying the user's current account details.
func StatsBuildTopUsers(orders []*Order) TopUsersByCurrency {
	userMap := make(map[string]map[int64]*TopUserStat)
	for _, o := range orders {
		currency := PaymentOrderCurrency(o)
		users, ok := userMap[currency]
		if !ok {
			users = make(map[int64]*TopUserStat)
			userMap[currency] = users
		}
		us, ok := users[o.UserID]
		if !ok {
			us = &TopUserStat{UserID: o.UserID, Email: o.UserEmail}
			users[o.UserID] = us
		}
		us.Amount += o.PayAmount
	}
	result := make(TopUsersByCurrency, len(userMap))
	for currency, users := range userMap {
		userList := make([]*TopUserStat, 0, len(users))
		for _, us := range users {
			us.Amount = StatsRoundAmount(us.Amount)
			userList = append(userList, us)
		}
		sort.Slice(userList, func(i, j int) bool {
			return userList[i].Amount > userList[j].Amount
		})
		// Limit each currency's displayed ranking to ten users.
		limit := min(len(userList), 10)
		result[currency] = make([]TopUserStat, 0, limit)
		for i := range limit {
			result[currency] = append(result[currency], *userList[i])
		}
	}
	return result
}

// StatsRoundCurrencyAmounts rounds each supplied currency total in place for
// dashboard display. Nil and empty maps have no entries to update; callers
// observe replacements through that same map.
func StatsRoundCurrencyAmounts(amounts CurrencyAmounts) {
	for currency, amount := range amounts {
		amounts[currency] = StatsRoundAmount(amount)
	}
}

// StatsRoundAmount rounds the supplied display amount to two decimal places.
func StatsRoundAmount(amount float64) float64 {
	// Keep two decimal places for dashboard money display.
	return math.Round(amount*100) / 100
}
