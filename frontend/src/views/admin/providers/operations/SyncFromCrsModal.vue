<template>
  <BaseDialog
    :show="show"
    :title="t('admin.providers.syncFromCrsTitle')"
    width="normal"
    close-on-click-outside
    @close="handleClose"
  >
    <!-- Step 1: Input credentials -->
    <form
      v-if="currentStep === 'input'"
      v-content-reveal
      id="sync-from-crs-form"
      class="space-y-4"
      @submit.prevent="handlePreview"
    >
      <div class="text-sm text-gray-600 dark:text-dark-300">
        {{ t('admin.providers.syncFromCrsDesc') }}
      </div>
      <div
        class="rounded-control bg-gray-50 p-3 text-xs text-gray-500 dark:bg-dark-700/60 dark:text-dark-400"
      >
        {{ t('admin.providers.crsUpdateBehaviorNote') }}
      </div>
      <div
        class="rounded-control border border-amber-200 bg-amber-50 p-3 text-xs text-amber-600 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-400"
      >
        {{ t('admin.providers.crsVersionRequirement') }}
      </div>

      <div class="grid grid-cols-1 gap-4">
        <div>
          <label for="crs-base-url" class="input-label">{{
            t('admin.providers.crsBaseUrl')
          }}</label>
          <input
            id="crs-base-url"
            v-model="form.base_url"
            type="text"
            class="input"
            required
            :placeholder="t('admin.providers.crsBaseUrlPlaceholder')"
          />
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label for="crs-username" class="input-label">{{
              t('admin.providers.crsUsername')
            }}</label>
            <input
              id="crs-username"
              v-model="form.username"
              type="text"
              class="input"
              required
              autocomplete="username"
            />
          </div>
          <div>
            <label for="crs-password" class="input-label">{{
              t('admin.providers.crsPassword')
            }}</label>
            <input
              id="crs-password"
              v-model="form.password"
              type="password"
              class="input"
              required
              autocomplete="current-password"
            />
          </div>
        </div>

        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-300">
          <input
            v-model="form.sync_proxies"
            type="checkbox"
            class="rounded-compact border-gray-300 dark:border-dark-600"
          />
          {{ t('admin.providers.syncProxies') }}
        </label>
      </div>
    </form>

    <!-- Step 2: Preview & select -->
    <div v-else-if="currentStep === 'preview' && previewResult" class="space-y-4">
      <!-- Existing providers (read-only info) -->
      <div
        v-if="previewResult.existing_providers.length"
        class="rounded-control bg-gray-50 p-3 dark:bg-dark-700/60"
      >
        <div class="mb-2 text-sm font-medium text-gray-700 dark:text-dark-300">
          {{ t('admin.providers.crsExistingProviders') }}
          <span class="ml-1 text-xs text-gray-400"
            >({{ previewResult.existing_providers.length }})</span
          >
        </div>
        <div class="max-h-32 overflow-auto text-xs text-gray-500 dark:text-dark-400">
          <div
            v-for="acc in previewResult.existing_providers"
            :key="acc.crs_account_id"
            class="flex items-center gap-2 py-0.5"
          >
            <span
              class="inline-block rounded-compact bg-blue-100 px-1.5 py-0.5 text-xs font-medium text-blue-700 dark:bg-blue-900/30 dark:text-blue-400"
              >{{ acc.platform }} / {{ acc.type }}</span
            >
            <span class="truncate">{{ acc.name }}</span>
          </div>
        </div>
      </div>

      <!-- New providers (selectable) -->
      <div v-if="previewResult.new_providers.length">
        <div class="mb-2 flex items-center justify-between">
          <div class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.providers.crsNewProviders') }}
            <span class="ml-1 text-xs text-gray-400"
              >({{ previewResult.new_providers.length }})</span
            >
          </div>
          <div class="flex gap-2">
            <button
              type="button"
              class="text-xs text-primary-600 hover:text-primary-700 dark:text-primary-500"
              @click="selectAll"
            >
              {{ t('admin.providers.crsSelectAll') }}
            </button>
            <button
              type="button"
              class="text-xs text-gray-500 hover:text-gray-600 dark:text-gray-400"
              @click="selectNone"
            >
              {{ t('admin.providers.crsSelectNone') }}
            </button>
          </div>
        </div>
        <div
          class="max-h-48 overflow-auto rounded-control border border-gray-200 p-2 dark:border-dark-600"
        >
          <label
            v-for="acc in previewResult.new_providers"
            :key="acc.crs_account_id"
            class="flex cursor-pointer items-center gap-2 rounded-compact px-2 py-1.5 hover:bg-gray-50 dark:hover:bg-dark-700/40"
          >
            <input
              type="checkbox"
              :checked="selectedIds.has(acc.crs_account_id)"
              class="rounded-compact border-gray-300 dark:border-dark-600"
              @change="toggleSelect(acc.crs_account_id)"
            />
            <span
              class="inline-block rounded-compact bg-green-100 px-1.5 py-0.5 text-xs font-medium text-green-700 dark:bg-green-900/30 dark:text-green-400"
              >{{ acc.platform }} / {{ acc.type }}</span
            >
            <span class="truncate text-sm text-gray-700 dark:text-dark-300">{{ acc.name }}</span>
          </label>
        </div>
        <div class="mt-1 text-xs text-gray-400">
          {{ t('admin.providers.crsSelectedCount', { count: selectedIds.size }) }}
        </div>
      </div>

      <!-- Sync options summary -->
      <div class="flex items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
        <span>{{ t('admin.providers.syncProxies') }}:</span>
        <span
          :class="
            form.sync_proxies
              ? 'text-green-600 dark:text-green-400'
              : 'text-gray-400 dark:text-dark-500'
          "
        >
          {{ form.sync_proxies ? t('common.yes') : t('common.no') }}
        </span>
      </div>

      <!-- No new providers -->
      <div
        v-if="!previewResult.new_providers.length"
        class="rounded-control bg-gray-50 p-4 text-center text-sm text-gray-500 dark:bg-dark-700/60 dark:text-dark-400"
      >
        {{ t('admin.providers.crsNoNewProviders') }}
        <span v-if="previewResult.existing_providers.length">
          {{
            t('admin.providers.crsWillUpdate', { count: previewResult.existing_providers.length })
          }}
        </span>
      </div>
    </div>

    <!-- Step 3: Result -->
    <div v-else-if="currentStep === 'result' && result" class="space-y-4">
      <div class="space-y-2 rounded-surface border border-gray-200 p-4 dark:border-dark-700">
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.providers.syncResult') }}
        </div>
        <div class="text-sm text-gray-700 dark:text-dark-300">
          {{ t('admin.providers.syncResultSummary', result) }}
        </div>

        <div v-if="errorItems.length" class="mt-2">
          <div class="text-sm font-medium text-red-600 dark:text-red-400">
            {{ t('admin.providers.syncErrors') }}
          </div>
          <div
            class="mt-2 max-h-48 overflow-auto rounded-control bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800"
          >
            <div v-for="(item, idx) in errorItems" :key="idx" class="whitespace-pre-wrap">
              {{ item.kind }} {{ item.crs_account_id }} — {{ item.action
              }}{{ item.error ? `: ${item.error}` : '' }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <!-- Step 1: Input -->
        <template v-if="currentStep === 'input'">
          <button
            class="btn btn-secondary"
            type="button"
            :disabled="previewing"
            @click="handleClose"
          >
            {{ t('common.cancel') }}
          </button>
          <button
            class="btn btn-primary"
            type="submit"
            form="sync-from-crs-form"
            :disabled="previewing"
          >
            {{ previewing ? t('admin.providers.crsPreviewing') : t('admin.providers.crsPreview') }}
          </button>
        </template>

        <!-- Step 2: Preview -->
        <template v-else-if="currentStep === 'preview'">
          <button class="btn btn-secondary" type="button" :disabled="syncing" @click="handleBack">
            {{ t('admin.providers.crsBack') }}
          </button>
          <button
            class="btn btn-primary"
            type="button"
            :disabled="syncing || hasNewButNoneSelected"
            @click="handleSync"
          >
            {{ syncing ? t('admin.providers.syncing') : t('admin.providers.syncNow') }}
          </button>
        </template>

        <!-- Step 3: Result -->
        <template v-else-if="currentStep === 'result'">
          <button class="btn btn-secondary" type="button" @click="handleClose">
            {{ t('common.close') }}
          </button>
        </template>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { vContentReveal } from '@/directives/contentReveal'

import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { PreviewFromCRSResult } from '@/api/admin/providers'

/**
 * SyncFromCrsModal.vue previews and imports providers from a CRS installation using
 * administrator-entered connection credentials. Preview selects newly discovered records by
 * default, and synchronization submits the current selection with the proxy-import preference.
 * Results remain visible for per-item inspection, while reopening clears credentials and the
 * previous selection.
 */

/**
 * Props supplies parent-owned visibility for the CRS synchronization dialog.
 */
interface Props {
  show: boolean // parent-controlled CRS dialog visibility
}

/**
 * Emits reports dialog closure and completed synchronization requiring a parent refresh.
 */
interface Emits {
  /**
   * close requests that the parent hide the CRS synchronization dialog.
   */
  (e: 'close'): void
  /**
   * synced tells the parent a completed synchronization response requires a list refresh.
   */
  (e: 'synced'): void
}

/**
 * props and emit connect dialog visibility to the provider page, which refreshes records after
 * a completed synchronization response.
 */
const props = defineProps<Props>()
const emit = defineEmits<Emits>()

/**
 * Translation and application toasts provide field-validation feedback and distinguish full
 * synchronization success from responses containing failed items.
 */
const { t } = useI18n()
const appStore = useAppStore()

/**
 * Step identifies credential input, provider preview, and synchronization result views.
 */
type Step = 'input' | 'preview' | 'result'
/**
 * Synchronization state tracks preview and write activity, selected remote identifiers, and the
 * result shown after the import request settles.
 */
const currentStep = ref<Step>('input')
const previewing = ref(false)
const syncing = ref(false)
const previewResult = ref<PreviewFromCRSResult | null>(null)
const selectedIds = ref(new Set<string>())
const result = ref<Awaited<ReturnType<typeof adminAPI.providers.syncFromCrs>> | null>(null)

/**
 * form owns transient CRS credentials and proxy preference; reopening clears credentials, while
 * returning from preview retains them for correction.
 */
const form = reactive({
  /**
   * URL is trimmed for preview and synchronization requests; reopening clears the transient
   * connection draft.
   */
  base_url: '',
  /**
   * Username is trimmed for preview and synchronization requests; reopening clears the
   * transient credential draft.
   */
  username: '',
  /**
   * Password is checked for nonblank text but sent unchanged; reopening clears this transient
   * input.
   */
  password: '',
  sync_proxies: true, // include proxy synchronization preference
})

/**
 * Selection and error summaries distinguish an empty new-provider selection from failed or
 * skipped result items, excluding intentionally unselected records.
 */
const hasNewButNoneSelected = computed(() => {
  if (!previewResult.value) return false
  return previewResult.value.new_providers.length > 0 && selectedIds.value.size === 0
})

const errorItems = computed(() => {
  if (!result.value?.items) return []
  return result.value.items.filter(
    (i) => i.action === 'failed' || (i.action === 'skipped' && i.error !== 'not selected'),
  )
})

watch(
  () => props.show,
  (open) => {
    if (open) {
      currentStep.value = 'input'
      previewResult.value = null
      selectedIds.value = new Set()
      result.value = null
      form.base_url = ''
      form.username = ''
      form.password = ''
      form.sync_proxies = true
    }
  },
)

/**
 * handleClose requests closure only while preview and synchronization are both idle. In-flight
 * operations keep the dialog open through this handler so their eventual result remains
 * available for inspection.
 */
const handleClose = () => {
  if (syncing.value || previewing.value) {
    return
  }
  emit('close')
}

/**
 * handleBack returns to credential input and clears preview and selection state. The connection
 * fields remain available so the administrator can correct or reuse them before requesting
 * another preview.
 */
const handleBack = () => {
  currentStep.value = 'input'
  previewResult.value = null
  selectedIds.value = new Set()
}

/**
 * selectAll selects every newly discovered CRS account from the current preview.
 */
const selectAll = () => {
  if (!previewResult.value) return
  selectedIds.value = new Set(previewResult.value.new_providers.map((a) => a.crs_account_id))
}

/**
 * selectNone replaces the CRS provider selection with an empty set.
 */
const selectNone = () => {
  selectedIds.value = new Set()
}

/**
 * toggleSelect replaces selection with a copied set containing the toggled CRS account
 * identifier.
 */
const toggleSelect = (id: string) => {
  const s = new Set(selectedIds.value)
  if (s.has(id)) {
    s.delete(id)
  } else {
    s.add(id)
  }
  selectedIds.value = s
}

/**
 * handlePreview requires nonblank URL, username, and password before requesting the remote
 * preview. URL and username are trimmed, password text is passed unchanged, and success selects
 * all new provider identifiers.
 */
const handlePreview = async () => {
  if (!form.base_url.trim() || !form.username.trim() || !form.password.trim()) {
    appStore.showError(t('admin.providers.syncMissingFields'))
    return
  }

  previewing.value = true
  try {
    const res = await adminAPI.providers.previewFromCrs({
      base_url: form.base_url.trim(),
      username: form.username.trim(),
      password: form.password,
    })
    previewResult.value = res
    // Preview defaults to every newly discovered record, allowing the administrator to remove
    // unwanted remote identifiers before the synchronization request.
    selectedIds.value = new Set(res.new_providers.map((a) => a.crs_account_id))
    currentStep.value = 'preview'
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.providers.crsPreviewFailed'))
  } finally {
    previewing.value = false
  }
}

/**
 * handleSync submits the current CRS credentials, proxy preference, and selected account
 * identifiers. Success shows the result step and emits synced even with item failures, allowing
 * the parent to refresh imported records.
 */
const handleSync = async () => {
  if (!form.base_url.trim() || !form.username.trim() || !form.password.trim()) {
    appStore.showError(t('admin.providers.syncMissingFields'))
    return
  }

  syncing.value = true
  try {
    const res = await adminAPI.providers.syncFromCrs({
      base_url: form.base_url.trim(),
      username: form.username.trim(),
      password: form.password,
      sync_proxies: form.sync_proxies,
      selected_provider_ids: [...selectedIds.value],
    })
    result.value = res
    currentStep.value = 'result'

    if (res.failed > 0) {
      appStore.showError(t('admin.providers.syncCompletedWithErrors', res))
    } else {
      appStore.showSuccess(t('admin.providers.syncCompleted', res))
    }
    emit('synced')
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.providers.syncFailed'))
  } finally {
    syncing.value = false
  }
}
</script>
