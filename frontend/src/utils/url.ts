/**
 * url.ts filters configured URLs for browser consumers such as site branding.
 * Absolute HTTP and HTTPS URLs are parsed and normalized; callers can
 * additionally allow root-relative paths or image data URLs. Rejected input
 * returns an empty string, and the optional forms are accepted by prefix
 * rather than full payload validation.
 */

/**
 * SanitizeOptions opts browser URL consumers into additional accepted URL
 * forms.
 */
type SanitizeOptions = {
  /**
   * Accepts trimmed strings starting with / unless they start with //. This
   * literal prefix check also accepts a slash followed by a backslash.
   */
  allowRelative?: boolean
  /**
   * Allows strings beginning with data:image/ without parsing their image
   * payload or verifying their image format.
   */
  allowDataUrl?: boolean
}

/**
 * sanitizeUrl trims and filters a configured URL according to the requested
 * optional forms. Relative and image-data options accept literal prefixes
 * without URL parsing; the relative branch rejects // but accepts a slash
 * followed by a backslash. Malformed absolute web URLs return empty.
 */
export function sanitizeUrl(value: string, options: SanitizeOptions = {}): string {
  const trimmed = value.trim()
  if (!trimmed) {
    return ''
  }

  if (options.allowRelative && trimmed.startsWith('/') && !trimmed.startsWith('//')) {
    return trimmed
  }

  if (options.allowDataUrl && trimmed.startsWith('data:image/')) {
    return trimmed
  }

  if (!trimmed.match(/^https?:\/\//i)) {
    return ''
  }

  try {
    const parsed = new URL(trimmed)
    const protocol = parsed.protocol.toLowerCase()
    if (protocol !== 'http:' && protocol !== 'https:') {
      return ''
    }
    return parsed.toString()
  } catch {
    /**
     * Malformed absolute URL syntax produces an empty result, allowing browser
     * consumers to reject the update and retain their existing configured state.
     */
    return ''
  }
}
