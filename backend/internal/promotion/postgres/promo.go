package postgres

import (
	"context"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/promocode"
	"github.com/TokenFlux/TokenRouter/ent/promocodeusage"
	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// promo.go persists registration promotion controls and queries their
// redemption receipts.
// Administrator code edits can start from a snapshot read before another user
// redeems the same promotion. Update writes the editable grant controls while
// leaving used_count to its additive redemption update, so saving that
// snapshot cannot restore an earlier count and admit extra uses.
//
// Creation, locked reads, updates, deletion, receipt insertion, and counting
// prefer a context-carried transaction. Ordinary lookups and lists use the
// repository's bound client; the grant mutation binds that client to its
// transaction for consistent reads. Conversion builds service containers
// while retaining optional time pointers, and paged code sorting adds ID in
// the selected direction for equal primary values.

// promoCodeRepository persists registration grant controls and receipts
// through an Ent client.
type promoCodeRepository struct {
	// client is retained for ordinary lookups and lists. Mutation and
	// locked-read methods prefer an Ent transaction from context; callers manage
	// the selected connection's lifetime.
	client *dbent.Client // bound Promo persistence client
}

// NewPromoCodeRepository retains client for registration Promo controls and
// recipient receipts.
//
// The caller owns its lifetime; transaction-sensitive reads bind client to
// tx.Client because ordinary lookups use the bound client.
func NewPromoCodeRepository(client *dbent.Client) promotion.PromoCodeRepository {
	return &promoCodeRepository{client: client}
}

// Create persists a promotion and fills the supplied object's identifier and
// timestamps.
//
// The context transaction takes precedence; the caller supplies grant
// controls, including an optional deadline.
func (r *promoCodeRepository) Create(ctx context.Context, code *promotion.PromoCode) error {
	client := clientFromContext(ctx, r.client)
	builder := client.PromoCode.Create().
		SetCode(code.Code).
		SetBonusAmount(code.BonusAmount).
		SetMaxUses(code.MaxUses).
		SetUsedCount(code.UsedCount).
		SetStatus(code.Status).
		SetNotes(code.Notes)

	if code.ExpiresAt != nil {
		builder.SetExpiresAt(*code.ExpiresAt)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return err
	}

	code.ID = created.ID
	code.CreatedAt = created.CreatedAt
	code.UpdatedAt = created.UpdatedAt
	return nil
}

// GetByID returns the code with the requested identifier through the bound
// client.
//
// An absent row becomes ErrPromoCodeNotFound; a context-carried transaction
// alone does not change this method's selected client.
func (r *promoCodeRepository) GetByID(ctx context.Context, id int64) (*promotion.PromoCode, error) {
	m, err := r.client.PromoCode.Query().
		Where(promocode.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			// Translate Ent's missing row into the shared Promo error so service and
			// HTTP callers can classify an absent code.
			return nil, promotion.ErrPromoCodeNotFound
		}
		return nil, err
	}
	return promoCodeEntityToService(m), nil
}

// GetByCode returns a case-insensitive matching code through the bound
// client.
//
// An absent row becomes ErrPromoCodeNotFound; callers normalize optional
// surrounding whitespace before this query when needed.
func (r *promoCodeRepository) GetByCode(ctx context.Context, code string) (*promotion.PromoCode, error) {
	m, err := r.client.PromoCode.Query().
		Where(promocode.CodeEqualFold(code)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			// Translate an absent text match into the shared Promo error while keeping
			// other storage failures available to signup callers.
			return nil, promotion.ErrPromoCodeNotFound
		}
		return nil, err
	}
	return promoCodeEntityToService(m), nil
}

// GetByCodeForUpdate returns a case-insensitive match with an Ent row-lock
// request.
//
// A context transaction takes precedence; callers keep that transaction open
// when the lock must protect subsequent grant checks.
func (r *promoCodeRepository) GetByCodeForUpdate(ctx context.Context, code string) (*promotion.PromoCode, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.PromoCode.Query().
		Where(promocode.CodeEqualFold(code)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			// Translate a missing locked code into the shared Promo error so the grant
			// transaction exits before validation or credit.
			return nil, promotion.ErrPromoCodeNotFound
		}
		return nil, err
	}
	return promoCodeEntityToService(m), nil
}

