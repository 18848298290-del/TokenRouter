import { beforeEach, describe, expect, it } from 'vitest'
import { updateFavicon } from '@/utils/branding'

/**
 * branding.spec.ts checks favicon updates using the browser document head.
 * Accepted logo URLs replace the current icon, while an unsafe URL leaves the
 * original favicon intact so rejected branding input cannot become an
 * executable link.
 */

describe('updateFavicon', () => {
  beforeEach(() => {
    document.head.innerHTML = '<link rel="icon" href="/logo.svg">'
  })

  /**
   * The document starts with the default favicon and receives a configured web
   * logo. The existing icon link must point to the configured image afterward.
   */
  it('replaces the default favicon with the configured logo', () => {
    updateFavicon('https://example.com/custom-logo.png')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.href).toBe('https://example.com/custom-logo.png')
  })

  /**
   * A configured logo uses the javascript scheme instead of an accepted image
   * URL. The original favicon must remain unchanged after the rejected update.
   */
  it('ignores unsafe logo URLs', () => {
    updateFavicon('javascript:alert(1)')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.getAttribute('href')).toBe('/logo.svg')
  })
})
