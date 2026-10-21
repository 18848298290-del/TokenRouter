// previewCache.ts stores batch image thumbnails and enforces browser cache retention.
// Each page instance reuses one database opening promise, while persisted records
// remain available across visits. Storage identities include the batch, custom
// item identifier, and image index because separate jobs can reuse item labels.
// Every part is escaped before joining, keeping delimiter characters distinct.
//
// Creation time limits each thumbnail's lifetime independently of its access
// time. Cache hits touch access time asynchronously, and maintenance first drops
// expired entries before evicting the least recently accessed survivors until
// both the entry and byte budgets fit. IndexedDB request failures become misses
// or ignored writes so image downloads remain available through the page.

// PreviewCacheRecord stores a thumbnail and the metadata used by retention checks.
type PreviewCacheRecord = {
  key: string // escaped batch item identity
  blob: Blob // persisted thumbnail image bytes
  // size supplies the eviction byte estimate; zero or missing metadata uses
  // the stored Blob size when maintenance totals the live cache entries.
  size: number // bytes
  // createdAt fixes expiration, while lastAccessedAt orders eviction among live
  // records. Touching access time leaves creation time intact so reads cannot
  // extend a thumbnail's three day lifetime.
  createdAt: number // epoch milliseconds
  lastAccessedAt: number // epoch milliseconds
}

// Database identity and budgets are shared across page instances so stored
// thumbnails remain readable when the user revisits the batch image page.
const PREVIEW_CACHE_DB_NAME = 'tokenrouter-batch-image-preview-cache'
const PREVIEW_CACHE_STORE_NAME = 'thumbnails'
const PREVIEW_CACHE_MAX_AGE_MS = 3 * 24 * 60 * 60 * 1000
const PREVIEW_CACHE_MAX_ENTRIES = 120
const PREVIEW_CACHE_MAX_BYTES = 48 * 1024 * 1024

// previewCacheSupported reports whether the browser exposes the IndexedDB
// storage interface.
export function previewCacheSupported() {
  return typeof window !== 'undefined' && 'indexedDB' in window
}

// previewCacheKey builds the persisted identity from batch, item, and image
// identifiers. Each component is URI encoded before joining with colons, and
// omitted image indices select the first image at index zero.
export function previewCacheKey(batchId: string, customID: string, imageIndex = 0) {
  return [batchId, customID, imageIndex].map((part) => encodeURIComponent(String(part))).join(':')
}

// idbRequest converts an IndexedDB request into a promise of its result.
// Request errors reject with the browser error; transaction completion remains
// separate from request success and is handled by IndexedDB.
function idbRequest<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

