import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RequestTimeline from '../RequestTimeline.vue'

// RequestTimeline.spec.ts renders timing records with locale keys as text. It
// protects the waterfall rules shared by request, attempt and usage timelines:
// offsets appear in time order, unrecorded offsets get no bar, the total row
// needs the request duration, and a failed admission names its end point.
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('request timeline', () => {
  // Offsets arrive in pipeline order but are drawn by time, and only recorded ones get a bar.
  // The request duration adds the closing total row and sets the bar scale.
  it('orders recorded offsets and closes with the request total', () => {
    const wrapper = mount(RequestTimeline, {
      props: { timing: { upstream_first_response_byte_ms: 900, upstream_started_ms: 100, timing_stream: 1 }, duration: 1800 },
    })
    const rows = wrapper.findAll('li').map((row) => row.text())
    expect(rows).toEqual([
      'requests.timingNames.upstream_started_ms100ms',
      'requests.timingNames.upstream_first_response_byte_ms900ms',
      'requests.totalDuration1.80s',
    ])
    expect(wrapper.find('li span span').attributes('style')).toContain('width: 5.5')
  })

  // Attempt timelines omit the duration, so no total row appears.
  // A failed admission titles the duration sum as running until the request ended.
  it('omits the total row and names a failed admission', () => {
    const wrapper = mount(RequestTimeline, {
      props: { timing: { timing_admitted: 0, request_body_read_ms: 3, preprocess_ms: 4, user_slot_wait_ms: 0, provider_slot_wait_ms: 30 } },
    })
    expect(wrapper.text()).toContain('requests.timing.untilEnd')
    expect(wrapper.text()).toContain('37ms')
    expect(wrapper.text()).not.toContain('requests.totalDuration')
    expect(wrapper.find('ol').exists()).toBe(false)
  })
})
