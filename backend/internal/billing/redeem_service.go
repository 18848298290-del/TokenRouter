package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

const (
	redeemMaxErrorsPerHour = 20

	redeemLockDuration = 10 * time.Second // Expiry releases abandoned cache locks.
)

var (
	ErrRedeemCodeExists   = apperror.Conflict("REDEEM_CODE_EXISTS", "redeem code already exists")
	ErrRedeemCodeNotFound = apperror.NotFound("REDEEM_CODE_NOT_FOUND", "redeem code not found")
	ErrRedeemCodeUsed     = apperror.Conflict("REDEEM_CODE_USED", "redeem code already used")

	ErrRedeemPaymentRequired               = apperror.Forbidden("REDEEM_PAYMENT_REQUIRED", "a successful payment is required to redeem this code")
	ErrRedeemPaymentRequirementUnsupported = apperror.BadRequest("REDEEM_PAYMENT_REQUIREMENT_UNSUPPORTED", "invitation codes cannot require a payment")
	ErrRedeemCodeExpired                   = apperror.BadRequest("REDEEM_CODE_EXPIRED", "redeem code has expired")
	ErrRedeemCodeMaxUsed                   = apperror.Conflict("REDEEM_CODE_MAX_USED", "redeem code has reached maximum uses")
	ErrRedeemCodeAlreadyUsed               = apperror.Conflict("REDEEM_CODE_ALREADY_USED", "you have already used this redeem code")
	ErrRedeemRateLimited                   = apperror.TooManyRequests("REDEEM_RATE_LIMITED", "too many failed attempts, please try again later")
	ErrRedeemCodeLocked                    = apperror.Conflict("REDEEM_CODE_LOCKED", "redeem code is being processed, please try again")

	// These errors indicate missing atomic balance or concurrency adjustment support.
	ErrRedeemBalanceUnsupported     = errors.New("user repository does not support atomic redeem balance adjustments")
	ErrRedeemConcurrencyUnsupported = errors.New("user repository does not support atomic redeem concurrency adjustments")
)

type ctxKeySkipRedeemAffiliate struct{}

// RedeemTransactions commits entitlement changes, usage records, and use counts in one transaction.
type RedeemTransactions interface {
	Within(context.Context, func(context.Context) error) error
	ApplyBalance(context.Context, int64, float64) error
	ApplyConcurrency(context.Context, int64, int) error
}

type (
	RedeemAuthInvalidator interface{ InvalidateAuthCacheByUserID(context.Context, int64) }
	RedeemAffiliate       interface {
		IsEnabled(context.Context) bool
		AccrueInviteRebate(context.Context, int64, float64) (float64, error)
	}
)

type RedeemRuntime struct {
	// HasPaidOrder checks payment history; lookup errors stop entitlement issuance.
	HasPaidOrder func(context.Context, int64) (bool, error)
	Now          func() time.Time
	Observe      Observe
	Background   func(string, func())
}

// RedeemService applies redemption rules and runs cache invalidation and best-effort rebates after commit.
type RedeemService struct {
	redeemRepo           RedeemCodeRepository
	userRepo             BalanceReader
	subscriptionService  *SubscriptionService
	cache                RedeemCache
	billingCacheService  *Eligibility
	transactions         RedeemTransactions
	authCacheInvalidator RedeemAuthInvalidator
	affiliateService     RedeemAffiliate
	runtime              RedeemRuntime
}

// ContextSkipRedeemAffiliate skips redemption rebates when the payment-order workflow handles audited rebate deduplication.
func ContextSkipRedeemAffiliate(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySkipRedeemAffiliate{}, true)
}

