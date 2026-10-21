<template>
  <div>
    <div class="mb-1.5 flex items-center justify-between gap-2">
      <label :for="id" :class="labelClass">{{ label }}</label>
      <LocalizedTranslateButton
        :model-value="modelValue"
        :title="dialogTitle || label"
        :fallback-locale="fallbackLocale"
        @update:model-value="emit('update:modelValue', $event)"
      >
        <template #default="scope">
          <slot v-bind="scope">
            <input
              v-if="rows === 1"
              :id="scope.id"
              dir="auto"
              :value="String(scope.value ?? '')"
              class="input"
              :class="inputClass"
              :placeholder="placeholder"
              @input="scope.update(($event.target as HTMLInputElement).value as T)"
            />
            <textarea
              v-else
              :id="scope.id"
              dir="auto"
              :value="String(scope.value ?? '')"
              :rows="rows"
              class="input resize-y"
              :class="inputClass"
              :placeholder="placeholder"
              @input="scope.update(($event.target as HTMLTextAreaElement).value as T)"
            />
          </slot>
        </template>
      </LocalizedTranslateButton>
    </div>
    <slot
      :value="modelValue.source"
      :update="updateValue"
      :locale="modelValue.source_locale"
      :id="id"
    >
      <input
        v-if="rows === 1"
        :id="id"
        dir="auto"
        :value="String(modelValue.source ?? '')"
        class="input"
        :class="inputClass"
        :placeholder="placeholder"
        @input="updateValue(($event.target as HTMLInputElement).value as T)"
      />
      <textarea
        v-else
        :id="id"
        dir="auto"
        :value="String(modelValue.source ?? '')"
        :rows="rows"
        class="input resize-y"
        :class="inputClass"
        :placeholder="placeholder"
        @input="updateValue(($event.target as HTMLTextAreaElement).value as T)"
      />
    </slot>
  </div>
</template>

<script lang="ts">
/**
 * LocalizedEditor.vue edits a localized value through its source field and a
 * translation dialog. The parent owns the content object; source edits go through
 * updateSource, while the slot exposes the same value and update interface for custom
 * controls.
 */

/**
 * editorSequence supplies monotonically increasing field identifiers shared by editor
 * instances in this module.
 */
let editorSequence = 0
</script>

<script setup lang="ts" generic="T">
import { computed } from 'vue'
import LocalizedTranslateButton from '@/components/common/LocalizedTranslateButton.vue'
import { getLocale } from '@/i18n'
import type { LocalizedUpdate } from '@/i18n/content'
import { updateSource, withDefaultSource } from '@/i18n/contentEdit'

/**
 * props pairs localized content with its field label, default control presentation,
 * and optional preferred source language.
 */
const props = withDefaults(
  defineProps<{
    modelValue: LocalizedUpdate<T> /* parent controlled value */
    label: string /* visible field label */
    labelClass?: string /* field label classes */

    /**
     * dialogTitle names the field in the translation dialog; an empty or missing value
     * uses the visible label instead.
     */
    dialogTitle?: string
    /**
     * rows selects an input when exactly one and a textarea otherwise; the same choice
     * applies to source and translation editors.
     */
    rows?: number
    placeholder?: string /* empty input hint */
    inputClass?: string /* input element classes */
    /**
     * defaultSourceLocale can label unsaved source content before source edits;
     * otherwise the current interface locale supplies the source update language.
     */
    defaultSourceLocale?: string
  }>(),
  { rows: 1, labelClass: 'input-label mb-0' },
)
/**
 * emit publishes the complete localized value after source edits or dialog completion,
 * letting the parent retain content and revision metadata together.
 */
const emit = defineEmits<{
  'update:modelValue': [value: LocalizedUpdate<T>] /* replacement parent controlled value */
}>()
/**
 * The default slot receives the current value, language, input identifier, and updater
 * so custom controls can edit either source or translation content.
 */
defineSlots<{
  /**
   * default renders the supplied editor value with its language and update callback.
   */
  default(props: {
    value: T /* current editor value */
    update: (value: T) => void /* editor update callback */
    locale: string | null /* editor language code */
    id: string /* input association identifier */
  }): unknown
}>()

/**
 * The generated input identifier connects this editor instance to its label and keeps
 * sibling fields separately addressable.
 */
// eslint-disable-next-line no-useless-assignment -- The generated identifier uses this counter before rendering input labels.
const id = `localized-field-${++editorSequence}`

/**
 * fallbackLocale chooses the caller supplied source language or current interface
 * language for source updates with missing language metadata.
 */
const fallbackLocale = computed(() => props.defaultSourceLocale || getLocale())

/**
 * updateValue emits content with a replaced source value. A supplied
 * defaultSourceLocale can label unsaved content through withDefaultSource before
 * updateSource invalidates translation reviews; existing source language and
 * translation conflicts follow the helper rules.
 */
function updateValue(value: T): void {
  /**
   * Apply the default language helper before source edits so eligible unsaved content
   * receives its initial source label before translation review invalidation.
   */
  const base = props.defaultSourceLocale
    ? withDefaultSource(props.modelValue, props.defaultSourceLocale)
    : props.modelValue
  emit('update:modelValue', updateSource(base, value, fallbackLocale.value))
}
</script>
