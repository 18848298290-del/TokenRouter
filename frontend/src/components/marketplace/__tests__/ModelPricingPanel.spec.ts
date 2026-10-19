import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelPricingPanel from '../ModelPricingPanel.vue'
import type { MarketplaceModel, MarketplaceModelPricing } from '@/types'

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitName: { value: '点' },
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      // 带参数时把参数拼进结果，便于断言倍率和时区。
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    }),
  }
})

// Icon 打桩时透出 name，便于断言展开/收起箭头方向。
const IconStub = {
  name: 'Icon',
  props: {
    name: { type: String, default: '' },
    size: { type: String, default: 'md' },
    strokeWidth: { type: Number, default: 1.5 },
  },
  template: '<span class="icon-stub" :data-icon="name" />',
}

function marketplaceModel(id: string, pricing: MarketplaceModelPricing): MarketplaceModel {
  return {
    id,
    display_name: id,
    pricing,
  }
}

function mountPanel(model: MarketplaceModel) {
  return mount(ModelPricingPanel, {
    props: { model },
    global: {
      stubs: {
        Icon: IconStub,
      },
    },
  })
}

const tokenPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  input_price_per_token: 0.000001,
  output_price_per_token: 0.000002,
}

const fastPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  input_price_per_token: 0.000001,
  output_price_per_token: 0.000002,
  fast_input_price_per_token: 0.000005,
  fast_output_price_per_token: 0.000006,
}

const intervalPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  context_intervals: [
    {
      min_tokens: 0,
      max_tokens: 32000,
      input_price_per_token: 0.000001,
      output_price_per_token: 0.000002,
    },
    {
      min_tokens: 32000,
      max_tokens: null,
      input_price_per_token: 0.000003,
      output_price_per_token: 0.000004,
    },
  ],
}

const imagePricing: MarketplaceModelPricing = {
  pricing_mode: 'image',
  price_status: 'priced',
  image_price_1k: 0.5,
}

const unpricedPricing: MarketplaceModelPricing = {
  pricing_mode: 'unknown',
  price_status: 'unpriced',
}

