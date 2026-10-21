import { describe, it, expect, beforeEach, vi } from 'vitest'
import { isPrivateIp, getEntry, formatGeoLabel, fetchOne, fetchBatch } from '../ipGeoLookup'

/**
 * ipGeoLookup.spec.ts checks IP classification, per-address lookup state,
 * batching, and browser persistence with mocked fetch responses. Module
 * reloads and controlled time exercise hydration and expiration, while
 * response fixtures verify identity matching and distinguish failed requests
 * from missing individual records.
 */

describe('isPrivateIp', () => {
  /**
   * Representative IPv4 addresses fall within the private, loopback, and
   * link-local ranges handled here. Each must be classified locally so those
   * fixtures do not require public lookup.
   */
  it('identifies private/reserved IPv4 ranges', () => {
    expect(isPrivateIp('10.0.0.1')).toBe(true)
    expect(isPrivateIp('127.0.0.1')).toBe(true)
    expect(isPrivateIp('192.168.1.1')).toBe(true)
    expect(isPrivateIp('172.16.0.1')).toBe(true)
    expect(isPrivateIp('172.31.255.255')).toBe(true)
    expect(isPrivateIp('169.254.1.1')).toBe(true)
  })

  /**
   * Representative public IPv4 addresses sit outside the recognized private
   * ranges. Each must remain eligible for lookup, including the address just
   * above the private 172 range.
   */
  it('does not flag public IPv4 addresses', () => {
    expect(isPrivateIp('8.8.8.8')).toBe(false)
    expect(isPrivateIp('172.32.0.1')).toBe(false)
    expect(isPrivateIp('121.35.47.43')).toBe(false)
  })

  /**
   * IPv6 fixtures cover loopback, link-local, and unique-local prefixes. The
   * classifier must recognize every listed private example without requiring a
   * network response for classification.
   */
  it('identifies private/reserved IPv6 addresses', () => {
    expect(isPrivateIp('::1')).toBe(true)
    expect(isPrivateIp('fe80::1')).toBe(true)
    expect(isPrivateIp('fe90::1')).toBe(true)
    expect(isPrivateIp('febf::1')).toBe(true)
    expect(isPrivateIp('fc00::1')).toBe(true)
    expect(isPrivateIp('fd00::1')).toBe(true)
    expect(isPrivateIp('fdff::1')).toBe(true)
  })

  /**
   * IPv6 addresses lie immediately outside recognized private prefix ranges. The
   * classifier must return false for those neighbors rather than extending its
   * pattern beyond the intended ranges.
   */
  it('does not overmatch public IPv6 addresses near private ranges', () => {
    expect(isPrivateIp('fec0::1')).toBe(false)
    expect(isPrivateIp('fbff::1')).toBe(false)
    expect(isPrivateIp('fe7f::1')).toBe(false)
  })
})

describe('getEntry', () => {
  /**
   * An address has no cache entry and has never been requested. Reading it must
   * return idle state so consumers can distinguish absence from lookup failure.
   */
  it('returns an idle entry for an IP that has never been fetched', () => {
    expect(getEntry('203.0.113.9')).toEqual({ status: 'idle' })
  })
})

describe('formatGeoLabel', () => {
  /**
   * All country, region, and city fields contain display text. The label must
   * join them in that order with the expected separator between location parts.
   */
  it('joins country/region/city with a separator', () => {
    expect(formatGeoLabel({ countryCode: 'CN', region: 'Guangdong', city: 'Shenzhen' })).toBe(
      'CN · Guangdong · Shenzhen',
    )
  })

  /**
   * Only a subset of location fields is present in each fixture. Labels must
   * omit missing parts without introducing empty separators into the rendered
   * location text.
   */
  it('skips missing fields', () => {
    expect(formatGeoLabel({ countryCode: 'CN' })).toBe('CN')
    expect(formatGeoLabel({ countryCode: 'US', region: 'Massachusetts' })).toBe(
      'US · Massachusetts',
    )
  })
})

