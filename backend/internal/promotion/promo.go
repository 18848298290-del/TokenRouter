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

// Registration Promo codes grant balance after an account has been created,
// with eligibility checked again by the locked mutation before crediting.
// Previews report public availability, while an omitted code is accepted by
// validation and application as an optional signup input.
//
// Administrator edits distinguish omitted pointers from supplied values and
// use a zero expiration time to clear a deadline. The service retains that
// input pointer until persistence, so edit callers manage shared time values.
// Successful grant application requests authentication invalidation for
// nonzero credit and dispatches balance eviction with a detached bounded
// context. The storage mutation owns credit, receipt, count, and transaction
// completion; the background runner determines whether cleanup work is
// admitted.

// Promo errors identify missing, expired, disabled, exhausted, repeated, and
// otherwise invalid registration grant requests for service and HTTP callers.
var (
	ErrPromoCodeNotFound    = infraerrors.NotFound("PROMO_CODE_NOT_FOUND", "promo code not found")
	ErrPromoCodeExpired     = infraerrors.BadRequest("PROMO_CODE_EXPIRED", "promo code has expired")
	ErrPromoCodeDisabled    = infraerrors.BadRequest("PROMO_CODE_DISABLED", "promo code is disabled")
	ErrPromoCodeMaxUsed     = infraerrors.BadRequest("PROMO_CODE_MAX_USED", "promo code has reached maximum uses")
	ErrPromoCodeAlreadyUsed = infraerrors.Conflict("PROMO_CODE_ALREADY_USED", "you have already used this promo code")
	ErrPromoCodeInvalid     = infraerrors.BadRequest("PROMO_CODE_INVALID", "invalid promo code")
)

// PromoService manages registration grant codes, availability previews, and
// cache cleanup.
type PromoService struct {
	// promoRepo, mutations, billingCacheService, and authCacheInvalidator are
	// retained dependencies shared between requests. runtime supplies clock and
	// dispatch callbacks; dependency owners coordinate lifetime and mutable
	// state.
	promoRepo PromoCodeRepository // grant controls and receipts
	// locked grant transaction dependency
	mutations PromoMutations
	// billingCacheService is optional. An absent interface dependency skips
	// background balance eviction; a present dependency uses the detached
	// timeout context created after a grant.
	billingCacheService BalanceCache // balance cache cleanup dependency
	// authCacheInvalidator is optional. Nonzero grants request authentication
	// eviction when the dependency is present, while zero grants and an absent
	// dependency skip that request.
	// API Key authentication invalidator
	authCacheInvalidator AuthCacheInvalidator
	// clock and dispatch callbacks
	runtime Runtime
}

// PromoMutations applies one registration grant with locked validation and
// consistent usage writes.
type PromoMutations interface {
	// ApplyCode validates the locked code, credits the user, and records receipt
	// and count in a transaction.
	//
	// The callback is called with the locked controls before writes; now
	// timestamps the receipt. Existing per-user usage is rejected. Success
	// returns after the owned transaction commits, while earlier failures
	// attempt rollback, so the service can request cache invalidation after
	// storage reports a completed grant. Callers supply nonnil validation and
	// clock functions.
	ApplyCode(context.Context, int64, string, func(*PromoCode) error, func() time.Time) error
}

