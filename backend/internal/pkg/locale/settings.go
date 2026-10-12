package locale

import (
	"context"
	"encoding/json"
)

// TextSettingKey returns the key containing a text setting's localized content.
func TextSettingKey(key string) string { return key + "_localized" }

// ResolveSettingText resolves localized content, then the plain setting, then fallback.
// A valid localized block may intentionally resolve to an empty string.
func ResolveSettingText(values map[string]string, key, fallback, language string) string {
	if raw := values[TextSettingKey(key)]; raw != "" {
		var content Content[string]
		if json.Unmarshal([]byte(raw), &content) == nil {
			value, _ := content.Resolve(language)
			return value
		}
	}
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}

// ReadSettingText reads a text setting in the context language; read failures use fallback values.
func ReadSettingText(ctx context.Context, store interface {
	GetValue(context.Context, string) (string, error)
}, key, fallback string,
) string {
	original, _ := store.GetValue(ctx, key)
	translated, _ := store.GetValue(ctx, TextSettingKey(key))
	return ResolveSettingText(map[string]string{key: original, TextSettingKey(key): translated}, key, fallback, FromContext(ctx))
}

// ReadGroupedText resolves a field in a localized group, then its plain setting, then fallback.
func ReadGroupedText(ctx context.Context, store interface {
	GetValue(context.Context, string) (string, error)
}, group, key, fallback string,
) string {
	values := map[string]Content[string]{}
	raw, _ := store.GetValue(ctx, group)
	if json.Unmarshal([]byte(raw), &values) == nil {
		if content, exists := values[key]; exists {
			value, _ := content.Resolve(FromContext(ctx))
			return value
		}
	}
	if original, err := store.GetValue(ctx, key); err == nil && original != "" {
		return original
	}
	return fallback
}
