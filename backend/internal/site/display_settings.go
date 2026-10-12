package site

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// DisplaySettings reads the site name, frontend URL, and custom menus on demand.
type DisplaySettings struct {
	store interface {
		GetValue(context.Context, string) (string, error)
	}
	frontend func() string
}

func NewDisplaySettings(store interface {
	GetValue(context.Context, string) (string, error)
}, frontend func() string,
) *DisplaySettings {
	return &DisplaySettings{store: store, frontend: frontend}
}

func (s *DisplaySettings) GetSiteName(ctx context.Context) string {
	return locale.ReadGroupedText(ctx, s.store, SettingKeySiteTexts, SettingKeySiteName, "TokenRouter")
}

func (s *DisplaySettings) GetFrontendURL(ctx context.Context) string {
	return ReadFrontendURL(ctx, s.store, s.frontend)
}

// GetCustomMenuItemsRaw returns stored menu JSON, or "[]" when the read fails.
func (s *DisplaySettings) GetCustomMenuItemsRaw(ctx context.Context) string {
	value, err := s.store.GetValue(ctx, SettingKeyCustomMenuItems)
	if err != nil {
		return "[]"
	}
	return value
}

// ReadFrontendURL returns the trimmed stored URL, calling fallback when the value is blank or the read fails.
func ReadFrontendURL(ctx context.Context, store interface {
	GetValue(context.Context, string) (string, error)
}, fallback func() string,
) string {
	val, err := store.GetValue(ctx, SettingKeyFrontendURL)
	if err == nil && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return fallback()
}
