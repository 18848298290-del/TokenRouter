import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, describe, expect, it } from 'vitest'
import BalanceAmount from '../BalanceAmount.vue'

/**
 * BalanceAmount.spec.ts checks configured balance symbols and custom SVG display
 * through the real balance component. The cases inspect formatted text and icon markup
 * with a Pinia instance, then restore the injected application configuration after
 * each case.
 */

/**
 * originalConfig captures the injected application configuration so each balance
 * display case can restore the previous settings afterward.
 */
const originalConfig = window.__APP_CONFIG__

afterEach(() => {
  window.__APP_CONFIG__ = originalConfig
})

describe('BalanceAmount', () => {
  /**
   * A configured text currency symbol is present and no custom SVG is supplied. The
   * amount retains that symbol and the requested four fraction digits.
   */
  it('没有自定义 SVG 时使用配置的金额符号', () => {
    window.__APP_CONFIG__ = {
      ...originalConfig,
      balance_unit_symbol: '🍥',
      balance_icon_svg: '',
    }

    const wrapper = mount(BalanceAmount, {
      props: { amount: 1.2345, fractionDigits: 4 },
      global: { plugins: [createPinia()] },
    })

    expect(wrapper.text()).toBe('🍥1.2345')
    expect(wrapper.find('.balance-icon-svg').exists()).toBe(false)
  })

  /**
   * A custom balance SVG is configured beside a text currency symbol. The component
   * renders the SVG and formats the numeric amount without the text symbol.
   */
  it('配置自定义 SVG 时用图标替代文本金额符号', () => {
    window.__APP_CONFIG__ = {
      ...originalConfig,
      balance_unit_symbol: '🍥',
      balance_icon_svg: '<svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="6" /></svg>',
    }

    const wrapper = mount(BalanceAmount, {
      props: { amount: 2.5, fractionDigits: 2 },
      global: { plugins: [createPinia()] },
    })

    expect(wrapper.text()).toBe('2.50')
    expect(wrapper.find('.balance-icon-svg').exists()).toBe(true)
  })
})
