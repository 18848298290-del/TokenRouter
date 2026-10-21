import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RequestIdFilterField from '../RequestIdFilterField.vue'

/**
 * RequestIdFilterField.spec.ts checks when the request filter publishes its editable
 * draft. Enter trims and emits the value, unchanged blur avoids another search, and
 * parent resets update the input while a lightweight FilterField stub supplies the
 * slot.
 */

vi.mock('vue-i18n', async (original) => ({
  ...(await original<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

/**
 * mountField creates the request filter with a lightweight wrapper stub. The default
 * active identifier is empty, and callers can provide an existing value to exercise
 * blur or parent reset behavior.
 */
function mountField(modelValue = '') {
  return mount(RequestIdFilterField, {
    props: { modelValue },
    global: { stubs: { FilterField: { template: '<div><slot /></div>' } } },
  })
}

describe('RequestIdFilterField', () => {
  /**
   * A user types an identifier with surrounding spaces before pressing Enter. The
   * field delays publication until Enter, then emits the trimmed identifier and one
   * search request.
   */
  it('回车后写回去掉空白的 ID 并触发查询', async () => {
    const wrapper = mountField()
    await wrapper.get('input').setValue('  req-1  ')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.get('input').trigger('keydown.enter')
    expect(wrapper.emitted('update:modelValue')).toEqual([['req-1']])
    expect(wrapper.emitted('search')).toHaveLength(1)
  })

  /**
   * The applied identifier is unchanged when the input loses focus. The field emits no
   * search event, preventing a repeated query for the same active filter.
   */
  it('内容没变时失焦不重复查询', async () => {
    const wrapper = mountField('req-1')
    await wrapper.get('input').trigger('blur')
    expect(wrapper.emitted('search')).toBeUndefined()
  })

  /**
   * The parent clears an existing request identifier after the field mounts. The local
   * input draft follows that reset and displays an empty editable value.
   */
  it('父级清空 ID 后输入框同步清空', async () => {
    const wrapper = mountField('req-1')
    await wrapper.setProps({ modelValue: '' })
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('')
  })
})
