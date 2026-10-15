package site

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// ports.go defines announcement persistence operations and user observations
// required for targeting.
// Announcement writes validate copy, state, notification mode, schedule, and
// targeting before storage. User-facing eligibility additionally reads account
// balance and active plan identifiers through focused adapters, while a separate
// repository records each user's first read timestamp.

var (
	// Input errors identify the rejected announcement field so administrator
	// handlers can report validation failures before any repository write is attempted.
	ErrAnnouncementNilInput        = infraerrors.BadRequest("ANNOUNCEMENT_INPUT_REQUIRED", "announcement input is required")
	ErrAnnouncementInvalidTitle    = infraerrors.BadRequest("ANNOUNCEMENT_TITLE_INVALID", "announcement title is invalid")
	ErrAnnouncementContentRequired = infraerrors.BadRequest(
		"ANNOUNCEMENT_CONTENT_REQUIRED",
		"announcement content is required",
	)
	ErrAnnouncementInvalidStatus     = infraerrors.BadRequest("ANNOUNCEMENT_STATUS_INVALID", "announcement status is invalid")
	ErrAnnouncementInvalidNotifyMode = infraerrors.BadRequest(
		"ANNOUNCEMENT_NOTIFY_MODE_INVALID",
		"announcement notify_mode is invalid",
	)
	ErrAnnouncementInvalidSchedule = infraerrors.BadRequest(
		"ANNOUNCEMENT_TIME_RANGE_INVALID",
		"starts_at must be before ends_at",
	)
)

// AnnouncementListFilters selects publication states and source text for administrator queries.
type AnnouncementListFilters struct {
	Status string // optional publication state
	Search string // source text search
}

// AnnouncementRepository persists records for filtered administrator queries and
// scheduled user reads.
type AnnouncementRepository interface {
	// Create persists an announcement and fills its assigned identifier and timestamps.
	// The service has prepared copy, targeting, and scheduling before calling;
	// repository failures leave the caller responsible for reporting the failed creation.
	Create(ctx context.Context, a *Announcement) error
	// GetByID returns the announcement identified by its persistent record ID.
	// Missing rows return ErrAnnouncementNotFound in the PostgreSQL adapter, while
	// other query failures propagate so callers can distinguish absence from a
	// database failure.
	GetByID(ctx context.Context, id int64) (*Announcement, error)
	// Update saves the complete announcement when its stored UpdatedAt matches
	// the supplied value. The PostgreSQL adapter returns locale.ErrConflict when
	// that conditional update finds no row, including a missing record or a
	// changed timestamp. Nil schedule and actor pointers clear their stored fields.
	//
	// Callers retain UpdatedAt from the fetched record while editing: the copy's
	// localization revision alone cannot select the writable row because
	// publication and schedule edits share that record. Success copies the
	// persisted update timestamp into the caller's UpdatedAt for another edit.
	Update(ctx context.Context, a *Announcement) error
	// Delete removes the announcement selected by its persistent record identifier.
	Delete(ctx context.Context, id int64) error
	// ArchiveExpired changes active records with an end time at or before now
	// to archived and returns the affected count. Drafts, archived records, and
	// absent or future end times remain unchanged; start time does not
	// participate in this transition.
	//
	// User queries exclude a record at its exact end instant, so archiving uses
	// an inclusive end comparison. Callers supply the scan time and decide
	// whether a failure aborts a read or is logged for later scanning.
	ArchiveExpired(ctx context.Context, now time.Time) (int64, error)

	// List returns an administrator page using the supplied filters and pagination.
	// The service archives expired records before invoking this query, so repository
	// failures prevent the caller from presenting a successful current-state page.
	List(ctx context.Context, params pagination.PaginationParams, filters AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error)
	// ListActive returns active announcements whose half-open schedule includes
	// now. Missing endpoints leave their side unbounded, a present start is
	// inclusive, and a present end excludes the record at that instant.
	//
	// The PostgreSQL adapter caps these schedule candidates at two hundred,
	// ordered by descending ID. The service checks each candidate's targeting
	// afterward, so records outside that window cannot become visible through
	// this query even when their balance and subscription rules would match.
	ListActive(ctx context.Context, now time.Time) ([]Announcement, error)
}

// AnnouncementReadRepository stores first-read markers and supplies keyed timestamp views for display.
type AnnouncementReadRepository interface {
	// MarkRead stores the first read timestamp for this announcement and user pair.
	// Repeated writes keep the existing marker in the PostgreSQL adapter, allowing
	// retries to acknowledge the read without changing its first-observed time.
	MarkRead(ctx context.Context, announcementID, userID int64, readAt time.Time) error
	// GetReadMapByUser returns timestamps keyed by announcement for the selected user.
	// Missing markers are absent from the map, and an empty announcement list
	// produces an empty map so callers can identify unread entries by membership.
	GetReadMapByUser(ctx context.Context, userID int64, announcementIDs []int64) (map[int64]time.Time, error)
	// GetReadMapByUsers returns timestamps keyed by user for the selected announcement.
	// Users with no marker remain absent, and empty input yields an empty map
	// for administrator read-status pages with no selected users.
	GetReadMapByUsers(ctx context.Context, announcementID int64, userIDs []int64) (map[int64]time.Time, error)
	// CountByAnnouncementID counts user read markers stored for this announcement identifier.
	CountByAnnouncementID(ctx context.Context, announcementID int64) (int64, error)
}

// UserSnapshot contains the user fields needed for announcement display and eligibility checks.
type UserSnapshot struct {
	ID       int64   // persistent user identifier
	Email    string  // account email address
	Username string  // account display name
	Balance  float64 // current account balance
}
type (
	// UserListFilters supplies user search text for administrator announcement read-status queries.
	UserListFilters struct {
		Search string // administrator user search
	}
	// SubscriptionSnapshot identifies the plan of an active subscription used by targeting.
	SubscriptionSnapshot struct {
		PlanID int64 // active subscription plan
	}
)

// UserReader supplies user snapshots through an adapter wired by app.
type UserReader interface {
	// GetByID supplies current identity and balance for one announcement reader.
	GetByID(context.Context, int64) (*UserSnapshot, error)
	// ListWithFilters returns user snapshots and pagination for administrator read-status inspection.
	ListWithFilters(context.Context, pagination.PaginationParams, UserListFilters) ([]UserSnapshot, *pagination.PaginationResult, error)
}

// SubscriptionReader supplies active subscription snapshots through an adapter wired by app.
type SubscriptionReader interface {
	// ListActiveByUserID returns active subscription plans used to evaluate reader targeting.
	ListActiveByUserID(context.Context, int64) ([]SubscriptionSnapshot, error)
}