func NewRedeemService(repo RedeemCodeRepository, users BalanceReader, subs *SubscriptionService, cache RedeemCache, eligibility *Eligibility, transactions RedeemTransactions, auth RedeemAuthInvalidator, affiliate RedeemAffiliate, runtime RedeemRuntime) *RedeemService {
	if runtime.Now == nil {
		runtime.Now = time.Now
	}
	return &RedeemService{redeemRepo: repo, userRepo: users, subscriptionService: subs, cache: cache, billingCacheService: eligibility, transactions: transactions, authCacheInvalidator: auth, affiliateService: affiliate, runtime: runtime}
}

// CreateCode creates a redeem code with caller-provided code value.
// It is primarily used by admin integrations that require an external order ID
// to be mapped to a deterministic redeem code.
func (s *RedeemService) CreateCode(ctx context.Context, code *RedeemCode) error {
	if code == nil {
		return errors.New("redeem code is required")
	}
	code.Code = strings.TrimSpace(code.Code)
	if code.Code == "" {
		return errors.New("code is required")
	}
	if code.Type == "" {
		code.Type = RedeemTypeBalance
	}
	if code.Type != RedeemTypeInvitation && code.Value == 0 {
		return errors.New("value must not be zero")
	}
	if code.Status == "" {
		code.Status = StatusUnused
	}
	if code.MaxUses <= 0 {
		code.MaxUses = 1
	}
	if code.UsedCount < 0 {
		code.UsedCount = 0
	}
	if code.Type == RedeemTypeInvitation {
		if code.RequiresPayment {
			return ErrRedeemPaymentRequirementUnsupported
		}
		code.MaxUses = 1
	}
	code.Status = code.PersistedStatus()

	if err := s.redeemRepo.Create(ctx, code); err != nil {
		return fmt.Errorf("create redeem code: %w", err)
	}
	return nil
}

