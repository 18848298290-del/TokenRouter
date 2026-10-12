package locale

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContentReview checks that source edits invalidate translations until reviewed again.
func TestContentReview(t *testing.T) {
	zh := "zh-Hans"
	original := Original("原文")
	input := Update[string]{Content: original, ReviewedLocales: []string{"en"}}
	input.SourceLocale = &zh
	input.Translations = map[string]Translation[string]{"en": {Value: "Original"}}
	current, err := Prepare(original, input, func(string) error { return nil })
	require.NoError(t, err)
	value, resolution := current.Resolve("en")
	require.Equal(t, "Original", value)
	require.False(t, resolution.Fallback)
	input = Update[string]{Content: current}
	input.Source = "修改后"
	changed, err := Prepare(current, input, func(string) error { return nil })
	require.NoError(t, err)
	value, resolution = changed.Resolve("en")
	require.Equal(t, "修改后", value)
	require.True(t, resolution.Fallback)
	_, err = Prepare(changed, input, func(string) error { return nil })
	require.ErrorIs(t, err, ErrConflict)
	input = Update[string]{Content: changed, ReviewedLocales: []string{"en"}}
	reviewed, err := Prepare(changed, input, func(string) error { return nil })
	require.NoError(t, err)
	value, resolution = reviewed.Resolve("en")
	require.Equal(t, "Original", value)
	require.False(t, resolution.Fallback)
}

// TestLanguageNegotiation covers weights, excluded languages, and Chinese script variants.
func TestLanguageNegotiation(t *testing.T) {
	for _, tc := range []struct{ header, fallback, want string }{
		{"zh", "en", "zh-Hans"},
		{"en-US;q=0.2,zh-CN;q=0.9", "en", "zh-Hans"},
		{"en-US;q=0,zh-Hans;q=1", "en", "zh-Hans"},
		{"en;q=0.2,zh-CN;q=0.9", "en", "zh-Hans"},
		{"zh;q=0,en;q=1", "zh-Hans", "en"},
		{"zh-TW", "en", "en"},
		{"ja", "zh", "zh-Hans"},
		{"invalid; q=x", "invalid", "en"},
	} {
		require.Equal(t, tc.want, Negotiate(tc.header, tc.fallback), tc.header)
	}
}

// TestUnknownOriginalLanguage checks that legacy source language remains unknown.
func TestUnknownOriginalLanguage(t *testing.T) {
	value, resolution := Original("legacy").Resolve("zh-Hans")
	require.Equal(t, "legacy", value)
	require.Nil(t, resolution.Locale)
	require.True(t, resolution.Fallback)
}

// TestTranslationDeletionRequiresOperation covers partial updates, deletion, and duplicate aliases.
func TestTranslationDeletionRequiresOperation(t *testing.T) {
	zh := "zh-Hans"
	current := Content[string]{SourceLocale: &zh, Source: "中文", Revision: 3, SourceRevision: 2, Translations: map[string]Translation[string]{"en": {Value: "English", SourceRevision: 2}}}
	input := Update[string]{Content: current}
	input.Translations = nil
	retained, err := Prepare(current, input, func(string) error { return nil })
	require.NoError(t, err)
	require.Equal(t, current.Translations, retained.Translations)
	input = Update[string]{Content: retained, DeletedLocales: []string{"en"}}
	input.Translations = nil
	removed, err := Prepare(retained, input, func(string) error { return nil })
	require.NoError(t, err)
	require.Empty(t, removed.Translations)
	require.Len(t, retained.Translations, 1)
	input = Update[string]{Content: removed}
	input.Translations = map[string]Translation[string]{"en": {Value: "a"}, "en-US": {Value: "b"}}
	_, err = Prepare(removed, input, func(string) error { return nil })
	require.Error(t, err)
}

// TestClientCannotForgeReview checks that submitted revision numbers cannot approve a translation.
func TestClientCannotForgeReview(t *testing.T) {
	en := "en"
	current := Content[string]{SourceLocale: &en, Source: "Source", Revision: 2, SourceRevision: 2}
	input := Update[string]{Content: current}
	input.SourceRevision = 100
	input.Translations = map[string]Translation[string]{"zh-Hans": {Value: "译文", SourceRevision: 2}}
	saved, err := Prepare(current, input, func(string) error { return nil })
	require.NoError(t, err)
	actual, result := saved.Resolve("zh")
	require.Equal(t, "Source", actual)
	require.True(t, result.Fallback)
}

// TestErrorMessagesTranslateLegacyChinese checks English output for legacy Chinese errors.
func TestErrorMessagesTranslateLegacyChinese(t *testing.T) {
	require.Equal(t, "Team not found.", ErrorText("en", "TEAM_NOT_FOUND", 404, "团队不存在"))
	require.Equal(t, "团队不存在。", ErrorText("zh", "TEAM_NOT_FOUND", 404, "Team not found"))
	require.Equal(t, "Specific English detail", ErrorText("en", "VALIDATION_ERROR", 400, "Specific English detail"))
	for reason := range errorMessages["zh-Hans"] {
		require.NotEmpty(t, errorMessages["en"][reason], reason)
	}
	for _, value := range []string{"en-@bad", "en--US", "en-US\nInjected: x"} {
		require.Empty(t, Normalize(value))
	}
}

// TestSearchTextsExcludesStaleTranslations checks that search uses source and current translations.
func TestSearchTextsExcludesStaleTranslations(t *testing.T) {
	content := Content[string]{Source: "source", SourceRevision: 2, Translations: map[string]Translation[string]{"en": {Value: "current", SourceRevision: 2}, "zh-Hans": {Value: "stale", SourceRevision: 1}}}
	require.Equal(t, []string{"current", "source"}, SearchTexts(content, func(value string) []string { return []string{value} }))
}

// TestCatalogExtension checks that catalog additions control aliases, direction, and content fallback.
func TestCatalogExtension(t *testing.T) {
	previous := catalog
	t.Cleanup(func() { catalog = previous })
	catalog.Locales = append(append([]Definition(nil), catalog.Locales...), Definition{Code: "ar", Name: "Test RTL", Direction: "rtl", Aliases: []string{"ar-SA"}, Fallbacks: []string{"en"}})
	require.Equal(t, "ar", Negotiate("ar-SA,en;q=0.2", "en"))
	require.Equal(t, "rtl", Direction("ar-SA"))
	en := "en"
	content := Content[string]{SourceLocale: &en, Source: "Fallback content"}
	value, actual := content.Resolve("ar")
	require.Equal(t, "Fallback content", value)
	require.Equal(t, "en", *actual.Locale)
	require.True(t, actual.Fallback)
}
