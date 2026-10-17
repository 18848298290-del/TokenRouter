import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RequestTimingDetails from '../RequestTimingDetails.vue'

// RequestTimingDetails.spec.ts renders persisted observations with locale keys
// as text. It protects the compact grouping: historical records show only their
// offsets, measured zero stays visible, failed admission names the missing
// slot, and synchronous requests drop stream-only rows.
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('request timing grid', () => {
  // A failed admission keeps its durations, shows zero waits and labels the slot row.
  it('marks a failed admission at the slot row and keeps measured zero', () => {
    const wrapper = mount(RequestTimingDetails, {
      props: { timing: { timing_admitted: 0, timing_stream: 1, user_slot_wait_ms: 0, request_body_read_ms: 3 } },
    })
    expect(wrapper.text()).toContain('requests.timing.untilEnd')
    expect(wrapper.text()).not.toContain('requests.timing.beforeSlot')
    expect(wrapper.text()).not.toContain('requests.timing.totalIsSlot')
    expect(wrapper.text()).toContain('0ms')
    expect(wrapper.text()).toContain('requests.timing.notAdmitted')
    wrapper.unmount()
  })

  // Historical rows predate the admission split, so the duration group stays hidden.
  it('does not recover retired SSE timing from historical records', () => {
    const wrapper = mount(RequestTimingDetails, {
      props: { timing: { upstream_first_sse_data_ms: 1500 }, stream: true },
    })
    expect(wrapper.text()).not.toContain('1.50s')
    expect(wrapper.text()).not.toContain('requests.timing.beforeSlot')
    expect(wrapper.text()).not.toContain('requests.timing.notAdmitted')
    expect(wrapper.text()).toContain('requests.timingNames.first_visible_output_ms')
    wrapper.unmount()
  })

  // The record's own sync flag removes stream-only rows even when an offset leaked in.
  it('hides stream-only stages for synchronous requests', () => {
    const wrapper = mount(RequestTimingDetails, {
      props: { timing: { timing_stream: 0, first_visible_output_ms: 1200, upstream_wrote_request_ms: 40 }, stream: true },
    })
    expect(wrapper.text()).not.toContain('requests.timingNames.first_visible_output_ms')
    expect(wrapper.text()).not.toContain('1.20s')
    expect(wrapper.text()).toContain('40ms')
    wrapper.unmount()
  })

  // The heading total repeats the slot offset so readers can see the four parts add up to it.
  it('titles the durations with their total before the slot row', () => {
    const wrapper = mount(RequestTimingDetails, {
      props: { timing: { timing_admitted: 1, request_body_read_ms: 12, preprocess_ms: 4, user_slot_wait_ms: 0, provider_slot_wait_ms: 104, provider_slot_acquired_ms: 120 } },
    })
    expect(wrapper.text()).toContain('requests.timing.beforeSlot')
    expect(wrapper.text()).toContain('requests.timing.totalIsSlot')
    wrapper.unmount()
  })

  // Vue would cast an omitted boolean prop to false and hide these rows.
  it('keeps stream-only stages when neither the record nor the caller knows the mode', () => {
    const wrapper = mount(RequestTimingDetails, {
      props: { timing: { first_visible_output_ms: 1060 } },
    })
    expect(wrapper.text()).toContain('requests.timingNames.first_visible_output_ms')
    expect(wrapper.text()).toContain('1.06s')
    wrapper.unmount()
  })

  // Caller slot content shares the grid so request-level pairs align with timing rows.
  it('renders caller rows before the timing groups', () => {
    const wrapper = mount(RequestTimingDetails, {
      props: { timing: { upstream_wrote_request_ms: 40 } },
      slots: { default: '<span>size</span><span>8KB</span>' },
    })
    const text = wrapper.text()
    expect(text.indexOf('8KB')).toBeLessThan(text.indexOf('requests.timing.events'))
    wrapper.unmount()
  })
})
