import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import OpenAIFastPolicyUserSelector from '../OpenAIFastPolicyUserSelector.vue'

/**
 * OpenAIFastPolicyUserSelector.spec.ts checks persisted user labels, interactive
 * search selection, and unresolved historical IDs through the mounted selector.
 * API substitutes isolate account lookups, while fake timers make its debounce
 * observable before inspecting emitted rule identifiers.
 */

// A small translation fixture makes selected labels and button accessibility observable in these mounted tests.
const messages: Record<string, string> = {
  'admin.settings.openaiFastPolicy.userDeleted': '(deleted)',
  'admin.settings.openaiFastPolicy.userIdFallback': 'User #{id}',
  'admin.settings.openaiFastPolicy.removeUser': 'Remove user',
  'admin.settings.openaiFastPolicy.userSearchPlaceholder': 'Search users',
  'admin.settings.openaiFastPolicy.userSearchEmpty': 'No users found',
  'common.loading': 'Loading',
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const message = messages[key] ?? key
      return params
        ? Object.entries(params).reduce(
            (value, [name, replacement]) => value.replace(`{${name}}`, String(replacement)),
            message,
          )
        : message
    },
  }),
}))

// Independent search and hydration substitutes distinguish interactive queries from resolving the labels of saved rule IDs.
const mockSearchUsers = vi.fn()
const mockGetUserById = vi.fn()

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      searchUsers: (...args: unknown[]) => mockSearchUsers(...args),
    },
    users: {
      getById: (...args: unknown[]) => mockGetUserById(...args),
    },
  },
}))

describe('OpenAIFastPolicyUserSelector', () => {
  beforeEach(() => {
    // Each case controls the debounce clock and resets API results so earlier searches
    // cannot determine the request timing or account labels asserted by the next case.
    vi.useFakeTimers()
    mockSearchUsers.mockReset()
    mockGetUserById.mockReset()
  })

  afterEach(() => {
    // Restore the runner clock after each case so fake debounce timing does not leak
    // into other mounted component tests sharing this worker process.
    vi.useRealTimers()
  })

  // A saved rule references an existing user whose label requires hydration. The selector
  // displays the fetched email while leaving the persisted user identifiers unchanged.
  it('hydrates existing IDs to email labels without changing the saved IDs', async () => {
    mockGetUserById.mockResolvedValue({
      id: 7,
      email: 'existing@example.com',
      deleted_at: null,
    })

    const wrapper = mount(OpenAIFastPolicyUserSelector, {
      props: { modelValue: [7] },
      global: { stubs: { Icon: true } },
    })
    await flushPromises()

    expect(mockGetUserById).toHaveBeenCalledWith(7, true)
    expect(wrapper.text()).toContain('existing@example.com')
    expect(wrapper.text()).toContain('#7')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  // An administrator enters one character and waits for the debounce timer to fire.
  // Selecting the returned user emits that identifier in the rule selection.
  it('searches after one character and adds the selected user ID', async () => {
    mockSearchUsers.mockResolvedValue([{ id: 9, email: 'alice@example.com', deleted: false }])

    const wrapper = mount(OpenAIFastPolicyUserSelector, {
      props: { modelValue: [] },
      global: { stubs: { Icon: true } },
    })
    const input = wrapper.get('input')
    await input.trigger('focus')
    await input.setValue('a')
    await input.trigger('input')
    vi.advanceTimersByTime(300)
    await flushPromises()

    expect(mockSearchUsers).toHaveBeenCalledWith('a')
    const result = wrapper
      .findAll('button')
      .find((button) => button.text().includes('alice@example.com'))
    expect(result).toBeDefined()
    await result!.trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[[9]]])
  })

  // A saved user identifier cannot be resolved by the lookup endpoint. Its numeric
  // fallback remains visible and the removal action still emits an empty selection.
  it('keeps an unresolved saved ID visible and removable', async () => {
    mockGetUserById.mockRejectedValue(new Error('not found'))

    const wrapper = mount(OpenAIFastPolicyUserSelector, {
      props: { modelValue: [42] },
      global: { stubs: { Icon: true } },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('User #42')
    await wrapper.get('button[aria-label="Remove user"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[[]]])
  })
})
