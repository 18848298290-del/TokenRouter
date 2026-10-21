import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { DOMWrapper, VueWrapper } from '@vue/test-utils'

import RiskControlView from '../RiskControlView.vue'
import type {
  ContentModerationConfig,
  ContentModerationCyberWarning,
  ContentModerationLog,
  UpdateContentModerationConfig,
} from '@/api/admin/riskControl'

/**
 * RiskControlView.spec.ts exercises administrator risk settings and audit record
 * presentation through the mounted page. Network substitutes expose submitted
 * requests, while dialog and input stubs preserve the interactions needed to
 * verify credential precedence, validation order, detail cleanup, and runtime
 * card layout across moderation modes.
 */

// Hoisted service spies capture page requests and notifications while each test
// supplies its own response data.
const {
  getConfig,
  updateConfig,
  getStatus,
  listLogs,
  getLog,
  listCyberWarnings,
  getCyberWarning,
  getMediaContent,
  getCyberSummary,
  getGroups,
  getProxies,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
  getStatus: vi.fn(),
  listLogs: vi.fn(),
  getLog: vi.fn(),
  listCyberWarnings: vi.fn(),
  getCyberWarning: vi.fn(),
  getMediaContent: vi.fn(),
  getCyberSummary: vi.fn(),
  getGroups: vi.fn(),
  getProxies: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    riskControl: {
      getConfig,
      updateConfig,
      getStatus,
      listLogs,
      getLog,
      listCyberWarnings,
      getCyberWarning,
      getMediaContent,
      getCyberSummary,
      testAPIKeys: vi.fn(),
      deleteFlaggedHash: vi.fn(),
      clearFlaggedHashes: vi.fn(),
      unbanUser: vi.fn(),
    },
    groups: {
      getAll: getGroups,
    },
    proxies: {
      getAll: getProxies,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  // useAppStore supplies notification spies observed by the page's save and load tests.
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/utils/apiError', () => ({
  // extractApiErrorMessage returns the caller's fallback text for every mocked error.
  // The error object is ignored so page tests observe the fallback message selected
  // by the caller.
  extractApiErrorMessage: (_err: unknown, fallback: string) => fallback,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    // useI18n exposes the deterministic translation helper used by mounted page assertions.
    useI18n: () => ({
      // t renders the two fixture-specific messages and otherwise interpolates key text.
      // Optional parameters supply named values; missing values keep their brace
      // placeholders in the general interpolation path.
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.riskControl.preBlockAPIKeyLoadSummary') {
          return `同步并发 ${params?.active} / 可用 Key ${params?.available}，累计 ${params?.total} 次，worker：${params?.workerActive} / ${params?.workerTotal}`
        }
        if (key === 'admin.riskControl.teamAttribution') {
          return `团队 ${params?.teamId} · 付款 UID ${params?.billingUserId}`
        }
        return key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`))
      },
    }),
  }
})

/** baseConfig returns a complete saved moderation configuration for page initialization. */
const baseConfig = (): ContentModerationConfig => ({
  enabled: true,
  mode: 'pre_block',
  base_url: 'https://api.openai.com',
  model: 'omni-moderation-latest',
  proxy_id: null,
  api_key_configured: false,
  api_key_masked: '',
  api_key_count: 0,
  api_key_masks: [],
  api_key_statuses: [],
  timeout_ms: 3000,
  sample_rate: 100,
  all_groups: true,
  group_ids: [],
  record_non_hits: false,
  worker_count: 4,
  queue_size: 32768,
  block_status: 403,
  block_message: '内容审计命中风险规则，请调整输入后重试',
  email_on_hit: true,
  auto_ban_enabled: true,
  ban_threshold: 10,
  violation_window_hours: 720,
  retry_count: 2,
  hit_retention_days: 180,
  non_hit_retention_days: 3,
  pre_hash_check_enabled: false,
  cyber_warning_enabled: true,
  cyber_auto_ban_enabled: false,
  cyber_ban_threshold: 10,
  cyber_violation_window_hours: 720,
  blocked_keywords: [],
  keyword_blocking_mode: 'keyword_and_api',
  thresholds: {
    harassment: 0.98,
    sexual: 0.65,
  },
  model_filter: {
    type: 'all',
    models: [],
  },
  audit_user_text_max_chars: 1000000,
  audit_images: true,
  audit_tool_outputs: true,
  audit_tool_output_max_chars: 1000000,
})

/** runtimeStatus returns idle runtime metrics with an empty credential pool. */
const runtimeStatus = () => ({
  enabled: true,
  risk_control_enabled: true,
  mode: 'pre_block',
  worker_count: 4,
  max_workers: 32,
  active_workers: 0,
  idle_workers: 4,
  queue_size: 32768,
  queue_length: 0,
  queue_usage_percent: 0,
  enqueued: 0,
  dropped: 0,
  processed: 0,
  errors: 0,
  pre_block_active: 0,
  pre_block_checked: 0,
  pre_block_allowed: 0,
  pre_block_blocked: 0,
  pre_block_errors: 0,
  pre_block_avg_latency_ms: 0,
  pre_block_api_key_active: 0,
  pre_block_api_key_available_count: 0,
  pre_block_api_key_total_calls: 0,
  pre_block_api_key_loads: [],
  api_key_statuses: [],
  flagged_hash_count: 0,
  last_cleanup_deleted_hit: 0,
  last_cleanup_deleted_non_hit: 0,
})

// Layout and control substitutes retain slots, visibility, and model editing so page
// tests exercise the actual event handlers.
const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = defineComponent({
  props: {
    // show controls slot mounting, matching the detail and settings dialogs used by
    // the page under test.
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})
const ModelWhitelistSelectorStub = defineComponent({
  props: {
    // modelValue contains the selected model names; the input emits a replacement
    // array after each textual edit.
    modelValue: {
      type: Array,
      default: () => [],
    },
  },
  // update:modelValue supplies the selected model array consumed by the real page's
  // model-filter draft binding.
  emits: ['update:modelValue'],
  /**
   * setup renders an editable substitute for the model whitelist selector.
   * It receives the selected names through props and emits parsed replacement
   * names through the component's model update event.
   */
  setup(props, { emit }) {
    /** onInput emits trimmed model names parsed from comma or newline separators. */
    const onInput = (event: Event) => {
      const value = (event.target as HTMLInputElement).value
      emit(
        'update:modelValue',
        value
          .split(/[,\n]/)
          .map((item) => item.trim())
          .filter(Boolean),
      )
    }
    return () =>
      h('input', {
        'data-test': 'model-filter-input',
        value: (props.modelValue as string[]).join('\n'),
        onInput,
      })
  },
})

/**
 * findButtonByText finds the first button containing the requested visible text.
 * A missing match throws immediately so interaction tests fail at the intended
 * action rather than continuing with another control.
 */
function findButtonByText(wrapper: VueWrapper, text: string): DOMWrapper<HTMLButtonElement> {
  const button = wrapper
    .findAll<HTMLButtonElement>('button')
    .find((item) => item.text().includes(text))
  if (!button) {
    throw new Error(`button not found: ${text}`)
  }
  return button
}

/**
 * mountRiskControl renders the page with network mocks and lightweight controls.
 * Callers await initialization before arranging drafts and unmount the returned
 * wrapper after assertions to release page timers and details.
 */
function mountRiskControl() {
  return mount(RiskControlView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        BaseDialog: BaseDialogStub,
        Icon: true,
        Select: true,
        Toggle: true,
        Pagination: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
        ProxySelector: true,
      },
    },
  })
}

// RiskControlSetup exposes page actions used to arrange competing draft operations.
interface RiskControlSetup {
  // configForm is the mounted component draft; tests arrange conflicting operations
  // directly before invoking the actual save action.
  configForm: Record<string, unknown> // Mutable settings form draft.
  // pendingDeleteApiKeyHashes belongs to the mounted page; tests add entries to
  // exercise deletion precedence against metadata and replacement.
  pendingDeleteApiKeyHashes: string[] // Queued stored credential removals.
  // apiKeyMetadataDrafts maps stored hashes to mutable edits, allowing tests to
  // verify that removed hashes omit metadata updates.
  apiKeyMetadataDrafts: Record<
    string,
    {
      priority: number // Arranged credential priority input.
      note: string // Arranged credential note input.
    }
  > // Unsaved stored credential metadata.
  /**
   * saveConfig validates the arranged draft before invoking the mocked persistence service.
   * Validation failures report an error and skip persistence; successful saves apply
   * the response before closing settings and refreshing runtime records.
   */
  saveConfig: () => Promise<void> // Persist current validated draft.
  /**
   * openInputDetail selects a moderation row and loads its complete record and media.
   * Tests await completion to observe published detail state, while a later open
   * or close invalidates the pending response through the shared generation.
   */
  openInputDetail: (row: ContentModerationLog) => Promise<void> // Load selected moderation record.
  /**
   * closeInputDetail invalidates pending detail requests and clears the moderation row.
   * Shared media URLs are released before the row disappears, and the moderation
   * loading flag resets for the closed dialog.
   */
  closeInputDetail: () => void // Close moderation detail dialog.
}

/**
 * pageSetup exposes the mounted page's live draft and action bindings for tests.
 * Callers arrange competing operations through these bindings, then exercise
 * the page's actual save or detail lifecycle handlers.
 */
function pageSetup(wrapper: VueWrapper): RiskControlSetup {
  return wrapper.vm.$.setupState as unknown as RiskControlSetup
}

describe('admin RiskControlView', () => {
  beforeEach(() => {
    getConfig.mockReset()
    updateConfig.mockReset()
    getStatus.mockReset()
    listLogs.mockReset()
    getLog.mockReset()
    listCyberWarnings.mockReset()
    getCyberWarning.mockReset()
    getMediaContent.mockReset()
    getCyberSummary.mockReset()
    getGroups.mockReset()
    getProxies.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    getConfig.mockResolvedValue(baseConfig())
    getStatus.mockResolvedValue(runtimeStatus())
    listLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    getMediaContent.mockResolvedValue(new Blob(['image']))
    listCyberWarnings.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    getCyberSummary.mockResolvedValue({
      events: 0,
      requests: 0,
      users: 0,
      providers: 0,
      by_user: [],
      by_provider: [],
    })
    getGroups.mockResolvedValue([])
    getProxies.mockResolvedValue([])
    updateConfig.mockImplementation(async (payload: UpdateContentModerationConfig) => ({
      ...baseConfig(),
      ...payload,
      model_filter: payload.model_filter ?? baseConfig().model_filter,
      api_key_configured: false,
      api_key_masked: '',
      api_key_count: 0,
      api_key_masks: [],
      api_key_statuses: [],
    }))
  })

  // Competing credential edits can coexist while a form is being prepared.
  // Submission chooses clear, replacement, deletion, and metadata fields in the
  // documented order.
  it.each([
    { clear: true, mode: 'append', text: '', entries: false, deletes: false, updates: false },
    {
      clear: true,
      mode: 'append',
      text: 'sk-new | 20 | note',
      entries: true,
      deletes: true,
      updates: true,
    },
    {
      clear: false,
      mode: 'replace',
      text: 'sk-new',
      entries: true,
      deletes: false,
      updates: false,
    },
    { clear: false, mode: 'append', text: '', entries: false, deletes: true, updates: true },
  ])('chooses credential operations for $mode clear=$clear input=$text', async (scenario) => {
    const wrapper = mountRiskControl()
    await flushPromises()
    const state = pageSetup(wrapper)
    Object.assign(state.configForm, {
      clear_api_key: scenario.clear,
      api_keys_mode: scenario.mode,
      api_keys_text: scenario.text,
    })
    state.pendingDeleteApiKeyHashes.push('removed')
    state.apiKeyMetadataDrafts.removed = { priority: 10, note: 'discard' }
    state.apiKeyMetadataDrafts.kept = { priority: 0, note: '  keep  ' }
    await state.saveConfig()
    const payload = updateConfig.mock.calls[0][0]
    expect(payload.clear_api_key).toBe(scenario.clear && !scenario.entries)
    expect(Boolean(payload.api_key_entries)).toBe(scenario.entries)
    expect(payload.delete_api_key_hashes).toEqual(scenario.deletes ? ['removed'] : undefined)
    expect(payload.api_key_updates).toEqual(
      scenario.updates ? [{ key_hash: 'kept', priority: 100, note: 'keep' }] : undefined,
    )
    wrapper.unmount()
  })

  // Empty model selection and empty replacement credentials are independently invalid drafts.
  // The model error appears first and neither attempt sends an update request.
  it('validates model selection before replacement credentials', async () => {
    const wrapper = mountRiskControl()
    await flushPromises()
    const state = pageSetup(wrapper)
    Object.assign(state.configForm, {
      model_filter_type: 'include',
      model_filter_models: [],
      api_keys_mode: 'replace',
    })
    await state.saveConfig()
    expect(showError).toHaveBeenLastCalledWith('admin.riskControl.modelFilterModelsRequired')
    state.configForm.model_filter_type = 'all'
    await state.saveConfig()
    expect(showError).toHaveBeenLastCalledWith('admin.riskControl.apiKeysReplaceNoInput')
    expect(updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // A detail response can arrive after the administrator closes its dialog.
  // Closing suppresses both the late content and any subsequent media content request.
  it('ignores a detail response that arrives after close', async () => {
    const deferred = Promise.withResolvers<ContentModerationLog>()
    getLog.mockReturnValueOnce(deferred.promise)
    const wrapper = mountRiskControl()
    await flushPromises()
    const state = pageSetup(wrapper)
    const pending = state.openInputDetail({ id: 9 } as ContentModerationLog)
    state.closeInputDetail()
    deferred.resolve({
      id: 9,
      input_excerpt: 'late-content',
      media: [{ id: 77, snapshot_status: 'ready' }],
    } as ContentModerationLog)
    await pending
    expect(wrapper.text()).not.toContain('late-content')
    expect(getMediaContent).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // Media loading may finish after the owning page has already unmounted.
  // The newly created object URL is revoked and cannot survive in an abandoned detail view.
  it('releases media that finishes after unmount', async () => {
    const deferred = Promise.withResolvers<Blob>()
    getLog.mockResolvedValueOnce({ id: 9, media: [{ id: 77, snapshot_status: 'ready' }] })
    getMediaContent.mockReturnValueOnce(deferred.promise)
    const createObjectURL = vi.fn(() => 'blob:late-image')
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })
    const wrapper = mountRiskControl()
    await flushPromises()
    const pending = pageSetup(wrapper).openInputDetail({ id: 9 } as ContentModerationLog)
    await flushPromises()
    wrapper.unmount()
    deferred.resolve(new Blob(['image']))
    await pending
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledExactlyOnceWith('blob:late-image')
  })

  // Moderation records include distinct standard, keyword, and hash blocking decisions. The
  // result selector keeps separate option values and sends the selected hash-block filter to
  // the API.
  it('separates standard, keyword, and hash block filters', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })
    await flushPromises()

    const resultSelect = wrapper.findAllComponents({ name: 'Select' })[0]
    const options = resultSelect.props('options') as Array<{
      value: string // Backend result filter value.
      label: string // Localized result filter label.
    }>
    // Each result option maps to one backend action so selecting hash blocks does
    // not request ordinary or keyword block rows.
    expect(options.map((option) => option.value)).toEqual([
      '',
      'hit',
      'block',
      'keyword_block',
      'hash_block',
      'pass',
      'error',
    ])

    listLogs.mockClear()
    await resultSelect.vm.$emit('update:modelValue', 'hash_block')
    await resultSelect.vm.$emit('change')
    await flushPromises()

    expect(listLogs).toHaveBeenLastCalledWith(expect.objectContaining({ result: 'hash_block' }))
  })

  // A keyword block can originate from a team-funded request. The displayed row retains the
  // matched keyword, acting user context, team identifier, and separate billing owner
  // attribution.
  it('renders matched keyword and team attribution for keyword-block audit logs', async () => {
    const log: ContentModerationLog = {
      id: 1,
      request_id: 'req-keyword',
      user_id: 1001,
      user_email: 'user@example.com',
      billing_user_id: 9001,
      team_id: 8001,
      api_key_id: 2001,
      api_key_name: 'default-key',
      group_id: 3001,
      group_name: 'default',
      endpoint: '/v1/messages',
      provider: 'anthropic',
      model: 'claude-sonnet-4',
      mode: 'pre_block',
      action: 'keyword_block',
      flagged: true,
      highest_category: 'keyword',
      highest_score: 1,
      matched_keyword: 'secret-token',
      category_scores: { keyword: 1 },
      threshold_snapshot: { keyword: 1 },
      input_excerpt: 'please leak SECRET-TOKEN now',
      upstream_latency_ms: null,
      error: '',
      violation_count: 1,
      auto_banned: false,
      email_sent: false,
      user_status: 'active',
      queue_delay_ms: null,
      created_at: '2026-01-02T03:04:05Z',
    }
    listLogs.mockResolvedValue({ items: [log], total: 1, page: 1, page_size: 20, pages: 1 })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.matchedKeyword: secret-token')
    expect(wrapper.get('[data-test="moderation-team-attribution"]').text()).toBe(
      '团队 8001 · 付款 UID 9001',
    )
  })

  // Cyber warnings include team attribution in both list and detail responses. Opening the
  // detail uses its returned team and billing user rather than retaining stale list
  // attribution.
  it('renders cyber team attribution in the list and detail dialog', async () => {
    const warning: ContentModerationCyberWarning = {
      id: 11,
      request_id: 'req-cyber-team',
      user_id: 1101,
      user_email: 'member@example.com',
      billing_user_id: 9101,
      team_id: 8101,
      api_key_id: 2101,
      api_key_name: 'team-key',
      group_id: 3101,
      group_name: 'openai',
      provider_id: 4101,
      provider_name: 'upstream-provider',
      endpoint: '/v1/responses',
      model: 'gpt-5',
      upstream_status: 400,
      warning_text: 'cyber_policy',
      prompt_excerpt: 'blocked prompt',
      violation_count: 1,
      auto_banned: false,
      email_sent: true,
      user_status: 'active',
      created_at: '2026-01-02T03:04:05Z',
    }
    listCyberWarnings.mockResolvedValue({
      items: [warning],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    getCyberWarning.mockResolvedValue({
      ...warning,
      billing_user_id: 9202,
      team_id: 8202,
      content_complete: true,
      audit_complete: true,
      input_items: [],
      media: [],
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.recordTabs.cyber').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="cyber-team-attribution"]').text()).toBe(
      '团队 8101 · 付款 UID 9101',
    )

    await wrapper.get('[data-test="cyber-detail-button"]').trigger('click')
    await flushPromises()

    expect(getCyberWarning).toHaveBeenCalledWith(11)
    expect(wrapper.get('[data-test="cyber-detail-team-attribution"]').text()).toBe(
      '团队 8202 · 付款 UID 9202',
    )
  })

  // A hash-blocked record is flagged but represents a blocked action. Its visible badge uses
  // the blocked label and red styling rather than the ordinary hit presentation.
  it('renders hash blocks with a red blocked badge instead of a hit badge', async () => {
    const log: ContentModerationLog = {
      id: 2,
      request_id: 'req-hash',
      user_id: 1002,
      user_email: 'hash@example.com',
      api_key_id: 2002,
      api_key_name: 'hash-key',
      group_id: 3002,
      group_name: 'default',
      endpoint: '/v1/responses',
      provider: 'openai',
      model: 'gpt-5',
      mode: 'pre_block',
      action: 'hash_block',
      flagged: true,
      highest_category: 'hash',
      highest_score: 1,
      matched_keyword: '',
      category_scores: {},
      threshold_snapshot: {},
      input_excerpt: 'blocked by hash',
      upstream_latency_ms: null,
      error: '',
      violation_count: 0,
      auto_banned: false,
      email_sent: false,
      user_status: 'active',
      queue_delay_ms: null,
      created_at: '2026-01-02T03:04:05Z',
    }
    listLogs.mockResolvedValue({ items: [log], total: 1, page: 1, page_size: 20, pages: 1 })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    const badge = wrapper
      .findAll('span')
      .find((node) => node.text() === 'admin.riskControl.action.hashBlock')
    expect(badge).toBeDefined()
    expect(badge?.classes()).toEqual(expect.arrayContaining(['bg-red-100', 'text-red-700']))
    expect(wrapper.text()).not.toContain('admin.riskControl.result.hit')
  })

  // A review row initially contains an excerpt and receives full content on demand. Its
  // authenticated image preview displays after loading and releases its Blob URL when the
  // dialog closes.
  it('loads full review content and releases media blob URLs when closed', async () => {
    const log: ContentModerationLog = {
      id: 9,
      request_id: 'req-review',
      user_id: 1001,
      user_email: 'user@example.com',
      billing_user_id: 9001,
      team_id: 8001,
      api_key_id: 2001,
      api_key_name: 'default-key',
      group_id: 3001,
      group_name: 'default',
      endpoint: '/v1/responses',
      provider: 'openai',
      model: 'gpt-5.5',
      mode: 'pre_block',
      action: 'block',
      flagged: true,
      highest_category: 'sexual',
      highest_score: 0.9,
      matched_keyword: '',
      category_scores: { sexual: 0.9 },
      threshold_snapshot: { sexual: 0.65 },
      input_excerpt: 'short excerpt',
      upstream_latency_ms: 20,
      error: '',
      violation_count: 1,
      auto_banned: false,
      email_sent: false,
      user_status: 'active',
      queue_delay_ms: null,
      created_at: '2026-01-02T03:04:05Z',
    }
    const detail: ContentModerationLog = {
      ...log,
      content_complete: true,
      audit_complete: true,
      input_items: [
        { index: 0, source: 'tool', type: 'text', text: 'complete tool output with secret' },
      ],
      media: [
        {
          id: 77,
          source_index: 1,
          source: 'tool',
          mime_type: 'image/png',
          sha256: 'a'.repeat(64),
          byte_size: 5,
          original_ref: 'data:image/png;base64,aW1hZ2U=',
          snapshot_status: 'ready',
          snapshot_error: '',
          created_at: '2026-01-02T03:04:05Z',
        },
      ],
    }
    listLogs.mockResolvedValue({ items: [log], total: 1, page: 1, page_size: 20, pages: 1 })
    getLog.mockResolvedValue(detail)
    const createObjectURL = vi.fn(() => 'blob:review-image')
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-test="moderation-detail-button"]').trigger('click')
    await flushPromises()

    expect(getLog).toHaveBeenCalledWith(9)
    expect(getMediaContent).toHaveBeenCalledWith(77)
    expect(wrapper.text()).toContain('complete tool output with secret')
    expect(wrapper.get('[data-test="moderation-detail-team-attribution"]').text()).toBe(
      '团队 8001 · 付款 UID 9001',
    )
    expect(wrapper.get('img').attributes('src')).toBe('blob:review-image')

    await findButtonByText(wrapper, 'common.close').trigger('click')
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:review-image')
  })

  // The administrator restricts moderation to selected model names through the scope tab.
  // Saving sends include mode with those names and the zero sentinel for direct proxy access.
  it('saves the selected model filter mode and models', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.modelFilterInclude').trigger('click')
    await wrapper.get('[data-test="model-filter-input"]').setValue('gpt-5.5, gpt-5.4')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        proxy_id: 0,
        model_filter: {
          type: 'include',
          models: ['gpt-5.5', 'gpt-5.4'],
        },
      }),
    )
    expect(getProxies).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()
  })

  // The scope tab exposes separate user-text and tool-output character limits. Edited values
  // reach the configuration request together with the existing image and tool audit switches.
  it('saves upstream audit content limits from the existing scope tab', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')

    expect(wrapper.get('[data-test="audit-content-scope"]').text()).toContain(
      'admin.riskControl.auditContentScope',
    )
    await wrapper.get('[data-test="audit-user-text-max-chars"]').setValue('8000')
    await wrapper.get('[data-test="audit-tool-output-max-chars"]').setValue('2000')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        audit_user_text_max_chars: 8000,
        audit_images: true,
        audit_tool_outputs: true,
        audit_tool_output_max_chars: 2000,
      }),
    )
  })

  // A configured credential has editable priority and annotation separate from its health
  // status. Saving sends the edited metadata under its stable stored credential hash.
  it('saves priority and note edits for a stored moderation key', async () => {
    const keyHash = 'a'.repeat(64)
    const keyStatus = {
      index: 0,
      key_hash: keyHash,
      masked: 'sk-...main',
      status: 'ok' as const,
      failure_count: 0,
      success_count: 10,
      last_error: '',
      last_latency_ms: 80,
      last_http_status: 200,
      last_tested: true,
      configured: true,
      priority: 100,
      note: 'old note',
    }
    getConfig.mockResolvedValue({
      ...baseConfig(),
      api_key_configured: true,
      api_key_count: 1,
      api_key_masks: ['sk-...main'],
      api_key_statuses: [keyStatus],
    })
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      api_key_statuses: [keyStatus],
    })
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await wrapper.get('[data-test="api-key-priority"]').setValue('20')
    await wrapper.get('[data-test="api-key-note"]').setValue('Tier 1 backup')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        api_key_updates: [{ key_hash: keyHash, priority: 20, note: 'Tier 1 backup' }],
      }),
    )
  })

  // Threshold number inputs represent category values as percentages in settings.
  // Saving converts the edited harassment and sexual values to the corresponding
  // request ratios.
  it('submits edited risk control thresholds when saving moderation config', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    await wrapper.get('[data-test="risk-threshold-sexual"]').setValue('72')
    await wrapper.get('[data-test="risk-threshold-harassment"]').setValue('99')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        thresholds: expect.objectContaining({
          sexual: 0.72,
          harassment: 0.99,
        }),
      }),
    )
    expect(showError).not.toHaveBeenCalled()
  })

  // Observe mode processes audit work asynchronously and exposes queue utilization. The page
  // shows its worker runtime card, record count, and queue capacity in the expected mode.
  it('describes worker runtime as async audit and pre-block record processing', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      mode: 'observe',
      processed: 12,
      queue_length: 2,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.workerStatusHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('12')
    expect(wrapper.text()).toContain('2 / 32,768')
  })

  // Pre-block mode reports synchronous decisions and per-credential call load. Its metric card
  // and credential card remain siblings in the responsive grid with independently bounded
  // credential scrolling.
  it('shows pre-block synchronous moderation metrics separately from worker queue', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      pre_block_active: 2,
      pre_block_checked: 128,
      pre_block_allowed: 120,
      pre_block_blocked: 8,
      pre_block_errors: 1,
      pre_block_avg_latency_ms: 86,
      pre_block_api_key_active: 2,
      pre_block_api_key_available_count: 2,
      pre_block_api_key_total_calls: 128,
      active_workers: 3,
      worker_count: 7,
      pre_block_api_key_loads: [
        {
          index: 0,
          key_hash: 'hash-one',
          masked: 'sk-...one',
          status: 'ok',
          active: 1,
          total: 72,
          success: 70,
          errors: 2,
          avg_latency_ms: 84,
          last_latency_ms: 80,
          last_http_status: 200,
        },
        {
          index: 1,
          key_hash: 'hash-two',
          masked: 'sk-...two',
          status: 'ok',
          active: 1,
          total: 56,
          success: 56,
          errors: 0,
          avg_latency_ms: 90,
          last_latency_ms: 92,
          last_http_status: 200,
        },
      ],
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
          ProxySelector: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.workerStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('128')
    expect(wrapper.text()).toContain('120')
    expect(wrapper.text()).toContain('8')
    expect(wrapper.text()).toContain('86 ms')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockAPIKeyLoad')
    expect(wrapper.text()).toContain('sk-...one')
    expect(wrapper.text()).toContain('sk-...two')
    expect(wrapper.text()).toContain('72')
    expect(wrapper.text()).toContain('56')
    expect(wrapper.text()).toContain('同步并发 2 / 可用 Key 2，累计 128 次，worker：3 / 7')

    const runtimeCards = wrapper.get('[data-test="pre-block-runtime-cards"]')
    const syncCard = wrapper.get('[data-test="pre-block-sync-card"]')
    const apiKeyLoadCard = wrapper.get('[data-test="pre-block-api-key-load-card"]')

    expect(runtimeCards.classes()).toEqual(
      expect.arrayContaining([
        'grid',
        'grid-cols-1',
        'xl:grid-cols-[minmax(0,520px)_minmax(0,1fr)]',
      ]),
    )
    expect(syncCard.element.parentElement).toBe(runtimeCards.element)
    expect(apiKeyLoadCard.element.parentElement).toBe(runtimeCards.element)
    expect(syncCard.classes()).toContain('card')
    expect(apiKeyLoadCard.classes()).toContain('card')
    expect(syncCard.get('h2').text()).toBe('admin.riskControl.preBlockSyncStatus')
    expect(syncCard.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(apiKeyLoadCard.get('h2').text()).toBe('admin.riskControl.preBlockAPIKeyLoad')
    expect(apiKeyLoadCard.text()).toContain('admin.riskControl.preBlockAPIKeyLoadHint')
    expect(wrapper.get('[data-test="pre-block-api-key-load-list"]').classes()).toEqual(
      expect.arrayContaining([
        'max-h-[280px]', // check-ui-allow: Assert the registered local height of the runtime credential list.
        'overflow-y-auto',
      ]),
    )
  })
})
