package site

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Display names follow the context language, while frontend links and custom
// menus come from current stored settings. Reads occur when consumers request a
// value, so construction leaves storage and the deployment URL callback untouched.

// DisplaySettings reads localized branding, frontend links, and raw menu configuration.
type DisplaySettings struct {
	// store is queried for each requested value; read failures are converted by
	// the corresponding getter to its branding, link, or menu default.
	store interface {
		// GetValue reads one setting or returns an error handled by the getter.
		GetValue(context.Context, string) (string, error)
	}
	// frontend supplies the deployment URL when the stored frontend_url is
	// unavailable or blank; its result is returned exactly as supplied.
	frontend func() string
}

// NewDisplaySettings binds a settings reader and deployment URL callback.
// Neither dependency is invoked during construction. The callback is required
// when frontend_url is blank or its later storage read fails.
func NewDisplaySettings(store interface {
	// GetValue reads a setting for the later display getter call.
	GetValue(context.Context, string) (string, error)
}, frontend func() string,
) *DisplaySettings {
	return &DisplaySettings{store: store, frontend: frontend}
}

// GetSiteName resolves branding for the context language with TokenRouter as default.
// The grouped localized setting takes precedence over the legacy site_name
// value according to locale.ReadGroupedText, which also handles failed reads.
func (s *DisplaySettings) GetSiteName(ctx context.Context) string {
	return locale.ReadGroupedText(ctx, s.store, SettingKeySiteTexts, SettingKeySiteName, "TokenRouter")
}

// GetFrontendURL returns the stored frontend URL or the deployment callback result.
// Stored values are trimmed by ReadFrontendURL, and a blank or failed read
// invokes the callback at the time this getter is called.
func (s *DisplaySettings) GetFrontendURL(ctx context.Context) string {
	return ReadFrontendURL(ctx, s.store, s.frontend)
}

// GetCustomMenuItemsRaw returns the current menu value as supplied by storage.
// Read failures become an empty JSON array; successful empty or whitespace
// strings remain available for the consumer to interpret.
func (s *DisplaySettings) GetCustomMenuItemsRaw(ctx context.Context) string {
	value, err := s.store.GetValue(ctx, SettingKeyCustomMenuItems)
	// Pages interprets an empty array as having no visible entries. Returning
	// that value on failure keeps an unavailable menu from granting page access.
	if err != nil {
		return "[]"
	}
	return value
}

// ReadFrontendURL returns a trimmed nonblank frontend_url from the settings reader.
// A failed read or whitespace-only value calls fallback lazily and returns its
// result unchanged, so the deployment source controls its own normalization.
func ReadFrontendURL(ctx context.Context, store interface {
	// GetValue reads frontend_url before the deployment callback is considered.
	GetValue(context.Context, string) (string, error)
}, fallback func() string,
) string {
	val, err := store.GetValue(ctx, SettingKeyFrontendURL)
	if err == nil && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	// A missing usable stored link leaves the deployment callback authoritative;
	// invoke it here so consumers can observe deployment values changed after construction.
	return fallback()
}
