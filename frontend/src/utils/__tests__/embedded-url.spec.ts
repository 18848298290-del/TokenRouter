import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { buildEmbeddedUrl, detectTheme } from '../embedded-url'

/**
 * embedded-url.spec.ts checks context query parameters generated for embedded
 * pages. Browser location is replaced with a controlled source and restored
 * after each case, allowing URL metadata, optional omission, invalid input,
 * and one-time theme detection to be asserted independently.
 */

describe('embedded-url', () => {
  const originalLocation = window.location

  beforeEach(() => {
    /**
     * Replace browser location with a controlled source before URL construction;
     * the afterEach hook restores the original object so later tests retain their
     * environment.
     */
    Object.defineProperty(window, 'location', {
      value: {
        origin: 'https://app.example.com',
        href: 'https://app.example.com/user/purchase',
      },
      writable: true,
      configurable: true,
    })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    })
    document.documentElement.classList.remove('dark')
    vi.restoreAllMocks()
  })

  /**
   * An absolute checkout URL already contains a plan parameter and receives full
   * context. The builder must preserve the plan while adding identity, locale,
   * theme, and source metadata.
   */
  it('adds embedded query parameters including locale and source context', () => {
    const result = buildEmbeddedUrl(
      'https://pay.example.com/checkout?plan=pro',
      42,
      'token-123',
      'dark',
      'zh-CN',
    )

    const url = new URL(result)
    expect(url.searchParams.get('plan')).toBe('pro')
    expect(url.searchParams.get('user_id')).toBe('42')
    expect(url.searchParams.get('token')).toBe('token-123')
    expect(url.searchParams.get('theme')).toBe('dark')
    expect(url.searchParams.get('lang')).toBe('zh-CN')
    expect(url.searchParams.get('ui_mode')).toBe('embedded')
    expect(url.searchParams.get('src_host')).toBe('https://app.example.com')
    expect(url.searchParams.get('src_url')).toBe('https://app.example.com/user/purchase')
  })

  /**
   * Optional account, token, and locale inputs are empty or absent. The
   * resulting URL must omit those parameters while retaining the required
   * embedded-mode and theme context.
   */
  it('omits optional params when they are empty', () => {
    const result = buildEmbeddedUrl('https://pay.example.com/checkout', undefined, '', 'light')

    const url = new URL(result)
    expect(url.searchParams.get('theme')).toBe('light')
    expect(url.searchParams.get('ui_mode')).toBe('embedded')
    expect(url.searchParams.has('user_id')).toBe(false)
    expect(url.searchParams.has('token')).toBe(false)
    expect(url.searchParams.has('lang')).toBe(false)
  })

  /**
   * The supplied base string cannot be parsed as an absolute URL. The builder
   * must return that original string unchanged rather than constructing partial
   * context output.
   */
  it('returns original string for invalid url input', () => {
    expect(buildEmbeddedUrl('not a url', 1, 'token')).toBe('not a url')
  })

  /**
   * The document root has the dark theme class at detection time. The helper
   * must return dark for the URL-building caller using that current document
   * state.
   */
  it('detects dark mode from document root class', () => {
    document.documentElement.classList.add('dark')
    expect(detectTheme()).toBe('dark')
  })
})
