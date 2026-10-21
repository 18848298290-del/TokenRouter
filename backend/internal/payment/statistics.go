package payment

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

// statistics.go builds payment dashboard totals, calendar series, purchase
// distributions, and currency-specific user rankings.
// Payment dashboard history uses paid_at and the paid, recharging or completed
// states, while current-day cards are queried separately from the requested
// historical range. Calendar buckets use the range timezone and advance by
// dates, so a daylight-saving day can have a different elapsed duration from
// twenty-four hours.
//
// Monetary totals, purchase distributions, and balance-unit purchase prices
// keep currency groups from PaymentOrderCurrency. Purchase prices divide paid
// principal after fees by credited balance or a subscription's monthly quota.
// This keeps converted subscription payments in their channel currency while
// plan names can change independently of plan-ID grouping. Each currency is
// rounded independently, so consumers can display comparisons within one unit.

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

// StatsComputeReasoningPointPurchaseUnitPrice calculates average purchase prices
// separately for each payment currency. Callers supply nonnil orders already
// filtered by payment state and date; each call replaces the destination map
// and contributing order count, including when no observations qualify.
//
// Each currency divides its summed principal by its summed balance units before
// rounding. Subscription orders contribute monthly quota, and balance orders
// contribute credited Amount, so differently sized purchases receive their
// proportional weight without adding money from different currencies together.
func StatsComputeReasoningPointPurchaseUnitPrice(st *DashboardStats, orders []*Order) {
	principalTotals := make(CurrencyAmounts)
	pointTotals := make(CurrencyAmounts)
	st.AvgReasoningPointPurchaseUnitPrice = make(CurrencyAmounts)
	st.ReasoningPointPurchaseOrderCount = 0
	for _, o := range orders {
		principal, points, ok := StatsPaymentDashboardReasoningPointPurchase(o)
		if !ok {
			continue
		}
		currency := PaymentOrderCurrency(o)
		principalTotals[currency] += principal
		pointTotals[currency] += points
		st.ReasoningPointPurchaseOrderCount++
	}
	for currency, points := range pointTotals {
		// Round each currency's weighted price to four decimal places.
		st.AvgReasoningPointPurchaseUnitPrice[currency] = math.Round(principalTotals[currency]/points*10000) / 10000
	}
}

// StatsPaymentDashboardReasoningPointPurchase returns paid principal, purchased
// balance units, and eligibility for a nonnil order. Nonpositive principal or
// units exclude the observation; subscriptions require positive monthly quota.
//
// Principal uses PayAmount minus FeeAmount in the channel currency for every
// order type. A subscription's Amount can be its unconverted plan price, so
// using it would attach the channel currency to a different monetary quantity.
// Balance purchases use credited Amount as their unit quantity, including any
// configured recharge multiplier.
func StatsPaymentDashboardReasoningPointPurchase(o *Order) (principal float64, points float64, ok bool) {
	principal = o.PayAmount - o.FeeAmount
	if principal <= 0 {
		return 0, 0, false
	}
	if o.OrderType == OrderTypeSubscription {
		if o.PlanSnapshot.MonthlyLimitUSD == nil || *o.PlanSnapshot.MonthlyLimitUSD <= 0 {
			return 0, 0, false
		}
		return principal, *o.PlanSnapshot.MonthlyLimitUSD, true
	}
	if o.Amount <= 0 {
		return 0, 0, false
	}
	return principal, o.Amount, true
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

// StatsBuildPurchaseDistribution groups paid orders by purchase category and
// payment currency, keeping subscription plans separate within each currency.
// Callers supply nonnil, state-filtered orders and optional current plan names.
//
// The category key and currency jointly identify an aggregate, so payments in
// different currencies never contribute to the same amount or share. Each
// aggregate rounds its summed PayAmount before sorting by currency, descending
// amount, then count. Current plan names affect display labels while category
// identity continues to use the persisted plan identifier or snapshot name.
func StatsBuildPurchaseDistribution(orders []*Order, planNames map[int64]string) []PurchaseDistributionStat {
	distributionMap := make(map[string]*PurchaseDistributionStat)
	for _, o := range orders {
		key, item := StatsPurchaseDistributionKey(o, planNames)
		item.Currency = PaymentOrderCurrency(o)
		key = item.Currency + ":" + key
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
		if items[i].Currency != items[j].Currency {
			return items[i].Currency < items[j].Currency
		}
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
