import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { toRaw } from 'vue'

import HeaderOverrideEditor from '../HeaderOverrideEditor.vue'
import type { HeaderOverrideRow } from '@/components/provider/form/headerOverrides'

// HeaderOverrideEditor.spec.ts tests header-row rendering, insertion, deletion, and DOM
// identity reuse. The mounted editor uses a stubbed JSON toolbar so assertions can
// isolate the shared row list and its emitted arrays.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// mountEditor mounts the editor with supplied drafts and controlled child components.
// Callers own wrapper cleanup and inspect emitted updates to distinguish list replacement
// from mutation of retained row objects.
const mountEditor = (rows: HeaderOverrideRow[]) =>
  mount(HeaderOverrideEditor, {
    props: { rows },
    global: {
      stubs: {
        Icon: { props: ['name'], template: '<i :data-icon="name" />' },
        HeaderOverrideJsonTools: { template: '<div data-testid="json-tools" />' },
        'transition-group': true,
      },
    },
  })

// findButtonByText finds an editor button whose visible text contains the requested
// label. It throws when no matching button exists so the test fails at its intended
// interaction.
const findButtonByText = (wrapper: ReturnType<typeof mountEditor>, text: string) => {
  const button = wrapper.findAll('button').find((item) => item.text().includes(text))
  if (!button) throw new Error(`未找到按钮：${text}`)
  return button
}

describe('HeaderOverrideEditor', () => {
  // The editor receives one populated header row and its JSON toolbar stub. Rendering
  // must produce the expected inputs and toolbar without leaking template source into
  // visible text.
  it('只渲染行内容，不出现模板残片', () => {
    const wrapper = mountEditor([{ name: 'X-Trace', value: '1' }])

    expect(wrapper.text()).not.toContain('class=')
    expect(wrapper.findAll('input')).toHaveLength(2)
    expect(wrapper.find('[data-testid="json-tools"]').exists()).toBe(true)
  })

  // The add action runs with one existing header draft in the editor. Its update must
  // retain that row and append a new empty name and value pair.
  it('添加时追加一个空行', async () => {
    const rows = [{ name: 'X-Trace', value: '1' }]
    const wrapper = mountEditor(rows)

    await findButtonByText(wrapper, 'admin.providers.headerOverride.addRow').trigger('click')

    expect(wrapper.emitted('update:rows')?.[0]?.[0]).toEqual([rows[0], { name: '', value: '' }])
  })

  // The first of two rows is removed through its delete action. The emitted array must
  // retain the second object identity so existing field bindings can continue using it.
  it('删除时移除对应行并保留其余行对象', async () => {
    const rows = [
      { name: 'A', value: '1' },
      { name: 'B', value: '2' },
    ]
    const wrapper = mountEditor(rows)

    await wrapper.findAll('button[aria-label="common.delete"]')[0].trigger('click')

    const next = wrapper.emitted('update:rows')?.[0]?.[0] as HeaderOverrideRow[]
    expect(next).toHaveLength(1)
    expect(toRaw(next[0])).toBe(rows[1])
  })

  // The parent removes the first row while the second row already has a rendered input.
  // The surviving row must reuse that input node after the prop update.
  it('删除前面的行时复用后续行的输入框节点', async () => {
    const rows = [
      { name: 'A', value: '1' },
      { name: 'B', value: '2' },
    ]
    const wrapper = mountEditor(rows)
    const secondInput = wrapper.findAll('input')[2].element

    await wrapper.setProps({ rows: [rows[1]] })

    expect(wrapper.findAll('input')[0].element).toBe(secondInput)
  })
})
