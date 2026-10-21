import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { SubscriptionPlan } from '@/api/payment.types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number | null | undefined) => String(value ?? ''),
  }),
}))

import SubscriptionPlanCard from '../SubscriptionPlanCard.vue'

/**
 * SubscriptionPlanCard.spec.ts checks subscription price, duration, model scope, and title
 * layout through mounted cards. Plan fixtures vary currency and stored unit representations
 * while preserving user-visible names and keeping purchase actions isolated.
 */

// mountPlanCard mounts a default plan with configurable currency and original price.
// Currency defaults to an empty string, original price is omitted by default, and overrides
// replace any default plan fields last. The group-name argument is ignored by this fixture.
const mountPlanCard = (
  _groupName: string,
  currency = '',
  originalPrice?: number,
  overrides: Partial<SubscriptionPlan> = {},
) =>
  mount(SubscriptionPlanCard, {
    props: {
      plan: {
        id: 1,
        group_id: 10,
        name: 'Pro',
        description: '',
        price: 10,
        original_price: originalPrice,
        currency,
        validity_days: 30,
        validity_unit: 'day',
        features: [],
        for_sale: true,
        sort_order: 1,
        supported_model_scopes: ['claude', 'gemini_text', 'gemini_image'],
        ...overrides,
      },
    },
  })

describe('SubscriptionPlanCard', () => {
  // A plan has no configured model-family policy. Its card must omit scope labels so the absence
  // of a restriction is not shown as a specific family selection.
  it('does not display scopes when no model family policy is configured', () => {
    const text = mountPlanCard('mixed', '', undefined, { supported_model_scopes: [] }).text()
    expect(text).not.toContain('Claude')
    expect(text).not.toContain('Gemini')
    expect(text).not.toContain('Imagen')
  })

  // A plan combines model-family restrictions from its groups. The card must display the
  // configured scope labels so the purchase presentation describes the plan's allowed families.
  it('shows configured model scopes for mixed groups', () => {
    const text = mountPlanCard('antigravity').text()

    expect(text).toContain('Claude')
    expect(text).toContain('Gemini')
    expect(text).toContain('Imagen')
  })

  // Stored plans use both singular and plural duration units. Their cards must display
  // equivalent week or month suffixes and retain the configured duration counts.
  it('renders plural validity units instead of mislabeled days', () => {
    expect(
      mountPlanCard('openai', '', undefined, { validity_days: 1, validity_unit: 'months' }).text(),
    ).toContain('/ payment.perMonth')
    expect(
      mountPlanCard('openai', '', undefined, { validity_days: 3, validity_unit: 'months' }).text(),
    ).toContain('/ 3payment.months')
    expect(
      mountPlanCard('openai', '', undefined, { validity_days: 2, validity_unit: 'weeks' }).text(),
    ).toContain('/ 2payment.weeks')
    expect(
      mountPlanCard('openai', '', undefined, { validity_days: 30, validity_unit: 'day' }).text(),
    ).toContain('/ 30payment.days')
  })

  // A plan specifies a currency for both current and original prices. The card must use that
  // currency's symbol consistently across its price and discount presentation.
  it('uses the configured currency symbol on current and original prices', () => {
    const text = mountPlanCard('openai', 'NZD', 20).text()

    expect(text).toContain('NZ$10NZD')
    expect(text).toContain('NZ$20NZD')
    expect(mountPlanCard('openai', 'CNY', 20).text()).toContain('¥10CNY')
    expect(mountPlanCard('openai').text()).toContain('$10')
  })

  // Plan titles include long Chinese, English, and unbroken text. The title region must retain
  // the full name while keeping the two-line layout required by parallel cards.
  it.each([
    ['长中文', '企业全球加速专业订阅套餐（含高级模型与优先支持）'],
    ['长英文', 'Enterprise Global Acceleration Subscription with Priority Support'],
    ['无空格长词', 'EnterpriseGlobalAccelerationSubscriptionWithPrioritySupport1234567890'],
  ])('在固定两行区域中保留完整的%s套餐标题', (_label, name) => {
    const wrapper = mountPlanCard('openai', '', undefined, { name })
    const title = wrapper.get('h3')

    expect(title.text()).toBe(name)
    expect(title.attributes('title')).toBe(name)
    expect(title.classes()).toEqual(
      expect.arrayContaining([
        'min-w-0',
        'sm:h-12',
        'break-words',
        'line-clamp-2',
        '[overflow-wrap:anywhere]',
      ]),
    )
    expect(title.classes()).not.toContain('truncate')
  })

  // A plan card contains its title, prices, description, and purchase action. Those regions must
  // keep independent layout positions so a long title does not displace the amount.
  it('将标题、价格、描述和购买操作保持在独立的稳定区域', () => {
    const wrapper = mountPlanCard('openai', 'USD', undefined, {
      name: 'Enterprise Global Acceleration Subscription with Priority Support',
      price: 123.45,
      description: 'Includes advanced models and priority support.',
    })
    const title = wrapper.get('h3')
    const price = wrapper.findAll('span').find((node) => node.text() === '123.45')

    expect(title.element.parentElement?.classList).toContain('min-w-0')
    expect(title.element.parentElement?.classList).toContain('flex-1')
    // The price belongs to its own layout region so long plan titles retain their text without
    // displacing the purchase amount.
    expect(title.element.parentElement?.contains(price?.element ?? null)).toBe(false)
    expect(price?.classes()).toContain('tabular-nums')
    expect(wrapper.get('p').text()).toBe('Includes advanced models and priority support.')
    expect(wrapper.get('button').text()).toBe('payment.subscribeNow')
  })

  // A plan uses a short title that occupies little text space. Its card must retain the reserved
  // title height needed to align prices with neighboring cards.
  it('短标题也保持卡片对齐所需的固定高度', () => {
    const wrapper = mountPlanCard('openai', '', undefined, { name: 'Pro', description: '' })
    const title = wrapper.get('h3')

    expect(title.text()).toBe('Pro')
    expect(title.attributes('title')).toBe('Pro')
    expect(title.classes()).toEqual(
      expect.arrayContaining(['text-base', 'font-semibold', 'sm:h-12']),
    )
  })
})
