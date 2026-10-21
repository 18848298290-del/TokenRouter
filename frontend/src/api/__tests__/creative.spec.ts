import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { AxiosInstance, InternalAxiosRequestConfig } from 'axios'

vi.mock('@/i18n', () => ({
  /**
   * getLocale supplies the fixed locale used by this request-interceptor test
   * fixture.
   */
  getLocale: () => 'zh-CN',
}))

import { apiClient } from '@/api/client'
import {
  ackCreativeRunOutput,
  createCreativeRun,
  getCreativeRun,
  getCreativeRunOutputContent,
  getCreativeModels,
  getCreativeRuns,
  getCreativeActiveRuns,
  getCreativeCapabilities,
} from '@/api/creative'

/**
 * creative.spec.ts checks Creative Studio requests through the shared Axios
 * client. Adapter observations protect multipart uploads, workspace ownership,
 * optional idempotency keys, binary output downloads and normalization of the
 * history or active-run response shapes.
 */

/**
 * WORKSPACE_ID provides the controlled request fixtures used by creative tests
 * to inspect their observable results.
 */
const WORKSPACE_ID = '11111111-1111-4111-8111-111111111111'

/**
 * jsonResponse wraps supplied data in a successful Axios adapter envelope for
 * API tests.
 */
function jsonResponse(data: unknown) {
  return {
    status: 200,
    data: { code: 0, message: 'ok', data },
    headers: {},
    config: {},
    statusText: 'OK',
  }
}

