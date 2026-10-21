import messages from '../../../backend/internal/pkg/locale/error_messages.json'
import { getLocale } from './index'
import { normalizeLocale, defaultLocale } from './catalog'

// errors.ts selects browser error messages from the shared server dictionary and
// preserves response text according to the request path. Administrator content
// editing errors are looked up in the active interface dictionary, while other
// nonblank server messages remain available as supplied by the response.

// localizedErrorMessage chooses a reason message in the current supported
// interface language. It then tries the supplied original text, the HTTP status
// entry, and the generic failure message; non-string reasons skip reason lookup.
export function localizedErrorMessage(reason: unknown, status: number, original = ''): string {
  const code = typeof reason === 'string' ? reason.toUpperCase() : ''
  const language = normalizeLocale(getLocale()) || defaultLocale
  // Use the English dictionary when the normalized application language has no
  // bundled error map, keeping generic HTTP errors readable.
  const dictionary = (messages as Record<string, Record<string, string>>)[language] || messages.en
  // Prefer the reason-specific explanation, then supplied response text, so
  // status messages replace details only when no more specific text exists.
  return dictionary[code] || original || dictionary[`HTTP_${status}`] || dictionary.REQUEST_FAILED
}

// responseErrorMessage preserves nonblank response text except for recognized
// administrator localization errors. Those errors use localizedErrorMessage so
// content editors see the active interface language; empty response text follows
// the same dictionary lookup.
export function responseErrorMessage(
  path: string,
  reason: unknown,
  status: number,
  original = '',
): string {
  const code = typeof reason === 'string' ? reason.toUpperCase() : ''
  const contentError =
    /^(LOCALIZATION_|SOURCE_LOCALE_|INVALID_LOCALE|INVALID_TRANSLATION_|DUPLICATE_LOCALE|UNKNOWN_LOCALIZED_|LOCALIZED_|SITE_NAME_REQUIRED|LEGAL_|NAVIGATION_LABEL_|INVALID_LINK_URL|DUPLICATE_CONTENT_ID|LOCALIZED_PAGE_|ENDPOINT_NAME_REQUIRED|PLAN_NAME_REQUIRED|PLAN_TEXT_TOO_LONG|GROUP_DISPLAY_|GROUP_DESCRIPTION_|MODEL_DISPLAY_|INVALID_BLOCK_MESSAGE|INVALID_CUSTOM_MESSAGE|PAYMENT_METHOD_NAME_REQUIRED)/.test(
      code,
    )
  if (original.trim() && (!path.includes('/admin/') || !contentError)) return original
  return localizedErrorMessage(reason, status, original)
}
