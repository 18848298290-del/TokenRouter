import { afterEach, describe, expect, it, vi } from 'vitest'
import { AxiosError } from 'axios'
import { apiClient } from '../client'
import { downloadFile } from '../admin/mcp'

// admin.mcp.spec.ts exercises protected downloads through the real browser API
// interceptors. A binary request can receive structured authorization errors;
// these cases protect the TOTP reason needed by the page and the uninterpreted
// bytes needed by file saving after a successful retry.

// savedAdapter restores the shared client after each isolated response fixture.
const savedAdapter = apiClient.defaults.adapter

afterEach(() => {
  apiClient.defaults.adapter = savedAdapter
  vi.restoreAllMocks()
})

describe('MCP protected downloads', () => {
  // A download receives JSON bytes requesting recent TOTP verification. The
  // shared error interceptor must preserve that reason so the page can prompt.
  it('preserves step-up requirements from binary error responses', async () => {
    apiClient.defaults.adapter = async (config) => {
      throw new AxiosError('Forbidden', 'ERR_BAD_REQUEST', config, undefined, {
        config,
        status: 403,
        statusText: 'Forbidden',
        headers: { 'content-type': 'application/json' },
        data: new TextEncoder().encode(
          JSON.stringify({ code: 403, reason: 'STEP_UP_REQUIRED', message: 'Verify again' }),
        ).buffer,
      })
    }
    await expect(downloadFile('owned-file')).rejects.toMatchObject({
      status: 403,
      reason: 'STEP_UP_REQUIRED',
    })
  })

  // A successful download returns non-text bytes after the client attaches its
  // normal authentication metadata. The file remains binary with its declared type.
  it('returns successful binary content without JSON interpretation', async () => {
    apiClient.defaults.adapter = async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: { 'content-type': 'application/gzip' },
      data: new Uint8Array([0, 255, 31]).buffer,
    })
    const result = await downloadFile('owned-file')
    expect(result).toBeInstanceOf(Blob)
    expect(result.type).toBe('application/gzip')
    expect(result.size).toBe(3)
  })
})
