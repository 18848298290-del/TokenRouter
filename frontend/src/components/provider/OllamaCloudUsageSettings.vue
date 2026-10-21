<template>
  <section
    v-if="state?.eligible"
    class="settings-section space-y-4"
    data-testid="ollama-cloud-usage-settings"
  >
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <h4 class="text-sm font-semibold text-primary-900 dark:text-dark-50">
          {{ t('admin.providers.ollamaCloud.title') }}
        </h4>
        <p class="input-hint">
          {{ t('admin.providers.ollamaCloud.sessionSecurityHint') }}
        </p>
      </div>
      <span
        class="whitespace-nowrap rounded-compact px-2 py-1 text-xs font-medium"
        :class="
          state.configured
            ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
            : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
        "
      >
        {{
          state.configured
            ? t('admin.providers.ollamaCloud.configured')
            : t('admin.providers.ollamaCloud.notConfigured')
        }}
      </span>
    </div>

    <ContentSkeleton v-if="loading" variant="form" :rows="3" class="py-4" />
    <template v-else>
      <SettingsNotice v-if="!state.encryption_key_configured" tone="warning">
        {{ t('admin.providers.ollamaCloud.encryptionKeyRequired') }}
      </SettingsNotice>

      <div
        v-if="snapshot"
        class="border-y border-gray-100 py-3 dark:border-dark-700"
        data-testid="ollama-cloud-usage-details"
      >
        <div class="grid grid-cols-[minmax(4rem,auto)_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.plan')
          }}</span>
          <span class="break-words text-gray-900 dark:text-white">{{
            snapshot.data?.plan || '-'
          }}</span>
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.fiveHour')
          }}</span>
          <span class="break-words text-gray-900 dark:text-white">{{
            windowSummary(snapshot.data?.five_hour)
          }}</span>
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.sevenDay')
          }}</span>
          <span class="break-words text-gray-900 dark:text-white">{{
            windowSummary(snapshot.data?.seven_day)
          }}</span>
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.balance')
          }}</span>
          <span class="break-words text-gray-900 dark:text-white">{{
            snapshot.data?.balance || '-'
          }}</span>
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.models')
          }}</span>
          <span class="break-words text-gray-900 dark:text-white">{{ modelSummary }}</span>
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.status')
          }}</span>
          <span class="break-words font-medium text-gray-900 dark:text-white">{{
            statusLabel
          }}</span>
          <span class="text-gray-500 dark:text-gray-400">{{
            t('admin.providers.ollamaCloud.updatedAt')
          }}</span>
          <span class="break-words text-gray-900 dark:text-white">{{
            formatDate(snapshot.fetched_at || snapshot.last_attempt_at)
          }}</span>
        </div>
        <p
          v-if="snapshot.last_error"
          class="mt-2 break-words border-t border-gray-100 pt-2 text-xs text-amber-700 dark:border-dark-700 dark:text-amber-300"
        >
          {{ t(`admin.providers.ollamaCloud.errors.${snapshot.last_error}`, snapshot.last_error) }}
        </p>
      </div>

      <div>
        <label class="input-label" for="ollama-cloud-session">{{
          t('admin.providers.ollamaCloud.sessionLabel')
        }}</label>
        <textarea
          id="ollama-cloud-session"
          v-model="session"
          rows="3"
          class="input font-mono text-xs"
          autocomplete="new-password"
          data-1p-ignore
          data-lpignore="true"
          data-bwignore="true"
          :placeholder="t('admin.providers.ollamaCloud.sessionPlaceholder')"
        />
        <p class="input-hint">{{ t('admin.providers.ollamaCloud.writeOnlyHint') }}</p>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="saving || !session.trim() || !state.encryption_key_configured"
          data-testid="ollama-cloud-session-save"
          @click="saveSession"
        >
          <Icon name="check" size="xs" class="mr-1.5" :animate-on-hover="false" />
          {{ t('common.save') }}
        </button>
        <button
          v-if="state.configured"
          type="button"
          class="btn btn-secondary btn-sm text-red-600 dark:text-red-400"
          :disabled="saving"
          data-testid="ollama-cloud-session-delete"
          @click="showDeleteConfirm = true"
        >
          <Icon name="trash" size="xs" class="mr-1.5" />
          {{ t('admin.providers.ollamaCloud.deleteSession') }}
        </button>
        <button
          v-if="state.configured"
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="refreshing"
          data-testid="ollama-cloud-refresh"
          @click="refreshUsage"
        >
          <Icon name="refresh" size="xs" class="mr-1.5" :class="{ 'animate-spin': refreshing }" />
          {{ t('admin.providers.ollamaCloud.refreshNow') }}
        </button>
      </div>

      <SettingToggleRow
        v-if="state.configured"
        id="ollama-cloud-auto-refresh"
        :model-value="state.auto_refresh_enabled"
        :disabled="saving"
        :label="t('admin.providers.ollamaCloud.autoRefresh')"
        :hint="t('admin.providers.ollamaCloud.autoRefreshHint')"
        testid="ollama-cloud-auto-refresh"
        @update:model-value="setAutoRefresh"
      />
    </template>

    <ConfirmDialog
      :show="showDeleteConfirm"
      :title="t('admin.providers.ollamaCloud.deleteSession')"
      :message="t('admin.providers.ollamaCloud.deleteConfirm')"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="deleteSession"
      @cancel="showDeleteConfirm = false"
    />
  </section>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import type {
  Provider,
  OllamaCloudUsageState,
  OllamaCloudUsageWindow,
} from '@/api/admin/providers.types'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import Icon from '@/components/icons/Icon.vue'

