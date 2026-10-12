package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// announcementRepoCapture records administrator list parameters while supplying empty announcement fixtures.
type announcementRepoCapture struct {
	site.AnnouncementRepository                             // unused repository method set
	listParams                  pagination.PaginationParams // captured announcement pagination
}

// announcementUserRepoCapture records user-query pagination used by administrator read-status requests.
type announcementUserRepoCapture struct {
	site.UserReader                             // unused user method set
	listParams      pagination.PaginationParams // captured user pagination
}

// announcementReadRepoCapture supplies an empty read map for empty user-page fixtures.
type announcementReadRepoCapture struct {
	site.AnnouncementReadRepository // unused read method set
}

// announcementUserSubRepoCapture supplies the unused subscription dependency for empty reader pages.
type announcementUserSubRepoCapture struct {
	site.SubscriptionReader // unused subscription method set
}

// TestAdminAnnouncementListSortParams supplies an explicit announcement sort field and uppercase direction.
// It verifies a successful response and unchanged forwarding of those query
// values to the announcement repository's pagination parameters.
func TestAdminAnnouncementListSortParams(t *testing.T) {
	announcementRepo := &announcementRepoCapture{}
	userRepo := &announcementUserRepoCapture{}
	router := newAnnouncementSortTestRouter(announcementRepo, userRepo)

	req := httptest.NewRequest(http.MethodGet, "/admin/announcements?sort_by=title&sort_order=ASC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "title", announcementRepo.listParams.SortBy)
	require.Equal(t, "ASC", announcementRepo.listParams.SortOrder)
}

// TestAdminAnnouncementListSortDefaults omits administrator announcement sort query parameters.
// It verifies the endpoint returns success and passes created_at descending
// to the repository as the default announcement ordering.
func TestAdminAnnouncementListSortDefaults(t *testing.T) {
	announcementRepo := &announcementRepoCapture{}
	userRepo := &announcementUserRepoCapture{}
	router := newAnnouncementSortTestRouter(announcementRepo, userRepo)

	req := httptest.NewRequest(http.MethodGet, "/admin/announcements", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", announcementRepo.listParams.SortBy)
	require.Equal(t, "desc", announcementRepo.listParams.SortOrder)
}

// TestAdminAnnouncementReadStatusSortParams supplies a balance sort field and uppercase descending direction.
// It verifies a successful read-status response and unchanged forwarding of
// both sort values to the focused user reader.
func TestAdminAnnouncementReadStatusSortParams(t *testing.T) {
	announcementRepo := &announcementRepoCapture{}
	userRepo := &announcementUserRepoCapture{}
	router := newAnnouncementSortTestRouter(announcementRepo, userRepo)

	req := httptest.NewRequest(http.MethodGet, "/admin/announcements/1/read-status?sort_by=balance&sort_order=DESC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "balance", userRepo.listParams.SortBy)
	require.Equal(t, "DESC", userRepo.listParams.SortOrder)
}

// TestAdminAnnouncementReadStatusSortDefaults omits ordering parameters while requesting an announcement's reader page.
// It verifies that user pagination receives email ascending and the HTTP
// response succeeds with the empty fixture results.
func TestAdminAnnouncementReadStatusSortDefaults(t *testing.T) {
	announcementRepo := &announcementRepoCapture{}
	userRepo := &announcementUserRepoCapture{}
	router := newAnnouncementSortTestRouter(announcementRepo, userRepo)

	req := httptest.NewRequest(http.MethodGet, "/admin/announcements/1/read-status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "email", userRepo.listParams.SortBy)
	require.Equal(t, "asc", userRepo.listParams.SortOrder)
}

// ArchiveExpired lets sorting tests list announcements without changing expiry state.
func (r *announcementRepoCapture) ArchiveExpired(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// List captures the supplied announcement pagination and returns an empty successful page.
// Filters and context are unused in this fixture, while returned page values
// let the handler encode the same requested pagination in its response.
func (r *announcementRepoCapture) List(ctx context.Context, params pagination.PaginationParams, filters site.AnnouncementListFilters) ([]site.Announcement, *pagination.PaginationResult, error) {
	r.listParams = params
	return []site.Announcement{}, &pagination.PaginationResult{
		Total:    0,
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    0,
	}, nil
}

// GetByID returns an active fixture announcement using the requested record identifier.
func (r *announcementRepoCapture) GetByID(ctx context.Context, id int64) (*site.Announcement, error) {
	return &site.Announcement{
		ID:        id,
		Title:     "announcement",
		Content:   "content",
		Status:    site.AnnouncementStatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

// ListWithFilters captures user pagination and returns an empty page for read-status handlers.
// Search and context are unused; the fixture's page metadata retains the
// requested page and page size for success-envelope construction.
func (r *announcementUserRepoCapture) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters site.UserListFilters) ([]site.UserSnapshot, *pagination.PaginationResult, error) {
	r.listParams = params
	return []site.UserSnapshot{}, &pagination.PaginationResult{
		Total:    0,
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    0,
	}, nil
}

// GetReadMapByUsers returns an empty successful read map for the fixture user page.
func (r *announcementReadRepoCapture) GetReadMapByUsers(ctx context.Context, announcementID int64, userIDs []int64) (map[int64]time.Time, error) {
	return map[int64]time.Time{}, nil
}

// newAnnouncementSortTestRouter binds administrator list handlers to repositories that capture pagination.
// It configures Gin test mode and registers announcement and read-status lists
// directly, so the returned engine exercises handler parameter mapping with the
// supplied empty-page fixtures.
func newAnnouncementSortTestRouter(announcementRepo *announcementRepoCapture, userRepo *announcementUserRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := site.NewAnnouncementService(
		announcementRepo,
		&announcementReadRepoCapture{},
		userRepo,
		&announcementUserSubRepoCapture{},
	)
	handler := NewAdminAnnouncementHandler(svc)
	router := gin.New()
	router.GET("/admin/announcements", handler.List)
	router.GET("/admin/announcements/:id/read-status", handler.ListReadStatus)
	return router
}
