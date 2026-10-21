import { reactive } from 'vue'

/**
 * ipGeoLookup.ts looks up IP geolocation and publishes reactive entries shared
 * by browser consumers. Recognized private addresses remain local, while
 * public candidates use single or batched GeoJS requests. Successful results
 * are eligible for a twenty-four-hour localStorage cache; loading, error, and
 * private states remain in memory.
 *
 * Batch responses are matched by returned IP rather than response position, so
 * omitted records become per-address errors without shifting other results.
 * The batch boolean reports request-level success and can remain true when
 * individual records are unusable. Loading entries suppress ordinary duplicate
 * requests, but forced single lookups can overlap. There is no cancellation or
 * generation check, so completion order can overwrite a newer observation.
 */

/**
 * IpGeoStatus distinguishes unrequested, active, successful, failed, and
 * locally classified private addresses.
 */
export type IpGeoStatus = 'idle' | 'loading' | 'success' | 'error' | 'private'

/**
 * IpGeoDetail contains optional location fields copied from a GeoJS response.
 */
export interface IpGeoDetail {
  countryCode?: string // country code text
  region?: string // region display name
  city?: string // city display name
  organization?: string // network organization text
  timezone?: string // timezone identifier text
  accuracy?: number // upstream accuracy value
  latitude?: string // latitude decimal string
  longitude?: string // longitude decimal string
}

/**
 * IpGeoEntry represents one shared address lookup state. Status controls
 * presentation, while label, detail, and fetchedAt describe a successful
 * observation and are normally absent for loading, private, or error entries.
 */
export interface IpGeoEntry {
  status: IpGeoStatus // current lookup status
  label?: string // location display label
  detail?: IpGeoDetail // optional location details
  /**
   * Records observation time in epoch milliseconds; successful entries expire
   * when the current clock exceeds this timestamp by the cache lifetime.
   */
  fetchedAt?: number
}

/**
 * IDLE_ENTRY is the shared missing-state sentinel; the cache key and lifetime
 * govern persistence, while the chunk limit and URLs govern GeoJS requests.
 */
const IDLE_ENTRY: IpGeoEntry = { status: 'idle' }
const CACHE_STORAGE_KEY = 'tokenrouter:ip-geo-cache:v1'
const CACHE_TTL_MS = 24 * 60 * 60 * 1000
const BATCH_CHUNK_SIZE = 50
const GEO_SINGLE_URL = 'https://get.geojs.io/v1/ip/geo'
const GEO_BATCH_URL = 'https://get.geojs.io/v1/ip/geo.json'

/**
 * StoredEntry preserves successful lookup data across page loads. Label and
 * optional detail share the fetchedAt observation time, which hydration uses
 * to decide whether the saved result is still reusable.
 */
interface StoredEntry {
  label: string // location display label
  detail?: IpGeoDetail // optional location details
  /**
   * Records observation time in epoch milliseconds; successful entries expire
   * when the current clock exceeds this timestamp by the cache lifetime.
   */
  fetchedAt: number
}

/**
 * cache owns reactive lookup entries for all consumers and is hydrated once
 * when this module is evaluated.
 */
const cache = reactive(new Map<string, IpGeoEntry>())

/**
 * isFreshSuccess reports whether a successful entry has a numeric timestamp
 * within the cache lifetime. It uses the current wall clock and accepts future
 * timestamps until their computed age exceeds the limit.
 */
function isFreshSuccess(entry: IpGeoEntry | undefined): entry is IpGeoEntry & {
  status: 'success' // current lookup status
  /**
   * Records observation time in epoch milliseconds; successful entries expire
   * when the current clock exceeds this timestamp by the cache lifetime.
   */
  fetchedAt: number
} {
  return (
    entry?.status === 'success' &&
    typeof entry.fetchedAt === 'number' &&
    Date.now() - entry.fetchedAt <= CACHE_TTL_MS
  )
}

/**
 * getFreshEntry reads a shared entry and evicts an expired success before
 * returning it. Eviction triggers a best-effort storage rewrite; loading,
 * error, and private entries remain available unchanged.
 */
function getFreshEntry(ip: string): IpGeoEntry | undefined {
  const entry = cache.get(ip)
  if (entry?.status === 'success' && !isFreshSuccess(entry)) {
    /**
     * Evict an expired successful entry before rewriting persistence so this read
     * returns absence and future page loads cannot restore that same stale record.
     */
    cache.delete(ip)
    persistToStorage()
    return undefined
  }
  return entry
}