describe('ModelPricingPanel', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('无定价模型不渲染展开入口', () => {
    const wrapper = mountPanel(marketplaceModel('m1', unpricedPricing))

    expect(wrapper.find('[data-testid="model-pricing-toggle"]').exists()).toBe(false)
  })

  it('点击触发条展开收起面板，右下角箭头同步切换方向', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))
    const toggle = wrapper.get('[data-testid="model-pricing-toggle"]')
    const drawer = wrapper.get('.motion-collapse')

    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(toggle.get('.icon-stub[data-icon="chevronDown"]').exists()).toBe(true)
    expect((drawer.element as HTMLElement).style.display).toBe('none')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(toggle.get('.icon-stub[data-icon="chevronDown"]').classes()).toContain('rotate-180')
    expect((drawer.element as HTMLElement).style.display).not.toBe('none')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(wrapper.get('[data-testid="model-pricing-toggle"]').get('.icon-stub[data-icon="chevronDown"]').exists()).toBe(true)
  })

  it('完整定价单列展示，窄屏允许标签和价格换行', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const rows = wrapper.get('[data-testid="pricing-rows"]')
    expect(wrapper.find('.md\\:grid-cols-2').exists()).toBe(false)
    expect(rows.findAll('.whitespace-nowrap')).toHaveLength(0)
    expect(rows.findAll('span').every((span) => span.classes().includes('min-w-0'))).toBe(true)
  })

  it('标准价格行展示全部计费项', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const labels = wrapper.get('[data-testid="pricing-rows"]').findAll('span:first-child').map((el) => el.text())
    expect(labels).toEqual(['marketplace.input', 'marketplace.output'])
  })

  it('展示独立的 1h 缓存写入价格', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', {
      ...tokenPricing,
      cache_write_price_per_token: 0.000003,
      cache_write_1h_price_per_token: 0.000006,
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const labels = wrapper.get('[data-testid="pricing-rows"]').findAll('span:first-child').map((el) => el.text())
    expect(labels).toContain('marketplace.cacheWrite1h')
    expect(wrapper.text()).toContain('6.00')
  })

  it('存在 fast mode 计价时展示切换，切换后只显示 fast 价格行', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', fastPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('marketplace.input')

    const fastSwitch = wrapper.get('[data-testid="pricing-fast-switch"]')
    await fastSwitch.findAll('button')[1].trigger('click')

    const labels = wrapper.get('[data-testid="pricing-rows"]').findAll('span:first-child').map((el) => el.text())
    expect(labels).toEqual(['marketplace.fastInput', 'marketplace.fastOutput'])
  })

  it('上下文区间模型展示区间切换，定价行随选中区间联动', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', intervalPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const intervalSwitch = wrapper.get('[data-testid="pricing-interval-switch"]')
    const buttons = intervalSwitch.findAll('button')
    expect(buttons.map((button) => button.text())).toEqual(['0-32k', '32k+'])

    // 第一个区间输入价 0.000001/Token，即 1.00 点/百万Token。
    expect(wrapper.text()).toContain('1.00')

    await buttons[1].trigger('click')
    // 第二个区间输入价 0.000003/Token，即 3.00 点/百万Token。
    expect(wrapper.text()).toContain('3.00')
    expect(wrapper.text()).not.toContain('1.00')
  })

  it('保留省略零价格字段的免费默认区间，并允许切换到收费区间', async () => {
    const wrapper = mountPanel(marketplaceModel('free-range', {
      pricing_mode: 'token', price_status: 'priced', context_intervals: [
        { min_tokens: 0, max_tokens: 100 },
        { min_tokens: 100, max_tokens: null, input_price_per_token: 0.000002 },
      ],
    }))
    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    const buttons = wrapper.get('[data-testid="pricing-interval-switch"]').findAll('button')
    expect(buttons).toHaveLength(2)
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('0.00')
    await buttons[1].trigger('click')
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('2.00')
    await buttons[0].trigger('click')
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('0.00')
  })

  it('Max 推理档位切换后全部 token 价格乘以倍率，Fast 价格同样适用', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', { ...fastPricing, max_reasoning_effort_multiplier: 1.5 }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    const effortButtons = wrapper.get('[data-testid="pricing-effort-switch"]').findAll('button')
    expect(effortButtons.map((button) => button.text())).toEqual(['marketplace.pricingDefaultEffort', 'marketplace.pricingMaxEffort'])
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('1.00')

    await effortButtons[1].trigger('click')
    // 输入 1.00、输出 2.00 乘以 1.5。
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('1.50')
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('3.00')

    await wrapper.get('[data-testid="pricing-fast-switch"]').findAll('button')[1].trigger('click')
    // Fast 输入 5.00 乘以 1.5。
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('7.50')
  })

  it('分时切换列出其他时段和各时段，选中时段后价格乘以对应倍率', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    // 上海时间 08:00，不在任何时段内，默认停在其他时段。
    vi.setSystemTime(new Date('2026-10-05T00:00:00Z'))
    const wrapper = mountPanel(marketplaceModel('m1', {
      ...tokenPricing,
      time_pricing: {
        timezone: 'Asia/Shanghai',
        weekdays_only: false,
        periods: [
          { start_time: '20:00:00', end_time: '00:00', multiplier: 0.5 },
          { start_time: '09:00', end_time: '12:00', multiplier: 2 },
        ],
      },
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    const buttons = wrapper.get('[data-testid="pricing-time-switch"]').findAll('button')
    expect(buttons.map((button) => button.text())).toEqual(['marketplace.timePricingOtherHours', '09:00-12:00', '20:00-24:00'])
    expect(buttons[0].classes()).toContain('segmented-item-active')
    expect(wrapper.get('[data-testid="pricing-time-note"]').text()).toBe('marketplace.timePricingNoteEveryDay:{"timezone":"Asia/Shanghai"}')

    await buttons[1].trigger('click')
    // 输入 1.00 乘以 2。
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('2.00')
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).not.toContain('1.00')
  })

  it('默认选中规则时区下当前所在的时段', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    // 上海时间 22:00，落在 20:00-24:00。
    vi.setSystemTime(new Date('2026-10-05T14:00:00Z'))
    const wrapper = mountPanel(marketplaceModel('m1', {
      ...tokenPricing,
      time_pricing: {
        timezone: 'Asia/Shanghai',
        weekdays_only: false,
        periods: [{ start_time: '20:00', end_time: '00:00', multiplier: 0.5 }],
      },
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const buttons = wrapper.get('[data-testid="pricing-time-switch"]').findAll('button')
    expect(buttons[1].classes()).toContain('segmented-item-active')
    // 输入 1.00 乘以 0.5。
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('0.5000')
  })

  it('分时规则覆盖全天且每天生效时省略其他时段', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', {
      ...tokenPricing,
      time_pricing: {
        timezone: 'UTC',
        weekdays_only: false,
        periods: [
          { start_time: '00:00', end_time: '12:00', multiplier: 0.8 },
          { start_time: '12:00', end_time: '00:00', multiplier: 1.2 },
        ],
      },
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const labels = wrapper.get('[data-testid="pricing-time-switch"]').findAll('button').map((button) => button.text())
    expect(labels).toEqual(['00:00-12:00', '12:00-24:00'])
  })

  it('仅工作日生效时周末默认停在其他时段', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    // 2026-10-04 是周日。
    vi.setSystemTime(new Date('2026-10-04T12:00:00Z'))
    const wrapper = mountPanel(marketplaceModel('m1', {
      ...tokenPricing,
      time_pricing: {
        timezone: 'UTC',
        weekdays_only: true,
        periods: [{ start_time: '00:00', end_time: '00:00', multiplier: 0.8 }],
      },
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const buttons = wrapper.get('[data-testid="pricing-time-switch"]').findAll('button')
    expect(buttons.map((button) => button.text())).toEqual(['marketplace.timePricingOtherHours', '00:00-24:00'])
    expect(buttons[0].classes()).toContain('segmented-item-active')
    expect(wrapper.get('[data-testid="pricing-time-note"]').text()).toContain('marketplace.timePricingNoteWeekdays')
  })

  it('没有倍率的模型不展示档位和分时切换', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    expect(wrapper.find('[data-testid="pricing-effort-switch"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pricing-time-switch"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pricing-time-note"]').exists()).toBe(false)
  })

  it('图片模型展示分档价格且不显示 fast 切换', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', imagePricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    expect(wrapper.text()).toContain('1K')
    expect(wrapper.find('[data-testid="pricing-fast-switch"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pricing-interval-switch"]').exists()).toBe(false)
  })
})

