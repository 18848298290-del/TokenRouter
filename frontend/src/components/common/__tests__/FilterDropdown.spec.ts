import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'

import FilterDropdown from '../FilterDropdown.vue'
import FilterField from '../FilterField.vue'
import Select from '../Select.vue'
import { BREAKPOINT_LG } from '@/constants/layout'

// FilterDropdown.spec.ts tests viewport placement and active chip registration in
// filter panels. Measured trigger positions exercise horizontal clamping, and a parent
// fixture combines text controls with Select instances to verify field order and
// distinct clearing values.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// stubs replaces icons and transition presentation so filter tests can inspect panel
// markup and placement immediately after interaction.
const stubs = { Icon: true, MotionTransition: { template: '<div><slot /></div>' } }

// openAt mounts a filter at a supplied horizontal position and opens it. It returns
// the rendered panel for placement assertions, with caller-selected columns and
// stubbed motion rendering.
const openAt = async (left: number, columns: 1 | 2 | 3) => {
  const wrapper = mount(FilterDropdown, {
    props: { activeCount: 0, columns },
    global: { stubs },
  })
  vi.spyOn(wrapper.element, 'getBoundingClientRect').mockReturnValue({
    left,
    right: left + 36,
    top: 0,
    bottom: 36,
    width: 36,
  } as DOMRect)
  await wrapper.get('button').trigger('click')
  return wrapper.get('.filter-panel')
}

describe('FilterDropdown', () => {
  // Release the resources retained by this case before the next one, keeping browser
  // state and component lifecycle observations isolated across assertions.
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // A three-column panel opens with enough space to the right of its trigger. Its
  // relative left offset remains zero and its rendered width matches the configured
  // rem recipe.
  it('右侧放得下时面板左缘对齐触发按钮', async () => {
    window.innerWidth = BREAKPOINT_LG
    const panel = await openAt(100, 3)
    expect(panel.attributes('style')).toContain('left: 0px')
    expect(panel.attributes('style')).toContain('width: 768px')
  })

  // A two-column panel opens near the viewport right edge with insufficient remaining
  // space. Its relative left offset shifts the panel inward while retaining the helper
  // viewport padding.
  it('右侧放不下时面板向左平移并留出视口边距', async () => {
    window.innerWidth = BREAKPOINT_LG
    const panel = await openAt(900, 2)

    expect(panel.attributes('style')).toContain('left: -436px')
  })

  // A three-column filter opens at desktop width with transition rendering stubbed.
  // Its field body must include the large-screen three-column class selected by the
  // columns presentation option.
  it('按 columns 切换栅格列数', async () => {
    window.innerWidth = BREAKPOINT_LG
    const panel = await openAt(100, 3)
    expect(panel.get('.filter-panel-body').classes()).toEqual(
      expect.arrayContaining(['lg:grid-cols-3']),
    )
  })

  // The filter starts with no active conditions, then receives a positive count. Reset
  // is initially disabled, the trigger displays the count, and clicking reset
  // publishes one request.
  it('没有生效条件时禁用重置，有条件时触发 reset', async () => {
    const wrapper = mount(FilterDropdown, { props: { activeCount: 0 }, global: { stubs } })
    await wrapper.get('button').trigger('click')
    expect(wrapper.get('[data-testid="filter-reset"]').attributes('disabled')).toBeDefined()

    await wrapper.setProps({ activeCount: 2 })
    expect(wrapper.get('button').text()).toBe('2')
    await wrapper.get('[data-testid="filter-reset"]').trigger('click')
    expect(wrapper.emitted('reset')).toHaveLength(1)
  })

  // Text and Select fields register active chips in different forms, including an
  // explicit reset value. Chip display follows field order and each removal restores
  // its own field default.
  it('面板顶部按字段顺序列出已选条件，移除标签会恢复该字段的默认值', async () => {
    const Host = defineComponent({
      components: { FilterDropdown, FilterField, AppSelect: Select },
      // setup provides independent brand, status, keyword, and range fixtures for chip
      // assertions. The option collections include inferred and explicit empty values
      // so each clearing path has a visible result.
      setup() {
        const brand = ref('openai')
        const status = ref('')
        const keyword = ref('gpt')
        const range = ref('6h')
        const options = [
          { value: '', label: '全部' },
          { value: 'openai', label: 'OpenAI' },
        ]
        const rangeOptions = [
          { value: '1h', label: '1 小时' },
          { value: '6h', label: '6 小时' },
        ]
        return { brand, status, keyword, range, options, rangeOptions }
      },
      template: `
        <FilterDropdown :active-count="3">
          <FilterField label="关键词" :value-text="keyword" @clear="keyword = ''">
            <input v-model="keyword" />
          </FilterField>
          <FilterField label="品牌"><AppSelect v-model="brand" :options="options" /></FilterField>
          <FilterField label="状态"><AppSelect v-model="status" :options="options" /></FilterField>
          <FilterField label="时间" empty-value="1h"><AppSelect v-model="range" :options="rangeOptions" /></FilterField>
        </FilterDropdown>
      `,
    })
    const wrapper = mount(Host, { global: { stubs } })
    await wrapper.get('button[aria-haspopup="dialog"]').trigger('click')

    // chipTexts returns the rendered filter chip labels in their current order.
    const chipTexts = () => wrapper.findAll('.filter-chip').map((chip) => chip.text())
    expect(chipTexts()).toEqual(['关键词gpt', '品牌OpenAI', '时间6 小时'])

    await wrapper.findAll('.filter-chip-remove')[1]!.trigger('click')
    expect(
      (
        wrapper.vm as unknown as {
          brand: string // Selected brand fixture value.
        }
      ).brand,
    ).toBe('')
    await wrapper.findAll('.filter-chip-remove')[1]!.trigger('click')
    expect(
      (
        wrapper.vm as unknown as {
          range: string // Selected range fixture value.
        }
      ).range,
    ).toBe('1h')
    await wrapper.findAll('.filter-chip-remove')[0]!.trigger('click')
    expect(wrapper.find('[data-testid="filter-chips"]').exists()).toBe(false)
  })
})
