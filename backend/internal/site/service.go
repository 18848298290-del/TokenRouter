package site

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Announcements are published for users whose current balance and active plans
// satisfy their targeting expression within the scheduled interval. User lists
// resolve a title and body together, join first-read markers by announcement ID,
// and place unread entries ahead of previously read entries. Administrator
// read-status views inspect targeting independently of publication scheduling.
//
// Content updates use revisioned localization, while schedule updates retain
// a distinct omission-versus-clear representation. Creating or editing an
// already ended active record archives it before saving. Administrator list
// and detail reads archive expired records before querying, and abort the read
// when that write fails, so an expiry failure is visible to the administrator.

// AnnouncementService validates publication changes and assembles user eligibility and read-status views.
type AnnouncementService struct {
	announcementRepo AnnouncementRepository     // announcement persistence dependency
	readRepo         AnnouncementReadRepository // first-read marker storage
	userRepo         UserReader                 // current user observations
	userSubRepo      SubscriptionReader         // active subscription observations
}

// CreateAnnouncementInput supplies source copy, publication rules, schedule, and creating actor.
type CreateAnnouncementInput struct {
	// Localization prepares a revisioned copy when supplied and overwrites input
	// Title and Content with its source before the base record is validated.
	Localization *locale.Update[AnnouncementCopy]
	Title        string // source announcement heading
	Content      string // source announcement body
	// Status defaults to draft when blank; active records with an end time
	// at or before creation's current time are saved as archived records.
	Status string
	// NotifyMode defaults to silent when blank; any nonblank value must be
	// one of the supported frontend presentation modes before the record is saved.
	NotifyMode string
	Targeting  AnnouncementTargeting // eligible reader expression
	// StartsAt and EndsAt are optional schedule endpoints, with start included
	// and end excluded. When both exist, start must precede end before creation.
	StartsAt *time.Time // inclusive start instant
	EndsAt   *time.Time // exclusive end instant
	// ActorID supplies administrator audit identity; positive values populate
	// both CreatedBy and UpdatedBy, while nil or nonpositive values leave them absent.
	ActorID *int64
}

// UpdateAnnouncementInput supplies selected announcement changes and distinguishes omitted schedule endpoints.
type UpdateAnnouncementInput struct {
	// Localization is required for source-copy edits. When supplied, Update
	// prepares it against the record and sets input Title and Content to nil.
	Localization *locale.Update[AnnouncementCopy]
	// Title and Content are legacy source fields; submitting either without
	// Localization returns a conflict, and submitting Localization clears them before field handling.
	Title   *string // legacy heading input
	Content *string // legacy body input
	// Status, NotifyMode, and Targeting replace their stored values when the
	// corresponding pointer is supplied. Nil pointers leave these publication
	// and eligibility settings unchanged while another field is edited.
	Status     *string                // optional publication update
	NotifyMode *string                // optional presentation update
	Targeting  *AnnouncementTargeting // optional eligibility update
	// StartsAt and EndsAt distinguish three cases: nil preserves the stored
	// endpoint, a pointer to nil clears it, and a pointer to a time replaces it.
	StartsAt **time.Time // start endpoint edit
	EndsAt   **time.Time // end endpoint edit
	// ActorID updates the editing administrator when positive; a nil or
	// nonpositive input leaves the record's current UpdatedBy audit identity in place.
	ActorID *int64
}

// UserAnnouncement pairs an eligible announcement with this user's first-read marker.
type UserAnnouncement struct {
	Announcement Announcement // eligible localized announcement
	// ReadAt is nil for unread announcements; a non-nil timestamp is copied
	// from the read map and places the entry after unread entries during sorting.
	ReadAt *time.Time
}

// AnnouncementUserReadStatus records administrator-visible targeting eligibility and one user's first read.
type AnnouncementUserReadStatus struct {
	UserID   int64   `json:"user_id"`  // persistent user identifier
	Email    string  `json:"email"`    // account email address
	Username string  `json:"username"` // account display name
	Balance  float64 `json:"balance"`  // current account balance
	// Eligible describes targeting alone for this administrator view; publication
	// status and schedule are not included in ListUserReadStatus's eligibility calculation.
	Eligible bool       `json:"eligible"`
	ReadAt   *time.Time `json:"read_at,omitempty"` // first read timestamp
}

