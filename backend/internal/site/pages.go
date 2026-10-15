package site

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// pages.go authorizes page and image reads through configured menu visibility.
// Menu entries decide whether Markdown pages and their images are accessible.
// A page_slug identifies a page directly, while older md: URLs carry the same
// slug in a different field. The first matching entry supplies visibility, so
// duplicate menu entries retain their configured order when access is checked.
// Missing or malformed menus leave page content unavailable.
//
// Content reads validate the slug before consulting menus. An admin-only entry
// requires the caller's administrator flag and uses source Markdown. Other
// visible entries can use a localized filesystem reader, while their images
// remain publicly accessible through the separate image endpoint.

// MaxPageFileSize caps Markdown bodies at one mebibyte for filesystem readers and
// HTTP responses, including files that grow during a bounded read.
const MaxPageFileSize = 1 << 20

var (
	// Page errors distinguish invalid requests, hidden or missing content, and
	// filesystem failures so the HTTP adapter can select its response status.
	ErrPageNotFound   = errors.New("page not found")
	ErrPageTooLarge   = errors.New("page too large")
	ErrPageReadFailed = errors.New("failed to read page")
	ErrPageSlug       = errors.New("invalid page slug")
	// validSlugPattern limits menu page names to one portable path component.
	// ReadLocalizedMarkdown also checks length before asking the storage adapter.
	validSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
)

// PageFiles supplies Markdown bodies, page names, and resolved image filenames.
type PageFiles interface {
	// ReadMarkdown reads source Markdown for a slug supplied by Pages.
	// Implementations enforce storage confinement and size limits; Pages has
	// already checked menu visibility before requesting the body.
	ReadMarkdown(context.Context, string) ([]byte, error)
	// ListPages lists source page slugs for the administrative page picker.
	// The caller does not apply menu visibility to this list, so implementations
	// may include files that have no matching menu entry.
	ListPages(context.Context) ([]string, error)
	// ImagePath resolves an image filename beneath the requested page's directory.
	// Pages checks visibility first; implementations still validate the filename
	// and resolved path before returning a path to the HTTP file sender.
	ImagePath(context.Context, string, string) (string, error)
}
type (
	// PageMenus supplies the configured menu JSON used for page authorization.
	PageMenus interface {
		// GetCustomMenuItemsRaw returns stored menu JSON for page visibility checks.
		GetCustomMenuItemsRaw(context.Context) string
	}
	// Pages applies menu visibility before delegating Markdown and image reads.
	Pages struct {
		files PageFiles // backing page storage
		// menus supplies page visibility; when absent, content and image requests
		// are hidden while administrative source-file listing remains available.
		menus PageMenus
	}
)

// NewPages binds the page storage and menu reader used by HTTP handlers.
// A nil menu reader hides Markdown and images. The files dependency is needed
// when listing pages or reading a visible entry.
func NewPages(files PageFiles, menus PageMenus) *Pages { return &Pages{files: files, menus: menus} }

// visibility returns the first menu entry matching slug and whether it exists.
// An unavailable reader, empty configuration, or malformed JSON produces a
// missing result, which the caller translates to a hidden page.
//
// Entries with page_slug use that identifier even when URL is also present.
// Older entries can identify Markdown through an md: URL; other URL forms do
// not supply a page slug. Configured ordering decides duplicate matches.
func (s *Pages) visibility(ctx context.Context, slug string) (string, bool) {
	if s.menus == nil {
		return "", false
	}
	raw := s.menus.GetCustomMenuItemsRaw(ctx)
	if raw == "" || raw == "[]" {
		return "", false
	}
	var items []struct {
		URL        string `json:"url"`        // legacy Markdown URL
		PageSlug   string `json:"page_slug"`  // configured page identifier
		Visibility string `json:"visibility"` // menu access class
	}
	// Invalid menu JSON cannot identify an authorized page, so callers receive
	// the same missing result used when no matching menu entry exists.
	if json.Unmarshal([]byte(raw), &items) != nil {
		return "", false
	}
	for _, item := range items {
		page := item.PageSlug
		// Older stored menus identify Markdown through their URL. Prefer page_slug
		// when present so a separate navigation URL cannot change the authorized page.
		if page == "" && strings.HasPrefix(item.URL, "md:") {
			page = strings.TrimPrefix(item.URL, "md:")
		}
		if page == slug {
			return item.Visibility, true
		}
	}
	return "", false
}

// ReadMarkdown returns the body selected for a menu-visible page and caller role.
// It uses the context language through ReadLocalizedMarkdown and discards the
// selected-language result when the caller needs the body alone.
func (s *Pages) ReadMarkdown(ctx context.Context, slug string, admin bool) ([]byte, error) {
	content, _, err := s.ReadLocalizedMarkdown(ctx, slug, admin)
	return content, err
}

// ReadLocalizedMarkdown returns an authorized page body and its selected language.
// Slugs accept one alphanumeric-led component of at most 64 bytes. Hidden pages
// and admin-only pages requested by ordinary users return ErrPageNotFound.
// Admin pages and adapters without localized reads return source text with an
// empty language; other reads use the language carried by ctx.
func (s *Pages) ReadLocalizedMarkdown(ctx context.Context, slug string, admin bool) ([]byte, string, error) {
	// Bound slug length before joining storage paths.
	if !validSlugPattern.MatchString(slug) || len(slug) > 64 {
		return nil, "", ErrPageSlug
	}
	visibility, found := s.visibility(ctx, slug)
	if !found || visibility == "admin" && !admin {
		return nil, "", ErrPageNotFound
	}
	if visibility != "admin" {
		if files, ok := s.files.(interface {
			// ReadLocalizedMarkdown selects stored content for the supplied language.
			// Its language result identifies the chosen translation, while an empty
			// result identifies source content returned after candidate selection.
			ReadLocalizedMarkdown(context.Context, string, string) ([]byte, string, error)
		}); ok {
			return files.ReadLocalizedMarkdown(ctx, slug, locale.FromContext(ctx))
		}
	}
	// Source-only adapters and administrator pages share the source Markdown
	// path, whose empty language lets HTTP consumers distinguish it from a translation.
	content, err := s.files.ReadMarkdown(ctx, slug)
	return content, "", err
}

// ListPages returns source-file slugs for the administrator's page picker.
// The storage adapter defines which filenames appear, and menu visibility does
// not filter this list because unlinked pages also need to be selectable.
func (s *Pages) ListPages(ctx context.Context) ([]string, error) { return s.files.ListPages(ctx) }

// ImagePath returns the storage path for an image on a non-admin visible page.
// Invalid slugs, missing menu entries, and admin-only entries return
// ErrPageNotFound because the public image endpoint cannot authenticate a role.
func (s *Pages) ImagePath(ctx context.Context, slug, filename string) (string, error) {
	// Image requests use the same bounded menu-slug format.
	if !validSlugPattern.MatchString(slug) || len(slug) > 64 {
		return "", ErrPageNotFound
	}
	visibility, found := s.visibility(ctx, slug)
	if !found || visibility == "admin" {
		return "", ErrPageNotFound
	}
	return s.files.ImagePath(ctx, slug, filename)
}
