import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick, ref } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'

import RuleListEditor from '../RuleListEditor.vue'
import { finishMotion, mockMotionEnvironment } from '@/__tests__/helpers/motion'

// RuleListEditor.spec.ts tests row controls, stable identities, and focus behavior in
// the shared list shell. A parent host owns mutations for interactive cases, while
// real motion hooks verify outgoing-row placement and delayed empty-state visibility.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// Row represents one editable name in the rule-list test host.
interface Row {
  name: string // Editable row name text.
}

// iconStub renders the requested icon name as a data attribute so row actions remain
// inspectable in tests.
const iconStub = { props: ['name'], template: '<i :data-icon="name" />' }

// wrapper owns the currently mounted test component so teardown can release its
// resources after each case.
let wrapper: VueWrapper | undefined
// Release the resources retained by this case before the next one, keeping browser
// state and component lifecycle observations isolated across assertions.
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
})

// mountEditor mounts the rule shell with an editable row slot and caller props. It
// retains the wrapper for teardown and stubs list transitions for immediate action and
// identity assertions.
const mountEditor = (props: Record<string, unknown>) => {
  wrapper = mount(RuleListEditor, {
    props: { testId: 'rules', ...props },
    slots: {
      row: `<template #row="{ item, index }"><input :data-testid="'field-' + index" :value="item.name" /></template>`,
    },
    global: { stubs: { Icon: iconStub, 'transition-group': true } },
  })
  return wrapper
}

// Host combines the real shared controls with parent-owned fixture state so cases can
// exercise cross-component visibility and event effects.
const Host = defineComponent({
  components: { RuleListEditor },
  props: { initial: { type: Array, default: () => [] } },
  // setup gives the test host mutable rows and user or programmatic operations. It
  // copies the initial array while retaining its row objects, allowing identity and
  // focus tests to distinguish structural edits.
  setup(props) {
    const items = ref<Row[]>([...(props.initial as Row[])])
    // add appends an empty fixture row in the owning test host.
    const add = () => items.value.push({ name: '' })
    // remove deletes the indexed fixture row from the test host collection.
    const remove = (index: number) => items.value.splice(index, 1)
    // appendSilently appends a preset fixture row through the host method.
    const appendSilently = () => items.value.push({ name: 'preset' })
    return { items, add, remove, appendSilently }
  },
  template: `
    <RuleListEditor :items="items" test-id="rules" title="规则" @add="add" @remove="remove">
      <template #row="{ item }"><input v-model="item.name" /></template>
    </RuleListEditor>
  `,
})

