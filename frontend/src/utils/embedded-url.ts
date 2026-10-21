/**
 * embedded-url.ts adds iframe context parameters to a supplied absolute URL.
 * Existing query parameters survive unless this helper replaces one of its own
 * context keys, and invalid input is returned unchanged for the caller to
 * handle. This builder passes optional authentication data through the query
 * and does not validate destination trust.
 */

/**
 * Embedded query names carry optional account identity and locale together
 * with theme, embedded mode, and the current source-page URL.
 */
const EMBEDDED_USER_ID_QUERY_KEY = 'user_id'
const EMBEDDED_AUTH_TOKEN_QUERY_KEY = 'token'
const EMBEDDED_THEME_QUERY_KEY = 'theme'
const EMBEDDED_LANG_QUERY_KEY = 'lang'
const EMBEDDED_UI_MODE_QUERY_KEY = 'ui_mode'
const EMBEDDED_UI_MODE_VALUE = 'embedded'
const EMBEDDED_SRC_HOST_QUERY_KEY = 'src_host'
const EMBEDDED_SRC_QUERY_KEY = 'src_url'

/**
 * buildEmbeddedUrl adds user, theme, locale, and source-page context to a
 * parseable absolute URL. Empty optional values are omitted, invalid URLs
 * return unchanged, and callers must approve any destination receiving the
 * optional token.
 */
export function buildEmbeddedUrl(
  baseUrl: string,
  userId?: number,
  authToken?: string | null,
  theme: 'light' | 'dark' = 'light',
  lang?: string,
): string {
  if (!baseUrl) return baseUrl
  try {
    const url = new URL(baseUrl)
    if (userId) {
      url.searchParams.set(EMBEDDED_USER_ID_QUERY_KEY, String(userId))
    }
    if (authToken) {
      url.searchParams.set(EMBEDDED_AUTH_TOKEN_QUERY_KEY, authToken)
    }
    url.searchParams.set(EMBEDDED_THEME_QUERY_KEY, theme)
    if (lang) {
      url.searchParams.set(EMBEDDED_LANG_QUERY_KEY, lang)
    }
    url.searchParams.set(EMBEDDED_UI_MODE_QUERY_KEY, EMBEDDED_UI_MODE_VALUE)

    if (typeof window !== 'undefined') {
      url.searchParams.set(EMBEDDED_SRC_HOST_QUERY_KEY, window.location.origin)
      url.searchParams.set(EMBEDDED_SRC_QUERY_KEY, window.location.href)
    }
    return url.toString()
  } catch {
    /**
     * A parse failure retains the original configured string; callers still own
     * destination validation and the presentation of unusable embedded URLs.
     */
    return baseUrl
  }
}

/**
 * detectTheme reads the document root dark class for one URL-building
 * operation. It returns light without a document and does not subscribe to
 * later theme changes, so callers rebuild URLs when needed.
 */
export function detectTheme(): 'light' | 'dark' {
  if (typeof document === 'undefined') return 'light'
  return document.documentElement.classList.contains('dark') ? 'dark' : 'light' // check-ui-allow: One-time document theme detection for noncomponent URL construction.
}
