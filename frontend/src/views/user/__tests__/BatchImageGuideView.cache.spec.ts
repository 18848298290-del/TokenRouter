import { Blob as NodeBlob } from 'node:buffer'
import { IDBFactory } from 'fake-indexeddb'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import BatchImageGuideView from '../BatchImageGuideView.vue'
import { getBatchImageItemContent } from '@/api/batchImage'
import type { ApiKey } from '@/api/keys.types'
import type { BatchImageItem } from '@/api/batchImage'
import { previewCacheKey } from '../batch-image/previewCache'
import { createThumbnailBlob } from '../batch-image/previewImages'

// BatchImageGuideView.cache.spec.ts exercises the page's IndexedDB thumbnail
// storage and mounted resource lifecycle. Tests use the page's setup state to
// exercise its actual storage and thumbnail modules, inspect database records
// through a separate handle, and observe resource release during detail reset
// and unmount.
//
// Each case receives an isolated IndexedDB factory and deterministic time, while
// mocked keys and image content prevent provider calls. Teardown unmounts first
// and drains separate write transactions before closing the observation handle,
// allowing cache access touches to finish both their read and write phases.
// Decoder fixtures check canvas dimensions, encoding arguments, and temporary
// resource release alongside the page's cache retention and image display paths.

vi.mock('@/api/batchImage', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/batchImage')>()),
  getBatchImageItemContent: vi.fn(),
  listBatchImageModels: vi.fn().mockResolvedValue({ data: [] }),
}))
vi.mock('@/api', () => ({ keysAPI: { list: vi.fn().mockResolvedValue({ items: [] }) } }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ fetchPublicSettings: vi.fn(), showError: vi.fn() }),
}))
vi.mock('@/composables/useLocaleRefresh', () => ({ useLocaleRefresh: vi.fn() }))
vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({ formatBalanceAmount: String }),
}))
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() }),
}))
vi.mock('@/i18n', () => ({ getLocale: () => 'en' }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
}))

// CacheState describes the production setup functions exercised through the
// mounted page. The component instance remains responsible for startup and
// teardown, and each database belongs to one isolated test case.
interface CacheState {
  // apiKeys supplies the eligible credential fixture used by manual preview
  // loading, and form selects that key through the actual page's computed state.
  // Tests assign these together before requesting authenticated content through the page.
  apiKeys: ApiKey[] // loaded API key fixtures
  form: {
    apiKeyId: number // selected creation credential identifier
  }
  // loadItemPreview fetches and displays one eligible item's first image.
  // Tests supply the actual page credential state and mock content transport,
  // allowing encoding failures to exercise its original-byte display recovery.
  loadItemPreview(item: BatchImageItem): Promise<void>
  // putCachedPreviewBlob stores a thumbnail with its creation and access timestamps.
  // Storage absence resolves without writing, and successful requests schedule
  // cache maintenance after the record has been submitted.
  putCachedPreviewBlob(key: string, blob: Blob): Promise<void>
  // getCachedPreviewBlob returns the stored thumbnail while its lifetime remains valid.
  // Missing or expired entries return null, and cache hits schedule a separate
  // write that updates the access timestamp.
  getCachedPreviewBlob(key: string): Promise<Blob | null>
  // cleanupPreviewCache removes expired records and enforces the cache size limits.
  // Remaining records are evicted by their last access timestamp until both the
  // entry count and stored byte count fit.
  cleanupPreviewCache(): Promise<void>
  // hydrateCachedItemPreviews allocates browser URLs for successful images found in storage.
  // Existing URLs and recorded preview errors skip hydration, and the page
  // releases allocated URLs during its unmount cleanup.
  hydrateCachedItemPreviews(items: unknown[]): Promise<void>
  // clearItemPreviews releases URLs currently published by the detail page.
  // It also clears loading and error indicators, allowing the next detail load
  // to populate its own thumbnails from browser storage or content downloads.
  clearItemPreviews(): void
  // itemPreviewUrls contains the allocated URLs that page cleanup revokes before
  // removing their entries, allowing hydration tests to observe the resource lifetime.
  itemPreviewUrls: Record<string, string>
}

