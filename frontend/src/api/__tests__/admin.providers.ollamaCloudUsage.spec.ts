import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get, post, put, del } provide the controlled request fixtures used by
 * admin.providers.ollamaCloudUsage tests to inspect their observable results.
 */
const { get, post, put, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put, delete: del },
}))

import {
  deleteOllamaCloudUsageSession,
  getOllamaCloudUsage,
  getOllamaCloudUsageSettings,
  refreshOllamaCloudUsage,
  saveOllamaCloudUsageSession,
  setOllamaCloudUsageAutoRefresh,
  updateOllamaCloudUsageSettings,
} from '@/api/admin/providers'

/**
 * admin.providers.ollamaCloudUsage.spec.ts checks Ollama Cloud usage
 * configuration and provider-session operations. Mocked requests distinguish
 * global settings from session storage, per-provider refresh and
 * automatic-refresh controls, and assertions inspect each action's dedicated
 * payload.
 * Session deletion also verifies the returned unconfigured state.
 */

/**
 * state provides the controlled request fixtures used by
 * admin.providers.ollamaCloudUsage tests to inspect their observable results.
 */
const state = {
  provider_id: 7,
  eligible: true,
  configured: true,
  auto_refresh_enabled: false,
  encryption_key_configured: true,
}

describe('admin Ollama Cloud usage API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    del.mockReset()
  })

  /**
   * Global Ollama Cloud usage settings are read and updated through mocked
   * requests. Both operations use their dedicated route and return the settings
   * object to the caller.
   */
  it('uses dedicated global settings endpoints', async () => {
    const settings = { enabled: false, interval_minutes: 60, debounce_minutes: 1 }
    get.mockResolvedValueOnce({ data: settings })
    put.mockResolvedValueOnce({ data: settings })

    await expect(getOllamaCloudUsageSettings()).resolves.toEqual(settings)
    await expect(updateOllamaCloudUsageSettings(settings)).resolves.toEqual(settings)
    expect(get).toHaveBeenCalledWith('/admin/providers/ollama-cloud-usage/settings')
    expect(put).toHaveBeenCalledWith('/admin/providers/ollama-cloud-usage/settings', settings)
  })

  /**
   * Provider session storage and refresh actions are exercised through their
   * separate endpoints. Requests carry session or enabled fields as appropriate
   * and return configuration state for the provider.
   */
  it('keeps session configuration write-only and separate from provider updates', async () => {
    get.mockResolvedValueOnce({ data: state })
    put.mockResolvedValueOnce({ data: state }).mockResolvedValueOnce({ data: state })
    del.mockResolvedValueOnce({ data: { ...state, configured: false } })
    post.mockResolvedValueOnce({ data: state })

    await expect(getOllamaCloudUsage(7)).resolves.toEqual(state)
    await expect(saveOllamaCloudUsageSession(7, 'wos-session=secret')).resolves.toEqual(state)
    await expect(setOllamaCloudUsageAutoRefresh(7, true)).resolves.toEqual(state)
    await expect(refreshOllamaCloudUsage(7)).resolves.toEqual(state)
    await expect(deleteOllamaCloudUsageSession(7)).resolves.toMatchObject({ configured: false })

    expect(put).toHaveBeenNthCalledWith(1, '/admin/providers/7/ollama-cloud-usage/session', {
      session: 'wos-session=secret',
    })
    expect(put).toHaveBeenNthCalledWith(2, '/admin/providers/7/ollama-cloud-usage/auto-refresh', {
      enabled: true,
    })
    expect(post).toHaveBeenCalledWith('/admin/providers/7/ollama-cloud-usage/refresh')
    expect(del).toHaveBeenCalledWith('/admin/providers/7/ollama-cloud-usage/session')
  })
})
