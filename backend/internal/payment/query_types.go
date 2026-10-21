package payment

// query_types.go defines payment order filters and dashboard results with
// currency-keyed monetary totals.
// Payment dashboards keep channel totals and user rankings keyed by
// recorded currency, so their amounts retain distinct units. Current-day
// summary values and current pending counts have different selection
// windows from the requested historical range. Purchase distributions and
// weighted unit prices retain a currency for every monetary aggregate.

// OrderListParams supplies paging and optional persisted-order filters for
// query adapters.
type OrderListParams struct {
	// Page and PageSize are normalized by QueryPagination in the
	// PostgreSQL list adapter. Nonpositive page selects one; nonpositive
	// size selects twenty, and sizes above one hundred are capped.
	Page     int // requested page number
	PageSize int // requested page size
	// Status, OrderType, and PaymentType apply exact stored-field
	// filters when nonempty. Empty values leave the corresponding
	// filter absent in the PostgreSQL user and administrator lists.
	Status      string // stored order state filter
	OrderType   string // stored entitlement type filter
	PaymentType string // stored payment method filter
	// Keyword applies to administrator lists, matching merchant number,
	// buyer email, or buyer name case-insensitively. The user-list adapter
	// does not apply this field to its query.
	Keyword string // administrator order search text
}

// DashboardStats retains payment totals, counts, purchase metrics, and
// requested-range series.
// @project-doc docs/domains/payments_and_entitlements.md#payment_statistics_units
type DashboardStats struct {
	// TodayAmount and TodayCount describe the current local day, while
	// TotalAmount, TotalCount, and AvgAmount describe the requested paid
	// order range. GetDashboardStatsWithRange fills today's values with
	// a separate query even when that day lies outside the range.
	TodayAmount CurrencyAmounts `json:"today_amount"` // current-day currency payment totals
	TotalAmount CurrencyAmounts `json:"total_amount"` // range currency payment totals
	TodayCount  int             `json:"today_count"`  // current-day paid-order count
	TotalCount  int             `json:"total_count"`  // range paid-order count
	AvgAmount   CurrencyAmounts `json:"avg_amount"`   // range average by currency
	// AvgReasoningPointPurchaseUnitPrice and ReasoningPointPurchaseOrderCount
	// use orders accepted by StatsPaymentDashboardReasoningPointPurchase.
	// Each currency divides paid principal after fees by purchased units
	// and rounds to four places. The count covers all accepted currencies.
	AvgReasoningPointPurchaseUnitPrice CurrencyAmounts `json:"avg_reasoning_point_purchase_unit_price"` // paid principal per unit by currency
	ReasoningPointPurchaseOrderCount   int             `json:"reasoning_point_purchase_order_count"`    // selected purchase record count
	// PendingOrders uses the store's current pending count independently
	// of the paid-order range. The PostgreSQL adapter counts PENDING
	// and PROCESSING orders across all users and dates.
	PendingOrders int `json:"pending_orders"` // current pending-order count

	DailySeries          []DailyStats               `json:"daily_series"`          // dated currency payment totals
	PaymentMethods       []PaymentMethodStat        `json:"payment_methods"`       // totals by payment method
	PurchaseDistribution []PurchaseDistributionStat `json:"purchase_distribution"` // totals by purchase category
	TopUsers             TopUsersByCurrency         `json:"top_users"`             // user rankings by currency
}

type (
	// CurrencyAmounts retains totals keyed by recorded payment currency
	// code. Distinct keys represent distinct units, so consumers keep
	// them separate when interpreting dashboard payment amounts.
	CurrencyAmounts map[string]float64
	// DailyStats retains one local date, its currency totals, and paid-order count.
	DailyStats struct {
		// Date, Amount, and Count describe the same local day selected
		// using the range start's location. Amount retains PayAmount totals
		// by currency, while Count includes the selected paid orders on that day.
		Date   string          `json:"date"`   // YYYY-MM-DD
		Amount CurrencyAmounts `json:"amount"` // paid totals by currency
		Count  int             `json:"count"`  // matching paid-order count
	}
)

// PaymentMethodStat groups paid-order counts and currency totals by stored method.
type PaymentMethodStat struct {
	// Type identifies the stored payment method shared by Amount and
	// Count. Amount retains its paid PayAmount totals by currency,
	// while Count includes every selected order for that method.
	Type   string          `json:"type"`   // stored payment method
	Amount CurrencyAmounts `json:"amount"` // paid totals by currency
	Count  int             `json:"count"`  // matching paid-order count
}

// PurchaseDistributionStat groups paid amounts and counts by currency and purchase category.
type PurchaseDistributionStat struct {
	Type     string `json:"type"`     // balance or subscription category
	Currency string `json:"currency"` // normalized payment currency code
	// Label prefers the current plan name, then the order snapshot
	// name, then a plan-ID or subscription label. Non-subscription
	// orders use the balance category in StatsPurchaseDistributionKey.
	Label  string `json:"label"`             // selected purchase display label
	PlanID *int64 `json:"plan_id,omitempty"` // optional purchased plan identifier
	// Amount sums PayAmount for this category and Currency. Separate
	// currencies receive separate records, each rounded to two decimal places.
	Amount float64 `json:"amount"` // summed raw channel amounts
	Count  int     `json:"count"`  // matching purchase order count
}

// TopUserStat identifies one user and paid amount within a currency ranking.
type TopUserStat struct {
	UserID int64 `json:"user_id"` // ranked local user identity
	// Email is the checkout email from the first order encountered
	// for that user in its currency bucket. Later profile changes
	// do not replace that recorded label in StatsBuildTopUsers.
	Email  string  `json:"email"`  // recorded buyer email
	Amount float64 `json:"amount"` // paid amount for currency
}

// TopUsersByCurrency retains separate highest-paid-user rankings for each
// recorded currency.
type TopUsersByCurrency map[string][]TopUserStat
