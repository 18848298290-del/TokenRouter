package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// TestLocalizedPageFiles creates translated Markdown, language assets, and shared images.
// It verifies reported language, source and image fallback, and rejection of
// external Markdown symlinks and image traversal through the filesystem adapter.
func TestLocalizedPageFiles(t *testing.T) {
	root := t.TempDir()
	files := New(root)
	dir := filepath.Join(root, "pages")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "guide", "en"), 0o755))
	for path, body := range map[string]string{"guide.md": "原文", "guide/en.md": "English", "guide/en/logo.png": "translated", "guide/shared.png": "shared"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(body), 0o600))
	}
	ctx := context.Background()
	body, language, err := files.ReadLocalizedMarkdown(ctx, "guide", "en-US")
	require.NoError(t, err)
	require.Equal(t, "English", string(body))
	require.Equal(t, "en", language)
	body, language, err = files.ReadLocalizedMarkdown(ctx, "guide", "zh")
	require.NoError(t, err)
	require.Equal(t, "原文", string(body))
	require.Empty(t, language)
	selected := locale.WithLanguage(ctx, "en")
	image, err := files.ImagePath(selected, "guide", "logo.png")
	require.NoError(t, err)
	require.Contains(t, image, "/guide/en/logo.png")
	image, err = files.ImagePath(selected, "guide", "shared.png")
	require.NoError(t, err)
	require.Contains(t, image, "/guide/shared.png")
	external := filepath.Join(t.TempDir(), "external.md")
	require.NoError(t, os.WriteFile(external, []byte("private"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(dir, "guide", "zh-Hans.md")))
	body, language, err = files.ReadLocalizedMarkdown(ctx, "guide", "zh")
	require.NoError(t, err)
	require.Equal(t, "原文", string(body))
	require.Empty(t, language)
	_, err = files.ImagePath(selected, "guide", "../guide.md")
	require.ErrorIs(t, err, site.ErrPageNotFound)
}

// TestMarkdownRootAndSize supplies in-root and external symlinks plus boundary-sized bodies.
// It verifies permitted internal targets and rejects escaped content and files
// one byte beyond the Markdown size limit.
func TestMarkdownRootAndSize(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	pages := filepath.Join(root, "pages")
	require.NoError(t, os.WriteFile(filepath.Join(pages, "guide.md"), []byte("guide"), 0o600))
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("private-fixture"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(pages, "escape.md")))
	_, err := store.ReadMarkdown(context.Background(), "escape")
	require.ErrorIs(t, err, site.ErrPageNotFound)
	require.NoError(t, os.Symlink(filepath.Join(pages, "guide.md"), filepath.Join(pages, "inside.md")))
	body, err := store.ReadMarkdown(context.Background(), "inside")
	require.NoError(t, err)
	require.Equal(t, "guide", string(body))
	require.NoError(t, os.WriteFile(filepath.Join(pages, "limit.md"), make([]byte, site.MaxPageFileSize), 0o600))
	body, err = store.ReadMarkdown(context.Background(), "limit")
	require.NoError(t, err)
	require.Len(t, body, site.MaxPageFileSize)
	require.NoError(t, os.WriteFile(filepath.Join(pages, "large.md"), make([]byte, site.MaxPageFileSize+1), 0o600))
	_, err = store.ReadMarkdown(context.Background(), "large")
	require.ErrorIs(t, err, site.ErrPageTooLarge)
}

// TestCleanPageImageRelativePath supplies plain and escaped image path components.
// It verifies normalization of safe paths and rejection of parent traversal,
// absolute paths, backslashes, NUL bytes, and invalid URL escapes.
func TestCleanPageImageRelativePath(t *testing.T) {
	tests := []struct {
		name string // named path scenario
		in   string // supplied URL path
		want string // expected normalized path
		ok   bool   // expected validity result
	}{
		{name: "single filename", in: "logo.png", want: "logo.png", ok: true},
		{name: "nested path", in: "images/logo.png", want: filepath.Join("images", "logo.png"), ok: true},
		{name: "dot prefix", in: "./logo.png", want: "logo.png", ok: true},
		{name: "url escaped slash", in: "images%2Flogo.png", want: filepath.Join("images", "logo.png"), ok: true},
		{name: "parent traversal", in: "../secret.png", ok: false},
		{name: "encoded parent traversal", in: "%2e%2e/secret.png", ok: false},
		{name: "backslash traversal", in: `images\secret.png`, ok: false},
		{name: "absolute path", in: "/etc/passwd", ok: false},
		{name: "encoded absolute path", in: "%2fetc/passwd", ok: false},
		{name: "encoded nul byte", in: "logo.png%00", ok: false},
		{name: "invalid escape", in: "logo.png%zz", ok: false},
		{name: "empty path", in: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cleanPageImageRelativePath(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolvePageImagePath creates direct and nested images in one page directory.
// It verifies resolved filenames for safe requests and rejects a parent path
// that tries to reference the page's Markdown body.
func TestResolvePageImagePath(t *testing.T) {
	root := t.TempDir()
	pagesDir := filepath.Join(root, "pages")
	base := filepath.Join(pagesDir, "guide")
	if err := os.MkdirAll(filepath.Join(base, "images"), 0o755); err != nil {
		t.Fatalf("create images dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "logo.png"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("create direct image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "images", "logo.png"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("create image: %v", err)
	}

	got, ok := resolvePageImagePath(pagesDir, base, "logo.png")
	if !ok {
		t.Fatal("expected direct image path to be accepted")
	}
	want := mustEvalSymlinks(t, filepath.Join(base, "logo.png"))
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	got, ok = resolvePageImagePath(pagesDir, base, "images/logo.png")
	if !ok {
		t.Fatal("expected nested image path to be accepted")
	}
	want = mustEvalSymlinks(t, filepath.Join(base, "images", "logo.png"))
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	if got, ok := resolvePageImagePath(pagesDir, base, "../guide.md"); ok {
		t.Fatalf("expected traversal to be rejected, got %q", got)
	}
}

// TestResolvePageImagePathRejectsSymlinkEscape links an image directory outside its page root.
// It verifies rejection of an otherwise valid image suffix when symlink
// resolution points to an asset outside the allowed directory.
func TestResolvePageImagePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	pagesDir := filepath.Join(root, "pages")
	base := filepath.Join(pagesDir, "guide")
	outside := filepath.Join(root, "outside")

	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create page dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("create outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "images")); err != nil {
		// This scenario requires an actual symlink escape. Platforms that cannot
		// create the fixture skip it instead of interpreting setup failure as access rejection.
		t.Skipf("symlink not supported: %v", err)
	}

	if got, ok := resolvePageImagePath(pagesDir, base, "images/secret.png"); ok {
		t.Fatalf("expected symlink escape to be rejected, got %q", got)
	}
}

// mustEvalSymlinks resolves a fixture path for exact expected-filename assertions.
// It marks itself as a helper and fails the owning test when fixture
// resolution fails, so comparisons use the filesystem's actual target spelling.
func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()

	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks for %q: %v", path, err)
	}
	return realPath
}
