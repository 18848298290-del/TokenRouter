<template>
  <div class="space-y-4">
    <p v-if="description" class="text-sm text-gray-600 dark:text-gray-300">{{ description }}</p>

    <div>
      <div class="mb-1.5 flex items-center gap-2">
        <label :for="uid" class="input-label mb-0">{{ label }}</label>
        <span
          v-if="count > 1"
          class="rounded-full bg-primary-50 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-500/10 dark:text-primary-300"
        >
          {{ t('admin.providers.oauth.keysCount', { count }) }}
        </span>
        <slot name="label-extra" />
      </div>
      <input
        v-if="password"
        :id="uid"
        v-model="value"
        type="password"
        class="input font-mono text-sm"
        :placeholder="placeholder"
        autocomplete="off"
        spellcheck="false"
      />
      <textarea
        v-else
        :id="uid"
        v-model="value"
        :rows="rows"
        class="input resize-y font-mono text-sm"
        :placeholder="placeholder"
        spellcheck="false"
      ></textarea>
      <p v-if="count > 1 && countHint" class="input-hint">{{ countHint }}</p>
      <p v-if="hint" class="input-hint">{{ hint }}</p>
    </div>

    <slot />

    <SettingsNotice v-if="error" tone="error">
      <p class="whitespace-pre-line">{{ error }}</p>
    </SettingsNotice>

    <div class="flex justify-end">
      <button
        type="button"
        class="btn btn-primary"
        :disabled="loading || !value.trim()"
        @click="emit('submit')"
      >
        <Icon
          v-if="loading"
          name="loader"
          size="sm"
          :animate-on-hover="false"
          class="animate-spin"
        />
        <Icon v-else name="sparkles" size="sm" />
        {{ loading ? loadingLabel : submitLabel }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import Icon from '@/components/icons/Icon.vue'

// OAuthCredentialImport.vue renders a credential draft as either a password input or
// multiline text with a submit action. The caller owns parsing, entry counts, and network
// work; this component emits submit and displays the supplied loading, error, and batch
// guidance.
withDefaults(
  defineProps<{
    label: string // Displayed input label.
    submitLabel: string // Idle submit button text.
    loadingLabel: string // Busy submit button text.
    description?: string // Explanatory text for display.
    placeholder?: string // Empty credential input hint.
    hint?: string // Supplemental field help text.

    // Parsed entry count supplied by the caller; values above one reveal batch guidance.
    count?: number
    countHint?: string // Supplemental batch count explanation.
    rows?: number // Multiline credential textarea row count.

    // Renders one password input when true; otherwise the component uses a multiline
    // textarea.
    password?: boolean
    loading?: boolean // Caller-owned pending operation flag.
    error?: string // Caller-owned error display text.
  }>(),
  {
    count: 0,
    rows: 3,
  },
)
// value binds the parent-owned pasted credential text draft to the shared controls
// through its model update event.
const value = defineModel<string>({ required: true })
// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  submit: [] // Credential submission request event.
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
</script>
