import { afterEach, describe, expect, it, vi } from 'vitest'
import { toRaw } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'

import ModelMappingEditor from '../ModelMappingEditor.vue'
import type { ModelMappingRow } from '@/utils/modelMappingRules'

// ModelMappingEditor.spec.ts tests mapping edits and row collection events through the
// shared editor. Assertions inspect actual input attributes and emitted objects,
// distinguishing replacement arrays from retained row identities and verifying indexed
// error feedback.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// wrapper owns the currently mounted test component so teardown can release its
// resources after each case.
let wrapper: VueWrapper | undefined
// Release the resources retained by this case before the next one, keeping browser
// state and component lifecycle observations isolated across assertions.
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

// mountEditor mounts mapping rows with standard labels and supplied prop overrides. It
// retains the wrapper for teardown and stubs icons and row transitions so cases can
// inspect input and event behavior.
const mountEditor = (rows: ModelMappingRow[], props: Record<string, unknown> = {}) => {
  wrapper = mount(ModelMappingEditor, {
    props: {
      modelValue: rows,
      sourceLabel: '来源模型',
      targetLabel: '目标模型',
      sourcePlaceholder: '例如 a',
      targetPlaceholder: '例如 b',
      testId: 'mapping',
      ...props,
    },
    global: {
      stubs: {
        Icon: { props: ['name'], template: '<i :data-icon="name" />' },
        'transition-group': true,
      },
    },
  })
  return wrapper
}

// lastEmittedRows reads the latest model payload from the current mapping wrapper.
// Call it after mounting and causing an update; missing emitted data produces
// undefined through the asserted return type.
const lastEmittedRows = () => {
  const emitted = wrapper!.emitted('update:modelValue') ?? []
  return emitted[emitted.length - 1]?.[0] as ModelMappingRow[]
}

describe('ModelMappingEditor', () => {
  // A mapping row receives caller labels, placeholders, and a test identifier prefix.
  // The rendered inputs expose those values, retain the source text, and use the
  // default monospace styling.
  it('透传占位、无障碍标签和测试钩子', () => {
    mountEditor([{ from: 'a', to: 'b' }])

    const source = wrapper!.get('[data-testid="mapping-source-0"]')
    const target = wrapper!.get('[data-testid="mapping-target-0"]')
    expect(source.attributes('placeholder')).toBe('例如 a')
    expect(source.attributes('aria-label')).toBe('来源模型')
    expect(target.attributes('aria-label')).toBe('目标模型')
    expect((source.element as HTMLInputElement).value).toBe('a')
    expect(source.classes()).toContain('font-mono')
  })

  // The user adds a mapping to an existing collection through the editor button. The
  // emitted collection is new, retains the existing row object, and shares its
  // appended row with add.
  it('添加时发出新数组和 add 事件', async () => {
    const rows = [{ from: 'a', to: 'b' }]
    mountEditor(rows)

    await wrapper!.get('[data-testid="mapping-add"]').trigger('click')

    const next = lastEmittedRows()
    expect(next).not.toBe(rows)
    expect(next).toHaveLength(2)
    expect(toRaw(next[0])).toBe(rows[0])
    expect(next[1]).toEqual({ from: '', to: '' })
    expect(wrapper!.emitted('add')?.[0]?.[0]).toBe(next[1])
  })

  // The user removes the first of two mapping rows through its action button. The
  // surviving row retains object identity, and remove reports the deleted row with its
  // former index.
  it('删除时保留其余行对象并发出 remove 事件', async () => {
    const rows = [
      { from: 'a', to: 'b' },
      { from: 'c', to: 'd' },
    ]
    mountEditor(rows)

    await wrapper!.get('[data-testid="mapping-remove-0"]').trigger('click')

    const next = lastEmittedRows()
    expect(next).toHaveLength(1)
    expect(toRaw(next[0])).toBe(rows[1])
    const removed = wrapper!.emitted('remove')?.[0]
    expect(toRaw(removed?.[0])).toBe(rows[0])
    expect(removed?.[1]).toBe(0)
  })

  // The user edits a target field on an existing mapping row. The row changes in place
  // and model publication retains its identity while publishing neither add nor
  // remove.
  it('输入时原地修改行并发出新数组，不触发增删事件', async () => {
    const rows = [{ from: 'a', to: 'b' }]
    mountEditor(rows)

    await wrapper!.get('[data-testid="mapping-target-0"]').setValue('gpt-5')

    expect(rows[0].to).toBe('gpt-5')
    expect(toRaw(lastEmittedRows()[0])).toBe(rows[0])
    expect(wrapper!.emitted('add')).toBeUndefined()
    expect(wrapper!.emitted('remove')).toBeUndefined()
  })

  // The caller supplies a source error for the second mapping and no error for the
  // first. The matching input receives error styling, accessible invalid state, and
  // the supplied alert text.
  it('按下标显示逐字段错误', () => {
    mountEditor(
      [
        { from: 'a', to: 'b' },
        { from: 'x*y', to: 'z' },
      ],
      { fieldErrors: [undefined, { from: '通配符无效' }] },
    )

    expect(wrapper!.get('[data-testid="mapping-source-0"]').classes()).not.toContain('input-error')
    const source = wrapper!.get('[data-testid="mapping-source-1"]')
    expect(source.classes()).toContain('input-error')
    expect(source.attributes('aria-invalid')).toBe('true')
    expect(wrapper!.get('[role="alert"]').text()).toBe('通配符无效')
  })

  // A mapping collection already contains the configured maximum number of rows. The
  // add button must be disabled so the list shell prevents another user-triggered row
  // request.
  it('达到上限时禁用添加', () => {
    mountEditor([{ from: 'a', to: 'b' }], { max: 1 })
    expect(wrapper!.get('[data-testid="mapping-add"]').attributes('disabled')).toBeDefined()
  })

  // The parent supplies footer content while the mapping collection is empty. The
  // editor must forward that slot through RuleListEditor and keep the supplied test
  // element rendered.
  it('渲染 footer 插槽', () => {
    wrapper = mount(ModelMappingEditor, {
      props: { modelValue: [], sourceLabel: 's', targetLabel: 't' },
      slots: { footer: '<div data-testid="presets" />' },
      global: { stubs: { Icon: true } },
    })
    expect(wrapper.find('[data-testid="presets"]').exists()).toBe(true)
  })
})
