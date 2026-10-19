import { defineComponent, nextTick } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ModelMarketplaceView from '../ModelMarketplaceView.vue'
import { modelIconData } from '@/utils/modelIconData'
import type { MarketplaceGroup, MarketplaceModelPricing } from '@/types'

const getMarketplaceModels = vi.hoisted(() => vi.fn())
const checkAuth = vi.hoisted(() => vi.fn())
const fetchPublicSettings = vi.hoisted(() => vi.fn())
const copyToClipboard = vi.hoisted(() => vi.fn())

vi.mock('@/api/marketplace', () => ({
  getMarketplaceModels,
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: { value: false }, copyToClipboard }),
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    isAuthenticated: true,
    isAdmin: false,
    checkAuth,
  }),
  useAppStore: () => ({
    siteName: 'TokenRouter',
    siteLogo: '',
    docUrl: '',
    cachedPublicSettings: null,
    publicSettingsLoaded: true,
    fetchPublicSettings,
  }),
}))

vi.mock('@/composables/useTheme', () => ({
  initTheme: vi.fn(),
  useTheme: () => ({ isDark: { value: true }, toggleTheme: vi.fn() }),
}))

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
      t: (key: string, params?: Record<string, string>) => {
        if (key === 'marketplace.rateMultiplierValue') {
          return `marketplace.rateMultiplierValue ${params?.multiplier || ''}`
        }
        if (key === 'marketplace.imageRateMultiplierValue') {
          return `marketplace.imageRateMultiplierValue ${params?.multiplier || ''}`
        }
        if (key === 'marketplace.maxDiscountOff') {
          return `marketplace.maxDiscountOff ${params?.percent || ''}`
        }

        return key
      },
    }),
  }
})

const SelectStub = defineComponent({
  name: 'TokenSelectStub',
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: null,
    },
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue', 'change'],
  template: `
    <div class="select-stub">
      <button
        v-for="option in options"
        :key="String(option.value)"
        type="button"
        :data-testid="'select-option-' + String(option.value)"
        @click="$emit('update:modelValue', option.value); $emit('change', option.value, option)"
      >
        {{ option.label }}
      </button>
    </div>
  `,
})

const SearchInputStub = defineComponent({
  name: 'SearchInput',
  props: {
    modelValue: {
      type: String,
      default: '',
    },
  },
  emits: ['update:modelValue'],
  template: `
    <input
      data-testid="marketplace-search"
      :value="modelValue"
      @input="$emit('update:modelValue', $event.target.value)"
    />
  `,
})

const tokenPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  input_price_per_token: 0.000001,
  image_input_price_per_token: 0.000003,
  output_price_per_token: 0.000002,
  fast_image_input_price_per_token: 0.000006,
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

function marketplaceFixture(): MarketplaceGroup[] {
  return [
	marketplaceGroup(1, 'Plus', [
      marketplaceModel('gpt-5.5', 'GPT 5.5', tokenPricing),
      marketplaceModel('legacy-unpriced', 'Legacy Unpriced', unpricedPricing),
    ]),
	marketplaceGroup(2, 'Pro', [
      marketplaceModel('gpt-5.5', 'GPT 5.5', tokenPricing),
    ]),
	marketplaceGroup(3, 'Plus 2', [
      marketplaceModel('gpt-5.5', 'GPT 5.5', tokenPricing),
    ]),
	marketplaceGroup(4, 'Pro 2', [
      marketplaceModel('gpt-5.5', 'GPT 5.5', tokenPricing),
    ]),
  ]
}

function marketplaceGroup(id: number, name: string, models: MarketplaceGroup['models']): MarketplaceGroup {
  return {
    id,
    name,
    description: `${name} group`,
    platform: 'openai',
    display_brand: 'OpenAI',
    sort_order: id,
    rate_multiplier: id,

    official_price_ratio: id / 10,
    official_price_rmb_equivalent: id,
    capacity: {
      concurrency_used: id,
      concurrency_max: 10,
      sessions_used: id,
      sessions_max: 20,
      rpm_used: id,
      rpm_max: 60,
    },
    model_count: models.length,
    models,
  }
}

function marketplaceModel(id: string, displayName: string, pricing: MarketplaceModelPricing): MarketplaceGroup['models'][number] {
  return {
    id,
    display_name: displayName,
    pricing,
  }
}

async function mountMarketplace() {
  const wrapper = mount(ModelMarketplaceView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot name="page-heading-actions" /><slot /></div>' },
        RouterLink: { template: '<a><slot /></a>' },
        Icon: { template: '<span />' },
        LoadingSpinner: { template: '<span />' },
        LocaleSwitcher: { template: '<span />' },
        GroupCapacityBadge: { template: '<span data-testid="group-capacity" />' },
        SearchInput: SearchInputStub,
        Select: SelectStub,
      },
    },
  })
  await flushPromises()
  return wrapper
}

