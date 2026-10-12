package payment

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

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

func (s *OrderQueries) StatsFillPaymentDashboardTodayStats(ctx context.Context, st *DashboardStats, todayStart time.Time, paidStatuses []string) error {
	// Today's cards use the current calendar day independently of the historical range.
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
		st.AvgReasoningPointPurchaseUnitPrice = math.Round(totalPrincipal/totalReasoningPoints*10000) / 10000
	}
}

func StatsPaymentDashboardReasoningPointPurchase(o *Order) (principal float64, points float64, ok bool) {
	switch o.OrderType {
	case OrderTypeSubscription:
		// Subscription points use a positive monthly quota snapshot; daily, weekly, and unlimited quotas cannot
		// supply this metric.
		if o.PlanSnapshot.MonthlyLimitUSD == nil || *o.PlanSnapshot.MonthlyLimitUSD <= 0 || o.Amount <= 0 {
			return 0, 0, false
		}
		return o.Amount, *o.PlanSnapshot.MonthlyLimitUSD, true
	default:
		// Amount is the credited reasoning points; PayAmount minus fees is the recharge principal.
		principal = o.PayAmount - o.FeeAmount
		if principal <= 0 || o.Amount <= 0 {
			return 0, 0, false
		}
		return principal, o.Amount, true
	}
}

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

func StatsStartOfPaymentStatsDay(t time.Time) time.Time {
	inLoc := t.In(t.Location())
	return time.Date(inLoc.Year(), inLoc.Month(), inLoc.Day(), 0, 0, 0, 0, inLoc.Location())
}

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

func StatsPurchaseDistributionKey(o *Order, planNames map[int64]string) (string, *PurchaseDistributionStat) {
	// Group balance recharges together and subscriptions by plan ID, preferring current plan names.
	if o.OrderType != OrderTypeSubscription {
		return OrderTypeBalance, &PurchaseDistributionStat{Type: OrderTypeBalance, Label: OrderTypeBalance}
	}
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
		limit := min(len(userList), 10)
		result[currency] = make([]TopUserStat, 0, limit)
		for i := range limit {
			result[currency] = append(result[currency], *userList[i])
		}
	}
	return result
}

func StatsRoundCurrencyAmounts(amounts CurrencyAmounts) {
	for currency, amount := range amounts {
		amounts[currency] = StatsRoundAmount(amount)
	}
}

func StatsRoundAmount(amount float64) float64 {
	return math.Round(amount*100) / 100
}
