<template>
  <div class="space-y-4">
    <LocalizedEditor
      :label="t('admin.modelAttributes.fields.display_name')"
      :model-value="
        modelValue.display_name_localization ||
        originalContent(
          modelValue.display_name || '',
          modelValue.display_name ? null : normalizeLocale(locale) || defaultLocale,
        )
      "
      :placeholder="inheritText(inherited?.display_name)"
      @update:model-value="
        emit('update:modelValue', {
          ...modelValue,
          display_name: $event.source || undefined,
          display_name_localization: $event,
        })
      "
    />
    <div class="grid gap-4 sm:grid-cols-3">
      <label v-for="field in attributeLimits" :key="field">
        <span class="input-label">{{ t(`admin.modelAttributes.fields.${field}`) }}</span>
        <input
          type="number"
          min="1"
          step="1"
          class="input"
          :value="modelValue[field] ?? ''"
          :placeholder="inheritText(inherited?.[field]?.toLocaleString(getLocale()))"
          @input="setNumber(field, ($event.target as HTMLInputElement).value)"
        />
      </label>
      <div v-for="field in attributeCapabilities" :key="field">
        <label class="input-label">{{ t(`admin.modelAttributes.fields.${field}`) }}</label>
        <Select
          :aria-label="t(`admin.modelAttributes.fields.${field}`)"
          :model-value="modelValue[field] === undefined ? 'inherit' : String(modelValue[field])"
          :options="booleanOptions(field)"
          @update:model-value="set(field, $event === 'inherit' ? undefined : $event === 'true')"
        />
      </div>
    </div>
    <div class="grid gap-4 sm:grid-cols-2">
      <fieldset
        v-for="field in ['input_modalities', 'output_modalities'] as const"
        :key="field"
        class="space-y-2 rounded-control border border-gray-200 p-3 dark:border-dark-600"
      >
        <legend class="px-1 text-sm font-medium">
          {{ t(`admin.modelAttributes.fields.${field}`) }}
        </legend>
        <label class="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            class="rounded-compact"
            :checked="modelValue[field] === undefined"
            @change="set(field, ($event.target as HTMLInputElement).checked ? undefined : [])"
          />
          {{ inheritText(inheritedModalities(field)) }}
        </label>
        <div v-if="modelValue[field] !== undefined" class="flex flex-wrap gap-3">
          <label
            v-for="modality in attributeModalities"
            :key="modality"
            class="flex items-center gap-2 text-sm"
          >
            <input
              type="checkbox"
              class="rounded-compact"
              :checked="modelValue[field]?.includes(modality)"
              @change="toggleModality(field, modality)"
            />
            {{ modalityLabel(modality) }}
          </label>
        </div>
      </fieldset>
    </div>
    <ModelCapabilityFields
      :model-value="modelValue"
      @update:model-value="emit('update:modelValue', $event)"
    />
  </div>
</template>

<script setup lang="ts">
import { getLocale } from '@/i18n'
import LocalizedEditor from '@/components/common/LocalizedEditor.vue'
import { normalizeLocale, defaultLocale } from '@/i18n/catalog'
import { originalContent } from '@/i18n/content'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import ModelCapabilityFields from './ModelCapabilityFields.vue'
import {
  attributeCapabilities,
  attributeLimits,
  attributeModalities,
  type ModelAttributes,
} from '@/api/modelAttributes.types'

// ModelAttributesFields.vue edits model attribute overrides with inherited values shown beside
// controls. Omitted keys inherit catalog or platform attributes, while false and empty
// modality arrays remain explicit choices. Display-name edits retain translation revisions;
// the owning page omits empty names and their translations from preview and save requests.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  // modelValue contains configured attribute overrides. Missing keys inherit values, while false
  // and empty modality arrays remain explicit settings.
  modelValue: ModelAttributes
  // inherited supplies resolved reference attributes for inheritance labels. Undefined fields
  // show the generic inheritance choice until a reference value exists.
  inherited?: ModelAttributes
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [ModelAttributes] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t, te, locale } = useI18n()

// inheritText formats an inheritance choice with an optional resolved reference value.
// Undefined references use the generic inheritance label, while supplied values show what the
// control would inherit.
function inheritText(value: string | undefined) {
  return value === undefined
    ? t('admin.modelAttributes.inherit')
    : t('admin.modelAttributes.inheritValue', { value })
}
// booleanOptions builds inheritance and explicit support choices for the selected capability.
function booleanOptions(field: (typeof attributeCapabilities)[number]) {
  const inheritedValue = props.inherited?.[field]
  return [
    {
      value: 'inherit',
      label: inheritText(
        inheritedValue === undefined
          ? undefined
          : t(`admin.modelAttributes.${inheritedValue ? 'supported' : 'unsupported'}`),
      ),
    },
    { value: 'true', label: t('admin.modelAttributes.supported') },
    { value: 'false', label: t('admin.modelAttributes.unsupported') },
  ]
}
// modalityLabel translates a known modality identifier for display. Identifiers with no
// translation retain their raw text so catalog additions remain visible before locale labels
// are available.
function modalityLabel(modality: string) {
  return te(`admin.modelAttributes.modalities.${modality}`)
    ? t(`admin.modelAttributes.modalities.${modality}`)
    : modality
}
// inheritedModalities formats reference modalities for the inheritance choice. Undefined
// remains unknown, an empty array displays none, and configured modalities are joined using
// their translated or raw labels.
function inheritedModalities(field: 'input_modalities' | 'output_modalities') {
  const values = props.inherited?.[field]
  if (values === undefined) return undefined
  return values.length ? values.map(modalityLabel).join(' / ') : t('admin.modelAttributes.none')
}

// set emits a copied attribute object containing the selected field value. Undefined deletes
// the override key, while explicit false and empty arrays remain present and keep their
// configured meaning.
function set<K extends keyof ModelAttributes>(key: K, value: ModelAttributes[K]) {
  const next = { ...props.modelValue, [key]: value }
  if (value === undefined) delete next[key]
  emit('update:modelValue', next)
}

// setNumber converts a numeric input value into an attribute override. An empty input delegates
// undefined to set, removing that override so the corresponding limit can inherit its value.
function setNumber(field: (typeof attributeLimits)[number], value: string) {
  set(field, value === '' ? undefined : Number(value))
}
// toggleModality emits the modality list with the selected value added or removed.
function toggleModality(field: 'input_modalities' | 'output_modalities', modality: string) {
  const values = props.modelValue[field] ?? []
  set(
    field,
    values.includes(modality)
      ? values.filter((value) => value !== modality)
      : [...values, modality],
  )
}
</script>
