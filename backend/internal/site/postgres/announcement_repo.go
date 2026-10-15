package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/announcement"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// announcement_repo.go persists announcements and queries publication
// candidates and administrator lists.
// Published announcement candidates require active status and a half-open
// schedule containing the supplied time. Administrator queries use separate
// status and source-text filters, with a selected sort field followed by ID
// in the same direction to keep equal-field records consistently ordered.
// The active candidate list is capped before service-level user targeting.
//
// Updates compare fetched UpdatedAt with storage, so a timestamp mismatch rejects
// the write even when the content revision alone appears usable. Creation fills
// identity and timestamps; successful updates copy persisted UpdatedAt back into
// the supplied record. Write methods select an Ent transaction client from
// context when present, while queries execute through the adapter's bound client.

// announcementRepository persists domain announcements through Ent queries and conditional record updates.
type announcementRepository struct {
	// client is the app-supplied Ent client shared by queries and nontransactional
	// writes; write helpers prefer the transaction client carried in their context.
	client *dbent.Client
}

// NewAnnouncementRepository binds the Ent client used by announcement persistence and queries.
// The adapter retains the client and leaves its lifecycle to the app.
// Context transaction selection occurs in write methods when they are invoked.
func NewAnnouncementRepository(client *dbent.Client) site.AnnouncementRepository {
	return &announcementRepository{client: client}
}

// Create stores the supplied announcement and fills its assigned ID and timestamps.
// It uses a context transaction when present and retains the caller's other
// fields, including nested targeting and localization values, after the successful save.
func (r *announcementRepository) Create(ctx context.Context, a *site.Announcement) error {
	client := clientFromContext(ctx, r.client)
	builder := client.Announcement.Create().
		SetTitle(a.Title).
		SetContent(a.Content).
		SetLocalization(a.Localization).
		SetStatus(a.Status).
		SetNotifyMode(a.NotifyMode).
		SetTargeting(a.Targeting)

	if a.StartsAt != nil {
		builder.SetStartsAt(*a.StartsAt)
	}
	if a.EndsAt != nil {
		builder.SetEndsAt(*a.EndsAt)
	}
	if a.CreatedBy != nil {
		builder.SetCreatedBy(*a.CreatedBy)
	}
	if a.UpdatedBy != nil {
		builder.SetUpdatedBy(*a.UpdatedBy)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return err
	}

	applyAnnouncementEntityToService(a, created)
	return nil
}

// GetByID queries one announcement through the adapter's bound Ent client.
// Missing rows become ErrAnnouncementNotFound. Entity conversion returns the
// stored nested values and pointer fields from the queried entity. The
// converter allocates the outer domain struct and assigns those nested values directly.
func (r *announcementRepository) GetByID(ctx context.Context, id int64) (*site.Announcement, error) {
	m, err := r.client.Announcement.Query().
		Where(announcement.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, announcementPersistenceError(err)
	}
	return announcementEntityToService(m), nil
}

// Update saves complete record a when stored UpdatedAt matches a.UpdatedAt.
// A missing record or timestamp mismatch returns locale.ErrConflict. Nil schedule
// and audit pointers clear their columns, while other stored fields are replaced
// with the supplied record values.
//
// The ID/timestamp predicate and field replacement belong to the same SQL
// update, so the timestamp is checked as part of the write. Callers keep a and
// its nested data unchanged until the operation finishes. Success copies the
// timestamp returned by Ent into a.UpdatedAt for later edits.
func (r *announcementRepository) Update(ctx context.Context, a *site.Announcement) error {
	client := clientFromContext(ctx, r.client)
	// Match both the record ID and fetched timestamp. A changed persisted timestamp
	// makes this update find no row, protecting the content and publication fields
	// saved after this editing copy was fetched.
	builder := client.Announcement.UpdateOneID(a.ID).
		Where(announcement.UpdatedAtEQ(a.UpdatedAt)).
		SetTitle(a.Title).
		SetContent(a.Content).
		SetLocalization(a.Localization).
		SetStatus(a.Status).
		SetNotifyMode(a.NotifyMode).
		SetTargeting(a.Targeting)

	if a.StartsAt != nil {
		builder.SetStartsAt(*a.StartsAt)
	} else {
		builder.ClearStartsAt()
	}
	if a.EndsAt != nil {
		builder.SetEndsAt(*a.EndsAt)
	} else {
		builder.ClearEndsAt()
	}
	if a.CreatedBy != nil {
		builder.SetCreatedBy(*a.CreatedBy)
	} else {
		builder.ClearCreatedBy()
	}
	if a.UpdatedBy != nil {
		builder.SetUpdatedBy(*a.UpdatedBy)
	} else {
		builder.ClearUpdatedBy()
	}

	updated, err := builder.Save(ctx)
	if dbent.IsNotFound(err) {
		return locale.ErrConflict
	}
	if err != nil {
		return announcementPersistenceError(err)
	}

	a.UpdatedAt = updated.UpdatedAt
	return nil
}

