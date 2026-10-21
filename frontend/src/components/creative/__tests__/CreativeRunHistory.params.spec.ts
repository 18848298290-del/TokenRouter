import { ref } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import CreativeRunHistory from '@/components/creative/CreativeRunHistory.vue'
import type { CreativeRun } from '@/api/creative'
import type { LocalRunParams } from '@/utils/creativeLocalStore'

/**
 * CreativeRunHistory.params.spec.ts checks submitted parameter presentation and
 * reuse through the mounted history panel. One fixture combines server task
 * fields with a browser-local prompt snapshot; another omits local parameters.
 * The assertions cover summary-to-detail transition, ordered parameter tags,
 * clipboard delegation, and reuse availability when local input is missing.
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

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number | null | undefined) => String(value ?? ''),
  }),
}))

/**
 * copyToClipboard records prompt-copy requests so the test can verify exact
 * text and success-message delegation from the panel.
 */
const copyToClipboard = vi.fn()
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard }),
}))

/**
 * run supplies server-side edit metadata used when local submission parameters
 * are either available or missing.
 */
const run: CreativeRun = {
  id: 'crun_params',
  status: 'failed',
  operation: 'edit',
  model: 'gpt-image-2',
  group_id: '1',
  requested_output_count: 2,
  image_size: '2K',
  aspect_ratio: '16:9',
  created_at: Date.now(),
  outputs: [],
}

/**
 * params supplies a complete local prompt snapshot whose optional values
 * determine the expanded parameter tags and reuse action.
 */
const params: LocalRunParams = {
  runId: run.id,
  optionKey: '1::gpt-image-2',
  groupName: 'Group A',
  operation: 'edit',
  prompt: '一只在窗边看书的橘猫',
  imageSize: '2K',
  aspectRatio: '16:9',
  quality: 'high',
  background: '',
  thinkingLevel: '',
  referenceCount: 1,
  createdAt: 1,
}

/**
 * createStudio builds reactive history state using the caller's local parameter
 * map. Mocked actions allow assertions on reuse and refresh delegation without
 * submitting requests or accessing browser persistence.
 */
function createStudio(runParams: Map<string, LocalRunParams>) {
  return {
    runHistory: ref([run]),
    currentRun: ref(null),
    loadingHistory: ref(false),
    outputAssetMap: ref(new Map()),
    runParamsMap: ref(runParams),
    refreshHistory: vi.fn(),
    importOutputToCanvas: vi.fn(),
    applyRunParams: vi.fn(() => true),
  }
}

/**
 * mountExpanded opens the mounted history sidebar and returns its failed-run
 * row button. The caller expands that row and owns unmounting after assertions,
 * keeping panel lifecycle cleanup in the scenario.
 */
async function mountExpanded(studio: ReturnType<typeof createStudio>) {
  const wrapper = mount(CreativeRunHistory, {
    props: { studio },
    global: { stubs: { Icon: true } },
  })
  await wrapper.get('button[aria-expanded="false"]').trigger('click')
  const rowButton = wrapper
    .findAll('button')
    .find((button) => button.text().includes('creative.status.failed'))
  expect(rowButton).toBeDefined()
  return { wrapper, rowButton: rowButton! }
}

describe('CreativeRunHistory 提交参数', () => {
  /**
   * A failed edit run has a complete local submission snapshot. Expanding it must
   * replace the summary with full prompt details and delegate copy and reuse
   * using that saved identity.
   */
  it('折叠行显示提示词摘要，展开后显示完整提示词和参数标签', async () => {
    const studio = createStudio(new Map([[run.id, params]]))
    const { wrapper, rowButton } = await mountExpanded(studio)

    expect(rowButton.text()).toContain(params.prompt)
    await rowButton.trigger('click')

    expect(rowButton.text()).not.toContain(params.prompt)
    const tags = wrapper.findAll('.history-param-tag').map((tag) => tag.text())
    expect(tags).toEqual([
      'creative.operations.edit',
      'creative.history.params.group Group A',
      'creative.history.params.size 2K',
      'creative.history.params.ratio 16:9',
      'creative.panel.quality creative.qualities.high',
      'creative.history.params.references 1',
      'creative.history.params.count 2',
    ])

    await wrapper.get('button[aria-label="creative.history.copyPrompt"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith(params.prompt, 'creative.history.promptCopied')
    await wrapper.get('button[aria-label="creative.history.reuseParams"]').trigger('click')
    expect(studio.applyRunParams).toHaveBeenCalledWith(run.id)
    wrapper.unmount()
  })

  /**
   * A history run has server metadata but no browser-local parameter snapshot.
   * Its details must show prompt-unavailable guidance, omit reuse, and retain
   * tags supported by server fields.
   */
  it('本机没有参数记录时显示说明，参数标签使用服务端返回的字段', async () => {
    const studio = createStudio(new Map())
    const { wrapper, rowButton } = await mountExpanded(studio)
    await rowButton.trigger('click')

    expect(wrapper.text()).toContain('creative.history.promptUnavailable')
    expect(wrapper.find('button[aria-label="creative.history.reuseParams"]').exists()).toBe(false)
    const tags = wrapper.findAll('.history-param-tag').map((tag) => tag.text())
    expect(tags).toEqual([
      'creative.operations.edit',
      'creative.history.params.size 2K',
      'creative.history.params.ratio 16:9',
      'creative.history.params.count 2',
    ])
    wrapper.unmount()
  })
})
