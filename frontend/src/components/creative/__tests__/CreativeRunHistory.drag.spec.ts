import { nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CreativeRunHistory from '@/components/creative/CreativeRunHistory.vue'
import { CREATIVE_OUTPUT_DRAG_MIME } from '@/utils/creativeDrag'
import { outputAssetKey, type LocalAsset } from '@/utils/creativeLocalStore'

/**
 * CreativeRunHistory.drag.spec.ts checks the mounted history panel's output
 * drag payload, active-task badge, and elapsed-time display. Local Blob records
 * and studio actions are controlled fixtures, while DOM events exercise the
 * real panel handlers. A fake clock advances active duration labels and is
 * restored after the timed scenario.
 */

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number | null | undefined) => String(value ?? ''),
  }),
}))

/**
 * run supplies a successful server record with one output so thumbnail actions
 * can resolve its stable local asset identity.
 */
const run = {
  id: 'crun_0123456789abcdef',
  status: 'succeeded' as const,
  model: 'gpt-image-2',
  created_at: Date.now(),
  outputs: [{ output_index: 0, status: 'succeeded' as const, mime_type: 'image/png' }],
}

/**
 * createStudio builds reactive history state around one locally available
 * output fixture.
 */
function createStudio(asset: LocalAsset) {
  return {
    runHistory: ref([run]),
    currentRun: ref(null),
    loadingHistory: ref(false),
    outputAssetMap: ref(new Map([[asset.key, asset]])),
    runParamsMap: ref(new Map()),
    refreshHistory: vi.fn(),
    importOutputToCanvas: vi.fn(),
    applyRunParams: vi.fn(),
  }
}

describe('CreativeRunHistory 拖放', () => {
  beforeEach(() => {
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:history-test'),
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn(),
    })
  })

  /**
   * A locally stored output is expanded and dragged from its thumbnail. The
   * transfer must advertise copying and carry the run identifier and output index
   * under the private MIME type.
   */
  it('为本地历史缩略图写入受校验的拖放载荷', async () => {
    const asset: LocalAsset = {
      key: outputAssetKey(run.id, 0),
      kind: 'output',
      blob: new Blob(['image'], { type: 'image/png' }),
      runId: run.id,
      outputIndex: 0,
      createdAt: Date.now(),
    }
    const wrapper = mount(CreativeRunHistory, {
      props: { studio: createStudio(asset) },
      global: { stubs: { Icon: true } },
    })

    await wrapper.get('button[aria-expanded="false"]').trigger('click')
    const rowButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('creative.status.succeeded'))
    expect(rowButton).toBeDefined()
    await rowButton!.trigger('click')

    const transfer = { effectAllowed: '', setData: vi.fn() }
    const event = new Event('dragstart', { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'dataTransfer', { value: transfer })
    wrapper.get('img').element.dispatchEvent(event)

    expect(transfer.effectAllowed).toBe('copy')
    expect(transfer.setData).toHaveBeenCalledWith(
      CREATIVE_OUTPUT_DRAG_MIME,
      JSON.stringify({ runId: run.id, outputIndex: 0 }),
    )
    wrapper.unmount()
  })

  /**
   * The parent supplies one active task to a collapsed history panel. The entry
   * button must display that count so ongoing work remains visible before opening
   * the sidebar.
   */
  it('在历史入口显示活动任务数量', () => {
    const asset: LocalAsset = {
      key: outputAssetKey(run.id, 0),
      kind: 'output',
      blob: new Blob(['image'], { type: 'image/png' }),
      runId: run.id,
      outputIndex: 0,
      createdAt: Date.now(),
    }
    const studio = createStudio(asset)
    const wrapper = mount(CreativeRunHistory, {
      props: { studio, activeRunCount: 1 },
      global: { stubs: { Icon: true } },
    })

    const badge = wrapper.find('button[aria-expanded="false"] span')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('1')
    wrapper.unmount()
  })

  /**
   * An active run starts sixty-five seconds before the controlled browser clock.
   * Both its row and expanded details must advance their elapsed labels when the
   * timer moves forward.
   */
  it('在活动任务详情中显示并更新生成耗时', async () => {
    vi.useFakeTimers()
    const now = new Date('2026-08-31T00:01:05.000Z')
    vi.setSystemTime(now)
    const startedAt = now.getTime() - 65_000
    const asset: LocalAsset = {
      key: outputAssetKey(run.id, 0),
      kind: 'output',
      blob: new Blob(['image'], { type: 'image/png' }),
      runId: run.id,
      outputIndex: 0,
      createdAt: startedAt,
    }
    const activeRun = {
      ...run,
      id: 'crun_active_timer',
      status: 'running' as const,
      created_at: startedAt,
      started_at: startedAt,
      outputs: [],
    }
    const studio = createStudio(asset)
    studio.runHistory.value = [activeRun]
    const wrapper = mount(CreativeRunHistory, {
      props: { studio },
      global: { stubs: { Icon: true } },
    })

    await wrapper.get('button[aria-expanded="false"]').trigger('click')
    const rowButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('creative.status.running'))
    expect(rowButton).toBeDefined()
    expect(rowButton!.text()).toContain('01:05')
    await rowButton!.trigger('click')

    expect(wrapper.get('[data-testid="creative-run-elapsed"]').text()).toBe('01:05')
    vi.advanceTimersByTime(2_000)
    await nextTick()
    expect(wrapper.get('[data-testid="creative-run-elapsed"]').text()).toBe('01:07')
    expect(rowButton!.text()).toContain('01:07')

    wrapper.unmount()
    vi.useRealTimers()
  })
})
