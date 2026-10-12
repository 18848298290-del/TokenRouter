package httpapi

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// AdminAnnouncementHandler handles admin announcement management.
type AdminAnnouncementHandler struct {
	announcementService *site.AnnouncementService
}

type CreateAnnouncementRequest struct {
	Localization *locale.Update[site.AnnouncementCopy] `json:"localization" binding:"required"`
	Title        string                                `json:"title"`
	Content      string                                `json:"content"`
	Status       string                                `json:"status" binding:"omitempty,oneof=draft active archived"`
	NotifyMode   string                                `json:"notify_mode" binding:"omitempty,oneof=silent popup"`
	Targeting    site.AnnouncementTargeting            `json:"targeting"`
	StartsAt     *int64                                `json:"starts_at"` // Unix seconds; nil or <=0 means immediate
	EndsAt       *int64                                `json:"ends_at"`   // Unix seconds; nil or <=0 means no expiry
}

type UpdateAnnouncementRequest struct {
	Localization *locale.Update[site.AnnouncementCopy] `json:"localization"`
	Title        *string                               `json:"title"`
	Content      *string                               `json:"content"`
	Status       *string                               `json:"status" binding:"omitempty,oneof=draft active archived"`
	NotifyMode   *string                               `json:"notify_mode" binding:"omitempty,oneof=silent popup"`
	Targeting    *site.AnnouncementTargeting           `json:"targeting"`
	StartsAt     *int64                                `json:"starts_at"` // Unix seconds; nil retains the value, 0 clears it
	EndsAt       *int64                                `json:"ends_at"`   // Unix seconds; nil retains the value, 0 clears it
}

// NewAdminAnnouncementHandler creates a new admin announcement handler.
func NewAdminAnnouncementHandler(announcementService *site.AnnouncementService) *AdminAnnouncementHandler {
	return &AdminAnnouncementHandler{
		announcementService: announcementService,
	}
}

// List returns announcements matching the requested filters and sort order.
// GET /api/v1/admin/announcements
func (h *AdminAnnouncementHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	status := strings.TrimSpace(c.Query("status"))
	search := strings.TrimSpace(c.Query("search"))
	sortBy := c.DefaultQuery("sort_by", "created_at")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	if len(search) > 200 {
		search = search[:200]
	}

	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    sortBy,
		SortOrder: sortOrder,
	}

	items, paginationResult, err := h.announcementService.List(
		c.Request.Context(),
		params,
		site.AnnouncementListFilters{Status: status, Search: search},
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]Announcement, 0, len(items))
	for i := range items {
		out = append(out, *AnnouncementFromService(&items[i]))
	}
	response.Paginated(c, out, paginationResult.Total, page, pageSize)
}

// GetByID returns an announcement by ID.
// GET /api/v1/admin/announcements/:id
func (h *AdminAnnouncementHandler) GetByID(c *gin.Context) {
	announcementID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || announcementID <= 0 {
		response.BadRequest(c, "Invalid announcement ID")
		return
	}

	item, err := h.announcementService.GetByID(c.Request.Context(), announcementID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, AnnouncementFromService(item))
}

// Create creates an announcement attributed to the authenticated actor.
// POST /api/v1/admin/announcements
func (h *AdminAnnouncementHandler) Create(c *gin.Context) {
	var req CreateAnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	input := &site.CreateAnnouncementInput{
		Localization: req.Localization,
		Title:        req.Title,
		Content:      req.Content,
		Status:       req.Status,
		NotifyMode:   req.NotifyMode,
		Targeting:    req.Targeting,
		ActorID:      &subject.UserID,
	}

	if req.StartsAt != nil && *req.StartsAt > 0 {
		t := time.Unix(*req.StartsAt, 0)
		input.StartsAt = &t
	}
	if req.EndsAt != nil && *req.EndsAt > 0 {
		t := time.Unix(*req.EndsAt, 0)
		input.EndsAt = &t
	}

	created, err := h.announcementService.Create(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, AnnouncementFromService(created))
}

// Update applies supplied announcement fields.
// PUT /api/v1/admin/announcements/:id
func (h *AdminAnnouncementHandler) Update(c *gin.Context) {
	announcementID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || announcementID <= 0 {
		response.BadRequest(c, "Invalid announcement ID")
		return
	}

	var req UpdateAnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	input := &site.UpdateAnnouncementInput{
		Localization: req.Localization,
		Title:        req.Title,
		Content:      req.Content,
		Status:       req.Status,
		NotifyMode:   req.NotifyMode,
		Targeting:    req.Targeting,
		ActorID:      &subject.UserID,
	}

	if req.StartsAt != nil {
		if *req.StartsAt == 0 {
			var cleared *time.Time
			input.StartsAt = &cleared
		} else {
			t := time.Unix(*req.StartsAt, 0)
			ptr := &t
			input.StartsAt = &ptr
		}
	}

	if req.EndsAt != nil {
		if *req.EndsAt == 0 {
			var cleared *time.Time
			input.EndsAt = &cleared
		} else {
			t := time.Unix(*req.EndsAt, 0)
			ptr := &t
			input.EndsAt = &ptr
		}
	}

	updated, err := h.announcementService.Update(c.Request.Context(), announcementID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, AnnouncementFromService(updated))
}

// Delete deletes an announcement by ID.
// DELETE /api/v1/admin/announcements/:id
func (h *AdminAnnouncementHandler) Delete(c *gin.Context) {
	announcementID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || announcementID <= 0 {
		response.BadRequest(c, "Invalid announcement ID")
		return
	}

	if err := h.announcementService.Delete(c.Request.Context(), announcementID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Announcement deleted successfully"})
}

// ListReadStatus returns users' read status for an announcement.
// GET /api/v1/admin/announcements/:id/read-status
func (h *AdminAnnouncementHandler) ListReadStatus(c *gin.Context) {
	announcementID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || announcementID <= 0 {
		response.BadRequest(c, "Invalid announcement ID")
		return
	}

	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "email"),
		SortOrder: c.DefaultQuery("sort_order", "asc"),
	}
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 200 {
		search = search[:200]
	}

	items, paginationResult, err := h.announcementService.ListUserReadStatus(
		c.Request.Context(),
		announcementID,
		params,
		search,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Paginated(c, items, paginationResult.Total, page, pageSize)
}