describe('fetchOne', () => {
  beforeEach(() => {
    localStorage.clear()
    global.fetch = vi.fn()
  })

  /**
   * A single lookup is requested for a recognized private IPv4 address. The
   * cache must publish private state and fetch must remain completely unused by
   * that operation.
   */
  it('marks a private IP without making a network request', async () => {
    await fetchOne('192.168.50.1')
    expect(getEntry('192.168.50.1')).toEqual({ status: 'private' })
    expect(global.fetch).not.toHaveBeenCalled()
  })

  /**
   * A successful single response includes location and organization information.
   * The request URL and published entry must preserve the expected address,
   * label, and copied detail fields.
   */
  it('fetches and stores a successful geolocation result', async () => {
    ;(global.fetch as any).mockResolvedValue({
      ok: true,
      json: async () => ({
        ip: '121.35.47.43',
        country_code: 'CN',
        region: 'Guangdong',
        city: 'Shenzhen',
        organization: 'AS4134 Chinanet',
        timezone: 'Asia/Shanghai',
        accuracy: 10,
        latitude: '22.5455',
        longitude: '114.0683',
      }),
    })

    await fetchOne('121.35.47.43')

    expect(global.fetch).toHaveBeenCalledWith('https://get.geojs.io/v1/ip/geo/121.35.47.43.json')
    const entry = getEntry('121.35.47.43')
    expect(entry.status).toBe('success')
    expect(entry.label).toBe('CN · Guangdong · Shenzhen')
    expect(entry.detail?.organization).toBe('AS4134 Chinanet')
  })

  /**
   * A successful HTTP response lacks the required country code. The address must
   * become an error entry rather than publishing incomplete data as successful
   * geolocation.
   */
  it('marks the entry as error when the response has no country_code', async () => {
    ;(global.fetch as any).mockResolvedValue({
      ok: true,
      json: async () => ({ ip: '192.0.2.55', organization: 'AS64512 Unknown' }),
    })

    await fetchOne('192.0.2.55')

    expect(getEntry('192.0.2.55').status).toBe('error')
  })

  /**
   * The single-address network request rejects with an error. The helper must
   * settle normally and publish error state so consumers can display failure
   * from the cache.
   */
  it('marks the entry as error when the request rejects', async () => {
    ;(global.fetch as any).mockRejectedValue(new Error('network down'))

    await fetchOne('198.51.100.7')

    expect(getEntry('198.51.100.7').status).toBe('error')
  })

  /**
   * The same successful address is requested repeatedly and then
   * force-refreshed. Ordinary requests reuse fresh state, while force must cause
   * another network request to occur.
   */
  it('does not re-fetch a cached successful IP unless forced', async () => {
    ;(global.fetch as any).mockResolvedValue({
      ok: true,
      json: async () => ({
        ip: '8.8.8.8',
        country_code: 'US',
        region: 'California',
        city: 'Mountain View',
      }),
    })

    await fetchOne('8.8.8.8')
    expect(global.fetch).toHaveBeenCalledTimes(1)

    await fetchOne('8.8.8.8')
    expect(global.fetch).toHaveBeenCalledTimes(1)

    await fetchOne('8.8.8.8', true)
    expect(global.fetch).toHaveBeenCalledTimes(2)
  })
})

describe('fetchBatch', () => {
  beforeEach(() => {
    localStorage.clear()
    global.fetch = vi.fn()
  })

  /**
   * A batch includes duplicate public addresses and a private address. Only the
   * distinct public target is requested, and both public success and private
   * classification are published.
   */
  it('deduplicates IPs and skips private addresses without a network call', async () => {
    ;(global.fetch as any).mockResolvedValue({
      ok: true,
      json: async () => [
        { ip: '203.0.113.10', country_code: 'US', region: 'Texas', city: 'Dallas' },
      ],
    })

    await fetchBatch(['203.0.113.10', '203.0.113.10', '10.0.0.5'])

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const calledUrl = (global.fetch as any).mock.calls[0][0] as string
    expect(calledUrl).toContain('ip=203.0.113.10')
    expect(calledUrl).not.toContain('203.0.113.10,203.0.113.10')
    expect(getEntry('10.0.0.5').status).toBe('private')
    expect(getEntry('203.0.113.10').status).toBe('success')
  })

  /**
   * The batch contains sixty-one distinct public addresses requiring multiple
   * requests. Fetch arguments must split them into a fifty-address chunk and an
   * eleven-address remainder.
   */
  it('splits more than 50 IPs into multiple chunk requests', async () => {
    const ips = Array.from(
      { length: 61 },
      (_, i) => `203.0.${Math.floor(i / 250)}.${(i % 250) + 1}`,
    )
    ;(global.fetch as any).mockImplementation(async (url: string) => ({
      ok: true,
      json: async () => {
        const queried = new URL(url).searchParams.get('ip')!.split(',')
        return queried.map((ip) => ({ ip, country_code: 'US' }))
      },
    }))

    await fetchBatch(ips)

    expect(global.fetch).toHaveBeenCalledTimes(2)
    const firstChunkIps = new URL((global.fetch as any).mock.calls[0][0]).searchParams
      .get('ip')!
      .split(',')
    const secondChunkIps = new URL((global.fetch as any).mock.calls[1][0]).searchParams
      .get('ip')!
      .split(',')
    expect(firstChunkIps.length).toBe(50)
    expect(secondChunkIps.length).toBe(11)
  })

  /**
   * A successful batch response omits one requested address. That address must
   * become an error while the returned batch boolean remains true for the
   * successful request.
   */
  it('marks individual IPs as error when they are missing from the batch response', async () => {
    ;(global.fetch as any).mockResolvedValue({
      ok: true,
      json: async () => [{ ip: '203.0.113.20', country_code: 'US' }],
    })

    const ok = await fetchBatch(['203.0.113.20', '203.0.113.21'])

    expect(getEntry('203.0.113.20').status).toBe('success')
    expect(getEntry('203.0.113.21').status).toBe('error')

    expect(ok).toBe(true)
  })

  /**
   * A batch request rejects before any records can be applied. The result must
   * be false and all addresses in the failed chunk must become error entries.
   */
  it('returns false when a chunk request fails at the network level', async () => {
    ;(global.fetch as any).mockRejectedValue(new Error('network down'))

    const ok = await fetchBatch(['203.0.113.50', '203.0.113.51'])

    expect(ok).toBe(false)
    expect(getEntry('203.0.113.50').status).toBe('error')
    expect(getEntry('203.0.113.51').status).toBe('error')
  })

  /**
   * A later batch mixes a fresh cached address with a new target. The second
   * request must contain only the new address while retaining the earlier
   * success.
   */
  it('skips IPs that already have a cached success entry', async () => {
    ;(global.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => [{ ip: '203.0.113.40', country_code: 'CN' }],
    })
    await fetchBatch(['203.0.113.40'])
    expect(global.fetch).toHaveBeenCalledTimes(1)

    ;(global.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => [{ ip: '203.0.113.41', country_code: 'CN' }],
    })
    await fetchBatch(['203.0.113.40', '203.0.113.41'])
    expect(global.fetch).toHaveBeenCalledTimes(2)
    const secondCallUrl = (global.fetch as any).mock.calls[1][0] as string
    expect(secondCallUrl).toContain('203.0.113.41')
    expect(secondCallUrl).not.toContain('203.0.113.40')
  })
})