/**
 * isPrivateIp recognizes the IPv4 and IPv6 private, loopback, and link-local
 * patterns handled here. It is a lookup suppression heuristic rather than a
 * complete IP parser or reserved-address classifier.
 */
export function isPrivateIp(ip: string): boolean {
  const v4 = ip.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/)
  if (v4) {
    const a = Number(v4[1])
    const b = Number(v4[2])
    if (a === 10) return true
    if (a === 127) return true
    if (a === 169 && b === 254) return true
    if (a === 172 && b >= 16 && b <= 31) return true
    if (a === 192 && b === 168) return true
    return false
  }
  const lower = ip.toLowerCase()
  if (lower === '::1') return true
  const firstSegment = lower.split(':', 1)[0]
  if (/^fe[89ab][0-9a-f]$/.test(firstSegment)) return true
  if (/^f[cd][0-9a-f]{2}$/.test(firstSegment)) return true
  return false
}

/**
 * loadFromStorage hydrates shared memory from persisted entries with numeric,
 * unexpired timestamps. Parsing or storage failures are ignored; hydration
 * occurs at module evaluation and does not validate every optional detail
 * field.
 */
function loadFromStorage(): void {
  try {
    const raw = localStorage.getItem(CACHE_STORAGE_KEY)
    if (!raw) return
    const parsed = JSON.parse(raw) as Record<string, StoredEntry>
    const now = Date.now()
    /**
     * Hydrate only timestamped records within the lifetime; storage remains a
     * best-effort cache and does not authorize publishing older observations as
     * fresh.
     */
    for (const [ip, stored] of Object.entries(parsed)) {
      if (!stored || typeof stored.fetchedAt !== 'number') continue
      if (now - stored.fetchedAt > CACHE_TTL_MS) continue
      cache.set(ip, {
        status: 'success',
        label: stored.label,
        detail: stored.detail,
        fetchedAt: stored.fetchedAt,
      })
    }
  } catch {
    /**
     * Storage errors or malformed cache data are ignored because geolocation can
     * still be requested and published directly into the shared memory map.
     */
  }
}

/**
 * persistToStorage writes successful labeled entries from the current shared
 * map to localStorage. It suppresses serialization and storage failures;
 * callers retain their in-memory results even when persistence fails.
 */
function persistToStorage(): void {
  try {
    const toStore: Record<string, StoredEntry> = {}
    for (const [ip, entry] of cache.entries()) {
      if (entry.status === 'success' && entry.label && entry.fetchedAt) {
        toStore[ip] = { label: entry.label, detail: entry.detail, fetchedAt: entry.fetchedAt }
      }
    }
    /**
     * The shared map already contains the published lookup state before this
     * best-effort snapshot write, so failed persistence leaves current consumers
     * able to use that result.
     */
    localStorage.setItem(CACHE_STORAGE_KEY, JSON.stringify(toStore))
  } catch {
    /**
     * Storage failure leaves the already published reactive entries available, so
     * callers do not lose successful lookup results merely because persistence is
     * blocked.
     */
  }
}

loadFromStorage()

/**
 * getEntry returns the shared cached entry or the shared idle sentinel.
 * Reading can evict an expired success; consumers should treat the returned
 * object as lookup state rather than mutate it.
 */
export function getEntry(ip: string): IpGeoEntry {
  return getFreshEntry(ip) ?? IDLE_ENTRY
}

/**
 * formatGeoLabel joins nonblank country, region, and city values for display.
 */
export function formatGeoLabel(detail: IpGeoDetail): string {
  const parts = [detail.countryCode, detail.region, detail.city].filter((part): part is string =>
    Boolean(part && part.trim()),
  )
  return parts.join(' · ')
}

/**
 * RawGeoResponse represents the GeoJS fields consumed during single and batch
 * lookup conversion.
 */
interface RawGeoResponse {
  ip: string // response IP address
  country_code?: string // upstream country code
  region?: string // region display name
  city?: string // city display name
  organization?: string // network organization text
  timezone?: string // timezone identifier text
  accuracy?: number // upstream accuracy value
  latitude?: string // latitude decimal string
  longitude?: string // longitude decimal string
}

/**
 * toDetail copies GeoJS location fields into the frontend detail
 * representation.
 */
function toDetail(raw: RawGeoResponse): IpGeoDetail {
  return {
    countryCode: raw.country_code,
    region: raw.region,
    city: raw.city,
    organization: raw.organization,
    timezone: raw.timezone,
    accuracy: raw.accuracy,
    latitude: raw.latitude,
    longitude: raw.longitude,
  }
}

