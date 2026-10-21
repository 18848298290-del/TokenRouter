<template>
  <BaseDialog
    :show="show"
    :title="t('admin.proxies.dataImportTitle')"
    width="normal"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="import-proxy-data-form" class="space-y-4" @submit.prevent="handleImport">
      <div class="text-sm text-gray-600 dark:text-dark-300">
        {{ t('admin.proxies.dataImportHint') }}
      </div>
      <div
        class="rounded-control border border-amber-200 bg-amber-50 p-3 text-xs text-amber-600 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-400"
      >
        {{ t('admin.proxies.dataImportWarning') }}
      </div>

      <div>
        <label class="input-label">{{ t('admin.proxies.dataImportFile') }}</label>
        <div
          class="flex items-center justify-between gap-3 rounded-control border border-dashed border-gray-300 bg-gray-50 px-4 py-3 dark:border-dark-600 dark:bg-dark-800"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-gray-700 dark:text-dark-200">
              {{ fileName || t('admin.proxies.dataImportSelectFile') }}
            </div>
            <div class="text-xs text-gray-500 dark:text-dark-400">JSON (.json)</div>
          </div>
          <button type="button" class="btn btn-secondary shrink-0" @click="openFilePicker">
            {{ t('common.chooseFile') }}
          </button>
        </div>
        <input
          ref="fileInput"
          type="file"
          class="hidden"
          accept="application/json,.json"
          @change="handleFileChange"
        />
      </div>

      <div
        v-if="result"
        class="space-y-2 rounded-surface border border-gray-200 p-4 dark:border-dark-700"
      >
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.proxies.dataImportResult') }}
        </div>
        <div class="text-sm text-gray-700 dark:text-dark-300">
          {{ t('admin.proxies.dataImportResultSummary', result) }}
        </div>

        <div v-if="errorItems.length" class="mt-2">
          <div class="text-sm font-medium text-red-600 dark:text-red-400">
            {{ t('admin.proxies.dataImportErrors') }}
          </div>
          <div
            class="mt-2 max-h-48 overflow-auto rounded-control bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800"
          >
            <div v-for="(item, idx) in errorItems" :key="idx" class="whitespace-pre-wrap">
              {{ item.kind }} {{ item.name || item.proxy_key || '-' }} — {{ item.message }}
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
        <button
          class="btn btn-primary"
          type="submit"
          form="import-proxy-data-form"
          :disabled="importing"
        >
          {{ importing ? t('admin.proxies.dataImporting') : t('admin.proxies.dataImportButton') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AdminDataImportResult } from '@/api/admin/providers.types'

// ImportDataModal.vue imports a selected proxy-data JSON file and displays created, reused, and
// failed counts. Opening resets the chosen file and previous result. Browser file-reading
// alternatives feed the same parser, and completed imports with failed records retain detailed
// errors for inspection.

// Props controls visibility of the proxy data import dialog.
interface Props {
  show: boolean // dialog visibility flag
}

// Emits reports dialog closure and imports completed without failed proxy records.
interface Emits {
  // The close event requests dialog dismissal when importing is idle. The imported event
  // reports a completed response with no failed proxies so the page can reload its list.
  (e: 'close'): void
  // The close event requests dialog dismissal when importing is idle. The imported event
  // reports a completed response with no failed proxies so the page can reload its list.
  (e: 'imported'): void
}

// props supplies the parent visibility flag; reopening resets the locally owned file
// selection and completed import result.
const props = defineProps<Props>()
// emit requests dismissal or reports an import with no failed records so the parent
// can reload its proxy list and close the completed dialog.
const emit = defineEmits<Emits>()

// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// appStore displays operation notifications after requests complete or validation prevents the
// requested action.
const appStore = useAppStore()

// importing controls the pending import state, while file and result retain the selected input
// and completed per-record outcome.
const importing = ref(false)
const file = ref<File | null>(null)
const result = ref<AdminDataImportResult | null>(null)

// fileInput exposes the browser picker, and fileName derives the chosen file label displayed
// beside the import controls.
const fileInput = ref<HTMLInputElement | null>(null)
const fileName = computed(() => file.value?.name || '')

// errorItems supplies failed import records from the completed response, using an empty list
// before results are available.
const errorItems = computed(() => result.value?.errors || [])

// Reopening clears both reactive selection and the native file input so choosing the same
// file again can produce a fresh change event.
watch(
  () => props.show,
  (open) => {
    if (open) {
      file.value = null
      result.value = null
      if (fileInput.value) {
        fileInput.value.value = ''
      }
    }
  },
)

// openFilePicker opens the mounted hidden file input for user selection.
const openFilePicker = () => {
  fileInput.value?.click()
}

// handleFileChange stores the first selected file or clears the file reference.
const handleFileChange = (event: Event) => {
  const target = event.target as HTMLInputElement
  file.value = target.files?.[0] || null
}

// handleClose requests dialog closure when no import is running. While importing, it leaves
// visibility to the pending operation so the current result can be presented in the dialog.
const handleClose = () => {
  if (importing.value) return
  emit('close')
}

// readFileAsText reads the chosen browser file using its available text API. It prefers
// File.text, then decodes arrayBuffer, and finally uses FileReader for environments lacking
// those methods. FileReader failures reject with its error or a localized read-failure error.
const readFileAsText = async (sourceFile: File): Promise<string> => {
  if (typeof sourceFile.text === 'function') {
    return sourceFile.text()
  }

  // Some browser file implementations lack text(); decoding their available byte buffer keeps
  // the same import parser usable in those environments.
  if (typeof sourceFile.arrayBuffer === 'function') {
    const buffer = await sourceFile.arrayBuffer()
    return new TextDecoder().decode(buffer)
  }

  // Older file implementations require FileReader callbacks, so bridge their completion and
  // read errors into the promise awaited by the import operation.
  return await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error || new Error(t('common.fileReadFailed')))
    reader.readAsText(sourceFile)
  })
}

// handleImport parses the selected file and submits its JSON to proxy import. Missing selection
// reports an error immediately, while parse and API failures produce their corresponding
// notification. The result remains visible with per-record errors when some proxies fail.
//
// Only a response with no failed proxies emits imported, allowing the page to refresh and close
// the modal. The importing flag brackets reading and submission, and is released in finally for
// every completion path.
const handleImport = async () => {
  if (!file.value) {
    appStore.showError(t('admin.proxies.dataImportSelectFile'))
    return
  }

  importing.value = true
  try {
    const text = await readFileAsText(file.value)
    const dataPayload = JSON.parse(text)

    const res = await adminAPI.proxies.importData({ data: dataPayload })

    result.value = res

    const msgParams: Record<string, unknown> = {
      proxy_created: res.proxy_created,
      proxy_reused: res.proxy_reused,
      proxy_failed: res.proxy_failed,
    }

    if (res.proxy_failed > 0) {
      appStore.showError(t('admin.proxies.dataImportCompletedWithErrors', msgParams))
    } else {
      appStore.showSuccess(t('admin.proxies.dataImportSuccess', msgParams))
      emit('imported')
    }
  } catch (error: any) {
    if (error instanceof SyntaxError) {
      appStore.showError(t('admin.proxies.dataImportParseFailed'))
    } else {
      appStore.showError(error?.message || t('admin.proxies.dataImportFailed'))
    }
  } finally {
    importing.value = false
  }
}
</script>
