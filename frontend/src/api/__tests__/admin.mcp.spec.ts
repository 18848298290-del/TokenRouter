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
    expect(result.blob).toBeInstanceOf(Blob)
    expect(result.blob.type).toBe('application/gzip')
    expect(result.blob.size).toBe(3)
  })

  // Download headers carry the filename separately from binary response bytes.
  // Ordinary and UTF-8 names survive API conversion, while malformed extended values retain the ordinary suggestion.
  it.each([
    ['attachment; filename="backup.sql.gz"', 'backup.sql.gz'],
    ["attachment; filename*=UTF-8''%E4%BC%98%E6%83%A0%E7%A0%81.json", '优惠码.json'],
    ["attachment; filename*=UTF-8''%ZZ; filename=backup.sql.gz", 'backup.sql.gz'],
    ['', 'tokenrouter-mcp-result'],
  ])('preserves download name from %s', async (header, filename) => {
    apiClient.defaults.adapter = async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: { 'content-type': 'application/gzip', 'content-disposition': header },
      data: new Uint8Array([0, 255, 31]).buffer,
    })
    const result = await downloadFile('named-file')
    expect(result.filename).toBe(filename)
    expect(result.blob.size).toBe(3)
  })
})