// PromoCodeRepository persists grant controls and recipient receipts for
// administrator and signup callers.
type PromoCodeRepository interface {
	// Create persists a grant code and updates its ID and creation/update
	// timestamps in place.
	//
	// The caller supplies valid controls; a context-carried transaction takes
	// precedence over the bound client.
	Create(ctx context.Context, code *PromoCode) error
	// GetByID returns one code by its stored identifier using the repository's
	// bound client.
	//
	// An absent row returns ErrPromoCodeNotFound; a context-carried transaction
	// alone does not change the read client.
	GetByID(ctx context.Context, id int64) (*PromoCode, error)
	// GetByCode returns a case-insensitive text match using the repository's
	// bound client.
	//
	// An absent row returns ErrPromoCodeNotFound; callers trim input and bind a
	// transaction client when that read must participate.
	GetByCode(ctx context.Context, code string) (*PromoCode, error)
	// GetByCodeForUpdate returns a case-insensitive code match with a row lock
	// through the selected client.
	//
	// Callers provide a transaction for a lock spanning subsequent validation
	// and grant writes.
	GetByCodeForUpdate(ctx context.Context, code string) (*PromoCode, error)
	// Update persists administrator-editable controls while leaving redemption
	// counting with IncrementUsedCount.
	//
	// A stale administrator snapshot can precede a successful grant, so this
	// write omits used_count and preserves that grant's increment. Nil
	// expiration clears the deadline, and the supplied object's mutation
	// timestamp is refreshed. Its count remains the input observation; callers
	// who need a current count read the code again after updating.
	Update(ctx context.Context, code *PromoCode) error
	// Delete hard-deletes a matched code through the context transaction or
	// bound client.
	//
	// A missing identifier succeeds with zero matches; persisted foreign-key
	// cascades also delete its associated receipts.
	Delete(ctx context.Context, id int64) error

	// List returns all code statuses on a page using the repository's default
	// filter path.
	//
	// Pagination and sort normalization follow ListWithFilters, and reads use
	// the bound client.
	List(ctx context.Context, params pagination.PaginationParams) ([]PromoCode, *pagination.PaginationResult, error)
	// ListWithFilters returns codes matching optional status and
	// case-insensitive code search with a total count.
	//
	// Reads use the bound client; supported sort fields add ID as a
	// same-direction tie-breaker.
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PromoCode, *pagination.PaginationResult, error)

	// CreateUsage persists a recipient receipt and updates its ID in place
	// through the selected client.
	//
	// The caller supplies the recorded grant timestamp; the database enforces
	// code/user pair uniqueness.
	CreateUsage(ctx context.Context, usage *PromoCodeUsage) error
	// GetUsageByPromoCodeAndUser returns the matching receipt using the
	// repository's bound client.
	//
	// An absent pair returns nil successfully; granting callers bind that client
	// to their transaction before checking.
	GetUsageByPromoCodeAndUser(ctx context.Context, promoCodeID, userID int64) (*PromoCodeUsage, error)
	// ListUsagesByPromoCode returns receipts with eagerly loaded recipients and
	// the total count for one code.
	//
	// The bound client reads descending receipt IDs; an absent code can yield an
	// empty page.
	ListUsagesByPromoCode(ctx context.Context, promoCodeID int64, params pagination.PaginationParams) ([]PromoCodeUsage, *pagination.PaginationResult, error)

	// IncrementUsedCount atomically adds one to the stored redemption count
	// through the selected client.
	//
	// The granting caller runs it after receipt insertion in the same
	// transaction as the balance credit.
	IncrementUsedCount(ctx context.Context, id int64) error
}

// RegistrationPromotionPreview contains public availability, eligible grant
// amount, or a reason code.
type RegistrationPromotionPreview struct {
	Valid bool // code eligibility result
	// BonusAmount is supplied when Valid is true and carries the eligible code's
	// configured grant. Invalid previews leave it at zero.
	BonusAmount float64 // USD
	// ErrorCode describes an invalid preview using a public reason string. Valid
	// results leave it empty; unrecognized failures use the generic invalid-code
	// reason.
	ErrorCode string // public availability reason
}

// NewPromoService retains repository, mutations, cache dependencies, and
// runtime for registration Promo operations.
//
// An absent clock selects time.Now; a missing background runner makes cleanup
// execute synchronously in the service caller.
func NewPromoService(repo PromoCodeRepository, mutations PromoMutations, auth AuthCacheInvalidator, balances BalanceCache, runtime Runtime) *PromoService {
	if runtime.Now == nil {
		runtime.Now = time.Now
	}
	return &PromoService{promoRepo: repo, mutations: mutations, authCacheInvalidator: auth, billingCacheService: balances, runtime: runtime}
}

// ValidatePromoCode returns the eligible code snapshot for a trimmed signup
// input.
//
// Empty input returns nil successfully. Query and eligibility errors are
// returned; this preliminary read reserves no grant capacity.
func (s *PromoService) ValidatePromoCode(ctx context.Context, code string) (*PromoCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil
	}

	promoCode, err := s.promoRepo.GetByCode(ctx, code)
	if err != nil {
		return nil, err
	}

	if err := s.validatePromoCodeStatus(promoCode); err != nil {
		return nil, err
	}

	return promoCode, nil
}

// validatePromoCodeStatus reports the reason a code snapshot fails grant
// eligibility.
//
// It consults the service clock for CanUseAt and again for expiration
// classification. Expiration takes precedence over disabled status, followed
// by an exhausted positive usage limit and the generic invalid case. Callers
// pass a nonnil code; the mutation callback reuses this classifier while
// holding the row lock, whereas preview observes the unlocked read.
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

// ApplyPromoCode requests a locked registration grant for a trimmed optional
// code.
//
// Empty input succeeds immediately. Mutation success triggers cache cleanup;
// its validation callback records the amount used for authentication
// invalidation.
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

	// Request balance eviction after the grant mutation returns committed
	// success; the runner may dispatch later and controls whether cleanup is
	// admitted.
	if s.billingCacheService != nil {
		s.background("service/promo_service.go:ApplyPromoCode", func() {
			// Detach cleanup from signup cancellation and bound its cache attempt to
			// five seconds, releasing the timer when the callback returns.
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Discard the cleanup error because the balance grant has already
			// committed; the injected cache implementation can observe its own
			// failure.
			_ = s.billingCacheService.InvalidateUserBalance(cacheCtx, userID)
		})
	}

	return nil
}