// NewAnnouncementService binds announcement storage and focused user, subscription, and read-marker readers.
// Administrator CRUD uses announcementRepo; user-facing and read-status methods
// also require the corresponding readers. Construction performs no queries or startup work.
// @project-doc docs/interfaces/http_api.md#announcement_api
func NewAnnouncementService(
	announcementRepo AnnouncementRepository,
	readRepo AnnouncementReadRepository,
	userRepo UserReader,
	userSubRepo SubscriptionReader,
) *AnnouncementService {
	return &AnnouncementService{
		announcementRepo: announcementRepo,
		readRepo:         readRepo,
		userRepo:         userRepo,
		userSubRepo:      userSubRepo,
	}
}

// Create validates and saves a new announcement, returning the persisted record.
// A nil input is rejected. Blank status and notify mode select draft and
// silent; source fields are trimmed for the base record, and schedule endpoints
// must be strictly ordered when both are supplied.
//
// Supplied localization prepares title and body together and updates those
// fields on input before base validation. Targeting is independently normalized.
// An active record whose end has already arrived becomes archived before the
// repository write, so creation cannot persist a currently ended active state.
// The returned record retains supplied schedule and positive actor pointers.
func (s *AnnouncementService) Create(ctx context.Context, input *CreateAnnouncementInput) (*Announcement, error) {
	if input == nil {
		return nil, ErrAnnouncementNilInput
	}

	localized := locale.Original(AnnouncementCopy{Title: input.Title, Content: input.Content})
	if input.Localization != nil {
		var err error
		localized, err = locale.Prepare(localized, *input.Localization, validateAnnouncementCopy)
		if err != nil {
			return nil, err
		}
		input.Title, input.Content = localized.Source.Title, localized.Source.Content
	}
	title := strings.TrimSpace(input.Title)
	content := strings.TrimSpace(input.Content)
	// Base record titles are limited in bytes after trimming.
	if title == "" || len(title) > 200 {
		return nil, ErrAnnouncementInvalidTitle
	}
	if content == "" {
		return nil, ErrAnnouncementContentRequired
	}

	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = AnnouncementStatusDraft
	}
	if !isValidAnnouncementStatus(status) {
		return nil, ErrAnnouncementInvalidStatus
	}

	targeting, err := input.Targeting.NormalizeAndValidate()
	if err != nil {
		return nil, err
	}

	notifyMode := strings.TrimSpace(input.NotifyMode)
	if notifyMode == "" {
		notifyMode = AnnouncementNotifyModeSilent
	}
	if !isValidAnnouncementNotifyMode(notifyMode) {
		return nil, ErrAnnouncementInvalidNotifyMode
	}

	if input.StartsAt != nil && input.EndsAt != nil {
		if !input.StartsAt.Before(*input.EndsAt) {
			return nil, ErrAnnouncementInvalidSchedule
		}
	}

	a := &Announcement{
		Localization: AnnouncementLocalization(localized),
		Title:        title,
		Content:      content,
		Status:       status,
		NotifyMode:   notifyMode,
		Targeting:    targeting,
		StartsAt:     input.StartsAt,
		EndsAt:       input.EndsAt,
	}
	if input.ActorID != nil && *input.ActorID > 0 {
		a.CreatedBy = input.ActorID
		a.UpdatedBy = input.ActorID
	}
	// Normalize expired publication state before the single creation write;
	// an already elapsed active schedule is stored as archived even before the periodic scanner runs.
	archiveAnnouncementIfExpired(a, time.Now())

	if err := s.announcementRepo.Create(ctx, a); err != nil {
		return nil, fmt.Errorf("create announcement: %w", err)
	}
	return a, nil
}

