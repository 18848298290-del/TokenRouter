import type { LocalizedUpdate } from './content'

// contentEdit.ts edits localized content drafts and tracks which translations
// remain eligible for previews and saving. Source edits invalidate existing
// approvals, while translation edits record a review decision for the server to
// apply against the next source revision. Helpers clone JSON data before
// changing a draft, except documented no-op cases that return the input. Locale
// codes are used as supplied; server validation checks supported languages.
//
// Changing the source language requires distinguishing an unlabeled legacy
// source from a known-language source. Labeling legacy content can retain
// current translations because the original value remains the same. Promoting a
// translation also moves the old original into its language slot, updates
// deletion requests, and clears approvals so every retained translation can be
// reviewed against the promoted source.

// TranslationStatus identifies a usable translation, an outdated translation
// needing review, or an absent translation. The translated, stale, and missing
// literals map to these three editor states respectively.
export type TranslationStatus = 'translated' | 'stale' | 'missing'

// copy clones a draft through JSON serialization for independent editing of JSON
// data. Values must support that round trip; serialization errors propagate and
// special JavaScript values follow JSON conversion rules.
function copy<T>(content: LocalizedUpdate<T>): LocalizedUpdate<T> {
  return JSON.parse(JSON.stringify(content)) as LocalizedUpdate<T>
}

// withLocale returns a deduplicated list containing the supplied locale code. An
// omitted list is treated as empty, and existing entries retain their insertion
// order in the returned array.
function withLocale(list: string[] | undefined, code: string): string[] {
  return [...new Set([...(list || []), code])]
}

// withoutLocale returns the supplied list with every matching locale code
// removed. An omitted list produces an empty array, and the input array is left
// available to the caller.
function withoutLocale(list: string[] | undefined, code: string): string[] {
  return (list || []).filter((item) => item !== code)
}

// invalidateTranslations clears draft approvals and marks every existing
// translation as unreviewed in place. Callers apply it to their editable copy
// after changing source content or its language so preview selection requires
// renewed review.
function invalidateTranslations<T>(content: LocalizedUpdate<T>): void {
  // Clear approvals before marking translations stale so a previous review
  // cannot keep translated content visible after the source changes.
  content.reviewed_locales = []
  for (const translation of Object.values(content.translations)) translation.source_revision = -1
}

// labelSourceLocale assigns the source language and determines which previous
// approvals survive that label change. Callers provide the previous content for
// status comparison and an independently editable next draft whose source value
// is unchanged.
//
// For an unknown previous language, translations already considered current
// remain reviewed after labeling. A previously labeled source clears every
// approval when this helper runs. Status is captured before
// invalidation, then reapplied after updating the language, so invalidation
// cannot erase the evidence used to retain eligible translations.
function labelSourceLocale<T>(
  previous: LocalizedUpdate<T>,
  next: LocalizedUpdate<T>,
  code: string,
): void {
  // Read current statuses before invalidating the next draft, preserving
  // approval evidence when only an unknown source language receives its first
  // label.
  const valid = previous.source_locale
    ? []
    : Object.keys(next.translations).filter(
        (item) => translationStatus(previous, item) === 'translated',
      )
  next.source_locale = code
  invalidateTranslations(next)
  next.reviewed_locales = valid
}

// withDefaultSource labels unsaved content when a nonempty default language is
// available and has no translation slot. It returns the input for all other
// cases; a changed result is a shallow copy that shares the source and
// translations.
export function withDefaultSource<T>(
  content: LocalizedUpdate<T>,
  fallbackLocale?: string,
): LocalizedUpdate<T> {
  if (
    content.revision !== 0 ||
    content.source_locale ||
    !fallbackLocale ||
    fallbackLocale in content.translations
  )
    return content
  return { ...content, source_locale: fallbackLocale }
}

// updateSource returns a JSON-cloned draft containing the supplied source value
// and invalidated translation reviews. Unknown source languages adopt
// fallbackLocale only when that language has no translation slot; the newly
// supplied value itself is retained by reference.
export function updateSource<T>(
  content: LocalizedUpdate<T>,
  value: T,
  fallbackLocale?: string,
): LocalizedUpdate<T> {
  const next = copy(content)
  next.source = value
  // A default language already occupied by a translation needs an administrator
  // decision, so keep the original language unknown until that conflict is
  // resolved.
  if (!next.source_locale && fallbackLocale && !(fallbackLocale in next.translations))
    next.source_locale = fallbackLocale
  invalidateTranslations(next)
  return next
}

// updateTranslation returns a JSON-cloned draft with the supplied translation
// marked as reviewed. The supplied value is retained by reference, and an
// existing deletion request remains until the caller clears it through
// addTranslation.
export function updateTranslation<T>(
  content: LocalizedUpdate<T>,
  code: string,
  value: T,
): LocalizedUpdate<T> {
  const next = copy(content)
  next.translations[code] = { value, source_revision: -1 }
  next.reviewed_locales = withLocale(next.reviewed_locales, code)
  return next
}

