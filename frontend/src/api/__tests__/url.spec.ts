import { afterEach, describe, expect, it, vi } from 'vitest'

/**
 * url.spec.ts checks browser API and gateway URL construction under configured
 * deployment bases. Cases protect canonical-prefix removal, proxy-path retention
 * and gateway origin selection when the API host differs from the frontend
 * deployment.
 */

describe('api url helpers', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.resetModules()
  })

  /**
   * API URL callers supply paths with and without the canonical prefix. Both
   * forms resolve to the same URL, protecting callers from duplicated /api/v1
   * segments.
   */
  it('builds api urls without duplicating api prefix', async () => {
    const { buildApiUrl } = await import('@/api/url')

    expect(buildApiUrl('/pages/intro')).toBe('/api/v1/pages/intro')
    expect(buildApiUrl('/api/v1/pages/intro')).toBe('/api/v1/pages/intro')
  })

  /**
   * An absolute configured API base includes a proxy prefix and trailing slash.
   * Generated page and refresh URLs retain that prefix and join paths with one
   * slash.
   */
  it('builds api urls from configured absolute api base', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.com/proxy/api/v1/')
    const { buildApiUrl } = await import('@/api/url')

    expect(buildApiUrl('/pages/intro')).toBe('https://api.example.com/proxy/api/v1/pages/intro')
    expect(buildApiUrl('/auth/refresh')).toBe('https://api.example.com/proxy/api/v1/auth/refresh')
  })

  /**
   * The configured API host differs from the browser deployment host. Gateway
   * URL construction uses that API origin with the supplied gateway path for the
   * request.
   */
  it('builds gateway urls from configured api origin', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.com/api/v1')
    const { buildGatewayUrl } = await import('@/api/url')

    expect(buildGatewayUrl('/v1/usage')).toBe('https://api.example.com/v1/usage')
  })
})
