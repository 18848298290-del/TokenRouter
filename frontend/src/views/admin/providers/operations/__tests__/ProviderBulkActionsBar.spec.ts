import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ProviderBulkActionsBar from '../ProviderBulkActionsBar.vue'

// ProviderBulkActionsBar.spec.ts checks bulk action visibility and event
// delivery for provider selections. Empty selection suppresses actions, while
// selected IDs expose selection expansion and token refresh through the
// toolbar and its teleported menu.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('ProviderBulkActionsBar', () => {
  // No provider rows are selected even though matching results exist. The bulk
  // action bar renders no buttons until the page supplies a nonempty
  // selection.
  it('renders nothing before any row is selected', () => {
    const wrapper = mount(ProviderBulkActionsBar, {
      props: {
        selectedIds: [],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false,
      },
    })

    expect(wrapper.find('button').exists()).toBe(false)
  })

  // Two selected providers are part of a larger result set. Clicking the
  // select-all action emits one event so the page can expand selection across
  // matching results.
  it('allows selecting all results once some rows are selected', async () => {
    const wrapper = mount(ProviderBulkActionsBar, {
      props: {
        selectedIds: [1, 2],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false,
      },
    })

    const button = wrapper
      .findAll('button')
      .find((item) => item.text().includes('admin.providers.bulkActions.selectAllResults'))

    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('select-all-results')).toHaveLength(1)
  })

  // A selected provider opens the More menu and requests token refresh. The
  // bar emits one refresh event, closes the teleported menu, and exposes no
  // billing probe action.
  it('emits refresh-token for selected providers', async () => {
    const wrapper = mount(ProviderBulkActionsBar, {
      attachTo: document.body,
      props: {
        selectedIds: [44],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false,
      },
    })

    // Open More before querying document.body because the refresh action is
    // created in a teleported menu outside the wrapper subtree.
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'admin.providers.bulkActions.more')!
      .trigger('click')
    const item = Array.from(
      document.body.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'),
    ).find((button) => button.textContent?.trim() === 'admin.providers.bulkActions.refreshToken')
    expect(item).toBeDefined()
    item!.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('refresh-token')).toHaveLength(1)
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    expect(wrapper.text()).not.toContain('admin.providers.bulkActions.probeUpstreamBilling')
    wrapper.unmount()
  })
})
