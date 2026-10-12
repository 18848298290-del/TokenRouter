package site

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

const MaxPageFileSize = 1 << 20

var (
	ErrPageNotFound   = errors.New("page not found")
	ErrPageTooLarge   = errors.New("page too large")
	ErrPageReadFailed = errors.New("failed to read page")
	ErrPageSlug       = errors.New("invalid page slug")
	validSlugPattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
)

type PageFiles interface {
	ReadMarkdown(context.Context, string) ([]byte, error)
	ListPages(context.Context) ([]string, error)
	ImagePath(context.Context, string, string) (string, error)
}
type (
	PageMenus interface{ GetCustomMenuItemsRaw(context.Context) string }
	Pages     struct {
		files PageFiles
		menus PageMenus
	}
)

func NewPages(files PageFiles, menus PageMenus) *Pages { return &Pages{files: files, menus: menus} }
func (s *Pages) visibility(ctx context.Context, slug string) (string, bool) {
	if s.menus == nil {
		return "", false
	}
	raw := s.menus.GetCustomMenuItemsRaw(ctx)
	if raw == "" || raw == "[]" {
		return "", false
	}
	var items []struct {
		URL        string `json:"url"`
		PageSlug   string `json:"page_slug"`
		Visibility string `json:"visibility"`
	}
	if json.Unmarshal([]byte(raw), &items) != nil {
		return "", false
	}
	for _, item := range items {
		page := item.PageSlug
		if page == "" && strings.HasPrefix(item.URL, "md:") {
			page = strings.TrimPrefix(item.URL, "md:")
		}
		if page == slug {
			return item.Visibility, true
		}
	}
	return "", false
}

// ReadMarkdown returns the body of a menu-visible page, subject to admin access.
func (s *Pages) ReadMarkdown(ctx context.Context, slug string, admin bool) ([]byte, error) {
	content, _, err := s.ReadLocalizedMarkdown(ctx, slug, admin)
	return content, err
}

// ReadLocalizedMarkdown selects a translation after checking menu visibility.
// It returns the selected language, or an empty language for the source text.
// Admin-only pages use the source text.
func (s *Pages) ReadLocalizedMarkdown(ctx context.Context, slug string, admin bool) ([]byte, string, error) {
	if !validSlugPattern.MatchString(slug) || len(slug) > 64 {
		return nil, "", ErrPageSlug
	}
	visibility, found := s.visibility(ctx, slug)
	if !found || visibility == "admin" && !admin {
		return nil, "", ErrPageNotFound
	}
	if visibility != "admin" {
		if files, ok := s.files.(interface {
			ReadLocalizedMarkdown(context.Context, string, string) ([]byte, string, error)
		}); ok {
			return files.ReadLocalizedMarkdown(ctx, slug, locale.FromContext(ctx))
		}
	}
	content, err := s.files.ReadMarkdown(ctx, slug)
	return content, "", err
}

func (s *Pages) ListPages(ctx context.Context) ([]string, error) { return s.files.ListPages(ctx) }
func (s *Pages) ImagePath(ctx context.Context, slug, filename string) (string, error) {
	if !validSlugPattern.MatchString(slug) || len(slug) > 64 {
		return "", ErrPageNotFound
	}
	visibility, found := s.visibility(ctx, slug)
	if !found || visibility == "admin" {
		return "", ErrPageNotFound
	}
	return s.files.ImagePath(ctx, slug, filename)
}
