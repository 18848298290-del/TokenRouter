//go:build embed

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// HTMLCache manages the cached index.html with injected settings.
type HTMLCache struct {
	mu              sync.RWMutex
	entries         map[string]CachedHTML
	baseHTMLHash    string // Hash of the base template supplied by SetBaseHTML.
	settingsVersion uint64 // Generation advanced by Invalidate.
}

// CachedHTML contains rendered HTML and its language-dependent ETag.
type CachedHTML struct {
	Content []byte
	ETag    string
}

// NewHTMLCache creates a new HTML cache instance.
func NewHTMLCache() *HTMLCache {
	return &HTMLCache{}
}

// SetBaseHTML sets the template hash; call it before publishing entries.
func (c *HTMLCache) SetBaseHTML(baseHTML []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hash := sha256.Sum256(baseHTML)
	c.baseHTMLHash = hex.EncodeToString(hash[:8]) // First 8 bytes for brevity
}

// Invalidate marks the cache as stale.
func (c *HTMLCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = nil
	c.settingsVersion++
}

// Snapshot returns an owned copy of a language entry and the current invalidation generation under one lock.
func (c *HTMLCache) Snapshot(code string) (*CachedHTML, uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot, ok := c.entries[code]
	if !ok {
		return nil, c.settingsVersion
	}
	return &CachedHTML{Content: append([]byte(nil), snapshot.Content...), ETag: snapshot.ETag}, c.settingsVersion
}

// Publish returns rendered HTML with an ETag incorporating language and settings.
// It stores a copy only if version still matches the current invalidation generation.
// The returned Content shares the caller's html slice, even when publication is skipped.
func (c *HTMLCache) Publish(code string, version uint64, html, settingsJSON []byte) CachedHTML {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := append([]byte(code+"\n"), settingsJSON...)
	rendered := CachedHTML{Content: html, ETag: c.generateETag(key)}
	if version == c.settingsVersion {
		if c.entries == nil {
			c.entries = map[string]CachedHTML{}
		}
		c.entries[code] = CachedHTML{Content: append([]byte(nil), html...), ETag: rendered.ETag}
	}
	return rendered
}

// generateETag combines the base template hash with the hash of the supplied language and settings bytes.
func (c *HTMLCache) generateETag(settingsJSON []byte) string {
	settingsHash := sha256.Sum256(settingsJSON)
	return `"` + c.baseHTMLHash + "-" + hex.EncodeToString(settingsHash[:8]) + `"`
}
