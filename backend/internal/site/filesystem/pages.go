package filesystem

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Markdown and image assets live beneath data/pages, with translated Markdown
// in slug/locale.md and shared source content in slug.md. Markdown reads try
// language candidates before source content, while images use a language
// directory only when the request explicitly selects one. Missing translated
// content can therefore reuse a shared page or image.
//
// Paths are checked after symlink resolution as well as during input cleanup.
// Markdown is opened through the resolved root, then sized and read through
// the same handle with a second limit on actual bytes. Image requests validate
// both the requested suffix and the resolved target's image suffix before
// returning a filename to the HTTP adapter.

// Pages reads page bodies and resolves image assets beneath one filesystem root.
type Pages struct {
	pagesDir string // configured pages directory
}

// New builds the page adapter rooted at the deployment data directory.
// It attempts to create dataDir/pages and defers any creation failure to later
// reads, which report missing pages or an empty administrative list.
func New(dataDir string) *Pages {
	dir := filepath.Join(dataDir, "pages")
	// Startup can construct the adapter even when the directory is unavailable;
	// subsequent operations expose that state through their page-read or list results.
	_ = os.MkdirAll(dir, 0o755)
	return &Pages{pagesDir: dir}
}

// ReadMarkdown reads source content for slug through the bounded root reader.
// The caller supplies an authorized slug; missing or escaped files return
// ErrPageNotFound, and bodies above MaxPageFileSize return ErrPageTooLarge.
// @project-doc docs/interfaces/http_api.md#site_pages
func (s *Pages) ReadMarkdown(ctx context.Context, slug string) ([]byte, error) {
	return s.readMarkdownPath(ctx, slug+".md")
}

// ReadLocalizedMarkdown returns the first available language candidate and its code.
// Missing candidates proceed to the next candidate and then source Markdown,
// whose language is empty. Size, read, and cancellation errors stop selection
// immediately because another version cannot complete that failed read.
func (s *Pages) ReadLocalizedMarkdown(ctx context.Context, slug, language string) ([]byte, string, error) {
	for _, code := range locale.Candidates(language) {
		content, err := s.readMarkdownPath(ctx, filepath.Join(slug, code+".md"))
		if err == nil {
			return content, code, nil
		}
		// A missing candidate permits the next language or source page to answer
		// the request; other errors stop selection so body failures remain observable.
		if !errors.Is(err, site.ErrPageNotFound) {
			return nil, "", err
		}
	}
	content, err := s.ReadMarkdown(ctx, slug)
	return content, "", err
}

// readMarkdownPath returns a confined Markdown body or a page-specific read error.
// It checks context cancellation before filesystem work and maps path, root,
// open, and metadata failures to ErrPageNotFound. Body-read failures retain a
// distinct error so the HTTP adapter can report a server failure.
//
// Root and target symlinks are resolved before the relative path is opened
// through os.OpenRoot. Stat and the bounded read use that same file handle;
// the extra byte detects growth beyond the size checked by Stat.
func (s *Pages) readMarkdownPath(ctx context.Context, relative string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Resolution, relative-path calculation, root/file opens, and metadata checks
	// define page availability. Their failures become ErrPageNotFound so the HTTP
	// response does not expose which filesystem step made this page unavailable.
	root, err := filepath.EvalSymlinks(s.pagesDir)
	if err != nil {
		return nil, site.ErrPageNotFound
	}
	target, err := filepath.EvalSymlinks(filepath.Join(s.pagesDir, relative))
	// Resolve the complete target before opening it so symlinked page names
	// can reference in-root content while external targets remain unavailable to readers.
	if err != nil || !isPathWithinBase(target, root) {
		return nil, site.ErrPageNotFound
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return nil, site.ErrPageNotFound
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, site.ErrPageNotFound
	}
	// The opened root bounds relative opens for this read. Release its descriptor
	// on every exit; a close failure does not replace the selected body/read result.
	defer func() { _ = base.Close() }()
	file, err := base.Open(rel)
	if err != nil {
		return nil, site.ErrPageNotFound
	}
	// Stat and body reads share this descriptor. Close it on every exit; a close
	// failure does not change an already completed body or selected page-read error.
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return nil, site.ErrPageNotFound
	}
	// Reject known oversized bodies before allocating read storage.
	if info.Size() > site.MaxPageFileSize {
		return nil, site.ErrPageTooLarge
	}
	// Read one extra byte to detect growth after Stat without accepting an
	// oversized response or allowing an unbounded allocation from a changing file.
	content, err := io.ReadAll(io.LimitReader(file, site.MaxPageFileSize+1))
	if err != nil {
		return nil, site.ErrPageReadFailed
	}
	if len(content) > site.MaxPageFileSize {
		return nil, site.ErrPageTooLarge
	}
	return content, nil
}