// CacheRecord contains the persisted fields inspected by cache retention assertions.
interface CacheRecord {
  key: string // escaped thumbnail storage identity
  lastAccessedAt: number // last access epoch milliseconds
}

// Each mounted wrapper and database are released after their test so interval
// callbacks and pending transactions cannot change another case's cache records.
let wrapper: VueWrapper | undefined
let database: IDBDatabase | null = null

// mountCache starts the actual page and returns its existing cache functions.
// Child rendering is stubbed while page lifecycle hooks still execute, so
// assertions cover storage integration and cleanup ownership across module calls.
async function mountCache(): Promise<CacheState> {
  wrapper = shallowMount(BatchImageGuideView)
  await flushPromises()
  const state = (wrapper.vm.$ as unknown as { setupState: CacheState }).setupState
  // Observe persisted records through a separate handle, keeping the storage
  // implementation's opening promise private while testing its actual IndexedDB format.
  database = await new Promise<IDBDatabase | null>((resolve) => {
    const request = indexedDB.open('tokenrouter-batch-image-preview-cache', 1)
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => resolve(null)
  })
  return state
}

// records reads persisted cache contents after earlier write transactions finish.
// Callers first mount the page with supported IndexedDB so its upgrade handler
// creates the thumbnail store before this inspection transaction starts.
function records(): Promise<CacheRecord[]> {
  return new Promise((resolve, reject) => {
    const request = database!.transaction('thumbnails').objectStore('thumbnails').getAll()
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

beforeEach(() => {
  vi.stubGlobal('indexedDB', new IDBFactory())
  vi.stubGlobal('Blob', NodeBlob)
  vi.spyOn(Date, 'now').mockReturnValue(1_800_000_000_000)
  vi.stubGlobal(
    'URL',
    class extends URL {
      // createObjectURL records thumbnail allocations with a deterministic browser URL.
      static createObjectURL = vi.fn(() => 'blob:cached-preview')
      // revokeObjectURL records resource disposal for the mounted page's preview URLs.
      static revokeObjectURL = vi.fn()
    },
  )
})

afterEach(async () => {
  // Unmount clears the page's cleanup interval before the database closes, so
  // later timer callbacks cannot open transactions against an already closed handle.
  wrapper?.unmount()
  wrapper = undefined
  if (database) {
    // Cache reads schedule a later write to refresh access time. Two exclusive
    // transactions let both phases finish before closing the test database handle.
    for (let phase = 0; phase < 2; phase += 1) {
      await new Promise<void>((resolve, reject) => {
        const transaction = database!.transaction('thumbnails', 'readwrite')
        transaction.oncomplete = () => resolve()
        transaction.onerror = () => reject(transaction.error)
      })
    }
    database.close()
  }
  database = null
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('batch image thumbnail cache', () => {
  // Two batches can reuse the same item label and include delimiter characters.
  // Their persisted thumbnails remain separate and the default image index is zero.
  it('stores thumbnails under escaped batch, item, and image identities', async () => {
    const cache = await mountCache()
    const first = previewCacheKey('batch:a', 'item/b')
    const second = previewCacheKey('batch', 'a:item/b')
    expect(first).toBe('batch%3Aa:item%2Fb:0')
    expect(second).not.toBe(first)
    expect(previewCacheKey('batch:a', 'item/b', 1)).not.toBe(first)
    await cache.putCachedPreviewBlob(first, new Blob(['first']))
    await cache.putCachedPreviewBlob(second, new Blob(['second']))
    expect(await (await cache.getCachedPreviewBlob(first))?.text()).toBe('first')
    expect(await (await cache.getCachedPreviewBlob(second))?.text()).toBe('second')
  })

  // A thumbnail is usable at the three day age limit and expires afterward.
  // Reading the expired entry returns no image and removes its persisted record.
  it('expires thumbnails only after their three day lifetime', async () => {
    const cache = await mountCache()
    await cache.putCachedPreviewBlob('entry', new Blob(['image']))
    const createdAt = Date.now()
    // The production lifetime is three days, measured in milliseconds from creation.
    vi.mocked(Date.now).mockReturnValue(createdAt + 3 * 24 * 60 * 60 * 1000)
    expect(await cache.getCachedPreviewBlob('entry')).not.toBeNull()
    vi.mocked(Date.now).mockReturnValue(Date.now() + 1)
    expect(await cache.getCachedPreviewBlob('entry')).toBeNull()
    await vi.waitFor(async () => expect(await records()).toEqual([]))
  })

  // Reading a valid cached thumbnail refreshes its eviction timestamp.
  // This protects recently viewed images when later cache maintenance chooses older records.
  it('touches the last access time after a cache hit', async () => {
    const cache = await mountCache()
    await cache.putCachedPreviewBlob('entry', new Blob(['image']))
    vi.mocked(Date.now).mockReturnValue(Date.now() + 1000)
    await cache.getCachedPreviewBlob('entry')
    await vi.waitFor(async () => {
      expect(await records()).toEqual([
        expect.objectContaining({ key: 'entry', lastAccessedAt: Date.now() }),
      ])
    })
  })

  // The cache exceeds its entry limit with records ordered by recent access.
  // Maintenance removes the oldest record and retains the newest 120 thumbnails.
  it('evicts the least recently accessed thumbnail above the entry limit', async () => {
    const cache = await mountCache()
    const transaction = database!.transaction('thumbnails', 'readwrite')
    const store = transaction.objectStore('thumbnails')
    for (let index = 0; index < 121; index += 1) {
      store.put({
        key: String(index),
        blob: new Blob(['x']),
        size: 1,
        createdAt: Date.now(),
        lastAccessedAt: index,
      })
    }
    await cache.cleanupPreviewCache()
    const retained = await records()
    expect(retained).toHaveLength(120)
    expect(retained.some((record) => record.key === '0')).toBe(false)
    expect(retained.some((record) => record.key === '120')).toBe(true)
  })

  // Browser storage can reject opening a database before any thumbnail is read.
  // The page treats that failure as a cache miss and permits later writes to resolve.
  it('degrades to cache misses when IndexedDB opening fails', async () => {
    vi.spyOn(indexedDB, 'open').mockImplementation(() => {
      const request = {} as IDBOpenDBRequest
      queueMicrotask(() => request.onerror?.(new Event('error')))
      return request
    })
    const cache = await mountCache()
    expect(await cache.getCachedPreviewBlob('missing')).toBeNull()
    await expect(
      cache.putCachedPreviewBlob('ignored', new Blob(['image'])),
    ).resolves.toBeUndefined()
  })

  // Hydration allocates a browser URL for a successful cached image.
  // Unmount releases that URL so a page visit does not retain the thumbnail in memory.
  it('revokes hydrated thumbnail URLs when the page unmounts', async () => {
    const cache = await mountCache()
    const key = previewCacheKey('batch', 'item')
    await cache.putCachedPreviewBlob(key, new Blob(['image']))
    await cache.hydrateCachedItemPreviews([
      { batch_id: 'batch', custom_id: 'item', status: 'succeeded', image_count: 1 },
    ])
    expect(cache.itemPreviewUrls[key]).toBe('blob:cached-preview')
    wrapper!.unmount()
    wrapper = undefined
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:cached-preview')
  })

  // Byte pressure can exceed the cache budget before the entry count limit.
  // Maintenance evicts older access records until retained metadata fits 48 MiB.
  it('enforces the byte budget independently of the entry limit', async () => {
    const cache = await mountCache()
    const store = database!.transaction('thumbnails', 'readwrite').objectStore('thumbnails')
    for (let index = 0; index < 3; index += 1) {
      store.put({
        key: String(index),
        blob: new Blob(['x']),
        size: 24 * 1024 * 1024,
        createdAt: Date.now(),
        lastAccessedAt: index,
      })
    }
    await cache.cleanupPreviewCache()
    expect((await records()).map((record) => record.key)).toEqual(['1', '2'])
  })

  // Bitmap decoding supplies a landscape image larger than the thumbnail limit.
  // Encoding keeps its aspect ratio and closes the decoded bitmap after drawing.
  it('encodes bounded WebP thumbnails and releases decoded bitmap memory', async () => {
    await mountCache()
    const bitmap = { width: 720, height: 360, close: vi.fn() }
    vi.stubGlobal('createImageBitmap', vi.fn().mockResolvedValue(bitmap))
    const drawImage = vi.fn()
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      drawImage,
    } as unknown as CanvasRenderingContext2D)
    const thumbnail = new Blob(['thumbnail'])
    const encode = vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (
      this: HTMLCanvasElement,
      callback,
    ) {
      expect(this.width).toBe(360)
      expect(this.height).toBe(180)
      callback(thumbnail)
    })
    expect(await createThumbnailBlob(new Blob(['source']))).toBe(thumbnail)
    expect(drawImage).toHaveBeenCalledWith(bitmap, 0, 0, 360, 180)
    expect(bitmap.close).toHaveBeenCalledOnce()
    expect(encode).toHaveBeenCalledWith(expect.any(Function), 'image/webp', 0.72)
  })

  // Clearing detail previews precedes loading another selected batch's items.
  // Each published URL is revoked and its identity disappears before cache hydration repeats.
  it('releases existing URLs when detail previews are cleared', async () => {
    const cache = await mountCache()
    const key = previewCacheKey('batch', 'item')
    await cache.putCachedPreviewBlob(key, new Blob(['image']))
    await cache.hydrateCachedItemPreviews([
      { batch_id: 'batch', custom_id: 'item', status: 'succeeded', image_count: 1 },
    ])
    cache.clearItemPreviews()
    expect(cache.itemPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:cached-preview')
  })

  // Thumbnail decoding can fail after authenticated image content has downloaded.
  // The page still displays small original bytes and persists them for later hydration.
  it('displays and caches a small original image after thumbnail decoding fails', async () => {
    const cache = await mountCache()
    cache.apiKeys = [
      {
        id: 7,
        status: 'active',
        key: 'local-test-key',
        group: { allowed_protocols: ['image_batches'] },
      } as ApiKey,
    ]
    cache.form.apiKeyId = 7
    vi.stubGlobal('createImageBitmap', vi.fn().mockRejectedValue(new Error('decode failed')))
    const original = new Blob(['original'])
    vi.mocked(getBatchImageItemContent).mockResolvedValueOnce(original)
    await cache.loadItemPreview({
      batch_id: 'batch',
      custom_id: 'item',
      status: 'succeeded',
      image_count: 1,
    } as BatchImageItem)
    expect(getBatchImageItemContent).toHaveBeenCalledWith('local-test-key', 'batch', 'item', 0)
    expect(URL.createObjectURL).toHaveBeenCalledWith(original)
    await vi.waitFor(async () => expect(await records()).toHaveLength(1))
  })

  // Browsers without bitmap decoding load an image element from a temporary URL.
  // A rejected element load releases that URL before reporting the decoder failure.
  it('revokes the temporary decoder URL when image element loading fails', async () => {
    await mountCache()
    Reflect.deleteProperty(window, 'createImageBitmap')
    vi.stubGlobal(
      'Image',
      class {
        // onerror receives the decoder's failure handler before the src setter
        // schedules its microtask, reproducing an asynchronous browser image load failure.
        onerror: (() => void) | null = null
        // src schedules a failed browser image load for this decoder fixture.
        // The microtask runs after handler assignment, allowing the decoder's
        // rejection path to release its temporary Blob URL before the assertion.
        set src(_value: string) {
          queueMicrotask(() => this.onerror?.())
        }
      },
    )
    await expect(createThumbnailBlob(new Blob(['image']))).rejects.toThrow('image unavailable')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:cached-preview')
  })
})
