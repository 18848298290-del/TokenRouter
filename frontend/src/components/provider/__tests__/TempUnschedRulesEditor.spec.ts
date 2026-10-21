import { afterEach, describe, expect, it, vi } from 'vitest'
import { toRaw } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'

import TempUnschedRulesEditor, { type TempUnschedRuleForm } from '../TempUnschedRulesEditor.vue'

// TempUnschedRulesEditor.spec.ts tests editable cooldown rules through preset, add,
// move, and remove actions. Assertions inspect emitted arrays and retained rule values
// so the parent-owned draft behavior remains visible independently of the list styling.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// wrapper retains the most recently mounted editor for afterEach cleanup so tests can
// inspect its events and release its component resources.
let wrapper: VueWrapper | undefined
// Release the retained editor wrapper after each case so mounted component resources do
// not survive into the next interaction test.
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

// rule creates a temporary unschedulability draft with caller-selected criteria for the
// test. The returned object can be compared by identity after add, move, and remove
// interactions.
const rule = (code: number): TempUnschedRuleForm => ({
  error_code: code,
  keywords: '',
  duration_minutes: 5,
  description: '',
})

// mountEditor mounts the supplied rule array and retains its wrapper for afterEach
// cleanup. Tests inspect emitted replacements and row identities through the shared
// interaction helpers below.
const mountEditor = (rules: TempUnschedRuleForm[]) => {
  wrapper = mount(TempUnschedRulesEditor, {
    props: { modelValue: rules },
    global: { stubs: { Icon: true, 'transition-group': true } },
  })
  return wrapper
}

// lastEmitted returns the latest rule-array update emitted by the mounted editor. Callers
// invoke an action first so the emitted value represents that interaction rather than an
// absent event.
const lastEmitted = () =>
  wrapper!.emitted('update:modelValue')?.at(-1)?.[0] as TempUnschedRuleForm[]

// findButton finds the rule-editor button matching the supplied visible label. Callers
// provide text produced by the translation stub so actions can be selected independently
// from their visual styling.
const findButton = (text: string) =>
  wrapper!.findAll('button').find((button) => button.text().includes(text))!

describe('TempUnschedRulesEditor', () => {
  // The rule editor starts with no rows before its add action is activated. The emitted
  // draft must have empty matching fields and the default thirty-minute cooldown.
  it('添加空规则时使用 30 分钟默认时长', async () => {
    mountEditor([])

    await findButton('admin.providers.tempUnschedulable.addRule').trigger('click')

    expect(lastEmitted()).toEqual([
      { error_code: null, keywords: '', duration_minutes: 30, description: '' },
    ])
  })

  // The administrator adds one of the predefined temporary scheduling rules. Its emitted
  // row must carry the expected rate-limit status code and ten-minute cooldown for the
  // selected preset.
  it('预设追加规则副本', async () => {
    mountEditor([])

    await findButton('admin.providers.tempUnschedulable.presets.rateLimitLabel').trigger('click')

    expect(lastEmitted()).toEqual([
      expect.objectContaining({ error_code: 429, duration_minutes: 10 }),
    ])
  })

  // Three rules exist before an adjacent move action is activated in the editor. The
  // resulting array must swap the selected neighbors and retain the other row position.
  it('上下移动交换相邻规则', async () => {
    const rules = [rule(429), rule(503), rule(529)]
    mountEditor(rules)

    await wrapper!.findAll('button[aria-label="common.moveDown"]')[0].trigger('click')

    expect(lastEmitted().map((item) => toRaw(item))).toEqual([rules[1], rules[0], rules[2]])
  })

  // The editor displays numbered rules and the administrator deletes one row. The emitted
  // array must retain the other row, and the initial rendering must include the
  // translated rule numbering label.
  it('删除指定规则并显示规则序号', async () => {
    const rules = [rule(429), rule(503)]
    mountEditor(rules)
    expect(wrapper!.text()).toContain('admin.providers.tempUnschedulable.ruleIndex')

    await wrapper!.findAll('button[aria-label="common.delete"]')[1].trigger('click')

    expect(lastEmitted().map((item) => toRaw(item))).toEqual([rules[0]])
  })
})
