import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ProviderCard from '@/components/admin/payment/ProviderCard.vue'
import type { ProviderInstance } from '@/api/payment.types'

// ProviderCard.spec.ts checks which payment method labels a provider card exposes. A
// Stripe fixture carries legacy subtype selections to verify their controls stay hidden,
// while an EasyPay fixture confirms configurable payment choices remain visible.
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

// providerFactory creates a provider fixture with caller-supplied field overrides. Each
// call creates fresh default containers, and supplied override containers are used
// directly by the mounted test component.
function providerFactory(overrides: Partial<ProviderInstance> = {}): ProviderInstance {
  return {
    id: 1,
    provider_key: 'stripe',
    name: 'Stripe',
    config: {},
    supported_types: ['card', 'alipay', 'wxpay', 'link'],
    enabled: true,
    payment_mode: '',
    refund_enabled: false,
    allow_user_refund: false,
    limits: '',
    sort_order: 0,
    ...overrides,
  }
}

// mountCard mounts a provider card with method options and translated test labels. The
// supplied provider is passed directly as a prop so tests can inspect its visible
// controls.
function mountCard(provider: ProviderInstance) {
  return mount(ProviderCard, {
    props: {
      provider,
      enabled: true,
      availableTypes: [
        { value: 'card', label: '银行卡' },
        { value: 'alipay', label: '支付宝' },
        { value: 'wxpay', label: '微信支付' },
        { value: 'link', label: 'Link' },
      ],
    },
    global: {
      stubs: {
        Icon: true,
        ToggleSwitch: true,
      },
    },
  })
}

describe('ProviderCard', () => {
  // A Stripe provider retains legacy payment subtypes in its stored data. The card must
  // omit subtype toggles because the Stripe dashboard determines available Checkout
  // methods.
  it('hides legacy Stripe subtype toggles because Checkout uses dashboard payment methods', () => {
    const wrapper = mountCard(providerFactory())

    expect(wrapper.text()).not.toContain('银行卡')
    expect(wrapper.text()).not.toContain('支付宝')
    expect(wrapper.text()).not.toContain('微信支付')
    expect(wrapper.text()).not.toContain('Link')
  })

  // A non-Stripe provider has configurable method choices supplied by its parent. The card
  // must continue rendering the supplied Alipay and WeChat Pay method labels in its
  // configurable controls.
  it('still shows configurable type toggles for non-Stripe providers', () => {
    const wrapper = mountCard(
      providerFactory({
        provider_key: 'easypay',
        name: 'EasyPay',
        supported_types: ['alipay'],
      }),
    )

    expect(wrapper.text()).toContain('支付宝')
    expect(wrapper.text()).toContain('微信支付')
  })
})