describe('RuleListEditor', () => {
  // The editor receives a title, hint, and custom add label with an empty collection.
  // Its header must display all supplied text and expose the requested add action
  // label.
  it('头部显示标题、提示和添加按钮', () => {
    mountEditor({ items: [], title: '模型重定向', hint: '说明', addLabel: '添加规则' })

    expect(wrapper!.text()).toContain('模型重定向')
    expect(wrapper!.text()).toContain('说明')
    expect(wrapper!.get('[data-testid="rules-add"]').text()).toContain('添加规则')
  })

  // The editor receives an empty collection with no custom add label. The add button
  // must render the generic translated label returned by the controlled i18n test
  // dependency.
  it('未传添加文案时使用通用文案', () => {
    mountEditor({ items: [] })
    expect(wrapper!.get('[data-testid="rules-add"]').text()).toContain('common.add')
  })

  // The editor places its add action in the footer for a nonempty list. The rendered
  // row must precede the add button in the resulting component markup.
  it('footer 模式把添加按钮放在列表之后', () => {
    mountEditor({ items: [{ name: 'a' }], addPlacement: 'footer' })

    const html = wrapper!.html()
    expect(html.indexOf('rules-row')).toBeLessThan(html.indexOf('rules-add'))
  })

  // The editor contains exactly the maximum permitted number of rows. Its add action
  // must be disabled so the UI cannot request another row beyond the supplied limit.
  it('达到上限时禁用添加', () => {
    mountEditor({ items: [{ name: 'a' }, { name: 'b' }], max: 2 })
    expect(wrapper!.get('[data-testid="rules-add"]').attributes('disabled')).toBeDefined()
  })

  // The editor receives addDisabled while its collection is empty. The add button must
  // be disabled independently of the global disabled prop or the current row count.
  it('addDisabled 单独禁用添加', () => {
    mountEditor({ items: [], addDisabled: true })
    expect(wrapper!.get('[data-testid="rules-add"]').attributes('disabled')).toBeDefined()
  })

  // The editor changes from empty to populated and then removes its empty-state text.
  // The dashed empty message appears only when both the collection and supplied text
  // permit it.
  it('只在无行且提供空态文案时显示空态', async () => {
    mountEditor({ items: [], emptyText: '暂无规则' })
    expect(wrapper!.text()).toContain('暂无规则')

    await wrapper!.setProps({ items: [{ name: 'a' }] })
    expect(wrapper!.text()).not.toContain('暂无规则')

    await wrapper!.setProps({ items: [], emptyText: undefined })
    expect(wrapper!.find('.border-dashed').exists()).toBe(false)
  })

  // The user removes and reorders rows through the visible action buttons. Events must
  // report zero-based indices for removal and both directions of movement to the
  // parent handler.
  it('删除和移动按下标发出事件', async () => {
    mountEditor({ items: [{ name: 'a' }, { name: 'b' }], reorderable: true })

    await wrapper!.get('[data-testid="rules-remove-1"]').trigger('click')
    await wrapper!.get('[data-testid="rules-move-down-0"]').trigger('click')
    await wrapper!.get('[data-testid="rules-move-up-1"]').trigger('click')

    expect(wrapper!.emitted('remove')).toEqual([[1]])
    expect(wrapper!.emitted('move')).toEqual([
      [0, 1],
      [1, 0],
    ])
  })

  // A two-row editor enables reordering at both ends of the collection. The first
  // upward and last downward actions are disabled while the valid downward action
  // remains available.
  it('首行不能上移，末行不能下移', () => {
    mountEditor({ items: [{ name: 'a' }, { name: 'b' }], reorderable: true })

    expect(wrapper!.get('[data-testid="rules-move-up-0"]').attributes('disabled')).toBeDefined()
    expect(wrapper!.get('[data-testid="rules-move-down-1"]').attributes('disabled')).toBeDefined()
    expect(wrapper!.get('[data-testid="rules-move-down-0"]').attributes('disabled')).toBeUndefined()
  })

  // The editor has exactly the minimum retained row count configured by its caller.
  // Its remove button must be disabled so the UI protects that lower collection limit.
  it('行数不超过下限时禁用删除', () => {
    mountEditor({ items: [{ name: 'a' }], min: 1 })
    expect(wrapper!.get('[data-testid="rules-remove-0"]').attributes('disabled')).toBeDefined()
  })

  // The editor receives removable false for a populated collection. The row must
  // render without its removal button, allowing callers to expose rows while
  // withholding that action.
  it('removable 为 false 时不渲染删除按钮', () => {
    mountEditor({ items: [{ name: 'a' }], removable: false })
    expect(wrapper!.find('[data-testid="rules-remove-0"]').exists()).toBe(false)
  })

  // A card-style editor receives a row-label callback with one item. The card must
  // show its numbered label and use the compact icon action size for the labeled
  // layout.
  it('卡片形态显示行序号并使用紧凑按钮', () => {
    mountEditor({
      items: [{ name: 'a' }],
      variant: 'card',
      itemLabel: (index: number) => `规则 #${index + 1}`,
    })

    const row = wrapper!.get('[data-testid="rules-row"]')
    expect(row.text()).toContain('规则 #1')
    expect(row.classes()).toContain('rounded-surface')
    expect(wrapper!.get('[data-testid="rules-remove-0"]').classes()).toContain('btn-icon-sm')
  })

  // A default editor displays a single row without card presentation options. The row
  // must use separator styling and the ordinary icon action size beside its editable
  // content.
  it('线形形态使用分隔线和常规按钮', () => {
    mountEditor({ items: [{ name: 'a' }] })

    expect(wrapper!.get('[data-testid="rules-row"]').classes()).toContain('border-b')
    expect(wrapper!.get('[data-testid="rules-remove-0"]').classes()).toContain('btn-icon')
  })

  // The editor receives globally disabled state with removal and reordering available.
  // Every rendered operation button must expose disabled state, including the add and
  // row action controls.
  it('禁用时所有操作按钮不可用', () => {
    mountEditor({ items: [{ name: 'a' }, { name: 'b' }], reorderable: true, disabled: true })

    const buttons = wrapper!.findAll('button')
    expect(buttons.length).toBeGreaterThan(0)
    expect(buttons.every((button) => button.attributes('disabled') !== undefined)).toBe(true)
  })

  // The caller supplies a collection-level error with an empty list. The editor must
  // render the exact supplied message through an alert region for accessible error
  // feedback.
  it('列表级错误以 alert 渲染', () => {
    mountEditor({ items: [], error: '存在未完成的规则' })
    expect(wrapper!.get('[role="alert"]').text()).toBe('存在未完成的规则')
  })

  // The parent host appends a row synchronously after the user clicks add. After
  // rendering, focus must move to the first input of the newly created final row.
  it('点击添加后聚焦新行的第一个输入框', async () => {
    wrapper = mount(Host, {
      props: { initial: [{ name: 'a' }] },
      attachTo: document.body,
      global: { stubs: { Icon: iconStub, 'transition-group': true } },
    })

    await wrapper.get('[data-testid="rules-add"]').trigger('click')
    await nextTick()

    const inputs = wrapper.findAll('input')
    expect(inputs).toHaveLength(2)
    expect(document.activeElement).toBe(inputs[1].element)
  })

  // The parent host appends a preset row through its exposed method rather than the
  // add button. The new row renders while keyboard focus remains on the document body.
  it('非点击添加产生的新行不抢焦点', async () => {
    wrapper = mount(Host, {
      attachTo: document.body,
      global: { stubs: { Icon: iconStub, 'transition-group': true } },
    })
    ;(
      wrapper.vm as unknown as {
        appendSilently: () => void // Programmatic row insertion helper.
      }
    ).appendSilently()
    await nextTick()

    expect(wrapper.findAll('input')).toHaveLength(1)
    expect(document.activeElement).toBe(document.body)
  })

  // The parent removes the first row while retaining the second object reference. The
  // surviving input element must be reused at its new index to retain row display
  // identity.
  it('删除前面的行时复用后续行的节点', async () => {
    const rows = [{ name: 'a' }, { name: 'b' }]
    mountEditor({ items: rows })
    const second = wrapper!.get('[data-testid="field-1"]').element

    await wrapper!.setProps({ items: [rows[1]] })

    expect(wrapper!.get('[data-testid="field-0"]').element).toBe(second)
  })

  // The editor disables animation before removing a row from its collection. The
  // rendered input count must reflect the deletion immediately after the prop update
  // completes.
  it('关闭动效时删除立即生效', async () => {
    wrapper = mount(RuleListEditor, {
      props: { items: [{ name: 'a' }, { name: 'b' }], animated: false, testId: 'rules' },
      slots: { row: `<template #row="{ item }"><input :value="item.name" /></template>` },
      global: { stubs: { Icon: iconStub } },
    })

    await wrapper.setProps({ items: [{ name: 'b' }] })

    expect(wrapper.findAll('input')).toHaveLength(1)
  })

  // The final row begins leaving while an empty-state message is configured. It stays
  // in document flow during exit, and the message appears after the departing row is
  // removed.
  it('删除末尾的行时留在文档流中淡出，退出完成后才显示空态', async () => {
    mockMotionEnvironment()
    wrapper = mount(RuleListEditor, {
      props: { items: [{ name: 'a' }], emptyText: '暂无规则', testId: 'rules' },
      slots: { row: `<template #row="{ item }"><input :value="item.name" /></template>` },
      global: { stubs: { Icon: iconStub } },
      attachTo: document.body,
    })

    await wrapper.setProps({ items: [] })
    const leaving = wrapper.get('[data-testid="rules-row"]')
    expect(leaving.classes()).toContain('rule-list-leave-in-flow')
    expect(wrapper.text()).not.toContain('暂无规则')

    await finishMotion(leaving.element)
    expect(wrapper.find('[data-testid="rules-row"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('暂无规则')
  })

  // The first of two rows begins leaving while the following row survives. The
  // departing row becomes inert and uses the ordinary leave layout so the surviving
  // row can fill its space.
  it('删除中间的行时后续行补位，退出行不留在文档流中', async () => {
    mockMotionEnvironment()
    const rows = [{ name: 'a' }, { name: 'b' }]
    wrapper = mount(RuleListEditor, {
      props: { items: rows, testId: 'rules' },
      slots: { row: `<template #row="{ item }"><input :value="item.name" /></template>` },
      global: { stubs: { Icon: iconStub } },
      attachTo: document.body,
    })

    await wrapper.setProps({ items: [rows[1]] })
    const leaving = wrapper.findAll('[data-testid="rules-row"]')[0]
    expect(leaving.attributes('inert')).toBeDefined()
    expect(leaving.classes()).not.toContain('rule-list-leave-in-flow')
  })
})
