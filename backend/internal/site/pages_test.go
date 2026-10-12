package site

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// pageMenus supplies literal menu JSON to exercise page authorization decisions.
type pageMenus string

// pageFixtureFiles supplies fixed page content and image paths for visibility tests.
type pageFixtureFiles struct{}

// GetCustomMenuItemsRaw returns the fixture's literal menu JSON to Pages.
func (m pageMenus) GetCustomMenuItemsRaw(context.Context) string { return string(m) }

// ReadMarkdown returns a fixed body when Pages permits the read.
func (pageFixtureFiles) ReadMarkdown(context.Context, string) ([]byte, error) {
	return []byte("body"), nil
}

// ListPages returns source slugs including an admin-only page for the picker.
func (pageFixtureFiles) ListPages(context.Context) ([]string, error) {
	return []string{"user", "admin"}, nil
}

// ImagePath returns a fixed filename for visibility-approved fixture image requests.
func (pageFixtureFiles) ImagePath(context.Context, string, string) (string, error) {
	return "image", nil
}

// TestPageVisibilityContracts supplies legacy and explicit page slugs with different visibility.
// It verifies content authorization, invalid-slug errors, and exclusion of admin
// page images from the public image path.
func TestPageVisibilityContracts(t *testing.T) {
	service := NewPages(pageFixtureFiles{}, pageMenus(`[{"url":"md:user","visibility":"user"},{"page_slug":"admin","visibility":"admin"}]`))
	ctx := context.Background()
	_, err := service.ReadMarkdown(ctx, "user", false)
	require.NoError(t, err)
	_, err = service.ReadMarkdown(ctx, "admin", false)
	require.ErrorIs(t, err, ErrPageNotFound)
	_, err = service.ReadMarkdown(ctx, "admin", true)
	require.NoError(t, err)
	_, err = service.ReadMarkdown(ctx, "missing", true)
	require.ErrorIs(t, err, ErrPageNotFound)
	_, err = service.ReadMarkdown(ctx, "../user", true)
	require.ErrorIs(t, err, ErrPageSlug)
	_, err = service.ImagePath(ctx, "user", "x.png")
	require.NoError(t, err)
	_, err = service.ImagePath(ctx, "admin", "x.png")
	require.ErrorIs(t, err, ErrPageNotFound)
}
