<template>
  <BaseDialog
    :show="show"
    :title="t('admin.providers.dataImportTitle')"
    width="normal"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="import-data-form" class="space-y-4" @submit.prevent="handleImport">
      <div class="text-sm text-gray-600 dark:text-dark-300">
        {{ t('admin.providers.dataImportHint') }}
      </div>
      <div
        class="rounded-control border border-amber-200 bg-amber-50 p-3 text-xs text-amber-600 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-400"
      >
        {{ t('admin.providers.dataImportWarning') }}
      </div>

      <div>
        <label class="input-label">{{ t('admin.providers.dataImportFile') }}</label>
        <div
          class="flex flex-col gap-3 rounded-control border border-dashed px-4 py-3 transition-colors sm:flex-row sm:items-center sm:justify-between"
          :class="
            dragActive
              ? 'border-primary-400 bg-primary-50/70 dark:border-primary-500 dark:bg-primary-900/20'
              : 'border-gray-300 bg-gray-50 dark:border-dark-600 dark:bg-dark-800'
          "
          @dragenter.prevent="handleDragEnter"
          @dragover.prevent
          @dragleave.prevent="handleDragLeave"
          @drop.prevent="handleDrop"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-gray-700 dark:text-dark-200" :title="fileListTitle">
              {{ selectedFilesLabel || t('admin.providers.dataImportSelectFile') }}
            </div>
            <div class="text-xs text-gray-500 dark:text-dark-400">
              JSON (.json)
              <span v-if="files.length > 1"> · {{ fileListTitle }}</span>
            </div>
          </div>
          <div class="flex shrink-0 flex-wrap gap-2">
            <button
              type="button"
              class="btn btn-secondary inline-flex items-center gap-1.5"
              @click="openFilePicker"
            >
              <Icon name="upload" size="sm" />
              {{ t('common.chooseFile') }}
            </button>
          </div>
        </div>
        <input
          ref="fileInput"
          type="file"
          class="hidden"
          accept="application/json,.json"
          multiple
          @change="handleFileChange"
        />
      </div>

      <div>
        <label class="input-label">{{ t('admin.providers.dataImportPasteData') }}</label>
        <textarea
          v-model="pastedDataText"
          class="input min-h-40 resize-y font-mono text-xs leading-5"
          :placeholder="pasteDataPlaceholder"
          spellcheck="false"
        />
        <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.providers.dataImportPasteHint') }}
        </div>
      </div>

      <div
        v-if="result"
        class="space-y-2 rounded-surface border border-gray-200 p-4 dark:border-dark-700"
      >
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.providers.dataImportResult') }}
        </div>
        <div class="text-sm text-gray-700 dark:text-dark-300">
          {{ t('admin.providers.dataImportResultSummary', result) }}
        </div>

        <div v-if="errorItems.length" class="mt-2">
          <div class="text-sm font-medium text-red-600 dark:text-red-400">
            {{ t('admin.providers.dataImportErrors') }}
          </div>
          <div
            class="mt-2 max-h-48 overflow-auto rounded-control bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800"
          >
            <div v-for="(item, idx) in errorItems" :key="idx" class="whitespace-pre-wrap">
              {{ item.kind }} {{ item.name || item.proxy_key || '-' }} - {{ item.message }}
            </div>
          </div>
        </div>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button class="btn btn-secondary" type="button" :disabled="importing" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button class="btn btn-primary" type="submit" form="import-data-form" :disabled="importing">
          {{
            importing ? t('admin.providers.dataImporting') : t('admin.providers.dataImportButton')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AdminDataImportResult, AdminDataPayload } from '@/api/admin/providers.types'

/**
 * ImportDataModal.vue imports administrative provider and proxy exports from pasted JSON or
 * selected files. Pasted text takes precedence over file selection. Each file must pass header
 * and array-shape checks before multiple payloads are combined, retaining provider credential
 * records as supplied for the import endpoint.
 *
 * Validation finishes before the single import request begins. Full success immediately
 * notifies the parent; partial success records whether providers or proxies were created and
 * defers that refresh notification until close. This lets administrators inspect per-item
 * errors while the dialog stays open. Closing is blocked during import, and reopening clears
 * file references, pasted content, and the previous result.
 */

/**
 * Props supplies parent-owned visibility for the administrative data import dialog.
 */
interface Props {
  show: boolean // parent-controlled import dialog visibility
}

/**
 * Emits reports dialog closure and successful creation requiring a parent refresh.
 */
interface Emits {
  /**
   * close requests that the parent hide the import dialog.
   */
  (e: 'close'): void
  /**
   * imported tells the parent that created records require a list refresh.
   */
  (e: 'imported'): void
}

/**
 * props and emit connect parent-owned visibility with import completion notifications,
 * including the deferred refresh after a partial import.
 */
const props = defineProps<Props>()
const emit = defineEmits<Emits>()

/**
 * Translation and application toasts report import source validation, file filtering, request
 * failures, and complete or partial import outcomes.
 */
const { t } = useI18n()
const appStore = useAppStore()

/**
 * Import state retains chosen files, pasted content, drag nesting, result details, and whether
 * partial success needs a close-time parent refresh.
 */
const importing = ref(false)
const files = ref<File[]>([])
const dragDepth = ref(0)
const pastedDataText = ref('')
const dragActive = computed(() => dragDepth.value > 0)
const hasCreatedData = ref(false)
const result = ref<AdminDataImportResult | null>(null)
const pasteDataPlaceholder = '{\n  "providers": [],\n  "proxies": []\n}'

/**
 * File-input references and labels describe the selected files; errorItems exposes individual
 * failures from the most recent import response.
 */
const fileInput = ref<HTMLInputElement | null>(null)
const selectedFilesLabel = computed(() => {
  if (files.value.length === 0) return ''
  if (files.value.length === 1) return files.value[0]?.name || ''
  return t('admin.providers.selectedCount', { count: files.value.length })
})
const fileListTitle = computed(() => files.value.map((item) => item.name).join(', '))

const errorItems = computed(() => result.value?.errors || [])

watch(
  () => props.show,
  (open) => {
    if (open) {
      files.value = []
      dragDepth.value = 0
      pastedDataText.value = ''
      hasCreatedData.value = false
      result.value = null
      if (fileInput.value) {
        fileInput.value.value = ''
      }
    }
  },
)

/**
 * openFilePicker opens the hidden native file selector when its element is available.
 */
const openFilePicker = () => {
  fileInput.value?.click()
}

/**
 * handleFileChange passes selected browser files through the JSON-file filter and clears the
 * input element. Clearing its value allows the same file to trigger a subsequent change event.
 */
const handleFileChange = (event: Event) => {
  const target = event.target as HTMLInputElement
  setSelectedFiles(target.files)
  target.value = ''
}

/**
 * handleClose closes only when no import is active. If a partial import created records, it
 * emits imported before close and clears the pending notification so repeated closes do not
 * repeat it.
 */
const handleClose = () => {
  if (importing.value) return
  if (hasCreatedData.value) {
    hasCreatedData.value = false
    emit('imported')
  }
  emit('close')
}

/**
 * isJsonFile accepts a JSON filename extension or the application/json MIME type.
 */
const isJsonFile = (sourceFile: File) => {
  const name = sourceFile.name.toLowerCase()
  return name.endsWith('.json') || sourceFile.type === 'application/json'
}

/**
 * setSelectedFiles replaces the chosen files with JSON candidates while import is idle. Empty
 * acceptable input reports an error, mixed input warns about ignored files, and accepted
 * selection clears the previous result.
 */
const setSelectedFiles = (sourceFiles: FileList | File[] | null | undefined) => {
  if (importing.value) return
  const incoming = Array.from(sourceFiles || [])
  const picked = incoming.filter(isJsonFile)
  if (!picked.length) {
    appStore.showError(t('admin.providers.dataImportSelectFile'))
    return
  }
  if (picked.length < incoming.length) {
    appStore.showWarning(
      t('admin.providers.dataImportIgnoredFiles', { count: incoming.length - picked.length }),
    )
  }
  files.value = picked
  result.value = null
}

/**
 * handleDragEnter increments drag nesting while the import form is idle.
 */
const handleDragEnter = () => {
  if (importing.value) return
  dragDepth.value += 1
}

/**
 * handleDragLeave decrements drag nesting while keeping its count nonnegative.
 */
const handleDragLeave = () => {
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

/**
 * handleDrop clears drag highlighting and passes dropped files through the selection filter. An
 * active import ignores the dropped input so its request source remains under the current
 * operation.
 */
const handleDrop = (event: DragEvent) => {
  dragDepth.value = 0
  if (importing.value) return
  setSelectedFiles(event.dataTransfer?.files)
}

/**
 * readFileAsText reads a file through text, then arrayBuffer, then FileReader according to
 * browser support. FileReader failures reject the promise so the caller can report the failing
 * source filename.
 */
const readFileAsText = async (sourceFile: File): Promise<string> => {
  if (typeof sourceFile.text === 'function') {
    return sourceFile.text()
  }

  // Older file implementations may expose bytes but no text method; decode those bytes before
  // using the event-based FileReader path.
  if (typeof sourceFile.arrayBuffer === 'function') {
    const buffer = await sourceFile.arrayBuffer()
    return new TextDecoder().decode(buffer)
  }

  return await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error || new Error('Failed to read file'))
    reader.readAsText(sourceFile)
  })
}