// Delete removes the selected record using the current context's write client.
// A context transaction participates in the deletion when present; the affected
// row count is discarded, so deleting a missing ID returns the query result.
func (r *announcementRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.Announcement.Delete().Where(announcement.IDEQ(id)).Exec(ctx)
	return err
}

// ArchiveExpired performs one bulk update of active rows whose nonnil EndsAt
// is at or before now. It returns the affected count and query error using
// the context transaction client when present.
//
// Status and end-time predicates belong to the same update that sets archived,
// keeping selection and publication transition in one database operation.
// StartsAt and targeting do not participate; drafts, absent ends, and future
// ends keep their existing state. The inclusive end comparison matches the
// instant at which ListActive excludes an ended announcement.
func (r *announcementRepository) ArchiveExpired(ctx context.Context, now time.Time) (int64, error) {
	client := clientFromContext(ctx, r.client)
	updated, err := client.Announcement.Update().
		Where(
			announcement.StatusEQ(site.AnnouncementStatusActive),
			announcement.EndsAtNotNil(),
			announcement.EndsAtLTE(now),
		).
		SetStatus(site.AnnouncementStatusArchived).
		Save(ctx)
	return int64(updated), err
}

// List returns a filtered administrator page and its total through the bound client.
// Status matches exactly and source title/content search is case-insensitive.
// Count and page are separate queries, with normalized ordering and ID ties
// applied before domain conversion.
func (r *announcementRepository) List(
	ctx context.Context,
	params pagination.PaginationParams,
	filters site.AnnouncementListFilters,
) ([]site.Announcement, *pagination.PaginationResult, error) {
	q := r.client.Announcement.Query()

	if filters.Status != "" {
		q = q.Where(announcement.StatusEQ(filters.Status))
	}
	if filters.Search != "" {
		q = q.Where(
			announcement.Or(
				announcement.TitleContainsFold(filters.Search),
				announcement.ContentContainsFold(filters.Search),
			),
		)
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	itemsQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range announcementListOrders(params) {
		itemsQuery = itemsQuery.Order(order)
	}

	items, err := itemsQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := announcementEntitiesToService(items)
	return out, pagination.ResultFromTotal(int64(total), params), nil
}

// announcementListOrder returns a supported storage field and normalized sort direction.
// The selected field name is trimmed and lowercased, and direction normalization
// uses descending as its default. Empty sortBy selects created_at using that
// direction, just like the named created_at selection.
//
// An unsupported field instead selects created_at and forces descending order,
// even if the requested direction was valid. This distinction keeps unknown
// field names from becoming query column names while retaining direction
// choices for an omitted or recognized sort field.
func announcementListOrder(params pagination.PaginationParams) (string, string) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	switch sortBy {
	case "title":
		return announcement.FieldTitle, sortOrder
	case "status":
		return announcement.FieldStatus, sortOrder
	case "notify_mode":
		return announcement.FieldNotifyMode, sortOrder
	case "starts_at":
		return announcement.FieldStartsAt, sortOrder
	case "ends_at":
		return announcement.FieldEndsAt, sortOrder
	case "id":
		return announcement.FieldID, sortOrder
	case "", "created_at":
		return announcement.FieldCreatedAt, sortOrder
	default:
		// Unknown columns use a complete default ordering, while an omitted
		// recognized default field above can still retain the caller's normalized direction.
		return announcement.FieldCreatedAt, pagination.SortOrderDesc
	}
}