// invalidatePromoCaches requests authentication eviction after a grant with
// nonzero amount.
//
// Zero grants or a missing invalidator skip the request; the errorless
// invalidator determines its own failure handling.
func (s *PromoService) invalidatePromoCaches(ctx context.Context, userID int64, bonusAmount float64) {
	if bonusAmount == 0 || s.authCacheInvalidator == nil {
		return
	}
	// Authentication cache entries include funding data, so a nonzero
	// registration credit requests their eviction after the grant mutation
	// succeeds.
	s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
}

// GenerateRandomCode returns sixteen uppercase hexadecimal characters from
// eight cryptographic random bytes.
//
// With the repository's Go toolchain, rand.Read fills its buffer or
// terminates the process when entropy reading fails.
func (s *PromoService) GenerateRandomCode() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(bytes)), nil
}

// Create persists an active code using supplied administrator grant controls.
//
// It trims and uppercases code text, generating a random code when empty. The
// returned code retains the input expiration pointer.
func (s *PromoService) Create(ctx context.Context, input *CreatePromoCodeInput) (*PromoCode, error) {
	code := strings.TrimSpace(input.Code)
	// A missing administrator code generates a concrete candidate that the
	// database checks for uniqueness when the new promotion is saved.
	if code == "" {
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

// GetByID returns the registration Promo controls for the requested stored
// identifier.
//
// An absent code returns the repository's missing-code error; other read
// failures propagate to the administrator caller.
func (s *PromoService) GetByID(ctx context.Context, id int64) (*PromoCode, error) {
	code, err := s.promoRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return code, nil
}

// Update replaces the supplied administrator fields on a loaded Promo record
// and persists it.
//
// Nil input pointers retain their corresponding stored controls. Code text is
// trimmed and uppercased; a nonnil zero expiration time requests clearing,
// and this method also replaces input.ExpiresAt with nil. A nonzero deadline
// remains shared with the input pointer. The repository writes editable
// controls while redemption's additive counter remains owned by the grant
// path.
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
			// Convert the supplied zero-time sentinel into the nil deadline expected
			// by repository clearing, and expose that normalization on the input
			// object.
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

// Delete requests hard deletion of a registration Promo and its
// database-cascaded usage receipts.
//
// An absent identifier can succeed with zero matches, while storage errors
// are wrapped for the caller.
func (s *PromoService) Delete(ctx context.Context, id int64) error {
	if err := s.promoRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete promo code: %w", err)
	}
	return nil
}

// List returns grant controls matching the supplied status and code-text
// search with pagination.
//
// Empty filters leave those criteria unrestricted, and repository sorting and
// effective-limit rules apply.
func (s *PromoService) List(ctx context.Context, params pagination.PaginationParams, status, search string) ([]PromoCode, *pagination.PaginationResult, error) {
	return s.promoRepo.ListWithFilters(ctx, params, status, search)
}

// ListUsages returns recipient receipts and pagination metadata for the
// supplied registration Promo identifier.
//
// The repository loads recipient details and returns an empty page when that
// identifier has no matching receipts.
func (s *PromoService) ListUsages(ctx context.Context, promoCodeID int64, params pagination.PaginationParams) ([]PromoCodeUsage, *pagination.PaginationResult, error) {
	return s.promoRepo.ListUsagesByPromoCode(ctx, promoCodeID, params)
}

// background passes named cleanup work to the injected runner or calls it
// synchronously when absent.
//
// A supplied runner controls admission and timing, and this errorless
// callback exposes no acceptance result.
func (s *PromoService) background(name string, fn func()) {
	if s.runtime.Background != nil {
		s.runtime.Background(name, fn)
	} else {
		fn()
	}
}

// PreviewRegistrationPromotion returns a public availability result for an
// optional submitted code.
//
// Recognized error values map to their public reasons; other failures and
// empty validation results map to generic invalid.
func (s *PromoService) PreviewRegistrationPromotion(ctx context.Context, code string) RegistrationPromotionPreview {
	v, e := s.ValidatePromoCode(ctx, code)
	if e != nil {
		// Expose one generic reason for unrecognized storage or eligibility
		// failures, while recognized service error values retain their specific
		// public reason.
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
		// Validation treats an empty code as optional signup input, but the preview
		// requires a concrete code and reports it as invalid.
		return RegistrationPromotionPreview{ErrorCode: "PROMO_CODE_INVALID"}
	}
	return RegistrationPromotionPreview{Valid: true, BonusAmount: v.BonusAmount}
}
