import { describe, expect, it, vi } from 'vitest'
import type { ContentModerationLog, ContentModerationCyberWarning } from '@/api/admin/riskControl'
import { useRiskControlDetails } from '../useRiskControlDetails'

/**
 * useRiskControlDetails.spec.ts verifies shared detail generations and preview
 * resource ownership. Deferred requests reproduce close, supersession, and media
 * failure interleavings while mocked authenticated services keep the tests
 * independent of stored audit content and external requests.
 */

/**
 * setupDetails creates isolated request mocks and deterministic preview URL spies.
 * Callers dispose the returned detail owner after assertions so any published
 * previews are released through the same cleanup path as the page.
 */
function setupDetails() {
  const services = {
    getLog: vi.fn(),
    getCyberWarning: vi.fn(),
    getMediaContent: vi.fn(),
    onError: vi.fn(),
  }
  const createObjectURL = vi.fn(() => 'blob:preview')
  const revokeObjectURL = vi.fn()
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })
  return { services, createObjectURL, revokeObjectURL, details: useRiskControlDetails(services) }
}

describe('risk-control detail resources', () => {
  // A cyber record can be selected while an older moderation request remains pending.
  // Only the newer generation publishes detail content or reports its request failure.
  it('suppresses a superseded moderation failure', async () => {
    const { services, details } = setupDetails()
    const pending = Promise.withResolvers<ContentModerationLog>()
    services.getLog.mockReturnValue(pending.promise)
    services.getCyberWarning.mockResolvedValue({ id: 2, warning_text: 'current', media: [] })
    const oldRequest = details.openInputDetail({ id: 1 } as ContentModerationLog)
    await details.openCyberDetail({ id: 2 } as ContentModerationCyberWarning)
    pending.reject(new Error('late failure'))
    await oldRequest
    expect(details.cyberDetailRow.value?.warning_text).toBe('current')
    expect(details.cyberDetailLoading.value).toBe(false)
    expect(services.onError).not.toHaveBeenCalled()
    details.dispose()
  })

  // One stored snapshot can fail while another ready snapshot downloads successfully.
  // The completed preview remains available and closing the dialog releases its URL once.
  it('keeps successful media and skips unavailable snapshots', async () => {
    const { services, details, revokeObjectURL } = setupDetails()
    services.getLog.mockResolvedValue({
      id: 1,
      media: [
        { id: 3, snapshot_status: 'ready' },
        { id: 4, snapshot_status: 'ready' },
        { id: 5, snapshot_status: 'failed' },
      ],
    })
    services.getMediaContent
      .mockResolvedValueOnce(new Blob(['ok']))
      .mockRejectedValueOnce(new Error('missing'))
    await details.openInputDetail({ id: 1 } as ContentModerationLog)
    expect(services.getMediaContent.mock.calls).toEqual([[3], [4]])
    expect(details.mediaObjectURLs.value).toEqual({ 3: 'blob:preview' })
    expect(details.inputDetailLoading.value).toBe(false)
    expect(services.onError).not.toHaveBeenCalled()
    details.closeInputDetail()
    details.dispose()
    expect(revokeObjectURL).toHaveBeenCalledExactlyOnceWith('blob:preview')
  })

  // A newer record can finish while the preceding record still downloads media.
  // The stale batch releases its own URL and cannot replace the newer preview map.
  it('discards late media after another detail opens', async () => {
    const { services, details, revokeObjectURL } = setupDetails()
    const pending = Promise.withResolvers<Blob>()
    services.getLog.mockResolvedValue({ id: 1, media: [{ id: 3, snapshot_status: 'ready' }] })
    services.getMediaContent.mockReturnValue(pending.promise)
    services.getCyberWarning.mockResolvedValue({ id: 2, media: [] })
    const oldRequest = details.openInputDetail({ id: 1 } as ContentModerationLog)
    await Promise.resolve()
    await details.openCyberDetail({ id: 2 } as ContentModerationCyberWarning)
    pending.resolve(new Blob(['late']))
    await oldRequest
    expect(details.mediaObjectURLs.value).toEqual({})
    expect(revokeObjectURL).toHaveBeenCalledExactlyOnceWith('blob:preview')
    details.dispose()
  })

  // A current detail request can fail before any full audit content is returned.
  // The list row remains visible, its loading state clears, and one error reaches the page.
  it('reports a current detail failure while keeping its selected row', async () => {
    const { services, details } = setupDetails()
    const error = new Error('unavailable')
    services.getLog.mockRejectedValue(error)
    await details.openInputDetail({ id: 1 } as ContentModerationLog)
    expect(details.inputDetailRow.value?.id).toBe(1)
    expect(details.inputDetailLoading.value).toBe(false)
    expect(services.onError).toHaveBeenCalledExactlyOnceWith(error)
    details.dispose()
  })
})
