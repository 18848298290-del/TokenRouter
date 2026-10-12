package billing

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// RedeemCache defines cache operations for redeem service.
type RedeemCache interface {
	GetRedeemAttemptCount(ctx context.Context, userID int64) (int, error)
	IncrementRedeemAttemptCount(ctx context.Context, userID int64) error

	AcquireRedeemLock(ctx context.Context, code string, ttl time.Duration) (bool, error)
	ReleaseRedeemLock(ctx context.Context, code string) error
}

type RedeemCodeRepository interface {
	Create(ctx context.Context, code *RedeemCode) error
	CreateBatch(ctx context.Context, codes []RedeemCode) error
	GetByID(ctx context.Context, id int64) (*RedeemCode, error)
	GetByIDForUpdate(ctx context.Context, id int64) (*RedeemCode, error)
	GetByCode(ctx context.Context, code string) (*RedeemCode, error)
	GetByCodeForUpdate(ctx context.Context, code string) (*RedeemCode, error)
	Update(ctx context.Context, code *RedeemCode) error
	BatchUpdate(ctx context.Context, ids []int64, fields RedeemCodeBatchUpdateFields) (int64, error)
	Delete(ctx context.Context, id int64) error
	Use(ctx context.Context, id, userID int64) error
	CreateUsage(ctx context.Context, usage *RedeemCodeUsage) error
	GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*RedeemCodeUsage, error)

	List(ctx context.Context, params pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error)
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, codeType, status, search string) ([]RedeemCode, *pagination.PaginationResult, error)
	// ListByUserPaginated returns a user's paginated redemption and adjustment history.
	// codeType filter is optional - pass empty string to return all types.
	ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]RedeemCode, *pagination.PaginationResult, error)
	// SumPositiveBalanceByUser returns the total recharged amount (sum of positive balance values) for a user.
	SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error)
}

type NullableTimeUpdate struct {
	Set   bool
	Value *time.Time
}

type RedeemCodeBatchUpdateFields struct {
	Status    *string
	ExpiresAt NullableTimeUpdate
	Notes     *string

	// These fields let batch updates reject core changes that require single-code entitlement consistency checks.
	Type    *string
	Value   *float64
	MaxUses *int
	PlanID  *int64
}

type RedeemCodeBatchUpdateInput struct {
	IDs    []int64
	Fields RedeemCodeBatchUpdateFields
}

type RedeemCodeBatchUpdateResult struct {
	Updated int64 `json:"updated"`
}

func (f RedeemCodeBatchUpdateFields) HasChanges() bool {
	return f.Status != nil ||
		f.ExpiresAt.Set ||
		f.Notes != nil ||
		f.Type != nil ||
		f.Value != nil ||
		f.MaxUses != nil ||
		f.PlanID != nil
}

func (f RedeemCodeBatchUpdateFields) HasCoreFieldChanges() bool {
	return f.Type != nil || f.Value != nil || f.MaxUses != nil || f.PlanID != nil
}

func (f RedeemCodeBatchUpdateFields) TouchesUsedSensitiveFields() bool {
	return f.Status != nil || f.ExpiresAt.Set
}
