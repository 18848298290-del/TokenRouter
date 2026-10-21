import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import TokenUsageTrend from '../TokenUsageTrend.vue'
import { setTheme } from '@/composables/useTheme'

// TokenUsageTrend.spec.ts verifies cache-read percentages, date labels, tooltip costs,
// and reactive chart colors. The Line substitute exposes data and options while invoking
// tooltip callbacks, so the tests can inspect bucket calculations and displayed labels
// together.

// messages supplies stable English labels for rendered assertions, with unknown keys
// passed through by the local translation substitute during each component test.
const messages: Record<string, string> = {
  'admin.dashboard.tokenUsageTrend': 'Token Usage Trend',
  'admin.dashboard.noDataAvailable': 'No data available',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    template: `
      <div>
        <div class="chart-data">{{ JSON.stringify(data) }}</div>
        <div class="chart-options">{{ JSON.stringify(options) }}</div>
        <div class="tooltip-title">{{ options.plugins.tooltip.callbacks.title?.([{ dataIndex: 0 }]) }}</div>
        <div class="tooltip-footer">{{ options.plugins.tooltip.callbacks.footer?.([{ dataIndex: 0 }]) }}</div>
      </div>
    `,
  },
}))

describe('TokenUsageTrend', () => {
  beforeEach(() => {
    setTheme(false)
  })

  // A bucket contains ordinary input and cache-read tokens. The cache percentage must
  // use their combined prompt total, and the series must retain a solid line with an
  // external tooltip.
  it('calculates cache hit rate against all prompt tokens', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 500,
            output_tokens: 100,
            cache_creation_tokens: 0,
            cache_read_tokens: 1500,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const options = (wrapper.vm as any).$?.setupState.lineOptions
    expect(options.plugins.tooltip.enabled).toBe(false)
    expect(options.plugins.tooltip.external).toBeTypeOf('function')
    const hitRateDataset = chartData.datasets.find((ds: any) => ds.label === 'Cached Input %')
    // Cache reads occupy three quarters of the complete prompt total in this fixture.
    expect(hitRateDataset.data[0]).toBe(75)
    // The cache-rate series uses the chart's default solid stroke.
    expect(hitRateDataset.borderDash).toBeUndefined()
  })

  // Every prompt token category is zero in an otherwise present bucket. The cache-read
  // series must contain zero percent so empty usage never produces division-by-zero
  // output.
  it('returns 0 hit rate when all prompt tokens are zero', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 0,
            input_tokens: 0,
            output_tokens: 0,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0,
            actual_cost: 0,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find((ds: any) => ds.label === 'Cached Input %')
    expect(hitRateDataset.data[0]).toBe(0)
  })

  // A bucket includes cache creation alongside ordinary input and cache reads. The cache
  // percentage must include all three prompt categories in its denominator and yield the
  // expected half share.
  it('includes cache_creation_tokens in denominator for Anthropic models', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 200,
            output_tokens: 50,
            cache_creation_tokens: 300,
            cache_read_tokens: 500,
            cost: 0.02,
            actual_cost: 0.01,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find((ds: any) => ds.label === 'Cached Input %')
    // Cache creation contributes to the prompt denominator, making these cache reads
    // exactly half of all input-side tokens.
    expect(hitRateDataset.data[0]).toBe(50)
  })

  // The component mounts in dark mode before the theme switches to light. Reactive
  // legend and axis options must update to the light theme text color after Vue flushes.
  it('updates chart text colors after switching from dark to light mode', async () => {
    setTheme(true)
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-07-25',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    expect(JSON.parse(wrapper.find('.chart-options').text()).plugins.legend.labels.color).toBe(
      '#DEE0E2',
    )

    setTheme(false)
    await wrapper.vm.$nextTick()

    const lightOptions = JSON.parse(wrapper.find('.chart-options').text())
    // The light theme supplies dark text to both legend and axes after the reactive
    // theme update.
    expect(lightOptions.plugins.legend.labels.color).toBe('#3F3F46')
    expect(lightOptions.scales.x.ticks.color).toBe('#3F3F46')
    expect(lightOptions.scales.y.ticks.color).toBe('#3F3F46')
  })

  // Hourly data contains a full date and time string. The chart axis must display the
  // compact hour while the tooltip title retains the complete source timestamp.
  it('uses compact hour labels while keeping the full date in the tooltip', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        granularity: 'hour',
        trendData: [
          {
            date: '2026-08-24 08:00',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['08:00'])
    expect(wrapper.find('.tooltip-title').text()).toBe('2026-08-24 08:00')
  })

  // Daily data contains a full year, month, and day. The axis must display month and day
  // while the tooltip title retains the original complete date.
  it('uses month-day labels for daily granularity while keeping the full date in the tooltip', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        granularity: 'day',
        trendData: [
          {
            date: '2026-08-24',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['08-24'])
    expect(wrapper.find('.tooltip-title').text()).toBe('2026-08-24')
  })

  // Standard-cost display is disabled for a bucket containing both charge amounts. The
  // tooltip footer must retain actual spending and omit the standard-cost label.
  it('omits standard cost from the tooltip when disabled', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        showStandardCost: false,
        trendData: [
          {
            date: '2026-08-24',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    expect(wrapper.find('.tooltip-footer').text()).toContain('Actual:')
    expect(wrapper.find('.tooltip-footer').text()).not.toContain('Standard:')
  })
})
