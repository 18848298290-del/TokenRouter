import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick, ref } from 'vue'
import BaseDialog from '../BaseDialog.vue'
import Select from '../Select.vue'
import DateTimePicker from '../DateTimePicker.vue'
import DateRangePicker from '../DateRangePicker.vue'
import FilterDropdown from '../FilterDropdown.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: ref('en') }),
}))

// 使用真实弹窗和 Teleport，覆盖页签隐藏与点击停止冒泡的组合。
const Host = defineComponent({
  components: { BaseDialog, AppSelect: Select, DateTimePicker, DateRangePicker, FilterDropdown },
  setup() {
    return { shown: ref(true), options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] }
  },
  template: `<BaseDialog :show="true" title="测试">
    <button id="switch" @click="shown = !shown">切换</button>
    <button id="outside">空白区域</button>
    <section v-show="shown">
      <AppSelect :model-value="'a'" :options="options" />
      <DateTimePicker model-value="" />
      <DateRangePicker start-date="2026-10-01" end-date="2026-10-02" />
      <FilterDropdown :active-count="0" keep-mounted>
        <AppSelect :model-value="'a'" :options="options" />
      </FilterDropdown>
    </section>
  </BaseDialog>`,
})

// 退出动画期间节点仍在 DOM，通过 inert 区分已关闭和可交互的面板。
const activeSelect = () => document.querySelector('[role="listbox"]:not([inert])')
const settle = async () => {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

async function withHost(run: (wrapper: ReturnType<typeof mount>) => Promise<void>) {
  const wrapper = mount(Host, { attachTo: document.body })
  try {
    await run(wrapper)
  } finally {
    wrapper.unmount()
  }
}

describe('浮层关闭', () => {
  it('Select 打开后点击弹窗页签会关闭，返回页签后保持关闭', async () => {
    await withHost(async wrapper => {
      const select = wrapper.findAllComponents(Select)[0]
      await select.get('button').trigger('click')
      expect(activeSelect()).not.toBeNull()
      document.querySelector<HTMLButtonElement>('#switch')!.click()
      await settle()
      expect(activeSelect()).toBeNull()
      document.querySelector<HTMLButtonElement>('#switch')!.click()
      await settle()
      expect(select.get('button').attributes('aria-expanded')).toBe('false')
    })
  })

  it.each(['style', 'class', 'hidden', 'inert'])('祖先通过 %s 隐藏或停用时关闭 Select', async mode => {
    await withHost(async wrapper => {
      await wrapper.findAllComponents(Select)[0].get('button').trigger('click')
      const section = document.querySelector('section')!
      const style = document.createElement('style')
      style.textContent = '.test-hidden-panel { display: none; }'
      document.head.append(style)
      try {
        if (mode === 'style') section.style.display = 'none'
        else if (mode === 'class') section.classList.add('test-hidden-panel')
        else section.setAttribute(mode, '')
        await settle()
        expect(activeSelect()).toBeNull()
      } finally {
        style.remove()
      }
    })
  })

  it.each([
    ['日期时间', DateTimePicker],
    ['日期范围', DateRangePicker],
  ] as const)('%s 面板内部操作保持打开，点击弹窗空白处关闭', async (_name, component) => {
    await withHost(async wrapper => {
      const picker = wrapper.findComponent(component)
      await picker.get('.input-trigger').trigger('click')
      expect(picker.get('.input-trigger').attributes('aria-expanded')).toBe('true')
      await picker.get('[aria-label="dates.nextMonth"]').trigger('click')
      expect(picker.get('.input-trigger').attributes('aria-expanded')).toBe('true')
      document.querySelector<HTMLButtonElement>('#outside')!.click()
      await settle()
      expect(picker.get('.input-trigger').attributes('aria-expanded')).toBe('false')
    })
  })

  it('筛选框内的 Teleport 选项可选择，点击弹窗空白处关闭筛选面板', async () => {
    await withHost(async wrapper => {
      const filter = wrapper.findComponent(FilterDropdown)
      await filter.get('button').trigger('click')
      const select = filter.findComponent(Select)
      await select.get('button').trigger('click')
      document.querySelectorAll<HTMLElement>('[role="option"]')[1].click()
      await settle()
      expect(select.emitted('update:modelValue')?.[0]).toEqual(['b'])
      expect(filter.get('button').attributes('aria-expanded')).toBe('true')
      document.querySelector<HTMLButtonElement>('#outside')!.click()
      await settle()
      expect(filter.get('button').attributes('aria-expanded')).toBe('false')
    })
  })
})