// markStillValid records a locale as reviewed on a JSON-cloned draft. It leaves
// translation values unchanged and does not require the locale to already have a
// translation entry.
export function markStillValid<T>(content: LocalizedUpdate<T>, code: string): LocalizedUpdate<T> {
  const next = copy(content)
  next.reviewed_locales = withLocale(next.reviewed_locales, code)
  return next
}

// addTranslation creates a reviewed translation initialized from a separate JSON
// copy of the source. It clears any pending deletion for that locale and
// replaces an existing translation slot when present.
export function addTranslation<T>(content: LocalizedUpdate<T>, code: string): LocalizedUpdate<T> {
  const next = copy(content)
  // Clear the pending deletion before recreating this translation so the
  // submitted draft does not request creation and deletion for one locale.
  next.deleted_locales = withoutLocale(next.deleted_locales, code)
  next.translations[code] = { value: copy(content).source, source_revision: -1 }
  next.reviewed_locales = withLocale(next.reviewed_locales, code)
  return next
}

// removeTranslation deletes a translation from a JSON-cloned draft and records
// the deletion for saving. It also removes the locale from pending reviews,
// including when no translation currently exists.
export function removeTranslation<T>(
  content: LocalizedUpdate<T>,
  code: string,
): LocalizedUpdate<T> {
  const next = copy(content)
  // Remove the local value and retain its deletion request so omission cannot
  // make the server keep the saved translation.
  delete next.translations[code]
  next.deleted_locales = withLocale(next.deleted_locales, code)
  next.reviewed_locales = withoutLocale(next.reviewed_locales, code)
  return next
}

// setSourceLocale labels a JSON-cloned source unless that language already has a
// translation. A conflict returns the input unchanged; a successful label change
// applies the legacy or known-language review rules in labelSourceLocale.
export function setSourceLocale<T>(
  content: LocalizedUpdate<T>,
  code: string,
): {
  content: LocalizedUpdate<T> // resulting or unchanged draft
  // conflict reports an existing translation in the requested source language;
  // content then remains the same object supplied by the caller.
  conflict: boolean
} {
  if (code in content.translations) return { content, conflict: true }
  const next = copy(content)
  labelSourceLocale(content, next, code)
  return { content: next, conflict: false }
}

// adoptSourceLocale chooses a source language and removes any translation
// occupying the same language slot. It returns a JSON-cloned draft and applies
// labelSourceLocale so current translations can survive labeling an
// unknown-language source.
export function adoptSourceLocale<T>(
  content: LocalizedUpdate<T>,
  code: string,
): LocalizedUpdate<T> {
  const next = code in content.translations ? removeTranslation(content, code) : copy(content)
  labelSourceLocale(content, next, code)
  return next
}

// promoteToOriginal replaces the original value with a selected translation and
// returns the input when that translation is absent. A successful promotion
// operates on JSON copies and makes every retained translation require review
// against the promoted source.
//
// When the previous source language is known, its original value moves into a
// translation slot and any pending deletion for that slot is cleared. The
// promoted slot is deleted and recorded for server removal. This ordering lets a
// later promotion in the same draft restore a language as a translation without
// also requesting its deletion.
export function promoteToOriginal<T>(
  content: LocalizedUpdate<T>,
  code: string,
): LocalizedUpdate<T> {
  const translation = content.translations[code]
  if (!translation) return content
  const next = copy(content)
  // Restore the previous source as a translation before replacing it, clearing
  // any deletion left by an earlier promotion in this draft.
  if (next.source_locale) {
    next.translations[next.source_locale] = { value: next.source, source_revision: -1 }
    next.deleted_locales = withoutLocale(next.deleted_locales, next.source_locale)
  }
  // Move the selected translation into the source and request removal of its old
  // slot before invalidating every retained translation review.
  next.source = copy(content).translations[code].value
  next.source_locale = code
  delete next.translations[code]
  next.deleted_locales = withLocale(next.deleted_locales, code)
  invalidateTranslations(next)
  return next
}

// translationStatus reports whether a translation exists and matches the current
// source or a draft review. It ignores pending deletion lists, so display
// selection uses resolveContent to account for deletion requests.
export function translationStatus<T>(content: LocalizedUpdate<T>, code: string): TranslationStatus {
  const translation = content.translations[code]
  if (!translation) return 'missing'
  if (content.reviewed_locales?.includes(code)) return 'translated'
  return translation.source_revision === content.source_revision ? 'translated' : 'stale'
}

// translationSummary lists stored translation codes and reports whether any
// translation needs review. It also flags an unknown source language once
// reviewed_locales exists, including an empty list created by a source edit.
export function translationSummary<T>(content: LocalizedUpdate<T>): {
  locales: string[] // existing translation locale codes
  stale: boolean // any translation needs review
  // sourceLocaleMissing reports an unlabeled source once a review list exists,
  // indicating a draft that requires a source-language choice.
  sourceLocaleMissing: boolean
} {
  const locales = Object.keys(content.translations)
  return {
    locales,
    stale: locales.some((code) => translationStatus(content, code) === 'stale'),
    sourceLocaleMissing: !content.source_locale && content.reviewed_locales !== undefined,
  }
}
