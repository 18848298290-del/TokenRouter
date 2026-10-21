<template>
  <div>
    <div class="mb-1.5 flex items-center justify-between gap-2">
      <label :for="inputId" class="text-sm font-medium text-primary-900 dark:text-dark-50">{{
        label
      }}</label>
      <!-- Reset clears this field override so the server uses its deployment default. -->
      <button
        v-if="overridden"
        type="button"
        class="inline-flex rounded-compact p-0.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 disabled:opacity-50 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-gray-300"
        :title="t('admin.settings.responsesWS.resetField')"
        :aria-label="`${label}: ${t('admin.settings.responsesWS.resetField')}`"
        :disabled="disabled"
        :data-testid="`responses-ws-reset-${field.key}`"
        @click="emit('reset')"
      >
        <Icon name="undo" size="sm" />
      </button>
    </div>
    <div class="relative">
      <input
        :id="inputId"
        class="input tabular-nums"
        :class="field.unit && 'pr-12'"
        type="number"
        :inputmode="field.step === '1' ? 'numeric' : 'decimal'"
        :min="field.min"
        :max="field.max"
        :step="field.step"
        :disabled="disabled"
        :value="modelValue"
        :placeholder="placeholder"
        :aria-describedby="field.hint ? `${inputId}-hint` : undefined"
        @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
      />
      <span
        v-if="field.unit"
        class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-gray-400 dark:text-dark-400"
        >{{ t(`admin.settings.responsesWS.units.${field.unit}`) }}</span
      >
    </div>
    <p v-if="field.hint" :id="`${inputId}-hint`" class="input-hint">
      {{ t(`admin.settings.responsesWS.hints.${field.key}`) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { ResponsesWSNumberField } from './responsesWsFields'

/**
 * ResponsesWSNumberField.vue renders one numeric WebSocket override with a unit label
 * and an optional reset action. The parent supplies values in display units and
 * interprets the emitted raw string, including an empty value that removes the override.
 */
// field supplies display metadata, modelValue supplies the editable override, and placeholder
// supplies its effective value. overridden enables removal, while disabled prevents edits
// during the parent save; conversion and persistence remain with that parent.
const props = defineProps<{
  field: ResponsesWSNumberField // Numeric bounds and unit metadata.
  modelValue: number | '' // Display-unit override or empty input.
  placeholder: string // Current effective display value.
  overridden: boolean // Shows the field reset action.
  disabled?: boolean // Blocks edits during parent save.
}>()

// Events let the parent interpret raw input and null removals while this field stays
// independent of persistence and the byte-to-MiB conversion owned by its caller.
const emit = defineEmits<{
  /** update:modelValue forwards the raw input, including empty strings that remove an override. */
  (event: 'update:modelValue', value: string): void
  /** reset requests removal of this field's currently displayed override. */
  (event: 'reset'): void
}>()

// Reactive labels and stable field IDs connect input accessibility with the current translated parameter name.
const { t } = useI18n()
const inputId = computed(() => `responses-ws-input-${props.field.key}`)
const label = computed(() => t(`admin.settings.responsesWS.fields.${props.field.key}`))
</script>