// announcementListOrders returns Ent ordering terms with a same-direction ID tie breaker.
// ID sorting needs one term. Other supported fields append ID so records
// with equal primary values have a consistent order during offset pagination.
func announcementListOrders(params pagination.PaginationParams) []func(*entsql.Selector) {
	field, sortOrder := announcementListOrder(params)

	if sortOrder == pagination.SortOrderAsc {
		if field == announcement.FieldID {
			return []func(*entsql.Selector){
				dbent.Asc(field),
			}
		}
		return []func(*entsql.Selector){
			dbent.Asc(field),
			dbent.Asc(announcement.FieldID),
		}
	}

	if field == announcement.FieldID {
		return []func(*entsql.Selector){
			dbent.Desc(field),
		}
	}
	return []func(*entsql.Selector){
		dbent.Desc(field),
		dbent.Desc(announcement.FieldID),
	}
}

// ListActive returns at most two hundred published schedule candidates, newest ID first.
// Start times are inclusive, end times are exclusive, and missing endpoints
// leave the respective side unbounded. The query uses the adapter's bound client.
//
// Status and both schedule sides are filtered together before the fixed limit.
// The service applies user targeting afterward, so an eligible record beyond
// this candidate window is not included by this call. This method does not
// archive records; persistent expiry transitions belong to the separate archive write.
func (r *announcementRepository) ListActive(ctx context.Context, now time.Time) ([]site.Announcement, error) {
	// Cap schedule candidates before balance and subscription targeting in the
	// service; descending IDs decide which records fit in the two-hundred-record window.
	q := r.client.Announcement.Query().
		Where(
			announcement.StatusEQ(site.AnnouncementStatusActive),
			announcement.Or(announcement.StartsAtIsNil(), announcement.StartsAtLTE(now)),
			announcement.Or(announcement.EndsAtIsNil(), announcement.EndsAtGT(now)),
		).
		Order(dbent.Desc(announcement.FieldID)).
		Limit(200)

	items, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	return announcementEntitiesToService(items), nil
}

// applyAnnouncementEntityToService copies persisted identity and timestamps into the supplied record.
// Nil source or destination values are ignored. Content, schedule, targeting,
// and localization remain the values already held by the caller's domain record.
func applyAnnouncementEntityToService(dst *site.Announcement, src *dbent.Announcement) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

// announcementEntityToService returns a domain record for an Ent announcement or nil.
// Pointer fields and nested localization or targeting storage are assigned as
// held by the entity; callers receive a new outer struct with shared nested values.
func announcementEntityToService(m *dbent.Announcement) *site.Announcement {
	if m == nil {
		return nil
	}
	return &site.Announcement{
		Localization: m.Localization,
		ID:           m.ID,
		Title:        m.Title,
		Content:      m.Content,
		Status:       m.Status,
		NotifyMode:   m.NotifyMode,
		Targeting:    m.Targeting,
		StartsAt:     m.StartsAt,
		EndsAt:       m.EndsAt,
		CreatedBy:    m.CreatedBy,
		UpdatedBy:    m.UpdatedBy,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// announcementEntitiesToService converts non-nil Ent records in their existing query order.
// Nil entries are omitted, and an empty input produces an empty slice.
// Each converted outer value retains its entity's nested pointer and collection values.
func announcementEntitiesToService(models []*dbent.Announcement) []site.Announcement {
	out := make([]site.Announcement, 0, len(models))
	for i := range models {
		if s := announcementEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// clientFromContext returns the context's Ent transaction client when one is present.
// Write methods use fallback for ordinary request contexts, keeping transaction
// ownership with the caller that supplied the transaction context.
func clientFromContext(ctx context.Context, fallback *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	// Ordinary writes have no context transaction to join. The bound client
	// then performs the operation using its normal connection and transaction behavior.
	return fallback
}

// announcementPersistenceError maps missing database rows to ErrAnnouncementNotFound.
// It attaches the storage error as the cause and returns other errors unchanged,
// allowing callers to distinguish missing announcements from database failures.
func announcementPersistenceError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err) {
		return site.ErrAnnouncementNotFound.WithCause(err)
	}
	return err
}