// Update applies selected fields to a stored announcement and saves the resulting record.
// A nil input is rejected. Localization is prepared against current content;
// direct Title or Content edits without it return a conflict. A supplied
// localization update clears those two pointers on input after accepting its source copy.
//
// Schedule pointer layers distinguish omission, clearing, and replacement before
// validating the resulting interval. Other supplied state fields are validated
// before saving. An elapsed active end archives the result, while the repository
// uses the fetched UpdatedAt value to reject a concurrent record edit.
func (s *AnnouncementService) Update(ctx context.Context, id int64, input *UpdateAnnouncementInput) (*Announcement, error) {
	if input == nil {
		return nil, ErrAnnouncementNilInput
	}

	a, err := s.announcementRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if a.Localization.Revision == 0 {
		// Unversioned records still supply source copy for revision preparation.
		// Initialize from their stored fields so the submitted update can be checked against that copy.
		a.Localization = AnnouncementLocalization(locale.Original(AnnouncementCopy{Title: a.Title, Content: a.Content}))
	}
	if input.Localization != nil {
		next, err := locale.Prepare(locale.Content[AnnouncementCopy](a.Localization), *input.Localization, validateAnnouncementCopy)
		if err != nil {
			return nil, err
		}
		a.Localization = AnnouncementLocalization(next)
		a.Title, a.Content = next.Source.Title, next.Source.Content
		input.Title, input.Content = nil, nil
	} else if input.Title != nil || input.Content != nil {
		return nil, locale.ErrConflict
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		// Base record title validation uses the trimmed byte length.
		if title == "" || len(title) > 200 {
			return nil, ErrAnnouncementInvalidTitle
		}
		a.Title = title
	}
	if input.Content != nil {
		content := strings.TrimSpace(*input.Content)
		if content == "" {
			return nil, ErrAnnouncementContentRequired
		}
		a.Content = content
	}
	if input.Status != nil {
		status := strings.TrimSpace(*input.Status)
		if !isValidAnnouncementStatus(status) {
			return nil, ErrAnnouncementInvalidStatus
		}
		a.Status = status
	}

	if input.NotifyMode != nil {
		notifyMode := strings.TrimSpace(*input.NotifyMode)
		if !isValidAnnouncementNotifyMode(notifyMode) {
			return nil, ErrAnnouncementInvalidNotifyMode
		}
		a.NotifyMode = notifyMode
	}

	if input.Targeting != nil {
		targeting, err := (*input.Targeting).NormalizeAndValidate()
		if err != nil {
			return nil, err
		}
		a.Targeting = targeting
	}

	if input.StartsAt != nil {
		a.StartsAt = *input.StartsAt
	}
	if input.EndsAt != nil {
		a.EndsAt = *input.EndsAt
	}

	if a.StartsAt != nil && a.EndsAt != nil {
		if !a.StartsAt.Before(*a.EndsAt) {
			return nil, ErrAnnouncementInvalidSchedule
		}
	}

	if input.ActorID != nil && *input.ActorID > 0 {
		a.UpdatedBy = input.ActorID
	}
	// An edited schedule can already have ended. Resolve that state before
	// saving so administrator changes and expiry scans use the same exclusive end-time rule.
	archiveAnnouncementIfExpired(a, time.Now())

	if err := s.announcementRepo.Update(ctx, a); err != nil {
		return nil, fmt.Errorf("update announcement: %w", err)
	}
	return a, nil
}

// Delete removes the selected announcement and adds context to a persistence failure.
func (s *AnnouncementService) Delete(ctx context.Context, id int64) error {
	if err := s.announcementRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete announcement: %w", err)
	}
	return nil
}

// GetByID archives expired announcements before returning the selected administrator record.
// Archiving is a persistent side effect of this read, and its failure prevents
// the detail query from returning a successful result.
func (s *AnnouncementService) GetByID(ctx context.Context, id int64) (*Announcement, error) {
	// The administrator detail response should reflect persisted expiry state.
	// Complete the archival write first; a failure stops the subsequent record query.
	if err := s.archiveExpired(ctx, time.Now()); err != nil {
		return nil, err
	}
	return s.announcementRepo.GetByID(ctx, id)
}

// List archives expired announcements before reading an administrator page with filters.
// An archival failure aborts listing, while successful archival leaves pagination
// and ordering to the repository query that follows it.
func (s *AnnouncementService) List(ctx context.Context, params pagination.PaginationParams, filters AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error) {
	// Persist expiry transitions before applying administrator status filters;
	// otherwise an active-only query could include records that have already reached their end.
	if err := s.archiveExpired(ctx, time.Now()); err != nil {
		return nil, nil, err
	}
	return s.announcementRepo.List(ctx, params, filters)
}

