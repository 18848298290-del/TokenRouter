import 'fake-indexeddb/auto'
import { Blob as NodeBlob } from 'node:buffer'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/**
 * Use Node Blob because fake-indexeddb relies on Node structuredClone, which
 * does not retain the jsdom Blob representation used by other DOM fixtures.
 */
globalThis.Blob = NodeBlob as unknown as typeof Blob
import {
  CREATIVE_WORKSPACE_STORAGE_KEY,
  LocalStoreQuotaError,
  LocalStoreError,
  __resetCreativeStoreForTest,
  clearAll,
  clearKind,
  deleteAsset,
  getCreativeWorkspaceId,
  listAssets,
  loadAsset,
  loadSceneJson,
  loadRunParamsMap,
  loadSetting,
  localAssetKey,
  outputAssetKey,
  rotateCreativeWorkspaceId,
  saveAsset,
  saveRunParams,
  saveSceneJson,
  saveSetting,
  type LocalAsset,
  type LocalRunParams,
} from '@/utils/creativeLocalStore'

/**
 * creativeLocalStore.spec.ts exercises creative persistence against
 * fake-indexeddb and origin-local workspace storage. Node Blob supplies
 * structured-cloneable image bytes for asset round trips, while injected
 * request failures exercise unavailable and quota classification. The suite
 * covers asset ordering, run-parameter retention, identity rotation, and
 * complete local clearing without connecting to a server.
 */

/**
 * makeRunParams builds a complete local parameter snapshot for one test run. It
 * fixes display values so retention and lookup assertions can focus on the
 * supplied identifier and persisted prompt.
 */
function makeRunParams(runId: string): LocalRunParams {
  return {
    runId,
    optionKey: 'g1::model-x',
    groupName: 'Group A',
    operation: 'generate',
    prompt: `prompt for ${runId}`,
    imageSize: '1K',
    aspectRatio: 'auto',
    quality: 'high',
    background: '',
    thinkingLevel: '',
    referenceCount: 0,
    createdAt: 1,
  }
}

/**
 * makeAsset supplies default image bytes and creation time for a local asset
 * fixture. Caller properties override those defaults, enabling ordering and
 * output-identity scenarios to share the same construction helper.
 */
function makeAsset(partial: Partial<LocalAsset> & Pick<LocalAsset, 'key' | 'kind'>): LocalAsset {
  return {
    blob: new Blob(['x'], { type: 'image/png' }),
    createdAt: Date.now(),
    ...partial,
  }
}

