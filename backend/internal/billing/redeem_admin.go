package billing

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// redeem_admin.go generates and edits redemption credentials and records
// administrator adjustment history.
// Administrator credential changes need to retain the meaning of benefits
// already granted. Usage records store recipient/time rather than value and
// plan snapshots, so editing a used credential's entitlement would reinterpret
// its historical grants. Locked single-code mutation therefore rejects those
// value/plan changes while allowing compatible capacity and expiry changes.
//
// Generation creates records sequentially, and batch deletion records partial
// success while skipping failures. Adjustment recording uses a distinct
// transaction callback to create one already-used credential plus its usage
// row; the actual fund or concurrency change is performed by its caller.
// List/history reads retain their repository filter and current-value rules.

// RedeemAdminTransactions supplies code mutation, adjustment-history
// transaction and plan-existence operations.
type RedeemAdminTransactions interface {
	// Mutation runs a code-edit callback in its selected transaction context. The
	// PostgreSQL adapter reuses an existing Ent transaction or owns a new one
	// when configured, while callers keep callback writes on that context.
	Mutation(context.Context, func(context.Context) error) error
	// Adjustment runs the linked credential/usage history callback using its
	// selected context. The PostgreSQL adapter starts a separate transaction
	// through its configured client, and the caller keeps the actual entitlement
	// mutation outside this history operation.
	Adjustment(context.Context, func(context.Context) error) error
	// PlanExists verifies a referenced subscription plan through the selected
	// persistence context. Absence and other lookup failures are returned for the
	// administration caller to reject the requested grant term.
	PlanExists(context.Context, int64) error
}

// RedeemAdmin manages credentials and records administrator entitlement-change
// history through injected persistence.
type RedeemAdmin struct {
	redeemCodeRepo RedeemCodeRepository    // credential and history persistence reader
	transactions   RedeemAdminTransactions // administration transaction and plan operations
	// now supplies the shared receipt timestamp in RecordAdjustment. That caller
	// needs a callable clock; other administrator operations do not read it.
	now func() time.Time // adjustment history observation clock
}

// GenerateRedeemCodesInput supplies credential text, quantity and entitlement
// terms for administration.
type GenerateRedeemCodesInput struct {
	// RequiresPayment requests a paid-order history check before granting this
	// credential. Invitation generation rejects true because those codes belong
	// to registration eligibility.
	RequiresPayment bool // paid-order grant eligibility requirement
	// Code and Count govern custom generation together. A trimmed nonempty Code
	// requires exactly one result, while an empty value asks the service to
	// generate random credentials.
	Code  string  // optional supplied credential text
	Count int     // requested generated credential count
	Type  string  // requested credential entitlement type
	Value float64 // requested entitlement value amount
	// MaxUses defaults to one when omitted and accepts zero as unlimited.
	// Invitation creation overrides supplied capacity to one regardless of this
	// pointer.
	MaxUses   *int       // optional lifetime use capacity
	ExpiresAt *time.Time // optional credential expiry timestamp
	// PlanID is required and verified for subscription generation. Other
	// credential types omit it from the generated record even when this input
	// pointer is supplied.
	PlanID *int64 // optional subscription grant plan
}

// UpdateRedeemCodeInput supplies optional single-code term replacements with
// explicit expiry presence.
type UpdateRedeemCodeInput struct {
	// RequiresPayment retains the current flag when nil. A supplied true value is
	// rejected for invitations before the single-code update is persisted.
	RequiresPayment *bool // optional payment condition replacement
	// Value is applied when supplied for balance or concurrency credentials and
	// is rejected after positive UsedCount. Concurrency replacements must be
	// nonzero integer amounts.
	Value *float64 // optional entitlement amount replacement
	// MaxUses retains capacity when nil, accepts zero as unlimited and rejects
	// negatives. A supplied positive cap cannot fall below UsedCount; invitations
	// are then forced to one.
	MaxUses *int // optional lifetime capacity replacement
	// ExpiresAt and ExpiresAtSet distinguish omission from clearing or
	// replacement. A false presence flag retains expiry; a true flag with nil
	// timestamp clears it.
	ExpiresAt    *time.Time // optional replacement expiry timestamp
	ExpiresAtSet bool       // expiry replacement presence flag
	// PlanID replaces a subscription plan when supplied, requiring a positive
	// existing plan and zero UsedCount. Other types reject this plan
	// replacement.
	PlanID *int64 // optional subscription plan replacement
}

// NewRedeemAdmin binds repository, transaction support and a clock without
// creating records. Repository operations require a usable repo; subscription
// checks and mutations require transactions, while RecordAdjustment requires
// the supplied clock to be callable.
func NewRedeemAdmin(repo RedeemCodeRepository, transactions RedeemAdminTransactions, now func() time.Time) *RedeemAdmin {
	return &RedeemAdmin{redeemCodeRepo: repo, transactions: transactions, now: now}
}

