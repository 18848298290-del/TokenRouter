import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import ProviderTableFilters from '../ProviderTableFilters.vue'
import type { AdminGroup } from '@/api/groups.types'

// ProviderTableFilters.spec.ts checks group-filter options for active and
// inactive provider groups. The select stub exposes the supplied option
// records so assertions verify inclusion and translated inactive labels
// independently of native browser selection behavior.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// SelectStub renders supplied option labels and exposes its model value,
// allowing inactive-group inclusion to be tested without native selection
// behavior.
const SelectStub = {
  props: [
    'modelValue', // current controlled input value
    'options', // available typed selection options
  ],
  emits: [
    'update:modelValue', // replacement controlled input value
    'change', // notify parent of input change
  ],
  template: `
    <div data-test="select-stub" :data-model-value="String(modelValue ?? '')">
      <!-- 测试只暴露项目 Select 的 options，不使用浏览器原生选择框行为。 -->
      <span v-for="option in options" :key="String(option.value)" data-test="select-option">
        {{ option.label }}
      </span>
    </div>
  `,
}

// group builds an active OpenAI group with optional policies disabled.
// Overrides are applied last so filter tests can vary group identity and
// status while retaining the remaining API shape.
function group(overrides: Partial<AdminGroup>): AdminGroup {
  return {
    id: 1,
    name: 'group',
    description: null,
    platform: 'openai',
    rate_multiplier: 1,
    is_exclusive: false,
    session_isolation_enabled: false,
    status: 'active',
    allow_image_generation: false,
    claude_code_only: false,
    fallback_group_id: null,
    fallback_group_id_on_invalid_request: null,
    unavailable_fallback_group_id: null,
    require_oauth_only: false,
    require_privacy_set: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    model_routing: null,
    model_routing_enabled: false,
    mcp_xml_inject: false,
    sort_order: 0,
    ...overrides,
  }
}

describe('ProviderTableFilters', () => {
  // The filter receives one active group and one inactive group. Opening its
  // controls retains both IDs and adds the translated inactive suffix only to
  // the disabled group.
  it('keeps inactive groups visible in the group filter', async () => {
    const wrapper = mount(ProviderTableFilters, {
      props: {
        searchQuery: '',
        filters: {
          platform: '',
          type: '',
          status: '',
          privacy_mode: '',
          group: '',
        },
        groups: [
          group({ id: 10, name: 'Active Pool', status: 'active' }),
          group({ id: 11, name: 'Disabled Pool', status: 'inactive' }),
        ],
      },
      global: {
        stubs: {
          Select: SelectStub,
          SearchInput: true,
        },
      },
    })

    await wrapper.get('button[aria-label="common.filter"]').trigger('click')

    const selectComponents = wrapper.findAllComponents(SelectStub)
    const groupOptions = selectComponents.at(4)?.props('options') as Array<{
      value: string // selected option identifier
      label: string // translated display label
    }>

    expect(groupOptions).toEqual(
      expect.arrayContaining([
        { value: '10', label: 'Active Pool' },
        { value: '11', label: 'Disabled Pool (common.inactive)' },
      ]),
    )
  })
})