/**
 * Supported export markers and version identify accepted provider/proxy envelopes before
 * individual records and their credentials reach the administrative import endpoint.
 */
const SUPPORTED_DATA_TYPES = ['tokenrouter-data', 'sub2api-data']
const SUPPORTED_DATA_VERSION = 2

/**
 * isValidDataPayload checks supported export type, version two, provider and proxy arrays, and
 * absence of the legacy accounts field. It validates the envelope shape, leaving individual
 * record validation to import handling.
 */
const isValidDataPayload = (payload: unknown): payload is AdminDataPayload => {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return false
  const candidate = payload as Record<string, unknown>
  if (!SUPPORTED_DATA_TYPES.includes(candidate.type as string)) {
    return false
  }
  if (candidate.version !== SUPPORTED_DATA_VERSION) {
    return false
  }
  return (
    !('accounts' in candidate) &&
    Array.isArray(candidate.proxies) &&
    Array.isArray(candidate.providers)
  )
}

/**
 * mergeDataPayloads returns the original payload when there is exactly one input. Multiple
 * inputs concatenate provider and proxy records, generate a new export timestamp, and sum
 * finite skipped-shadow counts without deduplicating records.
 */
const mergeDataPayloads = (payloads: AdminDataPayload[]): AdminDataPayload => {
  const [firstPayload] = payloads
  if (payloads.length === 1 && firstPayload) return firstPayload

  return {
    type: payloads.find((item) => typeof item.type === 'string')?.type,
    version: payloads.find((item) => typeof item.version === 'number')?.version,
    exported_at: new Date().toISOString(),
    proxies: payloads.flatMap((item) => item.proxies),
    providers: payloads.flatMap((item) => item.providers),
    skipped_shadows: payloads.reduce((sum, item) => {
      const count = Number(item.skipped_shadows || 0)
      return Number.isFinite(count) ? sum + count : sum
    }, 0),
  }
}

