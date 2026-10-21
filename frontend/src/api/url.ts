/**
 * url.ts constructs browser API and gateway URLs from the configured API base.
 * Relative API bases become root-relative and trailing slashes are removed,
 * while gateway requests use the API host's origin so separate frontend and
 * backend hosts remain supported.
 */

/**
 * DEFAULT_API_BASE_URL supplies the versioned browser API prefix when the
 * deployment leaves its API base unconfigured.
 */
const DEFAULT_API_BASE_URL = '/api/v1'

/**
 * API_BASE_URL holds the normalized deployment API base, computed once when this
 * URL module is loaded.
 */
const API_BASE_URL = normalizeAPIBaseURL(import.meta.env.VITE_API_BASE_URL)

/**
 * normalizePath makes a path root-relative by adding a missing leading slash.
 */
function normalizePath(path: string): string {
  return path.startsWith('/') ? path : `/${path}`
}

/**
 * normalizeAPIBaseURL resolves the configured base and strips trailing slashes.
 * Empty input selects /api/v1; absolute and protocol-relative URLs retain their
 * host syntax, while other values become root-relative paths.
 */
function normalizeAPIBaseURL(value: unknown): string {
  const raw = String(value || DEFAULT_API_BASE_URL).trim() || DEFAULT_API_BASE_URL
  const withoutTrailingSlash = raw.replace(/\/+$/, '')
  if (
    /^[a-z][a-z\d+.-]*:\/\//i.test(withoutTrailingSlash) ||
    withoutTrailingSlash.startsWith('//')
  ) {
    return withoutTrailingSlash
  }
  return normalizePath(withoutTrailingSlash)
}

/**
 * getAPIBaseURL returns the API base normalized when this module loads.
 */
export function getAPIBaseURL(): string {
  return API_BASE_URL
}

/**
 * buildApiUrl joins the configured API base and supplied path. An existing
 * /api/v1 prefix is removed from the path before joining, preventing duplicate
 * prefixes for callers passing canonical API paths.
 */
export function buildApiUrl(path: string): string {
  const base = getAPIBaseURL().replace(/\/+$/, '')
  let suffix = normalizePath(path)
  if (suffix === DEFAULT_API_BASE_URL) {
    suffix = ''
  } else if (suffix.startsWith(`${DEFAULT_API_BASE_URL}/`)) {
    suffix = suffix.slice(DEFAULT_API_BASE_URL.length)
  }
  return `${base}${suffix}`
}

/**
 * buildGatewayUrl joins a gateway path to the configured API origin.
 * Browser-relative bases resolve against the page origin; if origin parsing
 * fails, it returns the root-relative path for same-origin requests.
 */
export function buildGatewayUrl(path: string): string {
  const suffix = normalizePath(path)
  try {
    /**
     * Derive the gateway origin from the configured API base so frontend and backend
     * deployments can use different hosts.
     */
    const origin =
      typeof window === 'undefined'
        ? new URL(getAPIBaseURL()).origin
        : new URL(getAPIBaseURL(), window.location.origin).origin
    return `${origin}${suffix}`
  } catch {
    // When an origin cannot be resolved, a root-relative gateway path lets the
    // browser target its current origin while retaining the requested endpoint.
    return suffix
  }
}
