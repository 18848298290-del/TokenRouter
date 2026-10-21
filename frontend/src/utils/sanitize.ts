import DOMPurify from 'dompurify'

/**
 * sanitize.ts sanitizes SVG markup before browser consumers display it.
 * DOMPurify applies its SVG and SVG-filter profiles to the supplied string,
 * while empty input returns empty without invoking the sanitizer. Callers
 * receive the sanitized markup as a string.
 */

/**
 * sanitizeSvg returns markup processed by DOMPurify SVG and filter profiles.
 * Empty input returns an empty string; the helper delegates allowed elements
 * and attributes to the installed sanitizer policy.
 */
export function sanitizeSvg(svg: string): string {
  if (!svg) return ''
  return DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } })
}