// OllamaCloudUsageSettings.vue edits a saved provider browser session and displays its
// Ollama usage snapshot. The provider response seeds the display, and a missing snapshot
// state triggers a read on mount or identity change. Each mutation publishes the server
// response through updated.
//
// Session save and deletion clear the local secret draft after their endpoint succeeds.
// Auto-refresh and manual refresh use separate endpoints and loading indicators. These
// requests publish on completion and have no local generation check, so the owning dialog
// must account for pending work when replacing its provider.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  // Current provider supplied by the caller; pending requests may still complete after
  // replacement.
  provider: Provider
}>()
// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  updated: [state: OllamaCloudUsageState] // Latest server usage state.
}>()
// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// appStore displays operation feedback through the shared notification area used by the
// provider administration screens.
const appStore = useAppStore()
// state starts from the provider response and is replaced with successful usage or
// session endpoint responses.
const state = ref<OllamaCloudUsageState | null>(props.provider.ollama_cloud_usage ?? null)
// session holds the pasted browser secret and is cleared after successful save, deletion,
// or provider identity changes.
const session = ref('')
// loading tracks the pending read so the template can show its loading state while data
// is requested.
const loading = ref(false)
// saving covers session mutations and automatic-refresh changes so their controls can
// prevent another submission while work is pending.
const saving = ref(false)
// refreshing tracks manual upstream refresh separately from the state read and
// browser-session persistence operations in this panel.
const refreshing = ref(false)
// showDeleteConfirm controls confirmation before deleting the browser session associated
// with the currently supplied provider record.
const showDeleteConfirm = ref(false)
// snapshot exposes the latest server usage observation within state for the detailed
// usage cards rendered below.
const snapshot = computed(() => state.value?.snapshot)
// statusLabel maps absent, unauthorized, failed, and successful snapshot observations to
// the corresponding localized status text.
const statusLabel = computed(() => {
  if (!snapshot.value) return t('admin.providers.ollamaCloud.notRefreshed')
  if (snapshot.value.status === 'unauthorized') return t('admin.providers.ollamaCloud.unauthorized')
  if (snapshot.value.status === 'failed') return t('admin.providers.ollamaCloud.failed')
  return t('admin.providers.ollamaCloud.ok')
})
// modelSummary labels each model observation with its five-hour or seven-day window and
// displays the recorded request count.
const modelSummary = computed(
  () =>
    snapshot.value?.data?.models
      ?.map((model) => {
        const window =
          model.window === 'five_hour'
            ? t('admin.providers.ollamaCloud.fiveHourShort')
            : t('admin.providers.ollamaCloud.sevenDayShort')
        return `${window} ${model.model}: ${model.requests}`
      })
      .join(', ') || '-',
)

// formatPercent formats a finite percentage with zero or one fractional digit. Missing or
// non-finite values display a dash, and input is already percentage-scaled by the
// upstream usage response.
const formatPercent = (value?: number) =>
  typeof value === 'number' && Number.isFinite(value) ? `${value.toFixed(value % 1 ? 1 : 0)}%` : '-'
