package payment

type OrderListParams struct {
	Page        int
	PageSize    int
	Status      string
	OrderType   string
	PaymentType string
	Keyword     string
}
type DashboardStats struct {
	TodayAmount                        CurrencyAmounts `json:"today_amount"`
	TotalAmount                        CurrencyAmounts `json:"total_amount"`
	TodayCount                         int             `json:"today_count"`
	TotalCount                         int             `json:"total_count"`
	AvgAmount                          CurrencyAmounts `json:"avg_amount"`
	AvgReasoningPointPurchaseUnitPrice float64         `json:"avg_reasoning_point_purchase_unit_price"`
	ReasoningPointPurchaseOrderCount   int             `json:"reasoning_point_purchase_order_count"`
	PendingOrders                      int             `json:"pending_orders"`

	DailySeries          []DailyStats               `json:"daily_series"`
	PaymentMethods       []PaymentMethodStat        `json:"payment_methods"`
	PurchaseDistribution []PurchaseDistributionStat `json:"purchase_distribution"`
	TopUsers             TopUsersByCurrency         `json:"top_users"`
}

// CurrencyAmounts keeps amounts separate by ISO 4217 currency; values in different currencies must not be summed.
type (
	CurrencyAmounts map[string]float64
	DailyStats      struct {
		Date   string          `json:"date"`
		Amount CurrencyAmounts `json:"amount"`
		Count  int             `json:"count"`
	}
)

type PaymentMethodStat struct {
	Type   string          `json:"type"`
	Amount CurrencyAmounts `json:"amount"`
	Count  int             `json:"count"`
}
type PurchaseDistributionStat struct {
	Type   string  `json:"type"`
	Label  string  `json:"label"`
	PlanID *int64  `json:"plan_id,omitempty"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}
type TopUserStat struct {
	UserID int64   `json:"user_id"`
	Email  string  `json:"email"`
	Amount float64 `json:"amount"`
}

// TopUsersByCurrency keeps a separate ranking for each currency.
type TopUsersByCurrency map[string][]TopUserStat
