import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelAttributesFields from '../ModelAttributesFields.vue'
import ModelAttributeListInput from '../ModelAttributeListInput.vue'
import ModelCapabilityFields from '../ModelCapabilityFields.vue'
import Select from '@/components/common/Select.vue'

// ModelAttributesFields.spec.ts checks explicit attribute overrides, inherited labels,
// reasoning ordering, and tag input behavior through mounted field components. Emitted objects
// retain false, empty modality lists, and vendor-default null values, keeping those choices
// distinct from omitted keys.

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (
      key: string,
      params?: {
        value?: string // stored selector value
      },
    ) => (params?.value === undefined ? key : `${key}(${params.value})`),
    te: () => true,
  }),
}))

describe('模型属性编辑', () => {
  // Boolean support is changed between explicit false and inheritance while modalities are
  // cleared. Emitted attributes distinguish removed keys from configured false and an explicit
  // empty modality array.
  it('显式 false 与继承分别保存，空模态不会变成继承', async () => {
    const wrapper = mount(ModelAttributesFields, {
      props: { modelValue: { reasoning: true } },
      global: { stubs: { Select: true } },
    })
    wrapper
      .findAllComponents(Select)
      .find(
        (component) =>
          component.attributes('aria-label') === 'admin.modelAttributes.fields.reasoning',
      )!
      .vm.$emit('update:modelValue', 'false')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ reasoning: false })
    wrapper
      .findAllComponents(Select)
      .find(
        (component) =>
          component.attributes('aria-label') === 'admin.modelAttributes.fields.reasoning',
      )!
      .vm.$emit('update:modelValue', 'inherit')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({})
    await wrapper.find('fieldset input[type="checkbox"]').setValue(false)
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({
      reasoning: true,
      input_modalities: [],
    })
    wrapper.unmount()
  })

  // Reference attributes provide values for inherited controls in the editor. The inheritance
  // choices display those references so administrators can inspect the effective value before
  // adding an override.
  it('继承项显示参考属性的值', () => {
    const wrapper = mount(ModelAttributesFields, {
      props: {
        modelValue: {},
        inherited: { context: 1000, tool_call: false, output_modalities: [] },
      },
      global: { stubs: { Select: true } },
    })
    const toolCall = wrapper
      .findAllComponents(Select)
      .find(
        (component) =>
          component.attributes('aria-label') === 'admin.modelAttributes.fields.tool_call',
      )!
    expect(toolCall.props('options')[0].label).toBe(
      'admin.modelAttributes.inheritValue(admin.modelAttributes.unsupported)',
    )
    expect(wrapper.get('input[type="number"]').attributes('placeholder')).toMatch(
      /^admin\.modelAttributes\.inheritValue\(1.?000\)$/,
    )
    expect(wrapper.text()).toContain(
      'admin.modelAttributes.inheritValue(admin.modelAttributes.none)',
    )
    wrapper.unmount()
  })
})

describe('推理与媒体参数', () => {
  // Reasoning efforts are selected in an order different from their canonical list. The emitted
  // options use fixed ordering and retain null as the vendor-default choice.
  it('推理档位按固定顺序保存，null 保持为供应商默认', async () => {
    const wrapper = mount(ModelCapabilityFields, {
      props: { modelValue: { reasoning_options: [{ type: 'effort', values: [null, 'high'] }] } },
      global: { stubs: { Select: true } },
    })
    const efforts = wrapper
      .findAll('label')
      .filter(
        (label) => label.text() === 'low' || label.text() === 'admin.modelAttributes.vendorDefault',
      )
    expect(efforts.map((label) => label.get('input').element.checked)).toEqual([true, false])
    await efforts[1]!.get('input').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({
      reasoning_options: [{ type: 'effort', values: [null, 'low', 'high'] }],
    })
    wrapper.unmount()
  })

  // Tag input receives comma commits, a paste with duplicates, and Backspace on an empty draft.
  // Emitted values deduplicate additions and remove the final tag through those keyboard paths.
  it('列表输入按逗号提交，粘贴多项去重，退格删除最后一项', async () => {
    const wrapper = mount(ModelAttributeListInput, { props: { modelValue: ['1024x1024'] } })
    const input = wrapper.get('input')
    await input.setValue('512x512')
    await input.trigger('keydown', { key: ',' })
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['1024x1024', '512x512'])
    expect(input.element.value).toBe('')

    await input.trigger('paste', { clipboardData: { getData: () => '1024x1024, 2048x2048' } })
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['1024x1024', '2048x2048'])

    await input.trigger('keydown', { key: 'Backspace' })
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toBeUndefined()
    wrapper.unmount()
  })
})
