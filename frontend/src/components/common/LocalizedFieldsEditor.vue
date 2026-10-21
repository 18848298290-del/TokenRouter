<template>
  <div :class="layout">
    <div v-for="(field, index) in fields" :key="field.key" :class="field.class">
      <div class="mb-1.5 flex items-center justify-between gap-2">
        <label :for="`${id}-${field.key}`" class="input-label mb-0">
          {{ field.label }}
          <span v-if="field.required" class="text-red-500">*</span>
        </label>
        <LocalizedTranslateButton
          v-if="index === 0"
          :model-value="content"
          :title="dialogTitle || field.label"
          :fallback-locale="fallbackLocale"
          :reference-fields="fields"
          @update:model-value="emit('update:modelValue', $event)"
        >
          <template #default="{ value, update, id: scopeId }">
            <div :class="layout">
              <div v-for="item in fields" :key="item.key" :class="item.class">
                <label :for="`${scopeId}-${item.key}`" class="input-label">{{ item.label }}</label>
                <textarea
                  v-if="item.multiline"
                  :id="`${scopeId}-${item.key}`"
                  dir="auto"
                  :value="value[item.key]"
                  class="input resize-y"
                  :class="item.inputClass"
                  :rows="item.rows || 3"
                  :placeholder="item.placeholder"
                  @input="
                    update({ ...value, [item.key]: ($event.target as HTMLTextAreaElement).value })
                  "
                ></textarea>
                <input
                  v-else
                  :id="`${scopeId}-${item.key}`"
                  dir="auto"
                  :value="value[item.key]"
                  :type="item.url ? 'url' : 'text'"
                  class="input"
                  :class="item.inputClass"
                  :placeholder="item.placeholder"
                  @input="
                    update({ ...value, [item.key]: ($event.target as HTMLInputElement).value })
                  "
                />
              </div>
            </div>
          </template>
        </LocalizedTranslateButton>
      </div>
      <textarea
        v-if="field.multiline"
        :id="`${id}-${field.key}`"
        dir="auto"
        :value="content.source[field.key]"
        class="input resize-y"
        :class="field.inputClass"
        :rows="field.rows || 3"
        :placeholder="field.placeholder"
        :required="field.required"
        @input="updateField(field.key, ($event.target as HTMLTextAreaElement).value)"
      ></textarea>
      <input
        v-else
        :id="`${id}-${field.key}`"
        dir="auto"
        :value="content.source[field.key]"
        :type="field.url ? 'url' : 'text'"
        class="input"
        :class="field.inputClass"
        :placeholder="field.placeholder"
        :required="field.required"
        @input="updateField(field.key, ($event.target as HTMLInputElement).value)"
      />
    </div>
  </div>
</template>

<script lang="ts">
/**
 * LocalizedFieldsEditor.vue edits several source fields that share one localized
 * content revision. The first field opens the translation dialog for the complete
 * object, and field edits replace one source property before delegating revision
 * handling to updateSource.
 */

/**
 * fieldsSequence supplies distinct input identifier prefixes for each group of
 * localized fields mounted from this module.
 */
let fieldsSequence = 0

/**
 * LocalizedField describes one source property and its form control presentation.
 */
export interface LocalizedField<T> {
  key: keyof T & string /* source property identifier */
  label: string /* visible field label */
  placeholder?: string /* empty input hint */
  multiline?: boolean /* textarea control choice */
  /**
   * rows controls multiline textarea height; missing or zero values use three rows in
   * source and translation editors.
   */
  rows?: number
  url?: boolean /* URL input choice */
  required?: boolean /* native required validation */
  class?: string /* caller supplied classes */
  inputClass?: string /* input element classes */
}
</script>

<script setup lang="ts" generic="T extends Record<string, string>">
import { computed } from 'vue'
import LocalizedTranslateButton from '@/components/common/LocalizedTranslateButton.vue'
import { getLocale } from '@/i18n'
import { originalContent, type LocalizedUpdate } from '@/i18n/content'
import { updateSource } from '@/i18n/contentEdit'

/**
 * props supplies shared localized content or its initial source plus the ordered field
 * definitions used in both editor surfaces.
 */
const props = withDefaults(
  defineProps<{
    /**
     * modelValue takes precedence over source when provided; absent content is
     * constructed from source and sourceLocale for the editor.
     */
    modelValue?: LocalizedUpdate<T>
    source: T /* initial source object */
    sourceLocale?: string | null /* initial source language */
    fields: LocalizedField<T>[] /* ordered field definitions */
    layout?: string /* field container classes */
    dialogTitle?: string /* translation dialog heading */
  }>(),
  { layout: 'space-y-4' },
)
/**
 * emit publishes the shared localized object after individual source fields change or
 * the translation dialog returns completed edits.
 */
const emit = defineEmits<{
  'update:modelValue': [value: LocalizedUpdate<T>] /* replacement parent controlled value */
}>()

/**
 * The generated input identifier connects this editor instance to its label and keeps
 * sibling fields separately addressable.
 */
// eslint-disable-next-line no-useless-assignment -- The generated identifier uses this counter before rendering input labels.
const id = `localized-fields-${++fieldsSequence}`
/**
 * content uses the supplied localized value or constructs original content from the
 * caller source and source locale.
 */
const content = computed(
  () => props.modelValue || originalContent(props.source, props.sourceLocale),
)
/**
 * fallbackLocale chooses the caller supplied source language or current interface
 * language for source updates with missing language metadata.
 */
const fallbackLocale = computed(() => props.sourceLocale || getLocale())

/**
 * updateField emits a localized content update for one source property. Spreading the
 * source keeps the other field values together under the same revision before
 * updateSource handles language and translation status.
 */
function updateField(key: keyof T & string, value: string): void {
  emit(
    'update:modelValue',
    updateSource(content.value, { ...content.value.source, [key]: value }, fallbackLocale.value),
  )
}
</script>