// ListPages returns top-level Markdown filenames as source slugs for the page picker.
// Directory reads that fail become a successful empty list. Locale subdirectories
// and their translated files do not add entries to this source-file listing.
func (s *Pages) ListPages(context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.pagesDir)
	// An unavailable pages directory leaves the administrative picker empty;
	// listing does not expose filesystem errors or infer slugs from locale directories.
	if err != nil {
		return []string{}, nil
	}
	slugs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			slugs = append(slugs, strings.TrimSuffix(entry.Name(), ".md"))
		}
	}
	return slugs, nil
}

// ImagePath returns an existing image path in a page's selected asset directory.
// An explicit context language tries its own directory first, then shared page
// assets. Missing, rejected, or directory targets return ErrPageNotFound after
// the shared path is checked.
func (s *Pages) ImagePath(ctx context.Context, slug, filename string) (string, error) {
	if code, ok := locale.Explicit(ctx); ok {
		path, valid := resolvePageImagePath(s.pagesDir, filepath.Join(s.pagesDir, slug, code), filename)
		if valid {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	// A localized directory can be absent or lack this asset. Resolve the
	// shared directory independently so language-specific images can coexist with common assets.
	path, ok := resolvePageImagePath(s.pagesDir, filepath.Join(s.pagesDir, slug), filename)
	if !ok {
		return "", site.ErrPageNotFound
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", site.ErrPageNotFound
	}
	return path, nil
}

// resolvePageImagePath returns a resolved image filename when its confinement checks pass.
// It cleans URL path input and requires an allowed image suffix before joining
// the filename to imagesDir. Failure produces an empty path and false.
//
// Lexical containment protects the join, then symlink resolution verifies that
// the asset directory stays beneath pagesDir and the target stays beneath that
// asset directory. The resolved suffix is checked again because a permitted
// image name can be a symlink to a different file type.
func resolvePageImagePath(pagesDir, imagesDir, filename string) (string, bool) {
	relPath, ok := cleanPageImageRelativePath(filename)
	if !ok || !isPageImage(relPath) {
		return "", false
	}

	cleanedPagesDir := filepath.Clean(pagesDir)
	cleanedImagesDir := filepath.Clean(imagesDir)
	cleanedTarget := filepath.Clean(filepath.Join(cleanedImagesDir, relPath))
	if !isPathWithinBase(cleanedTarget, cleanedImagesDir) {
		return "", false
	}

	// Resolve roots and the target as one validation sequence. Unresolvable
	// paths produce the same inaccessible-image result as targets outside their checked directories.
	realPagesDir, err := filepath.EvalSymlinks(cleanedPagesDir)
	if err != nil {
		return "", false
	}
	realImagesDir, err := filepath.EvalSymlinks(cleanedImagesDir)
	if err != nil || !isPathWithinBase(realImagesDir, realPagesDir) {
		return "", false
	}
	realTarget, err := filepath.EvalSymlinks(cleanedTarget)
	if err != nil || !isPathWithinBase(realTarget, realImagesDir) || !isPageImage(realTarget) {
		return "", false
	}
	return realTarget, true
}

// isPageImage reports whether a path has an allowed case-insensitive image extension.
func isPageImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".avif", ".ico", ".bmp":
		return true
	default:
		return false
	}
}

// cleanPageImageRelativePath returns a normalized relative asset path and validity flag.
// It decodes URL escapes once, removes empty and dot components, and rejects
// parent components, absolute paths, backslashes, NUL bytes, and invalid escapes.
func cleanPageImageRelativePath(filename string) (string, bool) {
	if filename == "" || strings.HasPrefix(filename, "/") {
		return "", false
	}
	// Gin image parameters can contain URL escapes. Validate decoded separators
	// and parent components so encoded traversal receives the same rejection as plain input.
	decoded, err := url.PathUnescape(filename)
	if err != nil {
		return "", false
	}
	if decoded == "" || strings.HasPrefix(decoded, "/") || strings.Contains(decoded, "\\") || strings.ContainsRune(decoded, 0) {
		return "", false
	}

	parts := make([]string, 0)
	for part := range strings.SplitSeq(decoded, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", false
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", false
	}

	relPath := filepath.Join(parts...)
	if filepath.IsAbs(relPath) || filepath.VolumeName(relPath) != "" {
		return "", false
	}
	return relPath, true
}

// isPathWithinBase reports whether path is a descendant of base after cleaning.
// The base itself and parent-relative paths are rejected, so callers require
// a file or nested asset directory beneath their checked root.
func isPathWithinBase(path, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