/**
 * applyResult publishes a geolocation result for the requested IP. A missing
 * response or country code becomes an error entry; accepted records receive a
 * display label and the current observation timestamp.
 */
function applyResult(ip: string, raw: RawGeoResponse | undefined): void {
  if (!raw || !raw.country_code) {
    cache.set(ip, { status: 'error' })
    return
  }
  const detail = toDetail(raw)
  cache.set(ip, {
    status: 'success',
    label: formatGeoLabel(detail),
    detail,
    fetchedAt: Date.now(),
  })
}

/**
 * fetchOne updates one IP entry, optionally bypassing freshness and loading
 * checks. Recognized private addresses become private entries without a
 * network request.
 *
 * Ordinary calls reuse fresh successes and avoid starting another lookup while
 * that IP is loading. Force bypasses both checks, so multiple completions can
 * replace the same shared entry in completion order. HTTP and parsing failures
 * become error entries rather than promise rejections. Successful publication
 * precedes best-effort persistence, and callers receive no result payload.
 */
export async function fetchOne(ip: string, force = false): Promise<void> {
  if (isPrivateIp(ip)) {
    cache.set(ip, { status: 'private' })
    return
  }
  const existing = getFreshEntry(ip)
  if (!force && (isFreshSuccess(existing) || existing?.status === 'loading')) {
    return
  }
  cache.set(ip, { status: 'loading' })
  try {
    const response = await fetch(`${GEO_SINGLE_URL}/${encodeURIComponent(ip)}.json`)
    if (!response.ok) {
      cache.set(ip, { status: 'error' })
      return
    }
    const raw = (await response.json()) as RawGeoResponse
    applyResult(ip, raw)
    persistToStorage()
  } catch {
    /**
     * Network or decoding failure replaces loading with error state so consumers
     * can offer another lookup instead of remaining indefinitely marked loading.
     */
    cache.set(ip, { status: 'error' })
  }
}

/**
 * fetchBatch updates distinct public IPs that lack fresh success or loading
 * state. It marks all selected targets loading before issuing sequential
 * chunks of up to fifty addresses.
 *
 * Each chunk response is indexed by its IP field before applying results, so
 * response ordering and omissions cannot associate one address with another.
 * Failed chunks mark their addresses as errors and do not stop later chunks.
 * The returned boolean tracks HTTP or processing failures, while missing
 * individual records can still accompany true. No cancellation or
 * request-generation guard is provided.
 */
export async function fetchBatch(ips: string[]): Promise<boolean> {
  const unique = Array.from(new Set(ips))
  const targets: string[] = []
  for (const ip of unique) {
    if (isPrivateIp(ip)) {
      cache.set(ip, { status: 'private' })
      continue
    }
    const existing = cache.get(ip)
    if (isFreshSuccess(existing) || existing?.status === 'loading') continue
    targets.push(ip)
  }
  if (targets.length === 0) return true

  /**
   * Mark every selected target loading before the first request so another
   * ordinary lookup does not duplicate work while later chunks wait.
   */
  targets.forEach((ip) => cache.set(ip, { status: 'loading' }))

  let allChunksOk = true
  /**
   * Split selected targets into bounded requests and process them sequentially,
   * preserving per-address states while limiting the number encoded in each
   * batch URL.
   */
  for (let i = 0; i < targets.length; i += BATCH_CHUNK_SIZE) {
    const chunk = targets.slice(i, i + BATCH_CHUNK_SIZE)
    try {
      const response = await fetch(`${GEO_BATCH_URL}?ip=${chunk.map(encodeURIComponent).join(',')}`)
      if (!response.ok) {
        chunk.forEach((ip) => cache.set(ip, { status: 'error' }))
        allChunksOk = false
        continue
      }
      const results = (await response.json()) as RawGeoResponse[]
      /**
       * Index returned records by address before applying them, since a batch
       * response can omit entries or return them in a different order.
       */
      const byIp = new Map(results.map((r) => [r.ip, r]))
      chunk.forEach((ip) => applyResult(ip, byIp.get(ip)))
      persistToStorage()
    } catch {
      /**
       * Fail only this chunk and continue processing later chunks, preserving
       * independent results while returning false for the batch-level request
       * outcome.
       */
      chunk.forEach((ip) => cache.set(ip, { status: 'error' }))
      allChunksOk = false
    }
  }
  return allChunksOk
}