// HelpTooltip 通过 Teleport 挂到 body，隐藏时用 v-show（display:none）控制。
function visibleTooltips() {
  return Array.from(document.body.querySelectorAll<HTMLElement>('[role="tooltip"]'))
    .filter((el) => el.style.display !== 'none')
}

describe('ModelMarketplaceView', () => {
  beforeEach(() => {
    localStorage.clear()
    getMarketplaceModels.mockResolvedValue(marketplaceFixture())
    checkAuth.mockClear()
    fetchPublicSettings.mockClear()
    copyToClipboard.mockClear()
  })

  it('取数期间保留模型卡片占位，不提前显示空状态', async () => {
    let resolveModels!: (value: MarketplaceGroup[]) => void
    getMarketplaceModels.mockImplementationOnce(() => new Promise(resolve => { resolveModels = resolve }))
    const wrapper = await mountMarketplace()
    expect(wrapper.find('[data-testid="marketplace-skeleton"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('marketplace.emptyTitle')

    resolveModels(marketplaceFixture())
    await flushPromises()
    expect(wrapper.find('[data-testid="marketplace-skeleton"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="marketplace-group-section"]')).toHaveLength(4)
    wrapper.unmount()
  })

  it('默认按分组-模型展示', async () => {
    const wrapper = await mountMarketplace()

    expect(wrapper.findAll('[data-testid="marketplace-group-section"]')).toHaveLength(4)
  })

  it('用户侧不展示分组容量，并将可用率状态条靠右放置', async () => {
    const fixture = marketplaceFixture()
    fixture[0] = {
      ...fixture[0],
      availability: {
        window_days: 7,
        bucket_minutes: 1440,
        success_count: 6,
        total_count: 7,
        availability_rate: 6 / 7,
        days: [],
      },
    }
    getMarketplaceModels.mockResolvedValue(fixture)

    const wrapper = await mountMarketplace()

    expect(wrapper.findAll('[data-testid="group-capacity"]')).toHaveLength(0)
    expect(wrapper.get('[data-testid="marketplace-group-availability"]').classes()).toContain('xl:w-[560px]')
  })

  it('分组头部用最高优惠标签替换模型数标签，无有效折扣的分组不渲染', async () => {
    const fixture = marketplaceFixture()
    // ratio >= 1 表示无折扣；undefined 表示后端未下发官方价比例。
    fixture[1] = { ...fixture[1], official_price_ratio: 1.2 }
    fixture[2] = { ...fixture[2], official_price_ratio: undefined }
    getMarketplaceModels.mockResolvedValue(fixture)

    const wrapper = await mountMarketplace()
    const sections = wrapper.findAll('[data-testid="marketplace-group-section"]')

    // fixture 中 group 1 的 official_price_ratio 为 0.1，即最高优惠 90%。
    expect(sections[0].text()).toContain('marketplace.maxDiscountOff 90')
    expect(sections[0].text()).not.toContain('marketplace.modelsStat')
    expect(sections[1].text()).not.toContain('marketplace.maxDiscountOff')
    expect(sections[2].text()).not.toContain('marketplace.maxDiscountOff')
    expect(sections[3].text()).toContain('marketplace.maxDiscountOff 60')

    // 移动端点击 tag 也能查看说明：点击后弹出提示气泡。
    await sections[0].get('[data-testid="group-max-discount-tag"]').trigger('click')
    await nextTick()
    expect(visibleTooltips().some((el) => el.textContent?.includes('marketplace.maxDiscountHint'))).toBe(true)
    await sections[0].get('[data-testid="group-rate-multiplier-tag"]').trigger('click')
    await nextTick()
    expect(visibleTooltips().some((el) => el.textContent?.includes('marketplace.rateMultiplierHint'))).toBe(true)
  })

  it('模型卡片摘要保留免费默认区间', async () => {
    const fixture = marketplaceFixture()
    fixture[0].models[0].pricing = { pricing_mode: 'token', price_status: 'priced', context_intervals: [
      { min_tokens: 0, max_tokens: 100 },
      { min_tokens: 100, max_tokens: null, input_price_per_token: 0.000002 },
    ] }
    getMarketplaceModels.mockResolvedValue(fixture)
    const wrapper = await mountMarketplace()
    const section = wrapper.findAll('[data-testid="marketplace-group-section"]')[0]
    expect(section.text()).toContain('0.00')
    expect(section.text()).toContain('0-100')
    expect(section.text()).toContain('100+')
  })

  it('模型卡片中的模型 ID 支持一键复制', async () => {
    const wrapper = await mountMarketplace()

    // 卡片 ID 行的复制按钮直接复制模型 ID。
    const cardCopyButtons = wrapper.findAll('[data-testid="model-id-copy"]')
    expect(cardCopyButtons.length).toBeGreaterThan(0)
    await cardCopyButtons[0].trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('gpt-5.5')
  })

  it('模型卡片右上角展示输入输出能力标签', async () => {
    const wrapper = await mountMarketplace()

    const sections = wrapper.findAll('[data-testid="marketplace-group-section"]')
    const gptCards = sections[0].findAll('article')
    // fixture 中 gpt-5.5 定价数据带图片输入价，应展示 文字·图片 -> 文字。
    expect(gptCards[0].get('[data-testid="model-capability-tags"]').findAll('[data-modality]').map((tag) => tag.attributes('data-modality')))
      .toEqual(['input-text', 'input-image', 'output-text'])
    // 无定价数据的模型回退为纯文本能力。
    expect(gptCards[1].get('[data-testid="model-capability-tags"]').findAll('[data-modality]').map((tag) => tag.attributes('data-modality')))
      .toEqual(['input-text', 'output-text'])
  })

  it('能力标签优先使用接口下发的模态元数据', async () => {
    const fixture = marketplaceFixture()
    fixture[0] = {
      ...fixture[0],
      models: [
        // 本地规则会把 gpt-image-2 识别为 text+image 输入，接口数据应覆盖为纯文字输入。
        { ...marketplaceModel('gpt-image-2', 'GPT Image 2', imagePricing), input_modalities: ['text'], output_modalities: ['image'] },
      ],
    }
    getMarketplaceModels.mockResolvedValue(fixture)

    const wrapper = await mountMarketplace()
    const sections = wrapper.findAll('[data-testid="marketplace-group-section"]')

    expect(sections[0].get('[data-testid="model-capability-tags"]').findAll('[data-modality]').map((tag) => tag.attributes('data-modality')))
      .toEqual(['input-text', 'output-image'])
  })

  it('Gemini Flash 档位展示接口下发的完整音视频输入能力', async () => {
    const fixture = marketplaceFixture()
    // 档位 ID 保持不变，图标完全采用后端解析的能力元数据。
    fixture[0].models = ['gemini-3.7-flash-tiered', 'gemini-3.8-flash-tiered'].map((id) => ({
      ...marketplaceModel(id, id, imagePricing),
      input_modalities: ['text', 'image', 'audio', 'video'],
      output_modalities: ['text'],
    }))
    getMarketplaceModels.mockResolvedValue(fixture)
    const wrapper = await mountMarketplace()
    const cards = wrapper.findAll('[data-testid="marketplace-group-section"]')[0].findAll('article')
    expect(cards).toHaveLength(2)
    for (const card of cards) {
      expect(card.get('[data-testid="model-capability-tags"]').findAll('[data-modality]').map((tag) => tag.attributes('data-modality')))
        .toEqual(['input-text', 'input-image', 'input-audio', 'input-video', 'output-text'])
    }
  })

  it('模型信息浮层按目录顺序列出可用协议并标出原生协议，没有属性时也显示入口', async () => {
    getMarketplaceModels.mockResolvedValueOnce([
      marketplaceGroup(1, 'Plus', [
        { ...marketplaceModel('gpt-5.5', 'GPT 5.5', tokenPricing), protocols: ['openai_chat_completions', 'anthropic_messages'], native_protocols: ['openai_chat_completions'] },
        marketplaceModel('legacy-unpriced', 'Legacy Unpriced', unpricedPricing),
      ]),
    ])
    const wrapper = await mountMarketplace()
    const triggers = wrapper.findAll('[data-testid="model-attributes-trigger"]')
    expect(triggers).toHaveLength(1)

    await triggers[0].trigger('click')
    await nextTick()
    const tooltip = visibleTooltips().find((el) => el.querySelector('[data-testid="model-protocols"]'))
    expect(tooltip).toBeDefined()
    const chips = Array.from(tooltip!.querySelectorAll<HTMLElement>('[data-protocol]'))
    expect(chips.map((chip) => chip.dataset.protocol)).toEqual(['anthropic_messages', 'openai_chat_completions'])
    expect(chips.map((chip) => chip.title)).toEqual([
      'POST /v1/messages · marketplace.protocolConvertedHint',
      'POST /v1/chat/completions · marketplace.protocolNativeHint',
    ])
    expect(chips.map((chip) => chip.dataset.native)).toEqual(['false', 'true'])
    expect(tooltip!.querySelector('[data-testid="model-protocols-native-legend"]')).not.toBeNull()
    expect(chips[0].textContent).toContain('marketplace.protocolNames.anthropic_messages')
    wrapper.unmount()
  })

  it('xAI 分组展示品牌图形', async () => {
    const fixture = marketplaceFixture()
    fixture[0] = {
      ...fixture[0],
      name: 'Grok',
      display_brand: 'xAI',
    }
    getMarketplaceModels.mockResolvedValue(fixture)

    const wrapper = await mountMarketplace()

    // 品牌名 xAI 应映射到现有 Grok SVG，不能退回紫色字母 X。
    const grokGroup = wrapper.findAll('[data-testid="marketplace-group-section"]')
      .find((section) => section.get('h2').text() === 'Grok')
    expect(grokGroup?.find('.provider-icon[width="28px"]').exists()).toBe(true)
    expect(grokGroup?.find('.provider-icon-fallback').exists()).toBe(false)
  })

  // 分组名和展示品牌都可以提供智谱名称，两个尺寸的图标共用品牌映射。
  it.each([
    ['GLM', ''],
    ['zai', ''],
    ['Z.AI', ''],
    ['zai Pro', ''],
    ['自定义分组', '智谱'],
    ['自定义分组', 'zai'],
    ['自定义分组', 'z.ai'],
  ])('分组 %s、展示品牌 %s 显示智谱图标', async (name, displayBrand) => {
    getMarketplaceModels.mockResolvedValue([{
      ...marketplaceGroup(1, name, [marketplaceModel('glm-5', 'GLM 5', tokenPricing)]),
      display_brand: displayBrand,
    }])

    const wrapper = await mountMarketplace()
    const group = wrapper.get('[data-testid="marketplace-group-section"]')

    for (const size of ['14px', '28px']) {
      const icon = group.get(`svg.provider-icon[width="${size}"]`)
      expect(icon.findAll('path').map((path) => path.attributes('d'))).toEqual(modelIconData.zhipu.paths)
    }
    expect(group.find('.provider-icon-fallback').exists()).toBe(false)
    expect(group.text()).toContain('智谱')
    wrapper.unmount()
  })

  it('只展示统一分组倍率', async () => {
    const fixture = marketplaceFixture()
    fixture[0] = {
      ...fixture[0],

      models: [
        marketplaceModel('gpt-image-1', 'GPT Image', imagePricing),
      ],
    }
    getMarketplaceModels.mockResolvedValue(fixture)

    const wrapper = await mountMarketplace()

    expect(wrapper.findAll('[data-testid="marketplace-group-section"]').map((section) => section.text()).join('\n')).not.toContain('marketplace.imageRateMultiplierValue')
  })

  it('模型卡片可展开抽屉式定价面板并切换 fast mode', async () => {
    const wrapper = await mountMarketplace()

    const toggle = wrapper.get('[data-testid="model-pricing-toggle"]')
    expect(toggle.attributes('aria-expanded')).toBe('false')

    await toggle.trigger('click')
    await nextTick()

    // 面板原地展开，展示完整定价（标准计费行），且 fast mode 切换可用。
    const sections = wrapper.findAll('[data-testid="marketplace-group-section"]')
    const firstCard = sections[0].findAll('article')[0]
    expect(firstCard.text()).toContain('marketplace.input')
    expect(firstCard.text()).toContain('marketplace.imageInput')
    expect(firstCard.text()).toContain('marketplace.output')

    const fastSwitch = firstCard.get('[data-testid="pricing-fast-switch"]')
    await fastSwitch.findAll('button')[1].trigger('click')
    await nextTick()

    // fast mode 下展示 fast 加价行，标准行隐藏。
    expect(firstCard.text()).toContain('marketplace.fastImageInput')
    expect(firstCard.text()).not.toContain('marketplace.imageInput')

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    await nextTick()
    expect(wrapper.get('[data-testid="model-pricing-toggle"]').attributes('aria-expanded')).toBe('false')
  })
})

// Public model cards display the quoted video unit and expose its filter.
// Selecting video pricing removes token models while retaining free video tiers.
it('shows and filters custom video prices', async () => {
  getMarketplaceModels.mockResolvedValueOnce([marketplaceGroup(1, 'Videos', [
    marketplaceModel('custom-video', 'Custom Video', { pricing_mode: 'video', price_status: 'priced', video_prices: { '768p': 0.12, '2k': 0 } }),
    marketplaceModel('text-model', 'Text Model', tokenPricing),
  ])])
  const wrapper = await mountMarketplace()
  expect(wrapper.text()).toContain('768p')
  expect(wrapper.text()).toContain('marketplace.perSecond')
  await wrapper.get('[data-testid="select-option-video"]').trigger('click')
  expect(wrapper.text()).toContain('Custom Video')
  expect(wrapper.text()).not.toContain('Text Model')
  wrapper.unmount()
})
