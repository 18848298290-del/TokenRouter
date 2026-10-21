import { availableLocales, normalizeLocale } from './catalog'

// content.ts represents versioned operating content and chooses the source or a
// reviewed translation for display. Editors keep an overall revision for saving
// and a source revision for determining whether translated values still describe
// the current source. Draft review and deletion lists also influence previews
// before the server assigns revisions.
//
// Resolution follows the manifest language candidates and gives a matching
// source precedence over a translation in the same language. Missing, deleted,
// or stale translations lead to another candidate or the original content.
// Values retain their supplied references, so callers that edit structured
// content use the draft editing helpers to obtain separate JSON data.

// Translation pairs translated content with the source revision approved by its
// reviewer.
export interface Translation<T> {
  value: T // translated content value
  // source_revision identifies the source version reviewed for this translation;
  // previews also accept languages approved in the current draft.
  source_revision: number
}

// Localized stores source content, translations, and separate persistence and
// source review revisions.
export interface Localized<T> {
  // source_locale identifies the original language; null retains legacy content
  // whose language an administrator has not yet chosen.
  source_locale: string | null
  // source, translations, and source_revision determine the value displayed by
  // resolveContent. A translation with the matching source revision is current
  // unless the draft deletes it.
  source: T // original content value
  translations: Record<string, Translation<T>> // translations by locale code
  // revision is the saved edit version sent back to the server for its
  // optimistic persistence conflict check.
  revision: number
  source_revision: number // current source review version
}

// LocalizedUpdate adds pending translation review and deletion decisions to
// editable content.
export interface LocalizedUpdate<T> extends Localized<T> {
  // reviewed_locales marks languages approved against the draft source; an empty
  // list records invalidation after a source edit.
  reviewed_locales?: string[]
  // deleted_locales requests removal of saved translations and excludes matching
  // languages from preview selection before the update is persisted.
  deleted_locales?: string[]
}

// originalContent wraps a source value with zero revisions and an empty
// translation map. The source is retained by reference, and the language
// defaults to null for content whose original language is unknown.
export function originalContent<T>(source: T, sourceLocale: string | null = null): Localized<T> {
  return { source_locale: sourceLocale, source, translations: {}, revision: 0, source_revision: 0 }
}

// resolveContent selects the first usable source or translation for the
// requested language and reports the language actually returned. The returned
// value shares its data with the supplied content, and fallback reports a
// different or unknown selected language.
//
// Manifest candidates determine search order. A matching source wins within each
// candidate; translations require a matching source revision or draft review,
// and draft deletion takes precedence over either approval. When no candidate
// qualifies, the original value remains available even if its language is
// unknown.
export function resolveContent<T>(
  content: LocalizedUpdate<T>,
  requested: string,
): {
  value: T // selected content value
  // locale identifies the selected source or translation; null means the returned
  // original still has no known language for display or fallback comparison.
  locale: string | null
  // fallback reports an unknown selected language or one differing from the
  // normalized request, even when the returned content value is empty.
  fallback: boolean
} {
  const code = normalizeLocale(requested)
  const definition = availableLocales.find((item) => item.code === code)
  // Consider the source before translations for each candidate so duplicate
  // draft entries cannot replace the original language value.
  for (const candidate of definition ? [definition.code, ...definition.fallbacks] : []) {
    if (normalizeLocale(content.source_locale) === candidate) {
      return { value: content.source, locale: content.source_locale, fallback: candidate !== code }
    }
    const translation = content.translations[candidate]
    // A pending deletion suppresses even reviewed translations, keeping preview
    // output consistent with the removal submitted by the editor.
    if (
      translation &&
      !content.deleted_locales?.includes(candidate) &&
      (content.reviewed_locales?.includes(candidate) ||
        translation.source_revision === content.source_revision)
    ) {
      return { value: translation.value, locale: candidate, fallback: candidate !== code }
    }
  }
  // Exhausted or unsupported language candidates return original content so a
  // missing translation still leaves the configured value visible.
  return {
    value: content.source,
    locale: content.source_locale,
    fallback: !content.source_locale || normalizeLocale(content.source_locale) !== code,
  }
}

// contentSequence distinguishes locally generated identifiers when UUID support
// is unavailable and calls share the same clock reading.
let contentSequence = 0

// newContentID generates identifiers used to match translations across content
// list reordering. It removes UUID separators when browser crypto supports
// UUIDs; otherwise, a timestamp, process-local sequence, and random suffix
// distinguish generated entries.
export function newContentID(): string {
  // Browsers lacking UUID support combine clock time with a sequence so equal
  // timestamps and random values still distinguish adjacent calls.
  return (
    globalThis.crypto?.randomUUID?.().replace(/-/g, '') ||
    `${Date.now().toString(36)}-${(++contentSequence).toString(36)}-${Math.random().toString(36).slice(2, 10)}`
  )
}
