import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PaymentMethodSelector from '@/components/payment/PaymentMethodSelector.vue'

/**
 * PaymentMethodSelector.spec.ts checks custom payment method labels, fee text, wrapping, and
 * selected styles. Supplied options exercise exact built-in alias rules and large custom
 * collections without requiring checkout configuration or an active gateway.
 */

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, fallback?: string) => fallback ?? key,
  }),
}))

describe('PaymentMethodSelector', () => {
  // A large collection of custom methods includes long display labels. Cards must retain
  // wrapping and bounded label layout so additional methods do not widen the checkout panel.
  it('wraps large custom method collections without letting labels widen the selector', () => {
    const methods = Array.from({ length: 12 }, (_, index) => ({
      type: `custom_${index}`,
      display_name: `CUSTOM_PAYMENT_METHOD_${index}`,
      fee_fixed: 0,
      fee_rate: 0,
      available: true,
    }))

    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'custom_0',
        methods,
      },
    })

    const grid = wrapper.get('[data-testid="payment-method-grid"]')
    expect(grid.classes()).toEqual(
      expect.arrayContaining(['grid', 'grid-cols-2', 'sm:grid-cols-3']),
    )
    expect(grid.classes()).not.toContain('sm:flex')

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(methods.length)
    expect(buttons.every((button) => button.classes().includes('min-w-0'))).toBe(true)
    expect(
      buttons.every((button, index) => button.attributes('title') === methods[index].display_name),
    ).toBe(true)
    expect(
      wrapper
        .findAll('[data-testid="payment-method-label"]')
        .every((label) => label.classes().includes('truncate')),
    ).toBe(true)
  })

  // Method cards include fixed and percentage fees in a wrapping grid. Their fee labels must
  // remain visible together with the responsive layout used for method selection.
  it('keeps fixed and percentage fee labels in the wrapped grid', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'custom_fee',
        methods: [
          {
            type: 'custom_fee',
            display_name: 'Custom Pay',
            fee_fixed: 1.5,
            fee_rate: 2,
            available: true,
          },
        ],
      },
    })

    expect(wrapper.text()).toContain('payment.fee ¥1.50 + 2%')
  })

  // An administrator-defined EasyPay method supplies a display name. Its selector card must show
  // that configured name instead of exposing the internal payment code.
  it('shows the configured display name for custom EasyPay methods', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'ldc',
        methods: [{ type: 'ldc', display_name: 'LDC Pay', fee_rate: 0, available: true }],
      },
    })

    expect(wrapper.text()).toContain('LDC Pay')
    expect(wrapper.text()).not.toContain('ldc')
    expect(wrapper.text()).not.toContain('payment.methods.ldc')
  })

  // A custom method code contains a built-in wallet name as a substring. Selection styling must
  // remain generic because built-in wallet aliases require an exact match.
  it('uses the generic selected style for custom methods that contain built-in names', () => {
    const wrapper = mount(PaymentMethodSelector, {
      props: {
        selected: 'card_alipay',
        methods: [{ type: 'card_alipay', display_name: 'Card Pay', fee_rate: 0, available: true }],
      },
    })

    const button = wrapper.get('button')
    expect(button.classes()).toContain('border-primary-500')
    expect(button.classes()).not.toContain('border-[#02A9F1]')
  })
})
