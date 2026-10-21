import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ScopeDropdown from '../ScopeDropdown.vue'

// ScopeDropdown.spec.ts mounts the ownership dropdown with a controlled route and router
// replacement. It verifies that selecting team ownership updates the parent model, retains
// unrelated query parameters, and emits the completed scope change.

// The router mock captures query replacement while the route fixture retains an unrelated
// parameter that scope selection must preserve.
const mocks = vi.hoisted(() => ({ replace: vi.fn() }))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { retained: 'yes', scope: 'personal' } }),
  useRouter: () => ({ replace: mocks.replace }),
}))

describe('ScopeDropdown', () => {
  // A personal key list switches to team ownership while another query value is present. The
  // test protects both emitted scope events and retention of the unrelated URL parameter.
  it('以下拉菜单切换作用域并保留其他 URL 参数', async () => {
    mocks.replace.mockResolvedValue(undefined)
    const wrapper = mount(ScopeDropdown, { props: { modelValue: 'personal' } })

    expect(wrapper.get('[data-test="scope-dropdown-trigger"]').text()).toContain(
      'team.personalKeys',
    )
    await wrapper.get('[data-test="scope-dropdown-trigger"]').trigger('click')
    await wrapper.get('[data-test="scope-option-team"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([['team']])
    expect(mocks.replace).toHaveBeenCalledWith({ query: { retained: 'yes', scope: 'team' } })
    expect(wrapper.emitted('change')).toEqual([['team']])
  })
})