it('显示明确免费的图片尺寸，省略未定价尺寸', () => {
  const wrapper = mountPanel(marketplaceModel('free-image-size', {
    pricing_mode: 'image',
    price_status: 'priced',
    image_price_1k: 0,
  }))
  expect(wrapper.text()).toContain('1K')
  expect(wrapper.text()).not.toContain('2K')
  expect(wrapper.text()).not.toContain('4K')
  expect(wrapper.text()).not.toContain('marketplace.unpriced')
})

// Video rows retain their declared units, including free custom dimensions.
// Token mode controls must remain absent from this per-second price view.
it('shows custom video resolutions and the explicit default per second', async () => {
  const wrapper = mountPanel(marketplaceModel('video', { pricing_mode: 'video', price_status: 'priced', video_prices: { '768p': 0.12, '2k': 0 }, video_price_per_second: 0.3 }))
  await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
  const rows = wrapper.get('[data-testid="pricing-rows"]')
  expect(rows.text()).toContain('768p')
  expect(rows.text()).toContain('2k')
  expect(rows.text()).toContain('0.0000 点 marketplace.perSecond')
  expect(rows.text()).toContain('marketplace.defaultVideoPrice')
  expect(wrapper.find('[data-testid="pricing-fast-switch"]').exists()).toBe(false)
  wrapper.unmount()
})

// An explicit free default alone is enough to display a video quote.
// The expanded panel must not substitute zero-token rows for this valid quote.
it('shows a free default-only video quote', async () => {
  const wrapper = mountPanel(marketplaceModel('video', { pricing_mode: 'video', price_status: 'priced', video_price_per_second: 0 }))
  await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
  expect(wrapper.text()).toContain('marketplace.defaultVideoPrice')
  expect(wrapper.text()).toContain('marketplace.perSecond')
  expect(wrapper.text()).not.toContain('marketplace.input')
  wrapper.unmount()
})
