package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// pages.go serves authorized Markdown pages, lists page slugs, and delivers
// public page images.
// Markdown page bodies require a logged-in role, while permitted page images
// are served publicly. Body responses expose the selected language and preserve
// page-specific errors; image lookup failures collapse to an empty 404 response.

// PageHandler translates menu-authorized page reads into Markdown and image responses.
type PageHandler struct {
	pages *site.Pages // page authorization service
}

// NewPageHandler binds the page service used by the registered HTTP routes.
func NewPageHandler(pages *site.Pages) *PageHandler { return &PageHandler{pages: pages} }

// GetPageContent writes an authorized page body and its resolved content language.
// The caller's role comes from authentication middleware. Invalid slugs become
// 400 responses, hidden pages become 404, and oversized bodies become 413.
func (h *PageHandler) GetPageContent(c *gin.Context) {
	role, _ := authctx.GetUserRoleFromContext(c)
	content, language, err := h.pages.ReadLocalizedMarkdown(c.Request.Context(), c.Param("slug"), role == "admin")
	switch {
	case errors.Is(err, site.ErrPageSlug):
		response.BadRequest(c, "Invalid page slug")
	case errors.Is(err, site.ErrPageNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
	case errors.Is(err, site.ErrPageTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "page too large"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read page"})
	default:
		c.Header("Content-Language", language)
		c.Header("Vary", "Accept-Language")
		c.Data(http.StatusOK, "text/markdown; charset=utf-8", content)
	}
}

// ListPages writes source page slugs for the administrator's page picker.
// Register protects this endpoint with administrator middleware. Storage errors
// are discarded here, so the success envelope contains whatever slice the
// page service returned.
func (h *PageHandler) ListPages(c *gin.Context) {
	// Page listing is a picker response. Retain the returned slice even when
	// an adapter also reports an error, matching the endpoint's success-envelope behavior.
	slugs, _ := h.pages.ListPages(c.Request.Context())
	response.Success(c, slugs)
}

// ServePageImage serves a permitted public page image using an optional locale query.
// A normalized query language selects localized assets; absent or invalid
// languages use shared assets. Lookup failures return an empty 404 response.
func (h *PageHandler) ServePageImage(c *gin.Context) {
	ctx := c.Request.Context()
	if language := locale.Normalize(c.Query("locale")); language != "" {
		ctx = locale.WithLanguage(ctx, language)
	} else {
		// Image language selection belongs to the query parameter. Clear any
		// inherited context language so an absent query resolves the shared asset directory.
		ctx = locale.WithoutLanguage(ctx)
	}
	path, err := h.pages.ImagePath(ctx, c.Param("slug"), strings.TrimPrefix(c.Param("filename"), "/"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.File(path)
}

// Register installs authenticated body reads, public images, and administrator page listing.
// The separate groups let image requests avoid JWT middleware while retaining
// role checks on Markdown and administrator checks on the source-file picker.
func (h *PageHandler) Register(v1 *gin.RouterGroup, jwtAuth, adminAuth gin.HandlerFunc) {
	pages := v1.Group("/pages")
	pages.Use(jwtAuth)
	pages.GET("/:slug", h.GetPageContent)
	images := v1.Group("/pages")
	images.GET("/:slug/images/*filename", h.ServePageImage)
	admin := v1.Group("/pages")
	admin.Use(adminAuth)
	adminapi.Register(admin, ManagementOperations["pages.list"], h.ListPages)
}
