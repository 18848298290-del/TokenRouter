import { sanitizeUrl } from '@/utils/url'

/**
 * branding.ts updates the document favicon from the configured site logo. URL
 * policy permits web URLs, root-relative paths, and image data URLs; a
 * rejected value leaves the existing icon intact, while an accepted value
 * reuses or creates the icon link.
 */

/**
 * updateFavicon updates or creates the document icon link after URL
 * validation. It requires a browser document and leaves the existing icon
 * untouched for rejected input; the SVG MIME hint uses the final string
 * suffix.
 */
export function updateFavicon(logoUrl: string): void {
  const sanitizedLogoUrl = sanitizeUrl(logoUrl, {
    allowRelative: true,
    allowDataUrl: true,
  })
  if (!sanitizedLogoUrl) {
    return
  }

  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }

  link.type = sanitizedLogoUrl.endsWith('.svg') ? 'image/svg+xml' : 'image/x-icon'
  link.href = sanitizedLogoUrl
}