// ListRedeemCodes forwards pagination, type/status/search and sorting to the
// repository and returns its total. The repository must supply page metadata
// on success; filter interpretation and deterministic ordering remain with
// that adapter.
func (s *RedeemAdmin) ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]RedeemCode, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	codes, result, err := s.redeemCodeRepo.ListWithFilters(ctx, params, codeType, status, search)
	if err != nil {
		return nil, 0, err
	}
	return codes, result.Total, nil
}

// GetRedeemCode delegates an administrator read of the supplied code record
// ID.
func (s *RedeemAdmin) GetRedeemCode(ctx context.Context, id int64) (*RedeemCode, error) {
	return s.redeemCodeRepo.GetByID(ctx, id)
}

// GenerateRedeemCodes creates supplied-count credentials sequentially and
// returns their saved records. The caller supplies a non-nil input, a
// nonnegative bounded count and supported type-specific value terms. Custom
// nonempty text is trimmed, capped at thirty-two code points and requires
// count one.
//
// Capacity defaults to one; supplied zero is unlimited, while invitations
// force one and reject payment requirements. Subscription codes require an
// existing positive plan ID. Each record is created separately, so a later
// failure returns an error while earlier successful creates can remain stored;
// this method does not establish one batch transaction.
func (s *RedeemAdmin) GenerateRedeemCodes(ctx context.Context, input *GenerateRedeemCodesInput) ([]RedeemCode, error) {
	if input.Type == RedeemTypeInvitation && input.RequiresPayment {
		return nil, ErrRedeemPaymentRequirementUnsupported
	}
	maxUses := 1
	if input.MaxUses != nil {
		if *input.MaxUses < 0 {
			return nil, apperror.BadRequest("REDEEM_CODE_MAX_USES_INVALID", "max_uses must be greater than or equal to 0")
		}
		maxUses = *input.MaxUses
	}

	customCode := strings.TrimSpace(input.Code)
	if customCode != "" {
		if input.Count != 1 {
			return nil, apperror.BadRequest("REDEEM_CODE_CUSTOM_COUNT_INVALID", "count must be 1 when code is provided")
		}
		// Credential length follows the database/code-point limit, so
		// supplementary-plane Han characters count once alongside ordinary Han and
		// Latin characters.
		if utf8.RuneCountInString(customCode) > 32 {
			return nil, apperror.BadRequest("REDEEM_CODE_TOO_LONG", "code must be at most 32 characters")
		}
	}

	// Subscription credentials need a positive existing plan before any record is
	// created, so every generated code can reference a usable grant template.
	if input.Type == RedeemTypeSubscription {
		if input.PlanID == nil || *input.PlanID <= 0 {
			return nil, apperror.BadRequest("REDEEM_CODE_PLAN_REQUIRED", "plan_id is required for subscription type")
		}
		if err := s.transactions.PlanExists(ctx, *input.PlanID); err != nil {
			return nil, fmt.Errorf("plan not found: %w", err)
		}
	}
	if input.Type == RedeemTypeInvitation {
		maxUses = 1
	}

	codes := make([]RedeemCode, 0, input.Count)
	for range input.Count {
		codeValue := customCode
		// No custom credential text remains after trimming; generate a separate
		// random value for this record before its independent persistence attempt.
		if codeValue == "" {
			generatedCode, err := GenerateRedeemCode()
			if err != nil {
				return nil, err
			}
			codeValue = generatedCode
		}
		code := RedeemCode{
			RequiresPayment: input.RequiresPayment,
			Code:            codeValue,
			Type:            input.Type,
			Value:           input.Value,
			Status:          StatusUnused,
			MaxUses:         maxUses,
			ExpiresAt:       input.ExpiresAt,
		}
		if input.Type == RedeemTypeSubscription {
			code.PlanID = input.PlanID
		}
		if err := s.redeemCodeRepo.Create(ctx, &code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// DeleteRedeemCode reads the target and rejects any positive recorded use
// count before deleting. Read/delete errors propagate, while eligibility is
// checked before the repository delete rather than under a new transaction in
// this method.
func (s *RedeemAdmin) DeleteRedeemCode(ctx context.Context, id int64) error {
	code, err := s.redeemCodeRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if !code.CanDelete() {
		return apperror.Conflict("REDEEM_CODE_DELETE_USED", "cannot delete redeem code that has usage records")
	}
	return s.redeemCodeRepo.Delete(ctx, id)
}

// BatchDeleteRedeemCodes attempts supplied IDs in order and counts successful
// eligible deletes. Missing/unreadable or already-used records and delete
// errors are skipped; the method returns its partial count with nil error and
// opens no aggregate transaction.
func (s *RedeemAdmin) BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error) {
	var deleted int64
	for _, id := range ids {
		code, err := s.redeemCodeRepo.GetByID(ctx, id)
		// Batch deletion skips unreadable or used records so other supplied IDs can
		// still be attempted and the caller receives the count of successes.
		if err != nil || !code.CanDelete() {
			continue
		}
		// Count only successful delete calls; a failure for one eligible code
		// remains omitted from the batch result while later IDs continue.
		if err := s.redeemCodeRepo.Delete(ctx, id); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

// ExpireRedeemCode reads the credential, marks its status explicitly expired
// and persists that snapshot. It returns the updated object on successful
// write; this method performs no locked transaction around the read/update
// sequence.
func (s *RedeemAdmin) ExpireRedeemCode(ctx context.Context, id int64) (*RedeemCode, error) {
	code, err := s.redeemCodeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	code.Status = StatusExpired
	if err := s.redeemCodeRepo.Update(ctx, code); err != nil {
		return nil, err
	}
	return code, nil
}

// GetUserBalanceHistory returns the user's filtered grant/adjustment page,
// total and positive balance-history sum. The sum query remains independent of
// the type filter and differs from users.total_recharged; a failure in either
// query returns an error.
func (s *RedeemAdmin) GetUserBalanceHistory(ctx context.Context, userID int64, page, pageSize int, codeType string) ([]RedeemCode, int64, float64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	codes, result, err := s.redeemCodeRepo.ListByUserPaginated(ctx, userID, params, codeType)
	if err != nil {
		return nil, 0, 0, err
	}
	// The positive balance-history sum covers its own balance types, so filtering
	// the displayed page by another entitlement type does not narrow that total.
	totalRecharged, err := s.redeemCodeRepo.SumPositiveBalanceByUser(ctx, userID)
	if err != nil {
		return nil, 0, 0, err
	}
	return codes, result.Total, totalRecharged, nil
}

// UpdateRedeemCode changes eligible credential terms inside the supplied
// mutation transaction, then rereads after its success. Nil input is rejected;
// system record types stay immutable, and supplied positive capacity cannot
// fall below the completed-use count.
//
// Value or plan changes are rejected after any recorded use because usage
// history retains no amount/plan snapshot. Expiry presence distinguishes
// clearing from omission. Invitations retain one use, and clearing or moving a
// manual expiry can reactivate its capacity-derived status. The adapter
// determines owned versus reused transaction scope; a later response reread
// can fail after persistence succeeded.
func (s *RedeemAdmin) UpdateRedeemCode(ctx context.Context, id int64, input *UpdateRedeemCodeInput) (*RedeemCode, error) {
	if input == nil {
		return nil, apperror.BadRequest("REDEEM_CODE_UPDATE_REQUIRED", "update payload is required")
	}
	err := s.transactions.Mutation(ctx, func(opCtx context.Context) error {
		code, err := s.redeemCodeRepo.GetByIDForUpdate(opCtx, id)
		if err != nil {
			return err
		}
		if !isEditableRedeemCodeType(code.Type) {
			return apperror.Conflict("REDEEM_CODE_SYSTEM_RECORD", "system redeem records cannot be updated")
		}

		if input.RequiresPayment != nil {
			if code.Type == RedeemTypeInvitation && *input.RequiresPayment {
				return ErrRedeemPaymentRequirementUnsupported
			}
			code.RequiresPayment = *input.RequiresPayment
		}

		if input.MaxUses != nil {
			if *input.MaxUses < 0 {
				return apperror.BadRequest("REDEEM_CODE_MAX_USES_INVALID", "max_uses must be greater than or equal to 0")
			}
			if *input.MaxUses > 0 && *input.MaxUses < code.UsedCount {
				return apperror.BadRequest("REDEEM_CODE_MAX_USES_BELOW_USED", "max_uses cannot be less than used_count")
			}
			code.MaxUses = *input.MaxUses
		}

		if input.ExpiresAtSet {
			code.ExpiresAt = input.ExpiresAt
		}

		if input.Value != nil || input.PlanID != nil {
			// Individual usage rows retain recipient/time but no value/plan snapshot;
			// reject edits after recorded use so history continues to describe the
			// granted entitlement.
			if code.UsedCount > 0 {
				return apperror.Conflict("REDEEM_CODE_VALUE_LOCKED", "value or plan cannot be updated after the code has been redeemed")
			}
			if err := s.applyRedeemCodeValueUpdate(opCtx, code, input); err != nil {
				return err
			}
		}

		if code.Type == RedeemTypeInvitation {
			code.MaxUses = 1
		}
		if code.Status == StatusExpired && !code.IsNaturallyExpired() {
			// The current expiry state permits use again; remove the manual expired
			// label before deriving remaining or exhausted status from its retained use
			// count.
			code.Status = StatusUnused
		}
		code.Status = code.PersistedStatus()
		return s.redeemCodeRepo.Update(opCtx, code)
	})
	if err != nil {
		return nil, err
	}
	return s.redeemCodeRepo.GetByID(ctx, id)
}

// applyRedeemCodeValueUpdate mutates value/plan fields on a non-nil loaded
// code according to its type. Callers establish unused-code eligibility first;
// balance rejects plan inputs, concurrency requires a nonzero integer value,
// subscription rejects value replacement and verifies supplied positive plan
// IDs, and invitations reject either change.
func (s *RedeemAdmin) applyRedeemCodeValueUpdate(ctx context.Context, code *RedeemCode, input *UpdateRedeemCodeInput) error {
	switch code.Type {
	case RedeemTypeBalance:
		if input.PlanID != nil {
			return apperror.BadRequest("REDEEM_CODE_PLAN_UNSUPPORTED", "plan_id is only supported for subscription redeem codes")
		}
		if input.Value != nil {
			code.Value = *input.Value
		}
	case RedeemTypeConcurrency:
		if input.PlanID != nil {
			return apperror.BadRequest("REDEEM_CODE_PLAN_UNSUPPORTED", "plan_id is only supported for subscription redeem codes")
		}
		if input.Value != nil {
			if *input.Value == 0 || *input.Value != float64(int(*input.Value)) {
				return apperror.BadRequest("REDEEM_CODE_VALUE_INVALID", "concurrency value must be a non-zero integer")
			}
			code.Value = *input.Value
		}
	case RedeemTypeSubscription:
		if input.Value != nil {
			return apperror.BadRequest("REDEEM_CODE_VALUE_UNSUPPORTED", "value is not editable for subscription redeem codes")
		}
		if input.PlanID != nil {
			if *input.PlanID <= 0 {
				return apperror.BadRequest("REDEEM_CODE_PLAN_REQUIRED", "plan_id is required for subscription type")
			}
			if err := s.transactions.PlanExists(ctx, *input.PlanID); err != nil {
				return fmt.Errorf("plan not found: %w", err)
			}
			code.PlanID = input.PlanID
		}
	case RedeemTypeInvitation:
		if input.Value != nil || input.PlanID != nil {
			return apperror.BadRequest("REDEEM_CODE_VALUE_UNSUPPORTED", "invitation code value cannot be updated")
		}
	}
	return nil
}

// RecordAdjustment writes an already-used credential and its individual user
// usage record through Adjustment. The caller supplies the actual entitlement
// delta, record type and reason; this operation records history rather than
// applying that entitlement change.
//
// A random credential identifies the record, and the configured clock supplies
// one shared use timestamp. Creating the code precedes usage creation
// so its generated ID can be referenced. The transaction adapter owns
// completion, and both writes use its callback context to keep the adjustment
// history linked.
func (s *RedeemAdmin) RecordAdjustment(ctx context.Context, userID int64, codeType string, value float64, notes string) error {
	code, err := GenerateRedeemCode()
	if err != nil {
		return fmt.Errorf("generate adjustment redeem code: %w", err)
	}

	usedAt := s.now()
	record := &RedeemCode{
		Code:      code,
		Type:      codeType,
		Value:     value,
		Status:    StatusUsed,
		MaxUses:   1,
		UsedCount: 1,
		UsedBy:    &userID,
		UsedAt:    &usedAt,
		Notes:     notes,
	}

	// Create the adjustment credential before its usage row in the same callback
	// context, so the generated credential ID links both history records.
	return s.transactions.Adjustment(ctx, func(txCtx context.Context) error {
		if err := s.redeemCodeRepo.Create(txCtx, record); err != nil {
			return fmt.Errorf("create adjustment redeem code: %w", err)
		}
		if err := s.redeemCodeRepo.CreateUsage(txCtx, &RedeemCodeUsage{
			RedeemCodeID: record.ID,
			UserID:       userID,
			UsedAt:       usedAt,
		}); err != nil {
			return fmt.Errorf("create adjustment redeem usage: %w", err)
		}
		return nil
	})
}

// isEditableRedeemCodeType reports supported credential types eligible for
// single-code term editing.
func isEditableRedeemCodeType(codeType string) bool {
	switch codeType {
	case RedeemTypeBalance, RedeemTypeConcurrency, RedeemTypeSubscription, RedeemTypeInvitation:
		return true
	default:
		return false
	}
}
