import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get, post } provide the controlled request fixtures used by
 * admin.system.rollback tests to inspect their observable results.
 */
const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../client', () => ({
  apiClient: {
    get,
    post,
  },
}))

import {
  getRollbackVersions,
  performUpdate,
  rollback,
  type RollbackVersionInfo,
} from '@/api/admin/system'

/**
 * admin.system.rollback.spec.ts checks update and rollback requests from
 * administrator controls. Cases distinguish eligible-version reads, targeted
 * rollback and local-backup restoration, with timeout assertions protecting the
 * download budget used for binary replacement operations.
 */

describe('admin system rollback API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  /**
   * The server returns one eligible rollback release with publication metadata.
   * The wrapper uses the rollback-version list endpoint and exposes that version
   * list to the caller.
   */
  it('getRollbackVersions fetches the rollback version list', async () => {
    const versions: RollbackVersionInfo[] = [
      {
        version: '0.1.146',
        published_at: '2026-07-07T00:00:00Z',
        html_url: 'https://github.com/TokenFlux/TokenRouter/releases/tag/v0.1.146',
      },
    ]
    get.mockResolvedValue({ data: { versions } })

    const result = await getRollbackVersions()

    expect(get).toHaveBeenCalledWith('/admin/system/rollback-versions')
    expect(result.versions).toEqual(versions)
  })

  /**
   * An administrator requests an in-place system update. The POST uses the
   * fifteen-minute download budget so the shared client's shorter default does
   * not govern the request.
   */
  it('performUpdate waits for the backend update deadline', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    await performUpdate()

    expect(post).toHaveBeenCalledWith('/admin/system/update', undefined, {
      timeout: 15 * 60 * 1000,
    })
  })

  /**
   * Rollback is requested for a specific release version. The POST sends that
   * version with the extended download timeout and returns the server's restart
   * requirement.
   */
  it('rollback posts the target version in the request body', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    const result = await rollback('0.1.146')

    expect(post).toHaveBeenCalledWith(
      '/admin/system/rollback',
      { version: '0.1.146' },
      { timeout: 15 * 60 * 1000 },
    )
    expect(result.need_restart).toBe(true)
  })

  /**
   * Rollback is requested without selecting a release version. The POST carries
   * no body, selecting the backup restoration path while retaining the extended
   * request timeout.
   */
  it('rollback without a version posts no body (legacy backup rollback)', async () => {
    post.mockResolvedValue({ data: { message: 'ok', need_restart: true } })

    await rollback()

    expect(post).toHaveBeenCalledWith('/admin/system/rollback', undefined, {
      timeout: 15 * 60 * 1000,
    })
  })
})
