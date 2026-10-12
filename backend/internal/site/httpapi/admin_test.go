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

type announcementRepoCapture struct {
	site.AnnouncementRepository
	listParams pagination.PaginationParams
}

type announcementUserRepoCapture struct {
	site.UserReader
	listParams pagination.PaginationParams
}

type announcementReadRepoCapture struct {
	site.AnnouncementReadRepository
}

type announcementUserSubRepoCapture struct {
	site.SubscriptionReader
}

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

func (r *announcementRepoCapture) List(ctx context.Context, params pagination.PaginationParams, filters site.AnnouncementListFilters) ([]site.Announcement, *pagination.PaginationResult, error) {
	r.listParams = params
	return []site.Announcement{}, &pagination.PaginationResult{
		Total:    0,
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    0,
	}, nil
}

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

func (r *announcementUserRepoCapture) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters site.UserListFilters) ([]site.UserSnapshot, *pagination.PaginationResult, error) {
	r.listParams = params
	return []site.UserSnapshot{}, &pagination.PaginationResult{
		Total:    0,
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    0,
	}, nil
}

func (r *announcementReadRepoCapture) GetReadMapByUsers(ctx context.Context, announcementID int64, userIDs []int64) (map[int64]time.Time, error) {
	return map[int64]time.Time{}, nil
}

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