describe('ipGeoLookup localStorage persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.resetModules()
  })

  /**
   * LocalStorage contains a recent successful record before module evaluation.
   * Importing a fresh module must hydrate its shared map so the record is
   * immediately available.
   */
  it('hydrates the in-memory cache from a non-expired localStorage entry on module load', async () => {
    localStorage.setItem(
      'tokenrouter:ip-geo-cache:v1',
      JSON.stringify({
        '121.35.47.43': { label: 'CN · Guangdong · Shenzhen', fetchedAt: Date.now() },
      }),
    )

    const mod = await import('../ipGeoLookup')

    expect(mod.getEntry('121.35.47.43')).toEqual(
      expect.objectContaining({ status: 'success', label: 'CN · Guangdong · Shenzhen' }),
    )
  })

  /**
   * LocalStorage contains a successful record older than the cache lifetime. A
   * fresh module import must leave that address idle instead of hydrating
   * expired display data.
   */
  it('ignores expired localStorage entries on module load', async () => {
    const twentyFiveHoursAgo = Date.now() - 25 * 60 * 60 * 1000
    localStorage.setItem(
      'tokenrouter:ip-geo-cache:v1',
      JSON.stringify({
        '8.8.8.8': { label: 'US · California', fetchedAt: twentyFiveHoursAgo },
      }),
    )

    const mod = await import('../ipGeoLookup')

    expect(mod.getEntry('8.8.8.8')).toEqual({ status: 'idle' })
  })

  /**
   * A mocked single lookup succeeds after a fresh module import. LocalStorage
   * must receive the resulting display record so a later page load can reuse it.
   */
  it('persists a successful fetch result to localStorage', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ ip: '1.2.4.8', country_code: 'CN' }),
    })
    const mod = await import('../ipGeoLookup')

    await mod.fetchOne('1.2.4.8')

    const stored = JSON.parse(localStorage.getItem('tokenrouter:ip-geo-cache:v1') || '{}')
    expect(stored['1.2.4.8']).toEqual(expect.objectContaining({ label: 'CN' }))
  })

  /**
   * A hydrated entry is initially fresh and the clock then advances beyond its
   * lifetime. Reading the entry must evict it and return idle state.
   */
  it('expires a hydrated in-memory entry after the TTL elapses', async () => {
    const now = new Date('2026-07-01T00:00:00Z')
    vi.setSystemTime(now)
    localStorage.setItem(
      'tokenrouter:ip-geo-cache:v1',
      JSON.stringify({
        '8.8.4.4': { label: 'US · California', fetchedAt: now.getTime() },
      }),
    )

    const mod = await import('../ipGeoLookup')
    expect(mod.getEntry('8.8.4.4')).toEqual(expect.objectContaining({ status: 'success' }))

    vi.setSystemTime(new Date(now.getTime() + 25 * 60 * 60 * 1000))

    expect(mod.getEntry('8.8.4.4')).toEqual({ status: 'idle' })
  })

  /**
   * A fetched success becomes older than the cache lifetime after time advances.
   * The next ordinary lookup must issue another request instead of reusing stale
   * data.
   */
  it('re-fetches a successful in-memory cache entry after the TTL elapses', async () => {
    const now = new Date('2026-07-01T00:00:00Z')
    vi.setSystemTime(now)
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ ip: '8.8.8.8', country_code: 'US' }),
    })
    const mod = await import('../ipGeoLookup')

    await mod.fetchOne('8.8.8.8')
    expect(global.fetch).toHaveBeenCalledTimes(1)

    vi.setSystemTime(new Date(now.getTime() + 25 * 60 * 60 * 1000))
    await mod.fetchOne('8.8.8.8')

    expect(global.fetch).toHaveBeenCalledTimes(2)
  })
})
