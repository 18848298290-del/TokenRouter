import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { post } provide the controlled request fixtures used by
 * admin.providers.duplicate tests to inspect their observable results.
 */
const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import { duplicate } from '@/api/admin/providers'

/**
 * admin.providers.duplicate.spec.ts checks provider-copy operation keys before
 * and after ambiguous failures. Request mocks expose reuse within one module and
 * recovery after reload, while sessionStorage assertions protect the current
 * namespace and success cleanup.
 */

describe('admin provider duplicate API', () => {
  beforeEach(() => {
    sessionStorage.clear()
    post.mockReset()
    post.mockResolvedValue({ data: { id: 43, name: 'primary (Copy)' } })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue(
      '11111111-1111-4111-8111-111111111111',
    )
  })

  /**
   * A provider copy uses a deterministic UUID for its first request. The
   * idempotency header contains the source provider identifier and the returned
   * copy is unwrapped.
   */
  it('sends a stable idempotency key with the duplicate request', async () => {
    const provider = await duplicate(42)

    expect(post).toHaveBeenCalledWith('/admin/providers/42/duplicate', undefined, {
      headers: {
        'Idempotency-Key': 'provider-duplicate-42-11111111-1111-4111-8111-111111111111',
      },
    })
    expect(provider).toEqual({ id: 43, name: 'primary (Copy)' })
  })

  /**
   * A provider copy times out before a retry succeeds. The pending key uses the
   * TokenRouter sessionStorage namespace and both requests carry the same
   * operation header.
   */
  it('reuses the operation key after an ambiguous failed request', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(99)).rejects.toThrow('network timeout')

    const storedKey = sessionStorage.getItem('tokenrouter:admin:provider-duplicate:99')
    expect(storedKey).toBe('provider-duplicate-99-11111111-1111-4111-8111-111111111111')
    expect(sessionStorage.getItem('sub2api:admin:provider-duplicate:99')).toBeNull()

    post.mockResolvedValueOnce({ data: { id: 100, name: 'retry (Copy)' } })
    await duplicate(99)

    expect(post).toHaveBeenCalledTimes(2)
    const firstHeaders = post.mock.calls[0][2].headers
    const secondHeaders = post.mock.calls[1][2].headers
    expect(secondHeaders).toEqual(firstHeaders)
  })

  /**
   * A failed provider copy is followed by a module reload. The request reuses
   * the persisted operation header and successful completion removes its
   * sessionStorage entry.
   */
  it('reuses the operation key after a page reload', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(77)).rejects.toThrow('network timeout')
    const firstHeaders = post.mock.calls[0][2].headers

    vi.resetModules()
    post.mockResolvedValueOnce({ data: { id: 78, name: 'reload (Copy)' } })
    const { duplicate: duplicateAfterReload } = await import('@/api/admin/providers')
    await duplicateAfterReload(77)

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2].headers).toEqual(firstHeaders)
    expect(sessionStorage.length).toBe(0)
  })
})