// Update writes administrator-editable code controls and refreshes the
// supplied object's update timestamp.
//
// It uses the context transaction when present and clears expiration when the
// supplied deadline is nil. An administrator snapshot can predate a
// concurrent redemption, so used_count is deliberately absent from this write
// while IncrementUsedCount owns its additive change. This keeps grant
// accounting intact when code, amount, limit, status, or notes are saved from
// an earlier read. The supplied UsedCount remains the input snapshot; callers
// read again when they need its current value.
func (r *promoCodeRepository) Update(ctx context.Context, code *promotion.PromoCode) error {
	client := clientFromContext(ctx, r.client)
	// Omit used_count from administrator field replacement because a grant can
	// increment it after the input snapshot was read.
	builder := client.PromoCode.UpdateOneID(code.ID).
		SetCode(code.Code).
		SetBonusAmount(code.BonusAmount).
		SetMaxUses(code.MaxUses).
		SetStatus(code.Status).
		SetNotes(code.Notes)

	if code.ExpiresAt != nil {
		builder.SetExpiresAt(*code.ExpiresAt)
	} else {
		builder.ClearExpiresAt()
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			// Translate Ent's missing update target into the Promo error so
			// administrators receive the same absence classification as reads.
			return promotion.ErrPromoCodeNotFound
		}
		return err
	}

	code.UpdatedAt = updated.UpdatedAt
	return nil
}

// Delete hard-deletes matching Promo rows through the context transaction or
// bound client.
//
// Zero matches succeed. The database's code foreign-key cascade removes
// associated usage receipts with the deleted code.
func (r *promoCodeRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.PromoCode.Delete().Where(promocode.IDEQ(id)).Exec(ctx)
	return err
}

// List returns a page of Promo records using empty status and search filters.
//
// It delegates bound-client reads, pagination, and supported sort
// normalization to ListWithFilters for the actual query.
func (r *promoCodeRepository) List(ctx context.Context, params pagination.PaginationParams) ([]promotion.PromoCode, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, "", "")
}

// ListWithFilters returns code records and their total matching optional
// status and case-insensitive code search.
//
// It uses the bound client; counting and paging are separate queries, and
// pagination uses its effective limit.
func (r *promoCodeRepository) ListWithFilters(ctx context.Context, params pagination.PaginationParams, status, search string) ([]promotion.PromoCode, *pagination.PaginationResult, error) {
	q := r.client.PromoCode.Query()

	if status != "" {
		q = q.Where(promocode.StatusEQ(status))
	}
	if search != "" {
		q = q.Where(promocode.CodeContainsFold(search))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	codesQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range promoCodeListOrder(params) {
		codesQuery = codesQuery.Order(order)
	}

	codes, err := codesQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outCodes := promoCodeEntitiesToService(codes)

	return outCodes, pagination.ResultFromTotal(int64(total), params), nil
}

// promoCodeListOrder returns allowlisted primary ordering followed by a
// same-direction ID ordering.
//
// Unsupported fields select ID; invalid direction text selects descending
// through pagination normalization. The returned function slice is newly
// allocated.
func promoCodeListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	var field string
	switch sortBy {
	case "bonus_amount":
		field = promocode.FieldBonusAmount
	case "status":
		field = promocode.FieldStatus
	case "expires_at":
		field = promocode.FieldExpiresAt
	case "created_at":
		field = promocode.FieldCreatedAt
	case "code":
		field = promocode.FieldCode
	default:
		// Unsupported sort text selects the stored ID column, keeping arbitrary
		// administrator input out of the SQL ordering expression.
		field = promocode.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(promocode.FieldID)}
	}
	return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(promocode.FieldID)}
}

// CreateUsage persists a receipt and assigns its generated ID on the supplied
// object.
//
// The context transaction takes precedence, and database pair uniqueness
// rejects a repeated code/user receipt.
func (r *promoCodeRepository) CreateUsage(ctx context.Context, usage *promotion.PromoCodeUsage) error {
	client := clientFromContext(ctx, r.client)
	created, err := client.PromoCodeUsage.Create().
		SetPromoCodeID(usage.PromoCodeID).
		SetUserID(usage.UserID).
		SetBonusAmount(usage.BonusAmount).
		SetUsedAt(usage.UsedAt).
		Save(ctx)
	if err != nil {
		return err
	}

	usage.ID = created.ID
	return nil
}

// GetUsageByPromoCodeAndUser returns a recipient's receipt for one code
// through the bound client.
//
// An absent pair returns nil successfully; grant callers bind this repository
// to the granting transaction.
func (r *promoCodeRepository) GetUsageByPromoCodeAndUser(ctx context.Context, promoCodeID, userID int64) (*promotion.PromoCodeUsage, error) {
	m, err := r.client.PromoCodeUsage.Query().
		Where(
			promocodeusage.PromoCodeIDEQ(promoCodeID),
			promocodeusage.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			// An absent receipt means this recipient has no recorded use; the locked
			// granting caller may continue eligibility and credit checks.
			return nil, nil
		}
		return nil, err
	}
	return promoCodeUsageEntityToService(m), nil
}

