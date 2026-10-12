package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/site"
	"github.com/TokenFlux/TokenRouter/internal/site/filesystem"
)

// pageMenuFixture exposes a normal guide and an administrator-only page to handlers.
type pageMenuFixture struct{}

// GetCustomMenuItemsRaw returns menu visibility for the HTTP filesystem fixtures.
func (pageMenuFixture) GetCustomMenuItemsRaw(context.Context) string {
	return `[{"page_slug":"guide","visibility":"user"},{"page_slug":"private","visibility":"admin"}]`
}

// TestPageHTTPBoundaryAndVisibility routes page requests through fixture role middleware.
// It verifies authentication and administrator statuses, and checks that an
// external Markdown symlink never exposes its target body through the endpoint.
func TestPageHTTPBoundaryAndVisibility(t *testing.T) {
	data := t.TempDir()
	store := filesystem.New(data)
	pages := filepath.Join(data, "pages")
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("private-fixture"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(pages, "guide.md")))
	require.NoError(t, os.WriteFile(filepath.Join(pages, "private.md"), []byte("admin content"), 0o600))
	h := NewPageHandler(site.NewPages(store, pageMenuFixture{}))
	router := gin.New()
	auth := func(c *gin.Context) {
		role := c.GetHeader("X-Fixture-Role")
		if role == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set(authctx.ContextKeyUserRole, role)
		c.Next()
	}
	admin := func(c *gin.Context) {
		if c.GetHeader("X-Fixture-Role") != "admin" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
	h.Register(router.Group("/api/v1"), auth, admin)
	for _, test := range []struct {
		path, role string // requested URL path and caller role header
		status     int    // expected response status
	}{
		{"/api/v1/pages/guide", "", 401}, {"/api/v1/pages/guide", "user", 404}, {"/api/v1/pages/private", "user", 404}, {"/api/v1/pages/private", "admin", 200}, {"/api/v1/pages", "user", 403}, {"/api/v1/pages", "admin", 200}, {"/api/v1/pages/private/images/x.png", "admin", 404},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("X-Fixture-Role", test.role)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, test.status, response.Code, test.path)
		require.NotContains(t, response.Body.String(), "private-fixture")
	}
}

// TestPageImagesCannotReadMarkdown supplies translated bodies, ordinary documents, and a disguised symlink.
// It verifies that anonymous image requests reject those files while safe
// shared and localized images remain public and Markdown still requires authentication.
func TestPageImagesCannotReadMarkdown(t *testing.T) {
	data := t.TempDir()
	store := filesystem.New(data)
	dir := filepath.Join(data, "pages", "guide")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "en"), 0o700))
	for path, body := range map[string]string{
		"en.md":        "members-only translation",
		"zh-Hans.md":   "仅登录用户可读",
		"notes.txt":    "private notes",
		"example.html": "private document",
		"logo.PNG":     "shared image",
		"en/logo.webp": "translated image",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(body), 0o600))
	}
	require.NoError(t, os.Symlink(filepath.Join(dir, "en.md"), filepath.Join(dir, "alias.png")))
	handler := NewPageHandler(site.NewPages(store, pageMenuFixture{}))
	router := gin.New()
	auth := func(c *gin.Context) {
		if c.GetHeader("X-Fixture-Role") != "user" {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	}
	handler.Register(router.Group("/api/v1"), auth, auth)
	for _, name := range []string{"en.md", "en%2emd", "zh-Hans.md", "notes.txt", "example.html", "alias.png", "alias.png?locale=en"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pages/guide/images/"+name, nil))
		require.Equal(t, http.StatusNotFound, response.Code, name)
		require.Empty(t, response.Body.String())
	}
	for _, test := range []struct {
		path, role, body string // requested URL path, caller role header, and expected response body
		status           int    // expected response status
	}{
		{"/api/v1/pages/guide", "", "", http.StatusUnauthorized},
		{"/api/v1/pages/guide", "user", "members-only translation", http.StatusOK},
		{"/api/v1/pages/guide/images/logo.PNG", "", "shared image", http.StatusOK},
		{"/api/v1/pages/guide/images/logo.PNG?locale=en", "", "shared image", http.StatusOK},
		{"/api/v1/pages/guide/images/logo.webp?locale=en", "", "translated image", http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("X-Fixture-Role", test.role)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, test.status, response.Code, test.path)
		require.Equal(t, test.body, response.Body.String(), test.path)
	}
}