// archiveExpired persists due active-to-archived transitions through the announcement repository.
// The affected row count is discarded; callers need the error to decide
// whether their administrator query can proceed with current stored status.
func (s *AnnouncementService) archiveExpired(ctx context.Context, now time.Time) error {
	if _, err := s.announcementRepo.ArchiveExpired(ctx, now); err != nil {
		return fmt.Errorf("archive expired announcements: %w", err)
	}
	return nil
}

// archiveAnnouncementIfExpired changes an active announcement to archived when
// its end time is at or before now.
// Nil records, other statuses, and open-ended or future schedules remain eligible
// for their own state handling; the helper performs no repository writes.
func archiveAnnouncementIfExpired(a *Announcement, now time.Time) {
	if a == nil || a.Status != AnnouncementStatusActive || a.EndsAt == nil || a.EndsAt.After(now) {
		return
	}
	a.Status = AnnouncementStatusArchived
}

// ListForUser returns scheduled, targeted announcements with language copy and first-read state.
// It reads the user's balance and active plans, filters the repository's active
// candidates at one current-time value, and selects localized copy when the
// announcement has versioned content. No visible candidates returns an empty slice.
//
// Read markers are requested only for surviving announcement IDs, preventing
// unrelated markers from participating in output. unreadOnly removes previously
// read entries. Remaining unread records sort first, then descending ID orders
// each read-state group; read timestamps are copied into distinct pointer values.
func (s *AnnouncementService) ListForUser(ctx context.Context, userID int64, unreadOnly bool) ([]UserAnnouncement, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	activeSubs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list active subscriptions: %w", err)
	}
	activePlanIDs := make(map[int64]struct{}, len(activeSubs))
	for i := range activeSubs {
		activePlanIDs[activeSubs[i].PlanID] = struct{}{}
	}

	now := time.Now()
	anns, err := s.announcementRepo.ListActive(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("list active announcements: %w", err)
	}

	visible := make([]Announcement, 0, len(anns))
	ids := make([]int64, 0, len(anns))
	for i := range anns {
		a := anns[i]
		if !a.IsActiveAt(now) {
			continue
		}
		if !a.Targeting.Matches(user.Balance, activePlanIDs) {
			continue
		}
		if a.Localization.Revision > 0 {
			var copy AnnouncementCopy
			copy, a.Resolution = a.Localization.Resolve(locale.FromContext(ctx))
			a.Title, a.Content = copy.Title, copy.Content
		}
		visible = append(visible, a)
		ids = append(ids, a.ID)
	}

	if len(visible) == 0 {
		return []UserAnnouncement{}, nil
	}

	readMap, err := s.readRepo.GetReadMapByUser(ctx, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("get read map: %w", err)
	}

	out := make([]UserAnnouncement, 0, len(visible))
	for i := range visible {
		a := visible[i]
		readAt, ok := readMap[a.ID]
		if unreadOnly && ok {
			continue
		}
		var ptr *time.Time
		if ok {
			t := readAt
			ptr = &t
		}
		out = append(out, UserAnnouncement{
			Announcement: a,
			ReadAt:       ptr,
		})
	}

	// Read membership controls grouping independently of publication order. Keep
	// unread entries first, then use descending record IDs to order both groups predictably.
	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i], out[j]
		if (ai.ReadAt == nil) != (aj.ReadAt == nil) {
			return ai.ReadAt == nil
		}
		return ai.Announcement.ID > aj.Announcement.ID
	})

	return out, nil
}

// MarkRead records a user's read marker after checking publication and targeting eligibility.
// Missing users or repository failures return their errors. An inactive or
// untargeted announcement returns ErrAnnouncementNotFound, making inaccessible records
// unavailable through the read-marking endpoint as well as through listing.
//
// Eligibility uses the fetched balance, active plans, and one current-time
// value before the read write. The PostgreSQL read repository retains the first
// marker for repeated calls, so a retry acknowledges the read without replacing
// the timestamp observed by subsequent user and administrator queries.
func (s *AnnouncementService) MarkRead(ctx context.Context, userID, announcementID int64) error {
	// Eligibility precedes the read-marker write. Validate the same schedule and
	// targeting rules used by listing before permitting this user to acknowledge the announcement.
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	a, err := s.announcementRepo.GetByID(ctx, announcementID)
	if err != nil {
		return err
	}

	now := time.Now()
	if !a.IsActiveAt(now) {
		return ErrAnnouncementNotFound
	}

	activeSubs, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("list active subscriptions: %w", err)
	}
	activePlanIDs := make(map[int64]struct{}, len(activeSubs))
	for i := range activeSubs {
		activePlanIDs[activeSubs[i].PlanID] = struct{}{}
	}

	if !a.Targeting.Matches(user.Balance, activePlanIDs) {
		return ErrAnnouncementNotFound
	}

	if err := s.readRepo.MarkRead(ctx, announcementID, userID, now); err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// ListUserReadStatus returns a user page with targeting eligibility and recorded read timestamps.
