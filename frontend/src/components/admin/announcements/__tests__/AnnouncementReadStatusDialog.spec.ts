import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AnnouncementReadStatusDialog from '../AnnouncementReadStatusDialog.vue'

// AnnouncementReadStatusDialog.spec.ts checks request cancellation and queued search
// cleanup when the read-status dialog closes. The API promise remains pending so the
// test can inspect its abort signal, while fake timers expose any delayed reload left
// after dismissal.

// The hoisted read-status and notification spies let the dialog exercise its actual
// cancellation logic against a controlled pending request.
const { getReadStatus, showError } = vi.hoisted(() => ({
  getReadStatus: vi.fn(), // Read-status request spy.
  showError: vi.fn(), // Error notification spy.
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    announcements: {
      getReadStatus, // Read-status request spy.
    }, // Announcement endpoint substitutes.
  }, // Administrator API substitute.
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError, // Error notification spy.
  }), // Application notification-store substitute.
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key, // Translation key passthrough.
    }), // Translation composable substitute.
  }
})

vi.mock('@/composables/usePersistedPageSize', () => ({
  getPersistedPageSize: () => 20, // Persisted page-size substitute.
}))

// BaseDialogStub exposes both dialog slots while keeping modal rendering independent of
// the request and debounce behavior inspected by this test.
const BaseDialogStub = {
  props: ['show', 'title', 'width'], // Accepted component props.
  emits: ['close'], // Declared component events.
  template: '<div><slot /><slot name="footer" /></div>', // Rendered stub markup.
}

describe('AnnouncementReadStatusDialog', () => {
  beforeEach(() => {
    getReadStatus.mockReset()
    showError.mockReset()
    // Control the queued search delay so closing can be checked before advancing
    // time beyond the point where another request would otherwise begin.
    vi.useFakeTimers()
  })

  // An open dialog has an unresolved read-status request and a queued search. Closing
  // must abort its signal, emit close, and prevent another load after timers advance.
  it('closes by aborting active requests and clearing debounced reloads', async () => {
    let activeSignal: AbortSignal | undefined
    getReadStatus.mockImplementation(async (...args: any[]) => {
      activeSignal = args[4]?.signal
      return new Promise(() => {})
    })

    const wrapper = mount(AnnouncementReadStatusDialog, {
      props: {
        show: false, // Parent-controlled dialog visibility.
        announcementId: 1, // Selected announcement identifier.
      }, // Accepted component props.
      global: {
        stubs: {
          BaseDialog: BaseDialogStub, // Dialog rendering substitute.
          DataTable: true, // Table rendering substitute.
          Pagination: true, // Pagination rendering substitute.
          Icon: true, // Icon rendering substitute.
        }, // Replaced child components.
      }, // Mount-wide component overrides.
    })

    await wrapper.setProps({
      show: true, // Parent-controlled dialog visibility.
    })
    await flushPromises()

    expect(getReadStatus).toHaveBeenCalledTimes(1)
    expect(activeSignal?.aborted).toBe(false)

    const setupState = (wrapper.vm as any).$?.setupState
    setupState.search = 'alice'
    setupState.handleSearch()

    setupState.handleClose()
    await flushPromises()

    expect(activeSignal?.aborted).toBe(true)
    expect(wrapper.emitted('close')).toHaveLength(1)

    vi.advanceTimersByTime(350)
    await flushPromises()

    expect(getReadStatus).toHaveBeenCalledTimes(1)
  })
})
