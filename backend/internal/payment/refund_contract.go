package payment

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

type RefundPlan struct {
	OrderID         int64
	Order           *Order
	RefundAmount    float64
	GatewayAmount   float64
	Reason          string
	Force           bool
	DeductBalance   bool
	DeductionType   string
	BalanceToDeduct float64
	SubDaysToDeduct int
	SubscriptionID  int64
	// OperationID correlates local audit records; providers use their own idempotency keys.
	OperationID     string
	ChannelRefundID string
}
type RefundResult struct {
	Success         bool    `json:"success"`
	Warning         string  `json:"warning,omitempty"`
	RequireForce    bool    `json:"require_force,omitempty"`
	BalanceDeducted float64 `json:"balance_deducted,omitempty"`
	SubDaysDeducted int     `json:"subscription_days_deducted,omitempty"`
}
type RefundPendingDetail struct {
	RefundID            string  `json:"refundID"`
	DeductBalance       bool    `json:"deductBalance"`
	DeductionType       string  `json:"deductionType"`
	BalanceDeducted     float64 `json:"balanceDeducted"`
	SubDaysDeducted     int     `json:"subDaysDeducted"`
	SubscriptionID      int64   `json:"subscriptionID"`
	DeductionRollbackOK bool    `json:"deductionRollbackOK"`
	OperationID         string  `json:"operationID,omitempty"`
}

// RefundReceipt records the preparation transaction's actual deduction and order version.
type RefundReceipt struct {
	Version          int       `json:"version"`
	OperationID      string    `json:"operationID"`
	OrderID          int64     `json:"orderID"`
	OperationVersion time.Time `json:"operationVersion"`
	PreviousStatus   string    `json:"previousStatus"`
	RefundAmount     float64   `json:"refundAmount"`
	GatewayAmount    float64   `json:"gatewayAmount"`
	Reason           string    `json:"reason"`
	Force            bool      `json:"force"`
	RefundPendingDetail
}

// RefundRights operates within a transaction supplied and completed by the store.
type RefundRights interface {
	DeductBalance(context.Context, int64, float64) (float64, error)
	CompensateBalance(context.Context, int64, float64) error
	AdjustSubscription(context.Context, int64, int) error
	RevokeSubscription(context.Context, int64) error
}
type RefundMutation func(context.Context, RefundRights, *RefundPlan) error

// RefundStore provides atomic refund operations and owns their transaction lifetimes.
type RefundStore interface {
	RequestRefund(context.Context, int64, int64, float64, string, time.Time, string) (int, error)
	Order(context.Context, int64) (*Order, error)
	PrepareRefund(context.Context, *RefundPlan, RefundMutation) (*RefundReceipt, error)
	CompleteRefund(context.Context, *RefundPlan, *RefundReceipt, RefundMutation) (*RefundResult, error)
	CompensateRefund(context.Context, *RefundPlan, *RefundReceipt, string, string, RefundMutation) error
	FailPendingRefund(context.Context, *Order, RefundPendingDetail, string) (*Order, error)
	RefundRecovery(context.Context, *Order) (*RefundReceipt, error)
	PendingDetail(context.Context, int64) (RefundPendingDetail, error)
}

type (
	RefundUser         struct{ Balance float64 }
	RefundSubscription struct {
		ID     int64
		Status string
	}
)

// RefundRuntime supplies provider resolution, rights queries, auditing, and the clock.
type RefundRuntime struct {
	User          func(context.Context, int64) (*RefundUser, error)
	Subscriptions func(context.Context, int64) ([]RefundSubscription, error)
	Instance      func(context.Context, *Order) (*ProviderInstance, error)
	Warn          func(string, ...any)
	Now           func() time.Time
	Provider      func(context.Context, *Order) (Provider, error)
	Observe       func(context.Context) func()
	Audit         func(context.Context, int64, string, string, map[string]any)
}
type RefundWorkflow struct {
	store   RefundStore
	runtime RefundRuntime
}

func NewRefundWorkflow(store RefundStore, runtime RefundRuntime) *RefundWorkflow {
	if runtime.Warn == nil {
		runtime.Warn = func(string, ...any) {}
	}
	if runtime.Now == nil {
		runtime.Now = time.Now
	}
	if runtime.Observe == nil {
		runtime.Observe = func(context.Context) func() { return func() {} }
	}
	if runtime.Audit == nil {
		runtime.Audit = func(context.Context, int64, string, string, map[string]any) {}
	}
	return &RefundWorkflow{store: store, runtime: runtime}
}

func RefundRecoveryRequired(reason string) error {
	return apperror.Conflict("REFUND_RECOVERY_REQUIRED", "refund recovery requires manual verification").WithMetadata(map[string]string{"reason": reason})
}
