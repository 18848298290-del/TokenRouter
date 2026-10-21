import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import ModelAttributesSummary from '../ModelAttributesSummary.vue'

/**
 * ModelAttributesSummary.spec.ts checks modality graphics, capability evidence, and
 * linked control details in the summary. Fixtures distinguish absent, false,
 * conditional, and empty values, and incompatible variants verify that their separate
 * budgets and thinking restrictions remain visible.
 */

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('ModelAttributesSummary', () => {
  /**
   * The input list mixes text, PDF, and an unrecognized modality. Recognized
   * modalities render their distinct glyphs in order and the unknown vocabulary entry
   * adds no icon.
   */
  it('模态用图标展示，PDF 与文字使用不同图形，未识别的模态不渲染', () => {
    const wrapper = mount(ModelAttributesSummary, {
      props: {
        attributes: {
          input_modalities: ['text', 'pdf', 'unknown-kind'],
          output_modalities: ['text'],
        },
      },
    })

    const icons = wrapper
      .get('[data-testid="model-attributes-modalities"]')
      .findAll('[data-animated-icon]')
    expect(icons.map((icon) => icon.attributes('data-animated-icon'))).toEqual([
      'type',
      'file-text',
      'move-right',
      'type',
    ])
  })

  /**
   * Capabilities include true, false, and absent values in the same record. The
   * summary keeps their supported, unsupported, and unknown states visible through
   * distinct icons.
   */
  it('能力项用对勾、叉号和横线区分支持、不支持与未知', () => {
    const wrapper = mount(ModelAttributesSummary, {
      props: {
        attributes: { reasoning: true, temperature: false },
      },
    })

    /**
     * state reads a capability chip state and its final status glyph.
     * Callers supply a capability rendered by the mounted fixture; wrapper.get
     * throws when the expected chip is missing.
     */
    const state = (field: string) => {
      const item = wrapper.get(`[data-capability="${field}"]`)
      return [
        item.attributes('data-state'),
        item.findAll('[data-animated-icon]').at(-1)?.attributes('data-animated-icon'),
      ]
    }
    expect(state('reasoning')).toEqual(['supported', 'check'])
    expect(state('temperature')).toEqual(['unsupported', 'x'])
    expect(state('tool_call')).toEqual(['unknown', 'minus'])
  })
  /**
   * A model has a conditional temperature capability and an empty reasoning controls
   * list. The summary displays the restriction and nonadjustability while reasoning
   * support remains positive.
   */
  it('显示条件、推理开关与空的可调选项', () => {
    const wrapper = mount(ModelAttributesSummary, {
      props: {
        attributes: {
          temperature: true,
          conditions: { temperature: { efforts: ['none'] } },
          reasoning: true,
          reasoning_options: [],
        },
      },
    })
    expect(wrapper.get('[data-capability="temperature"]').attributes('data-state')).toBe(
      'conditional',
    )
    expect(wrapper.get('[data-capability="reasoning"]').attributes('data-state')).toBe('supported')
    expect(wrapper.text()).toContain('none')
    expect(wrapper.text()).toContain('admin.modelAttributes.notAdjustable')
  })
})

/**
 * Two variants have opposite thinking requirements and disjoint reasoning budget
 * intervals. The summary renders separate combinations with their original endpoints
 * and keeps temperature marked conditional.
 */
it('keeps incompatible condition and budget combinations separate', () => {
  const wrapper = mount(ModelAttributesSummary, {
    props: {
      attributes: {
        temperature: true,
        variants: [
          {
            temperature: true,
            conditions: { temperature: { thinking: true } },
            reasoning_options: [{ type: 'budget_tokens', min: 1, max: 10 }],
          },
          {
            temperature: true,
            conditions: { temperature: { thinking: false } },
            reasoning_options: [{ type: 'budget_tokens', min: 100, max: 200 }],
          },
        ],
      },
    },
  })
  const variants = wrapper.get('[data-testid="model-attributes-variants"]')
  expect(variants.findAll('section')).toHaveLength(2)
  expect(variants.text()).toContain('1 ~ 10')
  expect(variants.text()).toContain('100 ~ 200')
  expect(variants.text()).toContain('admin.modelAttributes.thinkingOn')
  expect(variants.text()).toContain('admin.modelAttributes.thinkingOff')
  expect(wrapper.get('[data-capability="temperature"]').attributes('data-state')).toBe(
    'conditional',
  )
})
