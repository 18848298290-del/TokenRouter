//go:build embed

package web

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTMLCache starts with an empty cache, publishes a language page,
// invalidates it, and changes serialized settings. Lookups must reflect storage
// and invalidation, while changed settings must produce a different quoted
// ETag.
func TestHTMLCache(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		cache := NewHTMLCache()
		value, _ := cache.Snapshot("en")
		assert.Nil(t, value)
	})
	t.Run("publish and invalidate", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))
		html := []byte("<html><body>test</body></html>")
		_, version := cache.Snapshot("en")
		cache.Publish("en", version, html, []byte(`{"key":"value"}`))
		value, _ := cache.Snapshot("en")
		require.NotNil(t, value)
		assert.Equal(t, html, value.Content)
		assert.True(t, strings.HasPrefix(value.ETag, `"`))
		assert.True(t, strings.HasSuffix(value.ETag, `"`))
		assert.Contains(t, value.ETag[1:len(value.ETag)-1], "-")
		cache.Invalidate()
		value, _ = cache.Snapshot("en")
		assert.Nil(t, value)
	})
	t.Run("settings change etag", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))
		html := []byte("<html>test</html>")
		_, version := cache.Snapshot("en")
		first := cache.Publish("en", version, html, []byte(`{"v":1}`))
		cache.Invalidate()
		_, version = cache.Snapshot("en")
		second := cache.Publish("en", version, html, []byte(`{"v":2}`))
		assert.NotEqual(t, first.ETag, second.ETag)
	})
}

// TestHTMLLanguageIsolation publishes English and Chinese pages with identical
// settings, mutates a snapshot, and invalidates both. Tags and stored content
// must stay language-specific, and stale publication must leave the new cache
// empty.
func TestHTMLLanguageIsolation(t *testing.T) {
	cache := NewHTMLCache()
	cache.SetBaseHTML([]byte("<html></html>"))
	_, version := cache.Snapshot("en")
	english := cache.Publish("en", version, []byte("English"), []byte(`{}`))
	chinese := cache.Publish("zh-Hans", version, []byte("中文"), []byte(`{}`))
	require.NotEqual(t, english.ETag, chinese.ETag)
	saved, _ := cache.Snapshot("en")
	require.Equal(t, "English", string(saved.Content))
	saved.Content[0] = 'X'
	saved, _ = cache.Snapshot("en")
	require.Equal(t, "English", string(saved.Content))
	cache.Invalidate()
	// Reuse the pre-invalidation generation to model an earlier render returning
	// late. Its attempted publication must leave both invalidated language entries absent.
	cache.Publish("en", version, []byte("Late"), []byte(`{}`))
	saved, _ = cache.Snapshot("en")
	require.Nil(t, saved)
	saved, _ = cache.Snapshot("zh-Hans")
	require.Nil(t, saved)
}