// It trims search before querying users, then joins markers by user ID and
// reads active plans for each returned user. Repository failures abort the
// page instead of supplying incomplete eligibility observations.
//
// Eligible answers whether targeting matches the user's current balance and
// plans. It does not include announcement status or scheduling, allowing an
// administrator to inspect the audience of drafts and archived records too.
// Pagination is inherited from the user query; read markers affect displayed
// state rather than which users belong to that page.
func (s *AnnouncementService) ListUserReadStatus(
	ctx context.Context,
	announcementID int64,
	params pagination.PaginationParams,
	search string,
) ([]AnnouncementUserReadStatus, *pagination.PaginationResult, error) {
	ann, err := s.announcementRepo.GetByID(ctx, announcementID)
	if err != nil {
		return nil, nil, err
	}

	filters := UserListFilters{
		Search: strings.TrimSpace(search),
	}

	users, page, err := s.userRepo.ListWithFilters(ctx, params, filters)
	if err != nil {
		return nil, nil, fmt.Errorf("list users: %w", err)
	}

	userIDs := make([]int64, 0, len(users))
	for i := range users {
		userIDs = append(userIDs, users[i].ID)
	}

	readMap, err := s.readRepo.GetReadMapByUsers(ctx, announcementID, userIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("get read map: %w", err)
	}

	out := make([]AnnouncementUserReadStatus, 0, len(users))
	for i := range users {
		u := users[i]
		subs, err := s.userSubRepo.ListActiveByUserID(ctx, u.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("list active subscriptions: %w", err)
		}
		activePlanIDs := make(map[int64]struct{}, len(subs))
		for j := range subs {
			activePlanIDs[subs[j].PlanID] = struct{}{}
		}

		readAt, ok := readMap[u.ID]
		var ptr *time.Time
		if ok {
			t := readAt
			ptr = &t
		}

		out = append(out, AnnouncementUserReadStatus{
			UserID:   u.ID,
			Email:    u.Email,
			Username: u.Username,
			Balance:  u.Balance,
			Eligible: ann.Targeting.Matches(u.Balance, activePlanIDs),
			ReadAt:   ptr,
		})
	}

	return out, page, nil
}

// isValidAnnouncementStatus reports whether status is one of the stored publication states.
func isValidAnnouncementStatus(status string) bool {
	switch status {
	case AnnouncementStatusDraft, AnnouncementStatusActive, AnnouncementStatusArchived:
		return true
	default:
		return false
	}
}

// isValidAnnouncementNotifyMode reports whether mode is a supported frontend notification presentation.
func isValidAnnouncementNotifyMode(mode string) bool {
	switch mode {
	case AnnouncementNotifyModeSilent, AnnouncementNotifyModePopup:
		return true
	default:
		return false
	}
}

// validateAnnouncementCopy validates a nonblank title and body for one language copy.
// Titles allow at most two hundred Unicode characters, while body length uses
// bytes and allows at most one mebibyte. Violations return the corresponding
// title, content-required, or page-size error.
func validateAnnouncementCopy(copy AnnouncementCopy) error {
	// Localized title length counts characters, including whitespace in supplied copy.
	if strings.TrimSpace(copy.Title) == "" || len([]rune(copy.Title)) > 200 {
		return ErrAnnouncementInvalidTitle
	}
	if strings.TrimSpace(copy.Content) == "" {
		return ErrAnnouncementContentRequired
	}
	// Bound each localized body to one mebibyte of encoded content.
	if len(copy.Content) > 1<<20 {
		return ErrPageTooLarge
	}
	return nil
}
