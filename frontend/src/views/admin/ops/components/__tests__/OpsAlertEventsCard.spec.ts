import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import OpsAlertEventsCard from '../OpsAlertEventsCard.vue'
import type { AlertEvent } from '@/api/admin/ops'

// OpsAlertEventsCard.spec.ts checks initial and appended alert loading in both
// responsive layouts. Deferred API responses keep the skeleton visible long
// enough to inspect table shape, existing rows, and cursor parameters before
// completing each page.

// Hoisted API and viewport controls let each scenario defer responses and select
// desktop or narrow layout before mounting.
const { listAlertEvents, viewport } = vi.hoisted(() => ({
  listAlertEvents: vi.fn(),
  viewport: { desktop: true },
}))

vi.mock('@/api/admin/ops', () => ({ opsAPI: { listAlertEvents } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('@vueuse/core', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@vueuse/core')>()),
  useMediaQuery: () => ref(viewport.desktop),
}))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

// event builds a firing alert with a distinct identifier and shared timestamps.
// Callers choose IDs to test cursor continuation when multiple events have the
// same recorded time.
function event(id: number): AlertEvent {
  return {
    id,
    rule_id: 1,
    title: `告警 ${id}`,
    severity: 'P1',
    status: 'firing',
    fired_at: '2026-09-30T00:00:00Z',
    created_at: '2026-09-30T00:00:00Z',
    email_sent: false,
  }
}

// mountCard renders the real alert list with selector and dialog chrome stubbed.
function mountCard() {
  return mount(OpsAlertEventsCard, {
    global: {
      stubs: {
        Select: true,
        Icon: true,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      },
    },
  })
}

describe('告警事件加载骨架', () => {
  beforeEach(() => listAlertEvents.mockReset())

  // Initial loading and cursor append are delayed in desktop and narrow layouts.
  // The component must show the appropriate skeleton structure and keep earlier
  // alert rows visible during append.
  it.each([true, false])('桌面布局为 %s 时，首屏与追加加载保留各自的内容结构', async (desktop) => {
    viewport.desktop = desktop
    let finish!: (rows: AlertEvent[]) => void
    listAlertEvents.mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve
      }),
    )
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('admin.ops.alertEvents.empty')
    if (desktop) {
      expect(wrapper.findAll('thead th')).toHaveLength(8)
      expect(wrapper.findAll('tbody tr')[0].findAll('td')).toHaveLength(8)
    } else {
      expect(wrapper.find('table').exists()).toBe(false)
    }

    finish(Array.from({ length: 10 }, (_, index) => event(index + 1)))
    await flushPromises()
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.text()).toContain('告警 1')

    // Keep the append request pending so assertions can distinguish retained
    // alert rows from the loading skeleton at the list tail.
    listAlertEvents.mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve
      }),
    )
    await wrapper.get('.overflow-y-auto').trigger('scroll')
    expect(listAlertEvents).toHaveBeenLastCalledWith(expect.objectContaining({ before_id: 10 }))
    expect(wrapper.text()).toContain('告警 1')
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(true)
    if (desktop) expect(wrapper.findAll('tbody')[0].findAll('tr')).toHaveLength(10)

    finish([event(11)])
    await flushPromises()
    expect(wrapper.text()).toContain('告警 11')
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    wrapper.unmount()
  })
})
