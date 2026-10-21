import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import OpsThroughputTrendChart from '../OpsThroughputTrendChart.vue'

// OpsThroughputTrendChart.spec.ts checks responsive header structure in the
// throughput chart with rendering stubbed. The test inspects classes and toolbar
// controls on the real component so narrow layouts can wrap actions while each
// action keeps its own width.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    template: '<div class="line-chart" />',
  },
}))

describe('OpsThroughputTrendChart', () => {
  // The chart mounts with empty data and its header controls remain available.
  // Responsive containers must permit wrapping and all three action buttons must
  // resist shrinking.
  it('allows the header controls to wrap on narrow screens', () => {
    const wrapper = mount(OpsThroughputTrendChart, {
      props: {
        points: [],
        loading: false,
        timeRange: '1h',
      },
      global: {
        stubs: {
          EmptyState: true,
          HelpTooltip: true,
        },
      },
    })

    const header = wrapper.get('[data-testid="throughput-chart-header"]')
    expect(header.classes()).toEqual(expect.arrayContaining(['flex-col', 'sm:flex-row']))

    const toolbar = wrapper.get('[data-testid="throughput-chart-toolbar"]')
    expect(toolbar.classes()).toEqual(expect.arrayContaining(['w-full', 'flex-wrap', 'sm:w-auto']))
    expect(toolbar.findAll('button')).toHaveLength(3)
    toolbar.findAll('button').forEach((button) => {
      expect(button.classes()).toContain('shrink-0')
      expect(button.classes()).not.toContain('ml-2')
    })
  })
})