func (s *RedeemService) BatchUpdate(ctx context.Context, input *RedeemCodeBatchUpdateInput) (*RedeemCodeBatchUpdateResult, error) {
	if input == nil {
		return nil, apperror.BadRequest("REDEEM_CODE_BATCH_UPDATE_INVALID", "batch update input is required")
	}
	if len(input.IDs) == 0 {
		return nil, apperror.BadRequest("REDEEM_CODE_BATCH_UPDATE_IDS_REQUIRED", "ids are required")
	}
	if !input.Fields.HasChanges() {
		return nil, apperror.BadRequest("REDEEM_CODE_BATCH_UPDATE_EMPTY", "at least one field must be selected")
	}
	if input.Fields.HasCoreFieldChanges() {
		return nil, apperror.BadRequest("REDEEM_CODE_CORE_FIELDS_IMMUTABLE", "type, value, max_uses and plan_id cannot be batch updated")
	}

	ids := make([]int64, 0, len(input.IDs))
	seen := make(map[int64]struct{}, len(input.IDs))
	for _, id := range input.IDs {
		if id <= 0 {
			return nil, apperror.BadRequest("REDEEM_CODE_BATCH_UPDATE_INVALID_ID", "ids must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, apperror.BadRequest("REDEEM_CODE_BATCH_UPDATE_IDS_REQUIRED", "ids are required")
	}

	if input.Fields.Status != nil {
		switch *input.Fields.Status {
		case StatusUnused, StatusDisabled:
		default:
			return nil, apperror.BadRequest("REDEEM_CODE_STATUS_INVALID", "status must be unused or disabled")
		}
	}
	if input.Fields.ExpiresAt.Set && input.Fields.ExpiresAt.Value != nil {
		expiresAt := input.Fields.ExpiresAt.Value.UTC()
		if !expiresAt.After(s.runtime.Now().UTC()) {
			return nil, apperror.BadRequest("REDEEM_CODE_EXPIRES_AT_INVALID", "expires_at must be in the future")
		}
		input.Fields.ExpiresAt.Value = &expiresAt
	}

	updated, err := s.redeemRepo.BatchUpdate(ctx, ids, input.Fields)
	if err != nil {
		return nil, err
	}
	return &RedeemCodeBatchUpdateResult{Updated: updated}, nil
}

// checkRedeemRateLimit rejects users whose failed-attempt count reaches the limit.
func (s *RedeemService) checkRedeemRateLimit(ctx context.Context, userID int64) error {
	if s.cache == nil {
		return nil
	}

	count, err := s.cache.GetRedeemAttemptCount(ctx, userID)
	if err != nil {
		// Cache errors permit redemption to continue.
		return nil
	}

	if count >= redeemMaxErrorsPerHour {
		return ErrRedeemRateLimited
	}

	return nil
}

// incrementRedeemErrorCount increments the failed-attempt counter on a best-effort basis.
func (s *RedeemService) incrementRedeemErrorCount(ctx context.Context, userID int64) {
	if s.cache == nil {
		return
	}

	_ = s.cache.IncrementRedeemAttemptCount(ctx, userID)
}

// acquireRedeemLock attempts the code's cache lock.
// False means a lock is held; cache absence or failure returns true and relies on database checks.
func (s *RedeemService) acquireRedeemLock(ctx context.Context, code string) bool {
	if s.cache == nil {
		return true // Continue with database checks when Redis is unavailable.
	}

	ok, err := s.cache.AcquireRedeemLock(ctx, code, redeemLockDuration)
	if err != nil {
		// Continue with database locking and state checks when Redis fails.
		return true
	}
	return ok
}

// releaseRedeemLock releases the cache lock on a best-effort basis.
func (s *RedeemService) releaseRedeemLock(ctx context.Context, code string) {
	if s.cache == nil {
		return
	}

	_ = s.cache.ReleaseRedeemLock(ctx, code)
}

// unsupportedRedeemTypeError returns a client error for invitation codes and unknown types.
func unsupportedRedeemTypeError(codeType string) error {
	if codeType == RedeemTypeInvitation {
		return apperror.BadRequest("REDEEM_CODE_UNSUPPORTED_TYPE", "invitation codes can only be used during registration")
	}
	return apperror.BadRequest("REDEEM_CODE_UNSUPPORTED_TYPE", fmt.Sprintf("unsupported redeem type: %s", codeType))
}

// Redeem validates and grants a code's benefits, then invalidates caches after commit.
// @project-doc docs/domains/payments_and_entitlements.md#redeem_eligibility
func (s *RedeemService) Redeem(ctx context.Context, userID int64, code string) (*RedeemCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, ErrRedeemCodeNotFound
	}

	// Check failed-attempt limits before acquiring the code lock.
	if err := s.checkRedeemRateLimit(ctx, userID); err != nil {
		return nil, err
	}

	// The cache lock reduces concurrent attempts; the transaction also locks the code row.
	if !s.acquireRedeemLock(ctx, code) {
		return nil, ErrRedeemCodeLocked
	}
	defer s.releaseRedeemLock(ctx, code)
	var redeemCode *RedeemCode
	err := s.transactions.Within(ctx, func(txCtx context.Context) error {
		var e error
		redeemCode, e = s.redeemInTx(ctx, txCtx, userID, code)
		return e
	})
	if err != nil {
		return nil, err
	}

	// Invalidate caches after a successful commit.
	s.invalidateRedeemCaches(ctx, userID, redeemCode)

	// Positive balance grants can earn rebates; payment workflows mark the context to prevent a second rebate.
	if redeemCode.Type == RedeemTypeBalance && redeemCode.Value > 0 {
		s.tryAccrueAffiliateRebateForRedeem(ctx, userID, redeemCode.Value)
	}

	// Reload the committed usage snapshot for the response.
	redeemCode, err = s.redeemRepo.GetByID(ctx, redeemCode.ID)
	if err != nil {
		return nil, fmt.Errorf("get updated redeem code: %w", err)
	}

	return redeemCode, nil
}

// redeemInTx uses txCtx for entitlement and usage writes; post-commit invalidation and rebates run in Redeem.
func (s *RedeemService) redeemInTx(ctx, txCtx context.Context, userID int64, code string) (*RedeemCode, error) {
	redeemCode, err := s.redeemRepo.GetByCodeForUpdate(txCtx, code)
	if err != nil {
		if errors.Is(err, ErrRedeemCodeNotFound) {
			s.incrementRedeemErrorCount(ctx, userID)
			return nil, ErrRedeemCodeNotFound
		}
		return nil, fmt.Errorf("get redeem code: %w", err)
	}

	if err := s.validateRedeemCodeForUser(txCtx, redeemCode, userID); err != nil {
		s.incrementRedeemErrorCount(ctx, userID)
		return nil, err
	}

	// Invitation codes belong to registration; this endpoint accepts direct entitlement grants.
	switch redeemCode.Type {
	case RedeemTypeBalance, RedeemTypeConcurrency:
	case RedeemTypeSubscription:
		if redeemCode.PlanID == nil || *redeemCode.PlanID <= 0 {
			return nil, apperror.BadRequest("REDEEM_CODE_INVALID", "invalid subscription redeem code: missing plan_id")
		}
	default:
		return nil, unsupportedRedeemTypeError(redeemCode.Type)
	}

	// Check payment eligibility before any grant or usage write, allowing retry after payment.
	if redeemCode.RequiresPayment {
		if s.runtime.HasPaidOrder == nil {
			return nil, errors.New("payment history lookup is unavailable")
		}
		paid, err := s.runtime.HasPaidOrder(txCtx, userID)
		if err != nil {
			return nil, fmt.Errorf("check redeem payment history: %w", err)
		}
		if !paid {
			return nil, ErrRedeemPaymentRequired
		}
	}

	_, err = s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	// The code row remains locked while benefits and usage are written.
	switch redeemCode.Type {
	case RedeemTypeBalance:
		if err := s.transactions.ApplyBalance(txCtx, userID, redeemCode.Value); err != nil {
			if errors.Is(err, ErrRedeemBalanceUnsupported) {
				return nil, err
			}
			return nil, fmt.Errorf("update user balance: %w", err)
		}
	case RedeemTypeConcurrency:
		if err := s.transactions.ApplyConcurrency(txCtx, userID, int(redeemCode.Value)); err != nil {
			if errors.Is(err, ErrRedeemConcurrencyUnsupported) {
				return nil, err
			}
			return nil, fmt.Errorf("update user concurrency: %w", err)
		}

	case RedeemTypeSubscription:
		_, _, err := s.subscriptionService.AssignOrExtendSubscription(txCtx, &AssignSubscriptionInput{
			UserID:     userID,
			PlanID:     *redeemCode.PlanID,
			AssignedBy: 0, // System assignment.
			Notes:      fmt.Sprintf("通过兑换码 %s 兑换", redeemCode.Code),
		})
		if err != nil {
			return nil, fmt.Errorf("assign or extend subscription: %w", err)
		}

	default:
		return nil, unsupportedRedeemTypeError(redeemCode.Type)
	}

	usageTime := s.runtime.Now()
	if err := s.redeemRepo.CreateUsage(txCtx, &RedeemCodeUsage{
		RedeemCodeID: redeemCode.ID,
		UserID:       userID,
		UsedAt:       usageTime,
	}); err != nil {
		return nil, fmt.Errorf("create redeem usage: %w", err)
	}

	redeemCode.UsedCount++
	redeemCode.UsedBy = &userID
	redeemCode.UsedAt = &usageTime
	redeemCode.Status = redeemCode.PersistedStatus()

	if err := s.redeemRepo.Update(txCtx, redeemCode); err != nil {
		return nil, fmt.Errorf("update redeem code usage snapshot: %w", err)
	}

	return redeemCode, nil
}

// invalidateRedeemCaches invalidates authentication and applicable balance caches after redemption.
func (s *RedeemService) invalidateRedeemCaches(ctx context.Context, userID int64, redeemCode *RedeemCode) {
	switch redeemCode.Type {
	case RedeemTypeBalance:
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		if s.billingCacheService == nil {
			return
		}
		s.runBackground("service/redeem_service.go:invalidateRedeemCaches", func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateUserBalance(cacheCtx, userID)
		})
	case RedeemTypeConcurrency:
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		if s.billingCacheService == nil {
			return
		}
	case RedeemTypeSubscription:
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		return
	}
}