describe('creative API', () => {
  let adapter: ReturnType<typeof vi.fn>

  beforeEach(() => {
    localStorage.clear()
    adapter = vi.fn()
    apiClient.defaults.adapter = adapter as AxiosInstance['defaults']['adapter']
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  describe('createCreativeRun', () => {
    /**
     * An assembled image-generation form is submitted with workspace and
     * idempotency identifiers. The adapter receives multipart FormData with its
     * prompt and both headers intact.
     */
    it('以 multipart 发送 FormData（不被 JSON 化）且带幂等键', async () => {
      adapter.mockResolvedValue(jsonResponse({ id: 'run-1', status: 'queued' }))

      const form = new FormData()
      form.append('prompt', 'hello')
      form.append('operation', 'generate')
      const run = await createCreativeRun(form, WORKSPACE_ID, 'idem-key-123')

      expect(run.id).toBe('run-1')
      expect(adapter).toHaveBeenCalledTimes(1)

      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.method).toBe('post')
      expect(config.url).toBe('/creative/runs')
      /**
       * FormData reaches the adapter as the same object, preserving uploaded binary
       * parts through Axios request transformation.
       */
      expect(config.data).toBeInstanceOf(FormData)
      expect((config.data as FormData).get('prompt')).toBe('hello')
      /**
       * The multipart override prevents the instance JSON content type from converting
       * uploaded files into JSON data.
       */
      expect(String(config.headers.get('Content-Type'))).toContain('multipart/form-data')
      expect(config.headers.get('X-Creative-Workspace-ID')).toBe(WORKSPACE_ID)
      expect(config.headers.get('Idempotency-Key')).toBe('idem-key-123')
    })

    /**
     * A creation call omits its optional idempotency key. The adapter observes
     * no Idempotency-Key header, while the request still proceeds through the
     * creation endpoint.
     */
    it('不传幂等键时不附加 Idempotency-Key 头', async () => {
      adapter.mockResolvedValue(jsonResponse({ id: 'run-2', status: 'queued' }))

      await createCreativeRun(new FormData(), WORKSPACE_ID)

      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.headers.get('Idempotency-Key')).toBeFalsy()
    })

    /**
     * Creation is exercised before and after storing a session token. API-key
     * headers remain absent, and the logged-in request receives the shared
     * client's Bearer JWT.
     */
    it('请求不携带任何 API Key 相关头；Authorization 仅为 JWT 拦截器注入', async () => {
      adapter.mockResolvedValue(jsonResponse({ id: 'run-3', status: 'queued' }))

      await createCreativeRun(new FormData(), WORKSPACE_ID)

      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.headers.get('x-api-key')).toBeFalsy()
      expect(config.headers.get('X-Api-Key')).toBeFalsy()
      expect(config.headers.get('apikey')).toBeFalsy()
      /**
       * An anonymous browser session supplies no JWT.
       */
      expect(config.headers.get('Authorization')).toBeFalsy()

      /**
       * The shared client interceptor attaches the logged-in session's Bearer token
       * before the adapter inspects this request.
       */
      localStorage.setItem('auth_token', 'jwt-token-abc')
      adapter.mockClear()
      await createCreativeRun(new FormData(), WORKSPACE_ID)
      const authed = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(authed.headers.get('Authorization')).toBe('Bearer jwt-token-abc')
    })
  })

  describe('getCreativeRunOutputContent', () => {
    /**
     * A transient image output is downloaded through the shared client. The same
     * Blob is returned with the binary response type, workspace header and
     * five-minute timeout.
     */
    it('使用 responseType blob 并原样返回二进制', async () => {
      const blob = new Blob(['png-bytes'], { type: 'image/png' })
      adapter.mockResolvedValue({
        status: 200,
        data: blob,
        headers: {},
        config: {},
        statusText: 'OK',
      })

      const result = await getCreativeRunOutputContent('run-9', 2, WORKSPACE_ID)

      expect(result).toBe(blob)
      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.url).toBe('/creative/runs/run-9/outputs/2/content')
      expect(config.responseType).toBe('blob')
      expect(config.timeout).toBe(300000)
      expect(config.headers.get('X-Creative-Workspace-ID')).toBe(WORKSPACE_ID)
    })
  })

  describe('getCreativeModels / getCreativeRuns', () => {
    /**
     * The model catalog is fetched for the Creative Studio selector. The
     * returned group-model entries are retained and the catalog request carries
     * no workspace header.
     */
    it('拉取模型目录', async () => {
      adapter.mockResolvedValue(jsonResponse([{ group_id: 'g1', model: 'm1' }]))

      const models = await getCreativeModels()

      expect(models).toEqual([{ group_id: 'g1', model: 'm1' }])
      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.url).toBe('/creative/models')
      expect(config.headers.get('X-Creative-Workspace-ID')).toBeFalsy()
    })

    /**
     * History responses alternate between a page object and a direct array.
     * Normalization retains explicit totals, derives array totals and sends
     * limit with the workspace header.
     */
    it('列表接口兼容 {items,total} 与数组两种返回', async () => {
      adapter.mockResolvedValueOnce(jsonResponse({ items: [{ id: 'r1' }], total: 5 }))
      expect(await getCreativeRuns(WORKSPACE_ID, 1, 20)).toEqual({
        items: [{ id: 'r1' }],
        total: 5,
      })

      adapter.mockResolvedValueOnce(jsonResponse([{ id: 'r2' }]))
      expect(await getCreativeRuns(WORKSPACE_ID, 1, 20)).toEqual({
        items: [{ id: 'r2' }],
        total: 1,
      })

      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.params.limit).toBe(20)
      expect(config.params.page).toBeUndefined()
      expect(config.params.page_size).toBeUndefined()
      expect(config.headers.get('X-Creative-Workspace-ID')).toBe(WORKSPACE_ID)
    })

    /**
     * Server input capabilities and an active-run cursor page are returned in
     * sequence. The client retains capability values and forwards the opaque
     * cursor with limit and workspace identity.
     */
    it('读取服务端能力和 active runs 游标', async () => {
      adapter.mockResolvedValueOnce(
        jsonResponse({
          max_prompt_chars: 9000,
          max_asset_bytes: 12,
          max_total_input_bytes: 20,
          max_mask_bytes: 8,
          allowed_mime_types: ['image/png'],
        }),
      )
      expect(await getCreativeCapabilities()).toEqual({
        max_prompt_chars: 9000,
        max_asset_bytes: 12,
        max_total_input_bytes: 20,
        max_mask_bytes: 8,
        allowed_mime_types: ['image/png'],
      })
      adapter.mockResolvedValueOnce(
        jsonResponse({ items: [{ id: 'r1' }], next_cursor: 'abc', has_more: true }),
      )
      expect(await getCreativeActiveRuns(WORKSPACE_ID, 'prev', 100)).toEqual({
        items: [{ id: 'r1' }],
        next_cursor: 'abc',
        has_more: true,
      })
      const config = adapter.mock.calls[1][0] as InternalAxiosRequestConfig
      expect(config.url).toBe('/creative/runs/active')
      expect(config.params).toMatchObject({ limit: 100, cursor: 'prev' })
      expect(config.headers.get('X-Creative-Workspace-ID')).toBe(WORKSPACE_ID)
    })
  })

  describe('getCreativeRun / ackCreativeRunOutput', () => {
    /**
     * A run detail is requested within a browser workspace. The adapter receives
     * the run-specific GET path and workspace header, and the returned run
     * identifier is retained.
     */
    it('查询单个 run 详情', async () => {
      adapter.mockResolvedValue(jsonResponse({ id: 'run-4', status: 'running' }))

      const run = await getCreativeRun('run-4', WORKSPACE_ID)

      expect(run.id).toBe('run-4')
      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.method).toBe('get')
      expect(config.url).toBe('/creative/runs/run-4')
      expect(config.headers.get('X-Creative-Workspace-ID')).toBe(WORKSPACE_ID)
    })

    /**
     * A locally persisted output is acknowledged by run and output index. The
     * request uses the output-specific POST path and carries the owning
     * workspace header.
     */
    it('确认输出已持久化', async () => {
      adapter.mockResolvedValue(jsonResponse(null))

      await ackCreativeRunOutput('run-4', 1, WORKSPACE_ID)

      const config = adapter.mock.calls[0][0] as InternalAxiosRequestConfig
      expect(config.method).toBe('post')
      expect(config.url).toBe('/creative/runs/run-4/outputs/1/ack')
      expect(config.headers.get('X-Creative-Workspace-ID')).toBe(WORKSPACE_ID)
    })
  })
})