/**
 * handleImport validates the selected input and submits one administrative import request.
 * Nonempty pasted text overrides selected files; otherwise every file is read and validated
 * before their payloads are merged. A file parse or header failure stops submission and
 * identifies that source.
 *
 * Returned item failures remain visible in result state. Full success emits imported
 * immediately, while partial success that created records sets a close-time notification flag.
 * Credentials inside payload records are passed through to the endpoint as supplied. The
 * operation flag clears in finally, including validation exits, allowing the administrator to
 * correct input and retry.
 */
const handleImport = async () => {
  importing.value = true
  try {
    const pastedText = pastedDataText.value.trim()
    if (!pastedText && files.value.length === 0) {
      appStore.showError(t('admin.providers.dataImportSelectSource'))
      return
    }

    let dataPayload: AdminDataPayload
    if (pastedText) {
      // A nonempty paste is the selected source, so previously chosen files do not contribute
      // records to this import request.
      const parsed: unknown = JSON.parse(pastedText)
      if (!isValidDataPayload(parsed)) {
        appStore.showError(t('admin.providers.dataImportInvalidFile', { name: 'JSON' }))
        return
      }
      dataPayload = parsed
    } else {
      const dataPayloads: AdminDataPayload[] = []
      for (const sourceFile of files.value) {
        let parsed: unknown
        try {
          parsed = JSON.parse(await readFileAsText(sourceFile))
        } catch {
          appStore.showError(
            t('admin.providers.dataImportParseFailedFile', { name: sourceFile.name }),
          )
          return
        }
        if (!isValidDataPayload(parsed)) {
          appStore.showError(t('admin.providers.dataImportInvalidFile', { name: sourceFile.name }))
          return
        }
        dataPayloads.push(parsed)
      }
      dataPayload = mergeDataPayloads(dataPayloads)
    }

    // Submit only after all selected envelopes pass validation, so an invalid later file cannot
    // leave earlier files separately imported.
    const res = await adminAPI.providers.importData({
      data: dataPayload,
    })

    result.value = res

    const msgParams: Record<string, unknown> = {
      provider_created: res.provider_created,
      provider_failed: res.provider_failed,
      proxy_created: res.proxy_created,
      proxy_reused: res.proxy_reused,
      proxy_failed: res.proxy_failed,
    }
    if (res.provider_failed > 0 || res.proxy_failed > 0) {
      // Keep partial failures inspectable while recording created data; closing later notifies
      // the parent to refresh records that were successfully imported.
      if (res.provider_created > 0 || res.proxy_created > 0) {
        hasCreatedData.value = true
      }
      appStore.showError(t('admin.providers.dataImportCompletedWithErrors', msgParams))
    } else {
      appStore.showSuccess(t('admin.providers.dataImportSuccess', msgParams))
      emit('imported')
    }
  } catch (error: any) {
    if (error instanceof SyntaxError) {
      appStore.showError(t('admin.providers.dataImportParseFailed'))
    } else {
      appStore.showError(error?.message || t('admin.providers.dataImportFailed'))
    }
  } finally {
    importing.value = false
  }
}
</script>