func (s *RedeemService) tryAccrueAffiliateRebateForRedeem(ctx context.Context, userID int64, amount float64) {
	if ctx.Value(ctxKeySkipRedeemAffiliate{}) != nil {
		return
	}
	if s.affiliateService == nil || !s.affiliateService.IsEnabled(ctx) {
		return
	}
	rebate, err := s.affiliateService.AccrueInviteRebate(ctx, userID, amount)
	if err != nil {
		s.runtime.Observe.Printf("service.redeem", "[Redeem] affiliate rebate failed for user %d amount %.2f: %v", userID, amount, err)
		return
	}
	if rebate > 0 {
		s.runtime.Observe.Printf("service.redeem", "[Redeem] affiliate rebate accrued %.8f for inviter of user %d", rebate, userID)
	}
}

// GetByID retrieves a code by database ID.
func (s *RedeemService) GetByID(ctx context.Context, id int64) (*RedeemCode, error) {
	code, err := s.redeemRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get redeem code: %w", err)
	}
	return code, nil
}

// GetByCode retrieves a code by its credential value.
func (s *RedeemService) GetByCode(ctx context.Context, code string) (*RedeemCode, error) {
	redeemCode, err := s.redeemRepo.GetByCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("get redeem code: %w", err)
	}
	return redeemCode, nil
}

