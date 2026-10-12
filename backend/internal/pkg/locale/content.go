package locale

import (
	"encoding/json"
	"reflect"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ErrConflict indicates that the submitted revision differs from the saved revision.
var ErrConflict = apperror.Conflict("LOCALIZATION_CONFLICT", "Content changed. Reload before saving.")

// Translation pairs translated content with the source revision it was reviewed against.
type Translation[T any] struct {
	Value          T     `json:"value"`
	SourceRevision int64 `json:"source_revision"`
}

// Content stores source content and complete translations by language.
// @project-doc docs/interfaces/user_localization.md#localized_content
type Content[T any] struct {
	SourceLocale   *string                   `json:"source_locale"`
	Source         T                         `json:"source"`
	Translations   map[string]Translation[T] `json:"translations"`
	Revision       int64                     `json:"revision"`
	SourceRevision int64                     `json:"source_revision"`
}

// Update includes the revision read by the editor and the languages reviewed or deleted.
type Update[T any] struct {
	Content[T]
	ReviewedLocales []string `json:"reviewed_locales,omitempty"`
	DeletedLocales  []string `json:"deleted_locales,omitempty"`
}

// Resolution identifies the selected language; nil means the source language is unknown.
type Resolution struct {
	Locale   *string `json:"locale"`
	Fallback bool    `json:"fallback"`
}

// TextUpdates groups edits to independent text fields.
type TextUpdates map[string]Update[string]

// TextContent gives schema generation a named string-content type.
type TextContent Content[string]

// Original wraps legacy content whose source language is unknown.
func Original[T any](value T) Content[T] {
	return Content[T]{Source: value, Translations: map[string]Translation[T]{}}
}

// Resolve selects source or current reviewed translations in locale candidate order.
// It returns the source when no candidate matches.
func (c Content[T]) Resolve(code string) (T, Resolution) {
	for _, candidate := range Candidates(code) {
		if c.SourceLocale != nil && Normalize(*c.SourceLocale) == candidate {
			return c.Source, Resolution{Locale: c.SourceLocale, Fallback: candidate != Normalize(code)}
		}
		if translated, ok := c.Translations[candidate]; ok && translated.SourceRevision == c.SourceRevision {
			return translated.Value, Resolution{Locale: &candidate, Fallback: candidate != Normalize(code)}
		}
	}
	return c.Source, Resolution{Locale: c.SourceLocale, Fallback: c.SourceLocale == nil || Normalize(*c.SourceLocale) != Normalize(code)}
}

// Prepare validates an update and advances its revisions.
// Persistence must still compare the previous Revision atomically when saving.
func Prepare[T any](current Content[T], input Update[T], validate func(T) error) (Content[T], error) {
	if current.Revision != input.Revision {
		return current, ErrConflict
	}
	next := input.Content
	if next.SourceLocale != nil {
		code := Normalize(*next.SourceLocale)
		if code == "" {
			return current, apperror.BadRequest("INVALID_LOCALE", "Language is not supported.")
		}
		next.SourceLocale = &code
	}
	changed := !reflect.DeepEqual(current.Source, next.Source) || !reflect.DeepEqual(current.SourceLocale, next.SourceLocale)
	if next.SourceLocale == nil {
		if changed || current.Revision == 0 {
			return current, apperror.BadRequest("SOURCE_LOCALE_REQUIRED", "Choose the original language.")
		}
	}
	if err := validate(next.Source); err != nil {
		return current, err
	}
	next.SourceRevision = current.SourceRevision
	if changed || next.SourceRevision == 0 {
		next.SourceRevision++
	}
	next.Translations = make(map[string]Translation[T], len(current.Translations)+len(input.Translations))
	deleted := map[string]bool{}
	for _, raw := range input.DeletedLocales {
		code := Normalize(raw)
		if code == "" {
			return current, apperror.BadRequest("INVALID_LOCALE", "Language is not supported.")
		}
		deleted[code] = true
	}
	// Omitted translations are retained; deletion requires a separate locale entry.
	for code, translation := range current.Translations {
		if !deleted[code] {
			next.Translations[code] = translation
		}
	}
	seen := map[string]bool{}
	reviewed := map[string]bool{}
	for _, code := range input.ReviewedLocales {
		code = Normalize(code)
		if code == "" {
			return current, apperror.BadRequest("INVALID_LOCALE", "Language is not supported.")
		}
		reviewed[code] = true
	}
	for raw, translation := range input.Translations {
		code := Normalize(raw)
		if code == "" || next.SourceLocale != nil && code == *next.SourceLocale {
			return current, apperror.BadRequest("INVALID_TRANSLATION_LOCALE", "Translation language must differ from the original.")
		}
		if seen[code] || deleted[code] {
			return current, apperror.BadRequest("DUPLICATE_LOCALE", "Translation language is duplicated.")
		}
		seen[code] = true
		if err := validate(translation.Value); err != nil {
			return current, err
		}
		previous, existed := current.Translations[code]
		translation.SourceRevision = previous.SourceRevision
		if !existed || !reflect.DeepEqual(previous.Value, translation.Value) {
			translation.SourceRevision = -1
		}
		if reviewed[code] {
			translation.SourceRevision = next.SourceRevision
		}
		next.Translations[code] = translation
	}
	for code := range next.Translations {
		if next.SourceLocale != nil && code == *next.SourceLocale {
			return current, apperror.BadRequest("INVALID_TRANSLATION_LOCALE", "Translation language must differ from the original.")
		}
	}
	next.Revision = current.Revision + 1
	// A JSON round trip detaches nested content from caller-owned references.
	data, err := json.Marshal(next)
	if err != nil {
		return current, err
	}
	var result Content[T]
	if err := json.Unmarshal(data, &result); err != nil {
		return current, err
	}
	return result, nil
}

// SearchTexts returns sorted, distinct source and current-translation text for search.
func SearchTexts[T any](content Content[T], fields func(T) []string) []string {
	seen := map[string]bool{}
	for _, value := range fields(content.Source) {
		if value != "" {
			seen[value] = true
		}
	}
	for _, translation := range content.Translations {
		if translation.SourceRevision != content.SourceRevision {
			continue
		}
		for _, value := range fields(translation.Value) {
			if value != "" {
				seen[value] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
