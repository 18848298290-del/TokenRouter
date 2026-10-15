package locale

import (
	"context"
	"encoding/json"
)

// settings.go resolves localized setting text while distinguishing saved empty
// content from missing settings.
// An accepted localized text block can deliberately contain an empty source or
// translation. Successful JSON decoding therefore establishes presence
// independently of the chosen text, and ResolveSettingText returns that text
// before consulting the plain setting or caller default. Corrupt or absent
// localized data allows those later choices.
//
// ReadSettingText obtains the plain and localized keys separately and delegates
// selection using the context language. ReadGroupedText first checks a named
// member of a decoded content group, then reads the plain key. Storage errors
// are suppressed according to each helper's read path, so callers receive a
// display value rather than a storage error.

// TextSettingKey appends the localized-content suffix to a plain text setting
// key.
func TextSettingKey(key string) string { return key + "_localized" }

// ResolveSettingText selects a decoded localized block, the nonempty plain
// setting, or fallback. A successfully decoded Content[string] owns the choice
// even when it resolves to an empty string, so intentional emptiness suppresses
// later defaults. JSON null and an empty object also decode successfully and
// select empty text.
//
// Missing or malformed localized JSON allows the plain key to supply text.
// Empty plain text then selects fallback. The language is interpreted by
// Content.Resolve, which may return the source when no current translation
// matches the requested locale candidates.
func ResolveSettingText(values map[string]string, key, fallback, language string) string {
	if raw := values[TextSettingKey(key)]; raw != "" {
		var content Content[string]
		if json.Unmarshal([]byte(raw), &content) == nil {
			value, _ := content.Resolve(language)
			return value
		}
	}
	// Missing or corrupt localized data has no accepted content block. A nonempty
	// plain value remains usable before the caller's display default is selected.
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}

// ReadSettingText reads plain and localized values separately and resolves them
// in the context language. Read errors are discarded, but any companion value
// returned by GetValue still participates in ResolveSettingText selection.
func ReadSettingText(ctx context.Context, store interface {
	// GetValue reads a setting by key using the supplied context. The text
	// consumer decides whether to use a companion value when this read reports an error.
	GetValue(context.Context, string) (string, error)
}, key, fallback string,
) string {
	// Rendering still needs a display value when storage reports an error. Both
	// reads discard errors and let returned text participate in normal content selection.
	original, _ := store.GetValue(ctx, key)
	translated, _ := store.GetValue(ctx, TextSettingKey(key))
	return ResolveSettingText(map[string]string{key: original, TextSettingKey(key): translated}, key, fallback, FromContext(ctx))
}

// ReadGroupedText resolves a named content member of group before the plain key
// and fallback. A decoded member is present even when its selected text is
// empty, and a missing member permits the plain setting to supply the display
// value.
//
// The group read error is discarded while its returned text is still decoded.
// Plain-key text is accepted only on a successful read and when nonempty.
// Absent, corrupt, or failed usable values therefore select fallback; the store
// must be callable for both requested keys.
func ReadGroupedText(ctx context.Context, store interface {
	// GetValue reads a setting by key using the supplied context. The text
	// consumer decides whether to use a companion value when this read reports an error.
	GetValue(context.Context, string) (string, error)
}, group, key, fallback string,
) string {
	values := map[string]Content[string]{}
	// A group read can report an error alongside text. Decode the returned text
	// first, then consult the plain setting when no accepted group member exists.
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
