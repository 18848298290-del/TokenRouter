package promotion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

var (
	ErrPromoCodeNotFound    = infraerrors.NotFound("PROMO_CODE_NOT_FOUND", "promo code not found")
	ErrPromoCodeExpired     = infraerrors.BadRequest("PROMO_CODE_EXPIRED", "promo code has expired")
	ErrPromoCodeDisabled    = infraerrors.BadRequest("PROMO_CODE_DISABLED", "promo code is disabled")
	ErrPromoCodeMaxUsed     = infraerrors.BadRequest("PROMO_CODE_MAX_USED", "promo code has reached maximum uses")
	ErrPromoCodeAlreadyUsed = infraerrors.Conflict("PROMO_CODE_ALREADY_USED", "you have already used this promo code")
	ErrPromoCodeInvalid     = infraerrors.BadRequest("PROMO_CODE_INVALID", "invalid promo code")
)

// PromoService manages registration promotion codes and their balance credits.
type PromoService struct {
	promoRepo            PromoCodeRepository
	mutations            PromoMutations
	billingCacheService  BalanceCache
	authCacheInvalidator AuthCacheInvalidator
	runtime              Runtime
}

// PromoMutations applies promotion entitlements within storage-managed transactions.
type PromoMutations interface {
	ApplyCode(context.Context, int64, string, func(*PromoCode) error, func() time.Time) error
}

// PromoCodeRepository reads and writes promotion codes and usage records.
type PromoCodeRepository interface {
	// Code persistence.
	Create(ctx context.Context, code *PromoCode) error
	GetByID(ctx context.Context, id int64) (*PromoCode, error)
	GetByCode(ctx context.Context, code string) (*PromoCode, error)
	GetByCodeForUpdate(ctx context.Context, code string) (*PromoCode, error) // Locks the code row for concurrent redemption checks
	Update(ctx context.Context, code *PromoCode) error
	Delete(ctx context.Context, id int64) error

	// Code lists.
	List(ctx context.Context, params pagination.PaginationParams) ([]PromoCode, *pagination.PaginationResult, error)
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PromoCode, *pagination.PaginationResult, error)

	// Usage records.
	CreateUsage(ctx context.Context, usage *PromoCodeUsage) error
	GetUsageByPromoCodeAndUser(ctx context.Context, promoCodeID, userID int64) (*PromoCodeUsage, error)
	ListUsagesByPromoCode(ctx context.Context, promoCodeID int64, params pagination.PaginationParams) ([]PromoCodeUsage, *pagination.PaginationResult, error)

	// Usage count updates.
	IncrementUsedCount(ctx context.Context, id int64) error
}

// RegistrationPromotionPreview contains validity, bonus amount, and a public error code.
type RegistrationPromotionPreview struct {
	Valid       bool
	BonusAmount float64
	ErrorCode   string
}

func NewPromoService(repo PromoCodeRepository, mutations PromoMutations, auth AuthCacheInvalidator, balances BalanceCache, runtime Runtime) *PromoService {
	if runtime.Now == nil {
		runtime.Now = time.Now
	}
	return &PromoService{promoRepo: repo, mutations: mutations, authCacheInvalidator: auth, billingCacheService: balances, runtime: runtime}
}

// ValidatePromoCode checks a code before registration.
// An empty code returns nil, nil.
func (s *PromoService) ValidatePromoCode(ctx context.Context, code string) (*PromoCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil // An empty code is optional.
	}

	promoCode, err := s.promoRepo.GetByCode(ctx, code)
	if err != nil {
		// Keep query errors distinguishable from a missing code.
		return nil, err
	}

	if err := s.validatePromoCodeStatus(promoCode); err != nil {
		return nil, err
	}

	return promoCode, nil
}

// validatePromoCodeStatus checks expiration, status, and usage limits.
func (s *PromoService) validatePromoCodeStatus(promoCode *PromoCode) error {
	if !promoCode.CanUseAt(s.runtime.Now()) {
		if promoCode.IsExpiredAt(s.runtime.Now()) {
			return ErrPromoCodeExpired
		}
		if promoCode.Status == PromoCodeStatusDisabled {
			return ErrPromoCodeDisabled
		}
		if promoCode.MaxUses > 0 && promoCode.UsedCount >= promoCode.MaxUses {
			return ErrPromoCodeMaxUsed
		}
		return ErrPromoCodeInvalid
	}
	return nil
}

// ApplyPromoCode credits the promotion after registration succeeds.
// The mutation implementation rechecks eligibility under a row lock in its transaction.
func (s *PromoService) ApplyPromoCode(ctx context.Context, userID int64, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil
	}

	var applied *PromoCode
	if err := s.mutations.ApplyCode(ctx, userID, code, func(value *PromoCode) error {
		if err := s.validatePromoCodeStatus(value); err != nil {
			return err
		}
		applied = value
		return nil
	}, s.runtime.Now); err != nil {
		return err
	}
	s.invalidatePromoCaches(ctx, userID, applied.BonusAmount)

	// Invalidate the balance cache after the mutation commits.
	if s.billingCacheService != nil {
		s.background("service/promo_service.go:ApplyPromoCode", func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.billingCacheService.InvalidateUserBalance(cacheCtx, userID)
		})
	}

	return nil
}