// formatDate formats a supplied timestamp using the browser local date display. Missing
// values display a dash, while unparseable strings remain visible as returned by the
// upstream usage API.
const formatDate = (value?: string) => {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
// windowSummary combines the observed percentage with its available reset description. A
// reset timestamp takes precedence over textual reset information, and an absent window
// produces a dash for display.
const windowSummary = (window?: OllamaCloudUsageWindow) => {
  if (!window) return '-'
  const reset = window.reset_at ? formatDate(window.reset_at) : window.reset_text
  return reset
    ? t('admin.providers.ollamaCloud.windowWithReset', {
        percent: formatPercent(window.used_percent),
        reset,
      })
    : formatPercent(window.used_percent)
}

// applyState publishes an endpoint response locally and emits it to the parent. Callers
// must supply the response they intend to display because this function performs no
// provider-identity validation.
const applyState = (next: OllamaCloudUsageState) => {
  state.value = next
  emit('updated', next)
}

// load reads the current provider usage state and publishes the successful response.
// Errors retain the previous display and show feedback, while the loading flag clears
// when the request settles.
const load = async () => {
  loading.value = true
  try {
    applyState(await adminAPI.providers.getOllamaCloudUsage(props.provider.id))
  } catch (error) {
    // A failed operation leaves the prior usage state visible and reports the API error, so
    // existing observations are not replaced with an empty response.
    appStore.showError(extractApiErrorMessage(error, t('admin.providers.ollamaCloud.loadFailed')))
  } finally {
    loading.value = false
  }
}

// saveSession saves a nonblank browser-session draft for the currently selected provider.
// The server response is published before the secret draft is cleared, so failed saves
// retain the input for correction.
const saveSession = async () => {
  if (!session.value.trim()) return
  saving.value = true
  try {
    applyState(
      await adminAPI.providers.saveOllamaCloudUsageSession(props.provider.id, session.value),
    )
    // Clear the secret draft after the successful endpoint response is published, keeping
    // failed submissions available for correction or retry.
    session.value = ''
    appStore.showSuccess(t('admin.providers.ollamaCloud.sessionSaved'))
  } catch (error) {
    // A failed operation leaves the prior usage state visible and reports the API error, so
    // existing observations are not replaced with an empty response.
    appStore.showError(
      extractApiErrorMessage(error, t('admin.providers.ollamaCloud.sessionSaveFailed')),
    )
  } finally {
    saving.value = false
  }
}

// deleteSession closes confirmation and deletes the saved provider browser session.
// Success publishes the returned state and clears the local draft; failure shows feedback
// and leaves existing state visible.
const deleteSession = async () => {
  saving.value = true
  showDeleteConfirm.value = false
  try {
    applyState(await adminAPI.providers.deleteOllamaCloudUsageSession(props.provider.id))
    // Clear the secret draft after the successful endpoint response is published, keeping
    // failed submissions available for correction or retry.
    session.value = ''
    appStore.showSuccess(t('admin.providers.ollamaCloud.sessionDeleted'))
  } catch (error) {
    // A failed operation leaves the prior usage state visible and reports the API error, so
    // existing observations are not replaced with an empty response.
    appStore.showError(
      extractApiErrorMessage(error, t('admin.providers.ollamaCloud.sessionDeleteFailed')),
    )
  } finally {
    saving.value = false
  }
}

// setAutoRefresh persists the provider automatic-refresh switch through its dedicated
// endpoint. Success replaces the displayed state with the response, and failure leaves
// that state unchanged while showing API error feedback.
const setAutoRefresh = async (enabled: boolean) => {
  saving.value = true
  try {
    applyState(await adminAPI.providers.setOllamaCloudUsageAutoRefresh(props.provider.id, enabled))
  } catch (error) {
    // A failed operation leaves the prior usage state visible and reports the API error, so
    // existing observations are not replaced with an empty response.
    appStore.showError(
      extractApiErrorMessage(error, t('admin.providers.ollamaCloud.autoRefreshFailed')),
    )
  } finally {
    saving.value = false
  }
}

// refreshUsage requests a manual upstream usage refresh and publishes its successful
// result. Failure uses structured localized error details, including retry information
// when supplied, and always releases the refresh indicator.
const refreshUsage = async () => {
  refreshing.value = true
  try {
    applyState(await adminAPI.providers.refreshOllamaCloudUsage(props.provider.id))
    appStore.showSuccess(t('admin.providers.ollamaCloud.refreshSuccess'))
  } catch (error) {
    // A failed operation leaves the prior usage state visible and reports the API error, so
    // existing observations are not replaced with an empty response.
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        'admin.providers.ollamaCloud.errors',
        t('admin.providers.ollamaCloud.refreshFailed'),
      ),
    )
  } finally {
    refreshing.value = false
  }
}

// Changing provider identity clears the local secret draft and seeds the next display from
// its record before requesting missing usage state.
watch(
  () => props.provider.id,
  () => {
    state.value = props.provider.ollama_cloud_usage ?? null
    session.value = ''
    if (!state.value) void load()
  },
)

onMounted(() => {
  if (!state.value) void load()
})
</script>
