/**
 * adminUIRequest.ts classifies browser requests for administrator and user
 * timing headers. Administrator classification considers both the target URL and
 * current page, while user classification uses a route allowlist that excludes
 * public payment verification and webhook endpoints.
 */

/**
 * ADMIN_UI_REQUEST_HEADER names the browser marker attached when request or page
 * paths identify an administrator operation.
 */
export const ADMIN_UI_REQUEST_HEADER = 'X-Admin-UI-Request'

/**
 * USER_UI_REQUEST_HEADER names the browser marker attached to requests covered
 * by the user timing route allowlist.
 */
export const USER_UI_REQUEST_HEADER = 'X-User-UI-Request'

/**
 * isAdminPath reports whether a path belongs to an administrator route family.
 */
function isAdminPath(path: string): boolean {
  return (
    path === '/admin' ||
    path.startsWith('/admin/') ||
    path === '/api/v1/admin' ||
    path.startsWith('/api/v1/admin/')
  )
}

/**
 * requestPath extracts a pathname from a relative or absolute URL. Empty input
 * stays empty, and failed URL parsing strips query or fragment text so
 * classification can continue with the available path.
 */
function requestPath(rawURL: string): string {
  const value = rawURL.trim()
  if (!value) return ''
  try {
    const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost'
    return new URL(value, origin).pathname
  } catch {
    // Malformed URL input can still contain a recognizable route; remove query
    // and fragment text before the caller applies path-family classification.
    return value.split(/[?#]/, 1)[0]
  }
}

/**
 * normalizeAPIPath normalizes Axios-relative and API-prefixed paths for browser
 * timing classification. Empty paths stay empty; recognized /api/v1 prefixes are
 * removed before comparisons with the local route allowlist.
 */
function normalizeAPIPath(path: string): string {
  const raw = requestPath(path)
  if (!raw) return ''
  if (raw === '/api/v1' || raw.startsWith('/api/v1/')) {
    return raw.slice('/api/v1'.length) || '/'
  }
  if (raw.startsWith('/')) {
    return raw
  }
  return `/${raw}`
}

/**
 * isUserTimingAPIPath reports whether a request belongs to the user timing
 * allowlist. Public payment and webhook routes are excluded, while authenticated
 * payment routes and the listed user API families qualify.
 */
export function isUserTimingAPIPath(requestURL: string): boolean {
  const path = normalizeAPIPath(requestURL)
  if (!path) return false

  if (
    path === '/auth/me' ||
    path === '/auth/revoke-all-sessions' ||
    path === '/auth/oauth/bind-token'
  ) {
    return true
  }
  if (path === '/user' || path.startsWith('/user/')) return true
  if (path === '/keys' || path.startsWith('/keys/')) return true
  if (path === '/groups/available' || path === '/groups/rates') return true
  if (path === '/channels/available') return true
  if (path === '/usage' || path.startsWith('/usage/')) return true
  if (path === '/announcements' || path.startsWith('/announcements/')) return true
  if (path === '/redeem' || path.startsWith('/redeem/')) return true
  if (path === '/subscriptions' || path.startsWith('/subscriptions/')) return true
  if (path === '/channel-monitors' || path.startsWith('/channel-monitors/')) return true
  if (path.startsWith('/payment/')) {
    if (path.startsWith('/payment/public') || path.startsWith('/payment/webhook')) {
      return false
    }
    return true
  }
  return false
}

/**
 * shouldMarkAdminUIRequest reports whether the target or current page is
 * administrative. Callers may supply pagePath for classification; otherwise the
 * browser pathname is used, with an empty page outside a browser.
 */
export function shouldMarkAdminUIRequest(requestURL: string, pagePath?: string): boolean {
  const currentPath = pagePath ?? (typeof window !== 'undefined' ? window.location.pathname : '')
  return isAdminPath(requestPath(requestURL)) || isAdminPath(currentPath)
}

/**
 * shouldMarkUserUIRequest reports whether the request qualifies for user timing
 * metadata.
 */
export function shouldMarkUserUIRequest(requestURL: string): boolean {
  return isUserTimingAPIPath(requestURL)
}