// createPreviewCache creates the thumbnail storage operations for one page instance.
// Database opening is lazy and its result is reused for subsequent operations;
// callers schedule maintenance and retain ownership of their displayed Blob URLs.
export function createPreviewCache() {
  // Reuse the opening result, including unavailable storage, across this page's
  // reads and writes so blocked openings do not produce repeated requests.
  let previewCacheDBPromise: Promise<IDBDatabase | null> | null = null

  // openPreviewCacheDB opens and reuses the thumbnail database for this cache
  // instance. Missing browser support, failed opening, and blocked opening
  // resolve to null; that result remains cached for the instance lifetime.
  function openPreviewCacheDB(): Promise<IDBDatabase | null> {
    if (!previewCacheSupported()) return Promise.resolve(null)
    if (previewCacheDBPromise) return previewCacheDBPromise

    previewCacheDBPromise = new Promise((resolve) => {
      const request = window.indexedDB.open(PREVIEW_CACHE_DB_NAME, 1)
      request.onupgradeneeded = () => {
        // The version-one upgrade creates the key-based record store and its
        // access-time index together, allowing later page visits to reuse persisted thumbnails.
        const db = request.result
        if (!db.objectStoreNames.contains(PREVIEW_CACHE_STORE_NAME)) {
          const store = db.createObjectStore(PREVIEW_CACHE_STORE_NAME, { keyPath: 'key' })
          store.createIndex('lastAccessedAt', 'lastAccessedAt', { unique: false })
        }
      }
      request.onsuccess = () => resolve(request.result)
      // Opening failure makes caching unavailable for this instance; the page
      // continues to obtain preview bytes through its authenticated content request.
      request.onerror = () => resolve(null)
      // A blocked upgrade must let preview loading proceed, because another
      // browser connection can hold the database version open beyond this request.
      request.onblocked = () => resolve(null)
    })
    return previewCacheDBPromise
  }

  // getCachedPreviewBlob reads a live thumbnail and schedules an access
  // timestamp update. Missing records and request failures return null;
  // expired records also return null while their deletion proceeds
  // asynchronously.
  //
  // Expiration uses creation time and a strict greater-than comparison, so the
  // exact three day limit remains readable. Access updates and expiry
  // deletions are separate operations and are not awaited before returning
  // bytes to the page, which can display a hit immediately.
  async function getCachedPreviewBlob(cacheKey: string): Promise<Blob | null> {
    const db = await openPreviewCacheDB()
    if (!db) return null
    // A failed record request contributes no cached image; the caller can
    // continue its preview flow without depending on browser persistence availability.
    const record = await idbRequest<PreviewCacheRecord | undefined>(
      db
        .transaction(PREVIEW_CACHE_STORE_NAME, 'readonly')
        .objectStore(PREVIEW_CACHE_STORE_NAME)
        .get(cacheKey),
    ).catch(() => undefined)
    if (!record?.blob) return null

    const now = Date.now()
    if (now - record.createdAt > PREVIEW_CACHE_MAX_AGE_MS) {
      // Expired bytes are withheld immediately while deletion proceeds independently,
      // so a slow cleanup cannot expose an image beyond its creation lifetime.
      void deleteCachedPreview(cacheKey)
      return null
    }
    // Return the cached bytes without waiting for the access write; the
    // timestamp affects future eviction ordering rather than this image's validity.
    void touchCachedPreview(cacheKey, now)
    return record.blob
  }

  // putCachedPreviewBlob stores thumbnail bytes and resets both retention
  // timestamps to the current time. Missing storage skips writing, and request
  // failures are ignored before scheduling maintenance; callers do not wait
  // for maintenance to finish.
  async function putCachedPreviewBlob(cacheKey: string, blob: Blob) {
    const db = await openPreviewCacheDB()
    if (!db) return
    const now = Date.now()
    const record: PreviewCacheRecord = {
      key: cacheKey,
      blob,
      size: blob.size,
      createdAt: now,
      lastAccessedAt: now,
    }
    // Persistence request errors are ignored because the page already owns
    // usable image bytes and browser storage is an optional preview optimization.
    await idbRequest(
      db
        .transaction(PREVIEW_CACHE_STORE_NAME, 'readwrite')
        .objectStore(PREVIEW_CACHE_STORE_NAME)
        .put(record),
    ).catch(() => null)
    // Queue maintenance after the put request settles so it can observe
    // the newly submitted record when calculating cache entry and byte pressure.
    void cleanupPreviewCache()
  }

  // touchCachedPreview updates an existing thumbnail's access timestamp
  // through a read followed by a write. Missing records and request errors are
  // ignored, and the caller supplies the epoch millisecond timestamp sampled
  // during its cache read.
  //
  // The stored Blob, size, and creation time are carried forward from the
  // fetched record. These separate transactions do not serialize concurrent
  // replacement or deletion with the touch, so the operation reflects the
  // record read by this invocation.
  async function touchCachedPreview(cacheKey: string, lastAccessedAt: number) {
    const db = await openPreviewCacheDB()
    if (!db) return
    // A failed access metadata read leaves the existing record unchanged;
    // the cache hit has already returned its Blob to the requesting page.
    const record = await idbRequest<PreviewCacheRecord | undefined>(
      db
        .transaction(PREVIEW_CACHE_STORE_NAME, 'readonly')
        .objectStore(PREVIEW_CACHE_STORE_NAME)
        .get(cacheKey),
    ).catch(() => undefined)
    if (!record) return
    // Carry the fetched record into the write so touching access time leaves
    // its creation lifetime and stored image bytes attached to the same identity.
    record.lastAccessedAt = lastAccessedAt
    // Touch failures leave the caller's image available; only the eviction
    // ordering misses this access observation until another read updates the record.
    await idbRequest(
      db
        .transaction(PREVIEW_CACHE_STORE_NAME, 'readwrite')
        .objectStore(PREVIEW_CACHE_STORE_NAME)
        .put(record),
    ).catch(() => null)
  }

  // deleteCachedPreview removes the persisted thumbnail associated with the
  // supplied encoded identity. Missing storage skips deletion and request
  // errors are ignored, since the requesting read already treats the expired
  // thumbnail as unavailable.
  async function deleteCachedPreview(cacheKey: string) {
    const db = await openPreviewCacheDB()
    if (!db) return
    // Deletion failure leaves the expired record stored, but later reads
    // still reject its creation age and continue treating it as a cache miss.
    await idbRequest(
      db
        .transaction(PREVIEW_CACHE_STORE_NAME, 'readwrite')
        .objectStore(PREVIEW_CACHE_STORE_NAME)
        .delete(cacheKey),
    ).catch(() => null)
  }

  // cleanupPreviewCache evicts thumbnails whose age, count, or combined size
  // exceed the browser cache budgets. Missing storage and failed record
  // enumeration leave the cache unchanged. Expiry is evaluated before capacity
  // so expired bytes do not displace live thumbnails.
  //
  // Live entries are sorted by last access time and removed from oldest to
  // newest until both capacity limits fit. Stored size metadata takes
  // precedence over Blob size, with missing sizes contributing zero. Deletion
  // requests are queued in one write transaction, whose completion is not
  // awaited by this function.
  async function cleanupPreviewCache() {
    const db = await openPreviewCacheDB()
    if (!db) return
    // Enumeration failure leaves existing records intact because an empty
    // observation cannot identify expired or least recently accessed entries safely.
    const records = await idbRequest<PreviewCacheRecord[]>(
      db
        .transaction(PREVIEW_CACHE_STORE_NAME, 'readonly')
        .objectStore(PREVIEW_CACHE_STORE_NAME)
        .getAll(),
    ).catch(() => [])
    if (!records.length) return

    const now = Date.now()
    const sorted = [...records].sort((a, b) => a.lastAccessedAt - b.lastAccessedAt)
    const deleteKeys = new Set<string>()
    let totalBytes = 0
    let keptCount = 0

    // Exclude expired records from the live capacity totals before evaluating
    // pressure, so expired bytes cannot trigger unnecessary eviction of live thumbnails.
    for (const record of sorted) {
      if (now - record.createdAt > PREVIEW_CACHE_MAX_AGE_MS) {
        deleteKeys.add(record.key)
        continue
      }
      totalBytes += record.size || record.blob?.size || 0
      keptCount += 1
    }

    // Remove oldest surviving records until both budgets fit; an entry-count
    // surplus and oversized image bytes independently require eviction from this ordering.
    for (const record of sorted) {
      if (deleteKeys.has(record.key)) continue
      if (keptCount <= PREVIEW_CACHE_MAX_ENTRIES && totalBytes <= PREVIEW_CACHE_MAX_BYTES) break
      deleteKeys.add(record.key)
      totalBytes -= record.size || record.blob?.size || 0
      keptCount -= 1
    }

    if (!deleteKeys.size) return
    const store = db
      .transaction(PREVIEW_CACHE_STORE_NAME, 'readwrite')
      .objectStore(PREVIEW_CACHE_STORE_NAME)
    // Queue selected deletions together after calculating the complete eviction
    // set, allowing IndexedDB to apply this maintenance pass within one write transaction.
    for (const key of deleteKeys) {
      store.delete(key)
    }
  }

  return { getCachedPreviewBlob, putCachedPreviewBlob, cleanupPreviewCache }
}
