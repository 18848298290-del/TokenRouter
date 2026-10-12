package site

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// announcementRepoStub holds mutable announcement fixtures and records administrator archive-query ordering.
type announcementRepoStub struct {
	// item is stored and returned by reference, allowing tests to observe the
	// service's in-place record changes before and after repository calls.
	item              *Announcement
	items             []Announcement // administrator list fixtures
	archiveExpiredErr error          // injected archive failure
	callOrder         []string       // recorded archive-query sequence
}

// Create retains the supplied record pointer so the test can inspect saved state.
// It performs no metadata assignment or database work; the service's normalized
// publication fields remain visible through the fixture pointer.
func (s *announcementRepoStub) Create(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

// GetByID returns the shared fixture record or ErrAnnouncementNotFound when absent.
// The supplied identifier is ignored, so tests exercise service state handling
// while repository ownership and concurrent-write behavior remain outside this fixture.
func (s *announcementRepoStub) GetByID(_ context.Context, _ int64) (*Announcement, error) {
	if s.item == nil {
		return nil, ErrAnnouncementNotFound
	}
	return s.item, nil
}

// Update retains the supplied record pointer for post-update state assertions.
// It accepts every write and performs no timestamp conflict check, leaving
// this fixture focused on service validation and expiry-state handling.
func (s *announcementRepoStub) Update(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

// Delete returns success for fixture deletions without examining the requested identifier.
func (*announcementRepoStub) Delete(context.Context, int64) error {
	return nil
}

// ArchiveExpired records an archive phase and returns the configured fixture error.
// It reports zero changed rows, allowing tests to check whether administrator
// listing proceeds or stops after that phase.
func (s *announcementRepoStub) ArchiveExpired(context.Context, time.Time) (int64, error) {
	s.callOrder = append(s.callOrder, "archive")
	return 0, s.archiveExpiredErr
}

// List records its query phase and returns the shared administrator fixture slice.
// Pagination and filters are ignored; the empty pagination result lets the
// tests focus on the service's ordering and error handling around archival.
func (s *announcementRepoStub) List(context.Context, pagination.PaginationParams, AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error) {
	s.callOrder = append(s.callOrder, "list")
	return s.items, &pagination.PaginationResult{}, nil
}

// ListActive returns no user-facing candidates from this administrator-focused repository fixture.
func (*announcementRepoStub) ListActive(context.Context, time.Time) ([]Announcement, error) {
	return nil, nil
}

// TestAnnouncementServiceCreateRejectsEqualStartEndTimes submits an active
// announcement with equal schedule endpoints. It expects ErrAnnouncementInvalidSchedule
// because a half-open publication range needs a start before its end.
func TestAnnouncementServiceCreateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)

	_, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:      "公告",
		Content:    "内容",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModePopup,
		StartsAt:   &now,
		EndsAt:     &now,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}

// TestAnnouncementServiceUpdateRejectsEqualStartEndTimes replaces both endpoints with the same timestamp.
// It verifies that update validates the resulting schedule and rejects the
// equal-time interval with the same domain error used during creation.
func TestAnnouncementServiceUpdateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{
		item: &Announcement{
			ID:         1,
			Title:      "公告",
			Content:    "内容",
			Status:     AnnouncementStatusActive,
			NotifyMode: AnnouncementNotifyModePopup,
		},
	}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)
	startsAt := &now
	endsAt := &now

	_, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{
		StartsAt: &startsAt,
		EndsAt:   &endsAt,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}

// TestAnnouncementServiceListArchivesExpiredBeforeQuery returns an archived fixture from the administrator list.
// It verifies that the service invokes archival before querying and returns
// the archived state supplied by the repository.
func TestAnnouncementServiceListArchivesExpiredBeforeQuery(t *testing.T) {
	repo := &announcementRepoStub{items: []Announcement{{ID: 1, Status: AnnouncementStatusArchived}}}
	svc := NewAnnouncementService(repo, nil, nil, nil)

	items, _, err := svc.List(context.Background(), pagination.PaginationParams{}, AnnouncementListFilters{})

	require.NoError(t, err)
	require.Equal(t, []string{"archive", "list"}, repo.callOrder)
	require.Equal(t, AnnouncementStatusArchived, items[0].Status)
}

// TestAnnouncementServiceListStopsWhenArchivingExpiredFails injects an error into the pre-query archive phase.
// It verifies that the error reaches the caller and the repository never
// records a subsequent list query.
func TestAnnouncementServiceListStopsWhenArchivingExpiredFails(t *testing.T) {
	archiveErr := errors.New("archive failed")
	repo := &announcementRepoStub{archiveExpiredErr: archiveErr}
	svc := NewAnnouncementService(repo, nil, nil, nil)

	_, _, err := svc.List(context.Background(), pagination.PaginationParams{}, AnnouncementListFilters{})

	require.ErrorIs(t, err, archiveErr)
	require.Equal(t, []string{"archive"}, repo.callOrder)
}

// TestAnnouncementServiceCreateArchivesAlreadyExpiredAnnouncement creates an active record whose end is in the past.
// It verifies that the returned record has archived status after service
// normalization and the repository's successful creation call.
func TestAnnouncementServiceCreateArchivesAlreadyExpiredAnnouncement(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	endedAt := time.Now().Add(-time.Hour)

	created, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:      "公告",
		Content:    "内容",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModeSilent,
		EndsAt:     &endedAt,
	})

	require.NoError(t, err)
	require.Equal(t, AnnouncementStatusArchived, created.Status)
}

// TestAnnouncementServiceUpdateArchivesAlreadyExpiredAnnouncement changes an active record's end to a past timestamp.
// It verifies that the returned record is archived after the update's
// schedule handling and successful repository call.
func TestAnnouncementServiceUpdateArchivesAlreadyExpiredAnnouncement(t *testing.T) {
	repo := &announcementRepoStub{item: &Announcement{
		ID:         1,
		Title:      "公告",
		Content:    "内容",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModeSilent,
	}}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	endedAt := time.Now().Add(-time.Hour)
	endsAt := &endedAt

	updated, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{EndsAt: &endsAt})

	require.NoError(t, err)
	require.Equal(t, AnnouncementStatusArchived, updated.Status)
}