func (s *PromoService) invalidatePromoCaches(ctx context.Context, userID int64, bonusAmount float64) {
	if bonusAmount == 0 || s.authCacheInvalidator == nil {
		return
	}
	s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
}

// GenerateRandomCode returns a random uppercase hexadecimal promotion code.
func (s *PromoService) GenerateRandomCode() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(bytes)), nil
}

// Create creates a promotion code, generating its value when omitted.
func (s *PromoService) Create(ctx context.Context, input *CreatePromoCodeInput) (*PromoCode, error) {
	code := strings.TrimSpace(input.Code)
	if code == "" {
		// Generate a code when the caller leaves it empty.
		var err error
		code, err = s.GenerateRandomCode()
		if err != nil {
			return nil, err
		}
	}

	promoCode := &PromoCode{
		Code:        strings.ToUpper(code),
		BonusAmount: input.BonusAmount,
		MaxUses:     input.MaxUses,
		UsedCount:   0,
		Status:      PromoCodeStatusActive,
		ExpiresAt:   input.ExpiresAt,
		Notes:       input.Notes,
	}

	if err := s.promoRepo.Create(ctx, promoCode); err != nil {
		return nil, fmt.Errorf("create promo code: %w", err)
	}

	return promoCode, nil
}

// GetByID returns a promotion code by ID.
func (s *PromoService) GetByID(ctx context.Context, id int64) (*PromoCode, error) {
	code, err := s.promoRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return code, nil
}

// Update applies the supplied promotion code fields.
func (s *PromoService) Update(ctx context.Context, id int64, input *UpdatePromoCodeInput) (*PromoCode, error) {
	promoCode, err := s.promoRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if input.Code != nil {
		promoCode.Code = strings.ToUpper(strings.TrimSpace(*input.Code))
	}
	if input.BonusAmount != nil {
		promoCode.BonusAmount = *input.BonusAmount
	}
	if input.MaxUses != nil {
		promoCode.MaxUses = *input.MaxUses
	}
	if input.Status != nil {
		promoCode.Status = *input.Status
	}
	if input.ExpiresAt != nil {
		if input.ExpiresAt.IsZero() {
			input.ExpiresAt = nil
		}
		promoCode.ExpiresAt = input.ExpiresAt
	}
	if input.Notes != nil {
		promoCode.Notes = *input.Notes
	}

	if err := s.promoRepo.Update(ctx, promoCode); err != nil {
		return nil, fmt.Errorf("update promo code: %w", err)
	}

	return promoCode, nil
}

// Delete removes a promotion code.
func (s *PromoService) Delete(ctx context.Context, id int64) error {
	if err := s.promoRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete promo code: %w", err)
	}
	return nil
}

// List returns promotion codes matching the status and search filters.
func (s *PromoService) List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PromoCode, *pagination.PaginationResult, error) {
	return s.promoRepo.ListWithFilters(ctx, params, status, search)
}

// ListUsages returns usage records for a promotion code.
func (s *PromoService) ListUsages(ctx context.Context, promoCodeID int64, params pagination.PaginationParams) ([]PromoCodeUsage, *pagination.PaginationResult, error) {
	return s.promoRepo.ListUsagesByPromoCode(ctx, promoCodeID, params)
}

// background schedules work through the injected runner or executes it synchronously when no runner is
// configured.
func (s *PromoService) background(name string, fn func()) {
	if s.runtime.Background != nil {
		s.runtime.Background(name, fn)
	} else {
		fn()
	}
}

// PreviewRegistrationPromotion checks a code and returns its public preview.
func (s *PromoService) PreviewRegistrationPromotion(ctx context.Context, code string) RegistrationPromotionPreview {
	v, e := s.ValidatePromoCode(ctx, code)
	if e != nil {
		reason := "PROMO_CODE_INVALID"
		switch e {
		case ErrPromoCodeNotFound:
			reason = "PROMO_CODE_NOT_FOUND"
		case ErrPromoCodeExpired:
			reason = "PROMO_CODE_EXPIRED"
		case ErrPromoCodeDisabled:
			reason = "PROMO_CODE_DISABLED"
		case ErrPromoCodeMaxUsed:
			reason = "PROMO_CODE_MAX_USED"
		case ErrPromoCodeAlreadyUsed:
			reason = "PROMO_CODE_ALREADY_USED"
		}
		return RegistrationPromotionPreview{ErrorCode: reason}
	}
	if v == nil {
		return RegistrationPromotionPreview{ErrorCode: "PROMO_CODE_INVALID"}
	}
	return RegistrationPromotionPreview{Valid: true, BonusAmount: v.BonusAmount}
}
