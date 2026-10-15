package locale

import (
	"encoding/json"
	"reflect"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// content.go prepares localized content edits and resolves reviewed
// translations for display.
// A translation is eligible for display only when its review revision equals
// the current source revision. Content edits keep an overall revision for
// optimistic persistence checks and a separate source revision for translation
// freshness. Resolve follows the requested locale and its catalog fallbacks
// before returning the source.
//
// Prepare validates an editor update, retains omitted translations, and applies
// deletions named by the editor. Source changes advance the source revision;
// submitted revision numbers cannot approve a translation, so ReviewedLocales
// records that decision. The result is reconstructed through JSON, and the
// storage owner still needs an atomic comparison against the previously read
// overall revision when saving it.

// ErrConflict reports an outdated editor revision so persistence callers can
// reject a stale localization update and ask the editor to reload.
var ErrConflict = apperror.Conflict("LOCALIZATION_CONFLICT", "Content changed. Reload before saving.")

// Translation pairs a language-specific value with the source revision approved
// by its reviewer.
type Translation[T any] struct {
	Value T `json:"value"` // typed translated content value
	// SourceRevision records reviewer approval. Resolve and SearchTexts include
	// this value only when the content's current source revision matches it.
	SourceRevision int64 `json:"source_revision"`
}

// Content stores typed source content, translations, and separate edit and
// source revisions.
// @project-doc docs/interfaces/user_localization.md#localized_content
type Content[T any] struct {
	// SourceLocale names the source language; nil represents a legacy source whose
	// language is unknown. Prepare requires a language when creating or changing it.
	SourceLocale *string `json:"source_locale"`
	// Source, Translations, and SourceRevision determine what Resolve can display.
	// A translation becomes current when its review revision equals SourceRevision;
	// a changed source advances that revision and can make retained translations stale.
	Source       T                         `json:"source"`       // typed source content value
	Translations map[string]Translation[T] `json:"translations"` // values by canonical locale
	// Revision is the edit version checked against an editor update. Persistence
	// compares the previous value atomically before publishing the prepared edit.
	Revision       int64 `json:"revision"`
	SourceRevision int64 `json:"source_revision"` // current source review version
}

// Update carries an editor's content state, expected revision, and translation
// review or deletion requests.
type Update[T any] struct {
	Content[T] // submitted content and revisions
	// ReviewedLocales approves submitted translations against the prepared source
	// revision. Entries are normalized before Prepare assigns their review versions.
	ReviewedLocales []string `json:"reviewed_locales,omitempty"`
	// DeletedLocales removes retained translations by normalized code. Submitting
	// a translation for the same normalized code is rejected as a duplicate operation.
	DeletedLocales []string `json:"deleted_locales,omitempty"`
}

// Resolution identifies the language selected for display and whether selection
// used another candidate.
type Resolution struct {
	// Locale identifies the selected source or translation. Nil means the returned
	// source has no known language, and source resolution can share its pointer.
	Locale *string `json:"locale"`
	// Fallback reports an unknown source language or a selected canonical language
	// differing from the normalized request, even when the returned text is empty.
	Fallback bool `json:"fallback"`
}

// TextUpdates maps independent text-setting names to their submitted
// localization updates.
type TextUpdates map[string]Update[string]

// TextContent names string content for schema generation and API type
// references.
type TextContent Content[string]

// Original wraps a legacy source value with unknown language and an empty
// translation map. The value is retained as supplied, including its referenced
// data, and both revisions begin at zero.
func Original[T any](value T) Content[T] {
	return Content[T]{Source: value, Translations: map[string]Translation[T]{}}
}

// Resolve selects the first source or current translation available in
// Candidates(code) order. It returns the source after exhausting candidates and
// reports whether the actual locale differs from the normalized request or
// remains unknown.
//
// The source takes precedence over a translation for the same candidate. A
// translation participates only when its SourceRevision equals the content's
// SourceRevision. Returned values can share mutable data with c, and source
// resolution also shares c.SourceLocale; callers copy these values before
// mutating them independently.
func (c Content[T]) Resolve(code string) (T, Resolution) {
	for _, candidate := range Candidates(code) {
		if c.SourceLocale != nil && Normalize(*c.SourceLocale) == candidate {
			return c.Source, Resolution{Locale: c.SourceLocale, Fallback: candidate != Normalize(code)}
		}
		if translated, ok := c.Translations[candidate]; ok && translated.SourceRevision == c.SourceRevision {
			return translated.Value, Resolution{Locale: &candidate, Fallback: candidate != Normalize(code)}
		}
	}
	// No candidate supplies a current translation or matching source. Return the
	// source with its known language so display consumers can identify the fallback.
	return c.Source, Resolution{Locale: c.SourceLocale, Fallback: c.SourceLocale == nil || Normalize(*c.SourceLocale) != Normalize(code)}
}

// Prepare validates the editor's expected revision and returns a revised
// content value, or current on error. validate must be callable for the source
// and each submitted translation and leave those values unchanged. Source-change
// comparison precedes source validation; new or changed source content requires
// a supported nonnil language.
//
// Source value or language changes advance SourceRevision, while every
// successful edit advances Revision. Omitted translations survive unless named
// in DeletedLocales. Submitted translations inherit their previous review
// revision, become unreviewed when their value changes, and receive the current
// source revision only through ReviewedLocales. Alias collisions and
// translations in the source language are rejected.
//
// The accepted content is encoded and decoded through JSON. T must support that
// round trip; custom JSON methods determine their own representation and
// allocation behavior. The caller persists the result with an atomic check of
// current.Revision, because this in-memory comparison alone cannot exclude
// another writer.
//
// Callers keep current immutable while preparing input. Mutable values can
// still be shared with input and passed to callbacks; this helper cannot undo
// mutations made by validate or custom JSON methods.
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
	// Partial editor input cannot imply deletion. Carry saved translations into
	// the next map unless the editor names their normalized language for removal.
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
		// Client revision fields cannot approve content. Start from saved approval,
		// mark new or edited values stale, then apply the editor's review operation.
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
	// Build the accepted JSON representation before returning a prepared edit.
	// Ordinary JSON fields are reconstructed on decode; custom methods control T's
	// allocation behavior, and either serialization error retains current content.
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

// SearchTexts returns sorted, distinct nonempty text extracted from the source
// and current translations. fields must be callable for each included value;
// an empty result is an allocated slice, and strings retain supplied whitespace.
//
// The source contributes regardless of its known language. A translation
// contributes only when its reviewed SourceRevision equals the content's
// current SourceRevision. Advancing that revision after a source edit excludes
// older translated terms from search while those translations remain stored.
// Duplicate text across the accepted values appears once.
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
