import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import ModelAttributesSummary from '../ModelAttributesSummary.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

describe('ModelAttributesSummary', () => {
  it('模态用图标展示，PDF 与文字使用不同图形，未识别的模态不渲染', () => {
    const wrapper = mount(ModelAttributesSummary, {
      props: {
        attributes: { input_modalities: ['text', 'pdf', 'unknown-kind'], output_modalities: ['text'] }
      }
    })

    const icons = wrapper.get('[data-testid="model-attributes-modalities"]').findAll('[data-animated-icon]')
    expect(icons.map((icon) => icon.attributes('data-animated-icon'))).toEqual(['type', 'file-text', 'move-right', 'type'])
  })

  it('能力项用对勾、叉号和横线区分支持、不支持与未知', () => {
    const wrapper = mount(ModelAttributesSummary, {
      props: {
        attributes: { reasoning: true, temperature: false }
      }
    })

    const state = (field: string) => {
      const item = wrapper.get(`[data-capability="${field}"]`)
      return [item.attributes('data-state'), item.findAll('[data-animated-icon]').at(-1)?.attributes('data-animated-icon')]
    }
    expect(state('reasoning')).toEqual(['supported', 'check'])
    expect(state('temperature')).toEqual(['unsupported', 'x'])
    expect(state('tool_call')).toEqual(['unknown', 'minus'])
  })
  it('显示条件、推理开关与空的可调选项', () => {
    const wrapper = mount(ModelAttributesSummary, {
      props: { attributes: { temperature: true, conditions: { temperature: { efforts: ['none'] } }, reasoning: true, reasoning_options: [] } }
    })
    expect(wrapper.get('[data-capability="temperature"]').attributes('data-state')).toBe('conditional')
    expect(wrapper.get('[data-capability="reasoning"]').attributes('data-state')).toBe('supported')
    expect(wrapper.text()).toContain('none')
    expect(wrapper.text()).toContain('admin.modelAttributes.notAdjustable')
  })

})

// Opposite thinking requirements and disjoint budgets describe separate usable
// combinations. Rendering must show both original intervals and restrictions
// rather than an invented continuous range or an unrestricted temperature badge.
it('keeps incompatible condition and budget combinations separate', () => {
  const wrapper = mount(ModelAttributesSummary, { props: { attributes: {
    temperature: true,
    variants: [
      { temperature: true, conditions: { temperature: { thinking: true } }, reasoning_options: [{ type: 'budget_tokens', min: 1, max: 10 }] },
      { temperature: true, conditions: { temperature: { thinking: false } }, reasoning_options: [{ type: 'budget_tokens', min: 100, max: 200 }] },
    ],
  } } })
  const variants = wrapper.get('[data-testid="model-attributes-variants"]')
  expect(variants.findAll('section')).toHaveLength(2)
  expect(variants.text()).toContain('1 ~ 10')
  expect(variants.text()).toContain('100 ~ 200')
  expect(variants.text()).toContain('admin.modelAttributes.thinkingOn')
  expect(variants.text()).toContain('admin.modelAttributes.thinkingOff')
  expect(wrapper.get('[data-capability="temperature"]').attributes('data-state')).toBe('conditional')
})
