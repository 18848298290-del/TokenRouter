import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import BulkEditUserModal from '../BulkEditUserModal.vue'

// BulkEditUserModal.spec.ts checks selective batch-limit submission through the mounted
// administrator dialog. API and notification spies isolate request construction, while
// confirmation spies decide whether an overwrite proceeds. Tests cover field enabling,
// selection limits, explicit zero, and confirmation cancellation.

// The hoisted mutation and notification spies capture submitted limits and feedback
// while keeping batch updates local to the component tests.
const { batchUpdateLimits, showSuccess, showError } = vi.hoisted(() => ({
  batchUpdateLimits: vi.fn(), // Batch-limit mutation spy.
  showSuccess: vi.fn(), // Success notification spy.
  showError: vi.fn(), // Error notification spy.
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      batchUpdateLimits, // Batch-limit mutation spy.
    }, // User endpoint substitutes.
  }, // Administrator API substitute.
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess, // Success notification spy.
    showError, // Error notification spy.
  }), // Application notification-store substitute.
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${JSON.stringify(params)}` : key, // Translation key passthrough.
  }), // Translation composable substitute.
}))

// mountModal opens the batch editor for two selected users with dialog slots stubbed.
// It returns the mounted wrapper for input and event assertions; the caller owns its
// eventual unmounting after component interactions finish.
const mountModal = () =>
  mount(BulkEditUserModal, {
    props: {
      show: true, // Parent-controlled dialog visibility.
      selectedIds: [4, 7], // Selected user identifiers.
    }, // Accepted component props.
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'], // Accepted component props.
          emits: ['close'], // Declared component events.
          template: '<div v-if="show"><slot /><slot name="footer" /></div>', // Rendered stub markup.
        }, // Dialog rendering substitute.
      }, // Replaced child components.
    }, // Mount-wide component overrides.
  })

describe('BulkEditUserModal', () => {
  beforeEach(() => {
    batchUpdateLimits.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    batchUpdateLimits.mockResolvedValue({
      affected: 2, // Updated user count.
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // An enabled concurrency field starts blank after the modal opens. The submit button
  // must remain disabled until that field contains a value accepted by the parser.
  it('disables submission until at least one enabled field has a value', async () => {
    const wrapper = mountModal()

    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="concurrency-input"]').setValue('5')
    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeUndefined()
  })

  // The selection contains 501 users even though the concurrency draft is valid. The
  // dialog must show the selection-limit message and keep submission disabled.
  it('disables submission when more than 500 users are selected', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({
      selectedIds: Array.from(
        {
          length: 501, // Requested fixture entry count.
        },
        (_, index) => index + 1,
      ), // Selected user identifiers.
    })
    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('5')

    expect(wrapper.text()).toContain('admin.users.bulkLimits.selectionLimit')
    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeDefined()
  })

  // RPM is enabled with explicit zero while concurrency remains disabled. Confirmation
  // must describe unlimited RPM, and the request must contain only RPM plus the
  // selected targets.
  it('submits only the enabled RPM field and preserves zero as unlimited', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-rpm-limit"]').trigger('click')
    await wrapper.get('[data-test="rpm-limit-input"]').setValue('0')
    expect(wrapper.text()).toContain('admin.users.bulkLimits.unlimited')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).toHaveBeenCalledWith({
      user_ids: [4, 7], // Explicit update targets.
      all: false, // All-user update flag.
      rpm_limit: 0, // Requests per minute.
    })
    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining('admin.users.bulkLimits.rpmUnlimitedValue'),
    )
    expect(wrapper.emitted('success')).toEqual([[2]])
  })

  // Concurrency is enabled and given a value while RPM remains disabled. After
  // confirmation, the request must include concurrency and omit the untouched RPM
  // field.
  it('omits disabled fields from the request', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('9')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).toHaveBeenCalledWith({
      user_ids: [4, 7], // Explicit update targets.
      all: false, // All-user update flag.
      concurrency: 9, // Concurrent request limit.
    })
  })

  // A valid concurrency draft reaches the overwrite confirmation but the administrator
  // cancels it. The component must leave the batch mutation spy uncalled after
  // submission settles.
  it('does not call the API when overwrite confirmation is cancelled', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('9')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).not.toHaveBeenCalled()
  })
})