// List returns paginated codes for administration.
func (s *RedeemService) List(ctx context.Context, params pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error) {
	codes, pagination, err := s.redeemRepo.List(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list redeem codes: %w", err)
	}
	return codes, pagination, nil
}

// Delete removes a code that has no recorded uses.
func (s *RedeemService) Delete(ctx context.Context, id int64) error {
	// Read the code to check whether it has been used.
	code, err := s.redeemRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get redeem code: %w", err)
	}

	// Keep used codes so usage history remains available.
	if !code.CanDelete() {
		return apperror.Conflict("REDEEM_CODE_DELETE_USED", "cannot delete redeem code that has usage records")
	}

	if err := s.redeemRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete redeem code: %w", err)
	}

	return nil
}

func (s *RedeemService) validateRedeemCodeForUser(ctx context.Context, redeemCode *RedeemCode, userID int64) error {
	if redeemCode == nil {
		return ErrRedeemCodeNotFound
	}
	if redeemCode.IsExpired() {
		return ErrRedeemCodeExpired
	}

	existingUsage, err := s.redeemRepo.GetUsageByRedeemCodeAndUser(ctx, redeemCode.ID, userID)
	if err != nil {
		return fmt.Errorf("check redeem usage: %w", err)
	}
	if existingUsage != nil {
		return ErrRedeemCodeAlreadyUsed
	}
	if !redeemCode.HasRemainingUses() {
		return ErrRedeemCodeMaxUsed
	}
	return nil
}

// GetUserHistory returns paginated redemption history in descending use-time order with the total count.
func (s *RedeemService) GetUserHistory(ctx context.Context, userID int64, page, pageSize int) ([]RedeemCode, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	codes, result, err := s.redeemRepo.ListByUserPaginated(ctx, userID, params, "")
	if err != nil {
		return nil, 0, fmt.Errorf("get user redeem history: %w", err)
	}
	return codes, result.Total, nil
}

func (s *RedeemService) runBackground(name string, fn func()) {
	if s.runtime.Background == nil {
		fn()
		return
	}
	s.runtime.Background(name, fn)
}