// ListUsagesByPromoCode returns a descending-ID page of receipts with eagerly
// loaded users and a total count.
//
// It uses the bound client; counts and records are separate queries with
// effective pagination limits.
func (r *promoCodeRepository) ListUsagesByPromoCode(ctx context.Context, promoCodeID int64, params pagination.PaginationParams) ([]promotion.PromoCodeUsage, *pagination.PaginationResult, error) {
	q := r.client.PromoCodeUsage.Query().
		Where(promocodeusage.PromoCodeIDEQ(promoCodeID))

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	usages, err := q.
		WithUser().
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(promocodeusage.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outUsages := promoCodeUsageEntitiesToService(usages)

	return outUsages, pagination.ResultFromTotal(int64(total), params), nil
}

// IncrementUsedCount adds one to the matched code's grant count through the
// selected client.
//
// Callers keep receipt insertion and this additive update in the same
// granting transaction; missing rows return Ent errors.
func (r *promoCodeRepository) IncrementUsedCount(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	// Use an additive counter update so redemption counting cannot replace
	// increments already stored by another completed grant transaction.
	_, err := client.PromoCode.UpdateOneID(id).
		AddUsedCount(1).
		Save(ctx)
	return err
}

// Entity-to-service conversions.

// promoCodeEntityToService returns a new grant record or nil for a nil
// entity.
//
// The expiration pointer stays shared, nullable notes become empty text, and
// usage edges are left unloaded.
func promoCodeEntityToService(m *dbent.PromoCode) *promotion.PromoCode {
	if m == nil {
		return nil
	}
	return &promotion.PromoCode{
		ID:          m.ID,
		Code:        m.Code,
		BonusAmount: m.BonusAmount,
		MaxUses:     m.MaxUses,
		UsedCount:   m.UsedCount,
		Status:      m.Status,
		ExpiresAt:   m.ExpiresAt,
		Notes:       promoNote(m.Notes),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// promoCodeEntitiesToService returns a newly allocated nonnil slice of
// converted nonnil code entities.
//
// Nil entries are skipped; the converted records retain each entity's
// optional expiration pointer.
func promoCodeEntitiesToService(models []*dbent.PromoCode) []promotion.PromoCode {
	out := make([]promotion.PromoCode, 0, len(models))
	for i := range models {
		if s := promoCodeEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// promoCodeUsageEntityToService returns a new receipt or nil for a nil
// entity.
//
// An eagerly loaded user is converted; the code edge remains unset, and
// receipt scalar values are copied.
func promoCodeUsageEntityToService(m *dbent.PromoCodeUsage) *promotion.PromoCodeUsage {
	if m == nil {
		return nil
	}
	out := &promotion.PromoCodeUsage{
		ID:          m.ID,
		PromoCodeID: m.PromoCodeID,
		UserID:      m.UserID,
		BonusAmount: m.BonusAmount,
		UsedAt:      m.UsedAt,
	}
	if m.Edges.User != nil {
		out.User = promoUserView(m.Edges.User)
	}
	return out
}

// promoCodeUsageEntitiesToService returns a newly allocated nonnil slice of
// converted nonnil grant receipt entities.
//
// Nil entries are skipped; optional recipient details follow promoUserView's
// retained account-pointer rules.
func promoCodeUsageEntitiesToService(models []*dbent.PromoCodeUsage) []promotion.PromoCodeUsage {
	out := make([]promotion.PromoCodeUsage, 0, len(models))
	for i := range models {
		if s := promoCodeUsageEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// promoNote returns saved administrator text or empty text for a nil pointer.
//
// The service uses a nonnullable string, so absent notes and a saved empty
// note share this representation.
func promoNote(s *string) string {
	if s == nil {
		// Absent nullable notes become the service's empty string, letting API
		// conversion expose one stable text representation for notes.
		return ""
	}
	return *s
}

// promoUserView returns a new recipient view or nil for a nil entity.
//
// Account time and threshold pointers remain shared, notification recipients
// are parsed from saved text, and group-grant slices remain unset.
func promoUserView(u *dbent.User) *promotion.UserView {
	if u == nil {
		return nil
	}
	return &promotion.UserView{
		ID:                         u.ID,
		Email:                      u.Email,
		Username:                   u.Username,
		Role:                       u.Role,
		Balance:                    u.Balance,
		FrozenBalance:              u.FrozenBalance,
		Concurrency:                u.Concurrency,
		Status:                     u.Status,
		LastActiveAt:               u.LastActiveAt,
		CreatedAt:                  u.CreatedAt,
		UpdatedAt:                  u.UpdatedAt,
		DeletedAt:                  u.DeletedAt,
		BalanceNotifyEnabled:       u.BalanceNotifyEnabled,
		BalanceNotifyThresholdType: u.BalanceNotifyThresholdType,
		BalanceNotifyThreshold:     u.BalanceNotifyThreshold,
		BalanceNotifyExtraEmails:   contact.ParseNotifyEmails(u.BalanceNotifyExtraEmails),
		TotalRecharged:             u.TotalRecharged,
		RPMLimit:                   u.RpmLimit,
		APIKeyLimit:                u.APIKeyLimit,
	}
}