describe('creativeLocalStore', () => {
  beforeEach(async () => {
    __resetCreativeStoreForTest()
    localStorage.clear()
    await clearAll()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  /**
   * An output asset includes image bytes and its server identity metadata. Saving
   * and loading by its key must retain the expected kind, ownership, size, and
   * creation timestamp.
   */
  it('saveAsset/loadAsset 往返保存 blob 与元信息', async () => {
    const asset = makeAsset({
      key: outputAssetKey('run-1', 2),
      kind: 'output',
      runId: 'run-1',
      outputIndex: 2,
    })

    await saveAsset(asset)
    const loaded = await loadAsset(asset.key)

    expect(loaded).not.toBeNull()
    expect(loaded?.kind).toBe('output')
    expect(loaded?.runId).toBe('run-1')
    expect(loaded?.outputIndex).toBe(2)
    expect(loaded?.blob.size).toBe(asset.blob.size)
    expect(loaded?.createdAt).toBe(asset.createdAt)
  })

  /**
   * A requested asset key has never been stored in the local database. Reading it
   * must return null so callers can handle missing images explicitly.
   */
  it('loadAsset 不存在时返回 null', async () => {
    expect(await loadAsset('missing:key')).toBeNull()
  })

  /**
   * Assets of different kinds are inserted out of creation-time order. Listing
   * one kind must filter unrelated records and return its own records oldest
   * first.
   */
  it('listAssets 按 kind 过滤并按 createdAt 升序', async () => {
    const sourceA = makeAsset({ key: localAssetKey('source', 'a'), kind: 'source', createdAt: 200 })
    const sourceB = makeAsset({ key: localAssetKey('source', 'b'), kind: 'source', createdAt: 100 })
    const output = makeAsset({ key: outputAssetKey('run-1', 0), kind: 'output', createdAt: 50 })

    await saveAsset(sourceA)
    await saveAsset(output)
    await saveAsset(sourceB)

    const sources = await listAssets('source')
    expect(sources.map((a) => a.key)).toEqual([sourceB.key, sourceA.key])
    expect((await listAssets('output')).map((a) => a.key)).toEqual([output.key])
    expect(await listAssets('mask')).toEqual([])
  })

  /**
   * A stored source asset is deleted twice through the public helper. Both
   * deletions must succeed, and a subsequent read must report the key as missing.
   */
  it('deleteAsset 幂等：重复删除不报错，删除后读取为 null', async () => {
    const asset = makeAsset({ key: localAssetKey('source', 'del'), kind: 'source' })
    await saveAsset(asset)

    await deleteAsset(asset.key)
    await deleteAsset(asset.key)
    expect(await loadAsset(asset.key)).toBeNull()
  })

  /**
   * The named scene snapshot starts absent and is then saved as JSON. Reading
   * must first return null and later return the exact serialized text.
   */
  it('saveSceneJson/loadSceneJson 往返', async () => {
    expect(await loadSceneJson('creative:canvas')).toBeNull()
    await saveSceneJson('creative:canvas', '{"objects":[]}')
    expect(await loadSceneJson('creative:canvas')).toBe('{"objects":[]}')
  })

  /**
   * A named setting is saved twice with distinct option keys. Reads must show the
   * latest value while the initial missing lookup returns null.
   */
  it('saveSetting/loadSetting 往返且覆盖写', async () => {
    expect(await loadSetting('creative:selection')).toBeNull()
    await saveSetting('creative:selection', { optionKey: 'a' })
    expect(await loadSetting('creative:selection')).toEqual({ optionKey: 'a' })
    await saveSetting('creative:selection', { optionKey: 'b' })
    expect(await loadSetting('creative:selection')).toEqual({ optionKey: 'b' })
  })

  /**
   * Run parameter records share the settings store with a normal selection
   * setting. Reading their index must include both runs and leave the unrelated
   * setting unchanged.
   */
  it('loadRunParamsMap 读回 run: 前缀的记录，设置项保持原值', async () => {
    await saveSetting('creative:selection', { optionKey: 'a' })
    await saveRunParams(makeRunParams('run-1'))
    await saveRunParams(makeRunParams('run-2'))

    const map = await loadRunParamsMap()

    expect([...map.keys()].sort()).toEqual(['run-1', 'run-2'])
    expect(map.get('run-1')?.prompt).toBe('prompt for run-1')
    expect(await loadSetting('creative:selection')).toEqual({ optionKey: 'a' })
  })

  /**
   * Two hundred and one parameter records are written with increasing update
   * timestamps. Retention must keep two hundred entries, remove the oldest, and
   * preserve the latest run.
   */
  it('saveRunParams 超过 200 条时删除最早写入的记录', async () => {
    let now = 1000
    vi.spyOn(Date, 'now').mockImplementation(() => now++)
    for (let i = 0; i < 201; i++) {
      await saveRunParams(makeRunParams(`run-${i}`))
    }

    const map = await loadRunParamsMap()

    expect(map.size).toBe(200)
    expect(map.has('run-0')).toBe(false)
    expect(map.has('run-200')).toBe(true)
  })

  /**
   * The browser has no creative workspace identity on its first read. Creation
   * must persist a valid UUID and later reads must reuse that same stored value.
   */
  it('工作区首次生成并在同源标签页之间复用', () => {
    const first = getCreativeWorkspaceId()

    expect(first).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    expect(localStorage.getItem(CREATIVE_WORKSPACE_STORAGE_KEY)).toBe(first)
    expect(getCreativeWorkspaceId()).toBe(first)
  })

  /**
   * The stored workspace identity is invalid before a read and later rotated.
   * Both resulting identities must be valid, and rotation must persist a
   * different value.
   */
  it('非法工作区值会重新生成，旋转后与原值不同', () => {
    localStorage.setItem(CREATIVE_WORKSPACE_STORAGE_KEY, 'invalid')
    const generated = getCreativeWorkspaceId()
    expect(generated).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    expect(generated).not.toBe('invalid')

    const rotated = rotateCreativeWorkspaceId()
    expect(rotated).not.toBe(generated)
    expect(localStorage.getItem(CREATIVE_WORKSPACE_STORAGE_KEY)).toBe(rotated)
  })

  /**
   * Browser localStorage throws a security error when reading workspace identity.
   * The helper must raise LocalStoreError rather than returning an identifier for
   * subsequent server requests.
   */
  it('本地存储不可用时工作区读取失败，不回退到共享标识', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })

    expect(() => getCreativeWorkspaceId()).toThrow(LocalStoreError)
  })

  /**
   * Assets, a scene, a setting, and task parameters are stored before clearing.
   * After clearAll completes, every public read must report its corresponding
   * data as empty or absent.
   */
  it('clearAll 清空素材、场景、设置和任务参数', async () => {
    await saveAsset(makeAsset({ key: localAssetKey('source', 'c'), kind: 'source' }))
    await saveSceneJson('creative:canvas', '{}')
    await saveSetting('creative:selection', {})
    await saveRunParams(makeRunParams('run-1'))

    await clearAll()

    expect(await listAssets('source')).toEqual([])
    expect(await loadSceneJson('creative:canvas')).toBeNull()
    expect(await loadSetting('creative:selection')).toBeNull()
    expect((await loadRunParamsMap()).size).toBe(0)
  })

  /**
   * The asset store contains both source and output images when one kind is
   * cleared. Source records must disappear while the unrelated output remains
   * available for lookup.
   */
  it('clearKind 只清空指定种类的素材', async () => {
    const source = makeAsset({ key: localAssetKey('source', 'k1'), kind: 'source' })
    const output = makeAsset({ key: outputAssetKey('run-1', 0), kind: 'output' })
    await saveAsset(source)
    await saveAsset(output)

    await clearKind('source')

    expect(await listAssets('source')).toEqual([])
    expect((await listAssets('output')).map((a) => a.key)).toEqual([output.key])
  })

  /**
   * An IndexedDB clear request reports an asynchronous underlying error. clearAll
   * must reject with the classified storage error used by callers to report
   * clearing failure.
   */
  it('clearAll 底层失败归一为 LocalStoreError', async () => {
    const failure = new Error('idb down')
    vi.spyOn(IDBObjectStore.prototype, 'clear').mockImplementation(() => {
      const request: Record<string, unknown> = { error: null, result: undefined }
      queueMicrotask(() => {
        request.error = failure
        const onerror = request.onerror as ((event: unknown) => void) | null
        onerror?.call(request, { target: request })
      })
      return request as unknown as IDBRequest
    })

    await expect(clearAll()).rejects.toMatchObject({ name: 'LocalStoreError' })
  })

  /**
   * An IndexedDB put request reports a quota-exceeded DOM exception. Saving an
   * asset must reject with LocalStoreQuotaError and retain the quota category
   * used by UI recovery.
   */
  it('QuotaExceededError 归一为带 type 标志的 LocalStoreQuotaError', async () => {
    const quotaError = new DOMException('quota exceeded', 'QuotaExceededError')
    /**
     * Trigger the request error asynchronously so production handlers attach before
     * the quota failure is delivered, matching the IndexedDB event ordering.
     */
    vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(() => {
      const request: Record<string, unknown> = { error: null, result: undefined }
      queueMicrotask(() => {
        request.error = quotaError
        const onerror = request.onerror as ((event: unknown) => void) | null
        onerror?.call(request, { target: request })
      })
      return request as unknown as IDBRequest
    })

    const attempt = saveAsset(makeAsset({ key: localAssetKey('source', 'quota'), kind: 'source' }))
    await expect(attempt).rejects.toBeInstanceOf(LocalStoreQuotaError)
    await expect(attempt).rejects.toMatchObject({ type: 'quota' })
  })
})
