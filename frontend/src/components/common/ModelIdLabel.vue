<template>
  <div class="flex items-center gap-1.5">
    <IdBadge />
    <p class="min-w-0 break-all font-mono text-xs text-gray-500 dark:text-dark-400">
      {{ modelId }}
    </p>
    <button
      type="button"
      class="shrink-0 rounded-compact p-0.5 text-gray-400 transition hover:bg-gray-200 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-dark-200"
      :aria-label="t('common.copy')"
      :title="t('common.copy')"
      data-testid="model-id-copy"
      @click="copyToClipboard(modelId)"
    >
      <Icon name="copy" size="xs" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import IdBadge from '@/components/common/IdBadge.vue'
import { useClipboard } from '@/composables/useClipboard'

/**
 * ModelIdLabel.vue renders a model identifier with a small ID badge and a copy action.
 * The identifier stays monospaced and can break across lines; clipboard feedback
 * belongs to useClipboard so model cards and pricing details share the same
 * interaction.
 */

/**
 * modelId supplies the visible identifier and the exact string passed to the
 * shared clipboard action from the copy button.
 */
defineProps<{
  modelId: string /* copyable model identifier */
}>()

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
/**
 * The clipboard composable owns copying and user feedback, and the template delegates
 * its copy action through this binding.
 */
const { copyToClipboard } = useClipboard()
</script>
