<template>
  <button
    type="button"
    class="rounded-control bg-primary-50 px-3 py-1 text-xs text-primary-700 transition-colors hover:bg-primary-100 dark:bg-primary-900/30 dark:text-primary-400 dark:hover:bg-primary-900/50"
    @click="toggleImportPanel"
  >
    {{ t('admin.providers.headerOverride.importJson') }}
  </button>
  <button
    type="button"
    class="rounded-control bg-primary-50 px-3 py-1 text-xs text-primary-700 transition-colors hover:bg-primary-100 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-primary-900/30 dark:text-primary-400 dark:hover:bg-primary-900/50"
    :disabled="!hasNamedRows"
    @click="copyAsJson"
  >
    {{ t('admin.providers.headerOverride.copyJson') }}
  </button>

  <div v-if="showImportPanel" ref="importPanelRef" class="w-full space-y-2">
    <textarea
      ref="importTextareaRef"
      v-model="importText"
      rows="5"
      class="input font-mono text-xs"
      :placeholder="IMPORT_JSON_PLACEHOLDER"
    ></textarea>
    <div class="flex gap-2">
      <button type="button" class="btn btn-primary btn-sm" @click="applyImport">
        {{ t('admin.providers.headerOverride.importJsonApply') }}
      </button>
      <button type="button" class="btn btn-secondary btn-sm" @click="closeImportPanel">
        {{ t('admin.providers.headerOverride.importJsonCancel') }}
      </button>
    </div>
    <p class="input-hint">
      {{ t('admin.providers.headerOverride.importJsonHint') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import {
  parseHeaderOverridesJson,
  serializeHeaderOverrideRows,
  type HeaderOverrideRow,
} from '@/components/provider/form/headerOverrides'

// HeaderOverrideJsonTools.vue imports and copies the header editor rows as JSON objects.
// Parsing and serialization use the shared header rules, and invalid input leaves the
// rows unchanged. The import panel waits for its DOM before scrolling and focusing the
// textarea.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  rows: HeaderOverrideRow[] // Parent-owned editable row objects.
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  // update:rows supplies the parsed header-row array after a valid JSON import. The
  // parent replaces its draft list, and an imported empty object intentionally emits an
  // empty row array.
  (e: 'update:rows', rows: HeaderOverrideRow[]): void
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// appStore displays operation feedback through the shared notification area used by the
// provider administration screens.
const appStore = useAppStore()
// copyToClipboard owns browser clipboard access and reports its outcome through the
// shared notification behavior.
const { copyToClipboard } = useClipboard()

// IMPORT_JSON_PLACEHOLDER is literal JSON because its braces would be interpreted as
// interpolation syntax by the translation compiler.
const IMPORT_JSON_PLACEHOLDER = '{"user-agent": "my-client/1.0", "x-relay-token": "..."}'

// showImportPanel controls the temporary JSON editor visibility independently from the
// persisted provider header override enabled flag.
const showImportPanel = ref(false)
// importText retains pasted JSON until import succeeds or the panel is closed by the
// administrator.
const importText = ref('')
// importPanelRef identifies the expanded panel that is scrolled into the surrounding
// dialog viewport after rendering.
const importPanelRef = ref<HTMLElement | null>(null)
// importTextareaRef receives focus after panel expansion so pasted JSON goes into the
// visible import field.
const importTextareaRef = ref<HTMLTextAreaElement | null>(null)

// hasNamedRows enables JSON copying when at least one row contains a nonblank header name
// to serialize.
const hasNamedRows = computed(() => props.rows.some((row) => row.name.trim()))

// toggleImportPanel toggles the JSON panel and focuses its textarea after opening. It
// waits for Vue to mount the panel before scrolling it into the dialog viewport and
// moving focus.
const toggleImportPanel = async () => {
  showImportPanel.value = !showImportPanel.value
  if (!showImportPanel.value) return
  // The panel must exist before scroll and focus run, otherwise expansion below the dialog
  // viewport can leave the import controls out of sight.
  await nextTick()
  importPanelRef.value?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  importTextareaRef.value?.focus({ preventScroll: true })
}

// closeImportPanel closes the JSON import panel and clears its text draft. The next
// opening starts empty even after the previous draft contained invalid JSON or an
// unapplied edit.
const closeImportPanel = () => {
  showImportPanel.value = false
  importText.value = ''
}

// applyImport parses the JSON draft and replaces rows when the parser accepts it. A null
// result shows an error and retains the panel contents; an empty object clears the rows.
const applyImport = () => {
  const rows = parseHeaderOverridesJson(importText.value)
  if (rows === null) {
    appStore.showError(t('admin.providers.headerOverride.importJsonInvalid'))
    return
  }
  emit('update:rows', rows)
  closeImportPanel()
}

// copyAsJson serializes the current header rows and delegates clipboard feedback to the
// shared helper. Serialization applies the shared header normalization rules before the
// clipboard receives the resulting JSON text.
const copyAsJson = () => {
  void copyToClipboard(serializeHeaderOverrideRows(props.rows))
}
</script>
