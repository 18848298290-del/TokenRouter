<template>
  <BaseDialog
    :show="show"
    :title="t('admin.providers.testDialog.title', { name: provider?.name ?? '' })"
    width="wide"
    :body-scroll="false"
    flush
    @close="handleClose"
  >
    <template #header-icon>
      <!-- The provider brand icon follows the shared model-brand display, with monochrome artwork inheriting the current text color. -->
      <span
        v-if="provider"
        class="flex h-10 w-10 shrink-0 items-center justify-center rounded-control border border-gray-200 bg-gray-50 text-gray-900 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
      >
        <ProviderIcon v-if="hasProviderBrandIcon" :brand="provider.platform" size="22px" />
        <PlatformIcon
          v-else
          :platform="provider.platform"
          size="lg"
          :class="platformIconClass(provider.platform)"
        />
      </span>
    </template>

    <template #header-actions>
      <SettingsSegmented
        v-model="testScope"
        :options="scopeOptions"
        :ariaLabel="t('admin.providers.testDialog.scope')"
        :disabled="busy"
        class="hidden shrink-0 sm:inline-flex"
      />
    </template>

    <div
      class="flex min-h-0 flex-1 flex-col overflow-y-auto md:h-[38rem] md:flex-initial md:flex-row md:overflow-hidden"
    >
      <!-- The left panel edits parameters for the current test request. -->
      <aside
        :aria-label="t('admin.providers.testDialog.settings')"
        class="flex shrink-0 flex-col gap-5 border-b border-gray-200 bg-gray-50/70 px-4 py-5 dark:border-dark-600 dark:bg-dark-950 sm:px-6 md:w-72 md:overflow-y-auto md:border-b-0 md:border-r"
      >
        <SettingsSegmented
          v-model="testScope"
          :options="scopeOptions"
          :ariaLabel="t('admin.providers.testDialog.scope')"
          :disabled="busy"
          block
          class="sm:hidden"
        />

        <div class="text-sm font-semibold text-gray-900 dark:text-dark-50">
          {{ t('admin.providers.testDialog.settings') }}
        </div>

        <div v-if="imageTestAvailable" class="space-y-1.5">
          <div class="input-label">{{ t('admin.providers.testDialog.type') }}</div>
          <SettingsSegmented
            v-model="testType"
            :options="testTypeOptions"
            :ariaLabel="t('admin.providers.testDialog.type')"
            :disabled="busy"
            block
          />
        </div>

        <div v-if="testScope === 'single'" class="space-y-1.5">
          <label class="input-label" :for="modelFieldId">{{
            t('admin.providers.testDialog.model')
          }}</label>
          <Select
            :id="modelFieldId"
            data-testid="provider-test-model"
            v-model="selectedModelId"
            :options="availableModels"
            :disabled="loadingModels || busy"
            value-key="id"
            label-key="id"
            creatable
            :placeholder="
              loadingModels ? t('common.loading') : t('admin.providers.testDialog.modelPlaceholder')
            "
          />
        </div>

        <p
          v-if="!loadingModels && availableModels.length === 0"
          class="input-hint"
          data-testid="provider-test-models-empty"
        >
          {{ t('admin.providers.testDialog.modelsEmpty') }}
        </p>

        <div v-if="testType === 'text'" class="space-y-1.5">
          <label class="input-label" :for="protocolFieldId">{{
            t('admin.providers.testDialog.protocol')
          }}</label>
          <Select
            :id="protocolFieldId"
            v-model="testProtocol"
            :options="protocolOptions"
            :disabled="busy || !protocolPlan.selectable || isCompactTestMode"
            :placeholder="t('admin.providers.testDialog.protocolNone')"
            data-testid="provider-test-protocol"
          />
          <p v-if="protocolHint" class="input-hint">{{ protocolHint }}</p>
        </div>

        <div v-if="isOpenAIProvider && testType === 'text'" class="space-y-1.5">
          <label class="input-label" :for="modeFieldId">{{
            t('admin.providers.openai.testMode')
          }}</label>
          <Select
            :id="modeFieldId"
            v-model="testMode"
            :options="openAITestModeOptions"
            :disabled="busy"
          />
        </div>

        <div v-if="testScope === 'batch'" class="space-y-1.5">
          <label class="input-label" :for="concurrencyFieldId">{{
            t('admin.providers.testDialog.batch.concurrency')
          }}</label>
          <Select
            :id="concurrencyFieldId"
            v-model="batch.concurrency"
            :options="concurrencyOptions"
            :disabled="busy"
            data-testid="provider-batch-concurrency"
          />
        </div>

        <SystemOneTestForm
          v-if="isDecisionProvider"
          :key="`${provider?.id}-${show}`"
          :disabled="busy"
          @change="decisionPayload = $event"
        />
        <TextArea
          v-else-if="!isCompactTestMode"
          v-model="testPrompt"
          :label="promptInputLabel"
          :placeholder="promptInputPlaceholder"
          :disabled="busy"
          data-testid="provider-test-prompt"
          rows="4"
        />
      </aside>

      <!-- The right panel shows a single result or batch models; on narrow screens it expands with content in the shared scroll area. -->
      <section
        :aria-label="t('admin.providers.testDialog.results')"
        class="flex min-w-0 flex-col px-4 py-5 sm:px-6 md:min-h-0 md:flex-1"
      >
        <ProviderTestResultView
          v-if="testScope === 'single'"
          :run="singleRun"
          :decision="isDecisionProvider"
        />
        <ProviderTestBatchPanel v-else :batch="batch" :decision="isDecisionProvider" />
      </section>
    </div>

    <footer
      class="flex shrink-0 items-center justify-end gap-3 rounded-b-surface border-t border-gray-200 bg-gray-50/70 px-4 py-3 dark:border-dark-600 dark:bg-dark-950 sm:rounded-b-dialog sm:px-6"
    >
      <button type="button" class="btn btn-secondary" @click="handleClose">
        {{ t('common.close') }}
      </button>

      <button
        v-if="testScope === 'single'"
        type="button"
        class="btn btn-primary"
        data-testid="provider-test-start"
        :disabled="!canTestSingle"
        @click="startTest"
      >
        <Icon
          v-if="singleRunning"
          name="loader"
          size="sm"
          class="animate-spin"
          :animate-on-hover="false"
        />
        <Icon v-else-if="singleRun.status === 'idle'" name="play" size="sm" />
        <Icon v-else name="refresh" size="sm" />
        {{ singleButtonLabel }}
      </button>

      <template v-else-if="batch.running">
        <button
          type="button"
          class="btn btn-secondary"
          data-testid="provider-batch-stop"
          :disabled="batch.stopping"
          @click="batch.stop()"
        >
          {{
            batch.stopping
              ? t('admin.providers.testDialog.batch.stoppingShort')
              : t('admin.providers.testDialog.batch.stop')
          }}
        </button>
      </template>

      <template v-else>
        <button
          v-if="batch.failedModels.length > 0"
          type="button"
          class="btn btn-secondary"
          data-testid="provider-batch-retry"
          :disabled="isDecisionProvider && !decisionPayload"
          @click="startBatch(batch.failedModels, true)"
        >
          {{ t('admin.providers.testDialog.batch.retry', { count: batch.failedModels.length }) }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          data-testid="provider-batch-start"
          :disabled="batch.selected.size === 0 || (isDecisionProvider && !decisionPayload)"
          @click="startBatch(selectedBatchModels, false)"
        >
          <Icon name="play" size="sm" />
          {{ t('admin.providers.testDialog.batch.start', { count: batch.selected.size }) }}
        </button>
      </template>
    </footer>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ProviderIcon from '@/components/common/ProviderIcon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Select from '@/components/common/Select.vue'
import TextArea from '@/components/common/TextArea.vue'
import SettingsSegmented from '@/components/common/settings/SettingsSegmented.vue'
import { Icon } from '@/components/icons'
import { adminAPI } from '@/api/admin'
import { platformIconClass } from '@/utils/platformColors'
import { resolveProviderBrand } from '@/utils/providerBrand'
import type { Provider } from '@/api/admin/providers.types'
import type { ClaudeModel } from '@/api/models.types'
import ProviderTestBatchPanel from './ProviderTestBatchPanel.vue'
import ProviderTestResultView from './ProviderTestResultView.vue'
import SystemOneTestForm from './SystemOneTestForm.vue'
import type { SystemOneTestPayload } from './systemOneTest'
import {
  defaultProviderTestProtocol,
  providerTestProtocolPlan,
  type ProviderTestProtocol,
} from './providerTestProtocols'
import {
  createProviderTestRun,
  executeProviderTest,
  resetProviderTestRun,
  type ProviderTestRequestBody,
} from './providerTestRun'
import { BATCH_CONCURRENCY_OPTIONS, useProviderBatchTest } from './useProviderBatchTest'

/**
 * ProviderTestModal.vue owns provider connection-test controls, model loading, and single or
 * batch execution. Administrators choose the model and output type, while platform and
 * authentication rules determine which protocol selectors and compact modes are available.
 * Decision providers submit the validated SystemOne form instead of a text prompt.
 *
 * The dialog aborts active requests when visibility or provider identity changes and on
 * unmount. Single output and batch rows have separate state, with a shared busy condition
 * preventing a second interactive start. Opening resets controls before loading the model
 * catalog. Catalog requests themselves have no cancellation or generation check, so their
 * completion is separate from the test-request lifecycle.
 */

/**
 * props identifies the visible provider dialog; the opening watcher resets controls when
 * visibility or provider identity changes.
 */
const props = defineProps<{
  show: boolean // parent-controlled dialog visibility
  provider: Provider | null // current provider or empty selection
}>()

/**
 * emit asks the owning provider page to close the dialog after active requests receive
 * cancellation.
 */
const emit = defineEmits<{
  /**
   * close requests that the parent hide the provider connection-test dialog.
   */
  (e: 'close'): void
}>()

/**
 * The translation function supplies reactive dialog labels, default prompts, and messages
 * passed to both test executors.
 */
const { t } = useI18n()

/**
 * Field identifiers connect the dialog labels to model, protocol, mode, and concurrency
 * controls in its template.
 */
const modelFieldId = 'provider-test-model'
const protocolFieldId = 'provider-test-protocol'
const modeFieldId = 'provider-test-mode'
const concurrencyFieldId = 'provider-test-concurrency'

/**
 * testScope and scopeOptions select single output or the batch panel while retaining their
 * separate result state.
 */
const testScope = ref<'single' | 'batch'>('single')
const scopeOptions = computed(() => [
  { value: 'single' as const, label: t('admin.providers.testDialog.scopeSingle') },
  { value: 'batch' as const, label: t('admin.providers.testDialog.scopeBatch') },
])

/**
 * singleRun, singleRunning, and singleController hold one request result and the cancellation
 * handle owned by this dialog.
 */
const singleRun = createProviderTestRun()
const singleRunning = computed(() => singleRun.status === 'connecting')
let singleController: AbortController | null = null

/**
 * batch exposes the reactive worker scheduler; busy combines its running state with the single
 * test state.
 */
const batch = reactive(useProviderBatchTest(t))
const busy = computed(() => singleRunning.value || batch.running)

/**
 * Model choices, selection, loading feedback, and prompt defaults are reset or refreshed when
 * the provider dialog opens.
 */
const availableModels = ref<ClaudeModel[]>([])
const selectedModelId = ref('')
const loadingModels = ref(false)
const testPrompt = ref('')
let lastDefaultPrompt = ''

/**
 * hasProviderBrandIcon selects the available branded icon, with the template using PlatformIcon
 * for platforms without brand artwork.
 */
const hasProviderBrandIcon = computed(() =>
  Boolean(resolveProviderBrand(props.provider?.platform).iconKey),
)

/**
 * Decision provider state selects the SystemOne form and receives its validated payload,
 * including null while the form is invalid.
 */
const isDecisionProvider = computed(() => props.provider?.platform === 'jev')
const decisionPayload = ref<SystemOneTestPayload | null>(null)

/**
 * Platform predicates control compact testing and native protocol choices without modifying the
 * saved provider credential configuration.
 */
const isOpenAIProvider = computed(() => props.provider?.platform === 'openai')
const isCNProvider = computed(() =>
  ['kimi', 'zhipu', 'deepseek'].includes(props.provider?.platform ?? ''),
)

/**
 * Protocol state combines provider endpoint capabilities with an administrator choice used only
 * by the current connection test.
 */
const protocolPlan = computed(() => providerTestProtocolPlan(props.provider))
const testProtocol = ref<ProviderTestProtocol | 'native' | 'all'>('native')
/**
 * protocolOptions adds the all-protocol choice for selectable CN providers and otherwise
 * displays the available endpoint labels and paths.
 */
const protocolOptions = computed(() => {
  const options: Array<{
    value: typeof testProtocol.value // protocol selector option value
    label: string // protocol selector display label
  }> = protocolPlan.value.options.map((item) => ({
    value: item.value,
    label: `${item.label} · ${item.path}`,
  }))
  // Selectable CN providers can omit a single protocol override and let the test endpoint
  // exercise its configured protocol sequence.
  if (isCNProvider.value && protocolPlan.value.selectable) {
    options.push({ value: 'all', label: t('admin.providers.testDialog.protocolAll') })
  }
  return options
})
// A provider change can remove the current choice; reset it before subsequent requests use an
// unsupported protocol selection.
watch(protocolPlan, (plan) => {
  if (!protocolOptions.value.some((item) => item.value === testProtocol.value)) {
    testProtocol.value = defaultProviderTestProtocol(props.provider, plan)
  }
})

/**
 * Test mode and output type control payload construction, editable prompts, and the
 * corresponding platform-specific selector choices.
 */
const testMode = ref<'default' | 'compact' | 'legacy_compact'>('default')
const testType = ref<'text' | 'image'>('text')

/**
 * isCompactTestMode hides prompt editing when an OpenAI compact test uses its fixed backend
 * payload and Responses endpoint.
 */
const isCompactTestMode = computed(() => isOpenAIProvider.value && testMode.value !== 'default')
watch(testMode, (mode) => {
  if (mode !== 'default' && protocolPlan.value.selectable) testProtocol.value = 'responses'
})
/**
 * OpenAI mode labels distinguish normal tests from compact modes, while protocolHint explains
 * fixed endpoints or compact endpoint selection.
 */
const openAITestModeOptions = computed(() => [
  { value: 'default', label: t('admin.providers.openai.testModeDefault') },
  { value: 'compact', label: t('admin.providers.openai.testModeCompact') },
  { value: 'legacy_compact', label: t('admin.providers.openai.testModeLegacyCompact') },
])
const protocolHint = computed(() => {
  if (isCompactTestMode.value) return t('admin.providers.testDialog.protocolCompact')
  if (protocolPlan.value.options.length === 0) return ''
  if (!protocolPlan.value.selectable) return t('admin.providers.testDialog.protocolFixed')
  return ''
})

/**
 * concurrencyOptions renders the scheduler-supported worker counts as selectable values and
 * matching numeric labels in the batch controls.
 */
const concurrencyOptions = BATCH_CONCURRENCY_OPTIONS.map((value) => ({
  value,
  label: String(value),
}))

/**
 * requestProtocol omits explicit protocol for image tests, fixed endpoints, and all-protocol
 * selection, while compact text requests choose Responses.
 */
const requestProtocol = computed<ProviderTestProtocol | undefined>(() => {
  if (testType.value !== 'text' || !protocolPlan.value.selectable) return undefined
  if (isCompactTestMode.value) return 'responses'
  if (testProtocol.value === 'native' || testProtocol.value === 'all') return undefined
  return testProtocol.value
})

/**
 * imageTestAvailable enables the output-type selector for OpenAI, Gemini, and Grok providers
 * that expose image connection testing.
 */
const imageTestAvailable = computed(() => {
  const platform = props.provider?.platform
  return platform === 'openai' || platform === 'gemini' || platform === 'grok'
})

/**
 * testTypeOptions supplies the translated text and image choices with the corresponding
 * modality icons for the segmented control.
 */
const testTypeOptions = computed(() => [
  {
    value: 'text' as const,
    label: t('admin.providers.testDialog.typeText'),
    icon: 'modalityText' as const,
  },
  {
    value: 'image' as const,
    label: t('admin.providers.testDialog.typeImage'),
    icon: 'modalityImage' as const,
  },
])

/**
 * Prompt labels and placeholders follow the chosen output type so the editor describes the
 * request currently being constructed.
 */
const promptInputLabel = computed(() =>
  testType.value === 'image'
    ? t('admin.providers.imagePromptLabel')
    : t('admin.providers.textPromptLabel'),
)
const promptInputPlaceholder = computed(() =>
  testType.value === 'image'
    ? t('admin.providers.imagePromptPlaceholder')
    : t('admin.providers.textPromptPlaceholder'),
)

/**
 * canTestSingle requires an idle dialog, a selected model, and a valid decision payload when
 * testing the Jev platform.
 */
const canTestSingle = computed(
  () =>
    !busy.value &&
    !!selectedModelId.value &&
    (!isDecisionProvider.value || !!decisionPayload.value),
)

/**
 * singleButtonLabel distinguishes active work, the initial idle run, and a repeat action after
 * a terminal result is available.
 */
const singleButtonLabel = computed(() => {
  if (singleRunning.value) return t('admin.providers.testDialog.running')
  if (singleRun.status === 'idle') return t('admin.providers.startTest')
  return t('admin.providers.testDialog.rerun')
})

/**
 * selectedBatchModels filters selection through catalog order so the batch receives models in
 * their displayed catalog sequence.
 */
const selectedBatchModels = computed(() =>
  batch.models.filter((model) => batch.selected.has(model)),
)

// Cancel test requests before resetting dialog controls; catalog loading then repopulates model
// choices for the opening provider selection.
watch(
  () => [props.show, props.provider?.id] as const,
  async ([open]) => {
    abortAll()
    if (open && props.provider) {
      testScope.value = 'single'
      testPrompt.value = ''
      lastDefaultPrompt = ''
      testMode.value = 'default'
      testType.value = 'text'
      updateDefaultPrompt()
      testProtocol.value = defaultProviderTestProtocol(props.provider, protocolPlan.value)
      resetProviderTestRun(singleRun)
      batch.reset([])
      await loadAvailableModels()
    }
  },
)

/**
 * updateDefaultPrompt replaces an empty or previously automatic prompt with the current
 * output-type default. A nonempty custom prompt remains intact when model selection or test
 * type changes.
 */
function updateDefaultPrompt() {
  const nextDefaultPrompt =
    testType.value === 'image'
      ? t('admin.providers.imagePromptDefault')
      : t('admin.providers.textPromptDefault')
  if (!testPrompt.value.trim() || testPrompt.value === lastDefaultPrompt) {
    testPrompt.value = nextDefaultPrompt
    lastDefaultPrompt = nextDefaultPrompt
  }
}
watch([selectedModelId, testType], updateDefaultPrompt)

watch(testType, (nextType) => {
  if (nextType === 'image') {
    testMode.value = 'default'
  }
})

/**
 * loadAvailableModels fetches the current provider catalog and resets batch selection after
 * settlement. Failure clears the catalog; Jev keeps its manual default model, and other
 * platforms require an administrator selection.
 */
const loadAvailableModels = async () => {
  if (!props.provider) return

  loadingModels.value = true
  // Jev starts with its manual model identifier; other platforms keep selection empty until the
  // administrator chooses or enters a model.
  selectedModelId.value = props.provider.platform === 'jev' ? 'jev-latest' : ''
  try {
    const models = await adminAPI.providers.getAvailableModels(props.provider.id)
    availableModels.value = models
  } catch (error) {
    console.error('Failed to load available models:', error)
    // Clearing failed catalog results leaves manual model entry available and prevents the
    // batch panel from offering stale catalog models.
    availableModels.value = []
    selectedModelId.value = props.provider.platform === 'jev' ? 'jev-latest' : ''
  } finally {
    loadingModels.value = false
    batch.reset(availableModels.value.map((model) => model.id))
  }
}

/**
 * buildRequestBody builds one request using the current dialog controls. Decision payloads take
 * precedence for Jev, compact requests suppress prompt text, and optional mode or protocol
 * fields follow platform selection rules.
 */
const buildRequestBody = (model: string): ProviderTestRequestBody => {
  if (isDecisionProvider.value && decisionPayload.value) {
    return { model_id: model, test_type: 'decision', systemone: decisionPayload.value }
  }
  const body: ProviderTestRequestBody = {
    model_id: model,
    prompt: isCompactTestMode.value ? '' : testPrompt.value.trim(),
    test_type: testType.value,
  }
  if (isOpenAIProvider.value) {
    body.mode = testMode.value
  }
  if (requestProtocol.value) {
    body.protocol = requestProtocol.value
  }
  return body
}

/**
 * abortAll cancels the single request and asks the batch scheduler to abort active requests. It
 * clears the single controller immediately, while batch state cleanup follows asynchronous
 * worker settlement.
 */
const abortAll = () => {
  singleController?.abort()
  singleController = null
  batch.abort()
}

/**
 * handleClose aborts active test requests before notifying the parent to close the dialog. The
 * parent controls visibility, and a later visibility watcher may repeat the cancellation
 * safely.
 */
const handleClose = () => {
  abortAll()
  emit('close')
}

/**
 * startTest begins a single request when a provider and valid model selection are available. It
 * replaces the prior controller and awaits the shared executor, which owns output and terminal
 * status updates.
 */
const startTest = async () => {
  if (!props.provider || !canTestSingle.value) return
  singleController?.abort()
  singleController = new AbortController()
  await executeProviderTest({
    providerId: props.provider.id,
    providerName: props.provider.name,
    body: buildRequestBody(selectedModelId.value),
    run: singleRun,
    signal: singleController.signal,
    t,
  })
}

/**
 * startBatch starts selected targets or retries through the shared batch scheduler. It
 * snapshots target order, rejects busy or invalid decision-form starts, and lets each worker
 * build its request from current controls.
 */
const startBatch = (targets: string[], retry: boolean) => {
  if (!props.provider || busy.value || (isDecisionProvider.value && !decisionPayload.value)) return
  void batch.start({
    targets: [...targets],
    retry,
    providerId: props.provider.id,
    providerName: props.provider.name,
    buildBody: buildRequestBody,
  })
}
// Component teardown cancels test traffic even when the parent removes the dialog without first
// emitting its close event.
onBeforeUnmount(abortAll)
</script>
