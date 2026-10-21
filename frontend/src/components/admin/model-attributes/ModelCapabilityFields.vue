<template>
  <div>
    <ModelAttributeDisclosure
      :title="t('admin.modelAttributes.reasoningSection')"
      :meta="configuredText(reasoningCount)"
    >
      <div class="space-y-4">
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label">{{
              t('admin.modelAttributes.fields.reasoning_options')
            }}</label>
            <Select
              :aria-label="t('admin.modelAttributes.fields.reasoning_options')"
              :model-value="reasoningMode"
              :options="reasoningModes"
              @update:model-value="setReasoningMode(String($event))"
            />
          </div>
          <div>
            <label class="input-label">{{ t('admin.modelAttributes.fields.interleaved') }}</label>
            <Select
              :aria-label="t('admin.modelAttributes.fields.interleaved')"
              :model-value="interleavedValue"
              :options="interleavedOptions"
              @update:model-value="setInterleaved(String($event))"
            />
          </div>
        </div>
        <SettingsSubpanel v-if="reasoningMode === 'custom'">
          <label class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              class="rounded-compact"
              :checked="hasOption('toggle')"
              @change="toggleOption('toggle', checked($event))"
            />
            {{ t('admin.modelAttributes.thinkingToggle') }}
          </label>
          <fieldset>
            <legend class="input-label">{{ t('admin.modelAttributes.efforts') }}</legend>
            <div class="flex flex-wrap gap-x-4 gap-y-2">
              <label
                v-for="effort in effortChoices"
                :key="String(effort)"
                class="flex items-center gap-2 text-sm"
              >
                <input
                  type="checkbox"
                  class="rounded-compact"
                  :checked="selectedEfforts.includes(effort)"
                  @change="toggleEffort(effort, checked($event))"
                />
                {{ effortLabel(effort) }}
              </label>
            </div>
          </fieldset>
          <label class="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              class="rounded-compact"
              :checked="hasOption('budget_tokens')"
              @change="toggleOption('budget_tokens', checked($event))"
            />
            {{ t('admin.modelAttributes.tokenBudget') }}
          </label>
          <div v-if="budget" class="grid gap-4 sm:grid-cols-2">
            <label v-for="field in ['min', 'max'] as const" :key="field">
              <span class="input-label">{{ t(`admin.modelAttributes.budget.${field}`) }}</span>
              <input
                class="input"
                type="number"
                :min="field === 'min' ? -1 : 0"
                step="1"
                :value="budget[field] ?? ''"
                @input="setBudget(field, numberValue($event))"
              />
            </label>
          </div>
        </SettingsSubpanel>
        <div>
          <label class="input-label">{{ t('admin.modelAttributes.fields.conditions') }}</label>
          <Select
            :aria-label="t('admin.modelAttributes.fields.conditions')"
            :model-value="modelValue.conditions === undefined ? 'inherit' : 'custom'"
            :options="overrideModes"
            @update:model-value="set('conditions', $event === 'inherit' ? undefined : {})"
          />
          <p class="input-hint">{{ t('admin.modelAttributes.conditionsHint') }}</p>
        </div>
        <SettingsSubpanel
          v-if="modelValue.conditions"
          class="divide-y divide-gray-200 dark:divide-dark-600"
        >
          <div
            v-for="field in conditionalCapabilities"
            :key="field"
            role="group"
            :aria-label="t(`admin.modelAttributes.fields.${field}`)"
            class="space-y-3 pt-4 first:pt-0"
          >
            <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <span class="text-sm font-medium text-gray-900 dark:text-dark-50">{{
                t(`admin.modelAttributes.fields.${field}`)
              }}</span>
              <Select
                class="sm:w-56"
                :aria-label="`${t(`admin.modelAttributes.fields.${field}`)} · ${t('admin.modelAttributes.thinkingState')}`"
                :model-value="String(modelValue.conditions[field]?.thinking ?? '')"
                :options="thinkingStates"
                @update:model-value="
                  setCondition(field, 'thinking', $event === '' ? undefined : $event === 'true')
                "
              />
            </div>
            <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
              <span class="text-xs text-gray-500 dark:text-dark-400">{{
                t('admin.modelAttributes.efforts')
              }}</span>
              <label
                v-for="effort in reasoningEfforts"
                :key="effort"
                class="flex items-center gap-2 text-sm"
              >
                <input
                  type="checkbox"
                  class="rounded-compact"
                  :checked="modelValue.conditions[field]?.efforts?.includes(effort)"
                  @change="toggleConditionEffort(field, effort, checked($event))"
                />
                {{ effort }}
              </label>
            </div>
          </div>
        </SettingsSubpanel>
      </div>
    </ModelAttributeDisclosure>
    <ModelAttributeDisclosure
      class="mt-4"
      :title="t('admin.modelAttributes.media')"
      :meta="configuredText(Object.keys(modelValue.media ?? {}).length)"
    >
      <p class="input-hint mb-4">{{ t('admin.modelAttributes.listHint') }}</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <div v-for="field in mediaListFields" :key="field">
          <label class="input-label" :for="`${uid}-${field}`">{{
            t(`admin.modelAttributes.mediaFields.${field}`)
          }}</label>
          <ModelAttributeListInput
            :id="`${uid}-${field}`"
            :model-value="modelValue.media?.[field]"
            :placeholder="t('admin.modelAttributes.inherit')"
            @update:model-value="setMedia(field, $event)"
          />
        </div>
        <div>
          <label class="input-label">{{
            t('admin.modelAttributes.mediaFields.input_fidelity')
          }}</label>
          <Select
            :aria-label="t('admin.modelAttributes.mediaFields.input_fidelity')"
            :model-value="String(modelValue.media?.input_fidelity ?? '')"
            :options="booleanModes"
            @update:model-value="
              setMedia('input_fidelity', $event === '' ? undefined : $event === 'true')
            "
          />
        </div>
        <label v-for="field in mediaLimitFields" :key="field">
          <span class="input-label">{{ t(`admin.modelAttributes.mediaFields.${field}`) }}</span>
          <input
            class="input"
            type="number"
            min="0"
            step="1"
            :value="modelValue.media?.[field] ?? ''"
            :placeholder="t('admin.modelAttributes.inherit')"
            @input="setMedia(field, numberValue($event))"
          />
        </label>
      </div>
    </ModelAttributeDisclosure>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import ModelAttributeDisclosure from './ModelAttributeDisclosure.vue'
import ModelAttributeListInput from './ModelAttributeListInput.vue'
import {
  conditionalCapabilities,
  mediaLimitFields,
  mediaListFields,
  reasoningEfforts,
  type ConditionalCapability,
  type MediaCapabilities,
  type ModelAttributes,
  type ParameterCondition,
  type ReasoningOption,
} from '@/api/modelAttributes.types'

// ModelCapabilityFields.vue edits detailed reasoning, conditional, and media capability
// overrides. Omitted keys inherit values, while explicit empty reasoning options mark an
// unadjustable model. A local custom-mode flag distinguishes an empty editing panel from that
// saved empty representation.
//
// Updates copy nested records and emit a complete attribute object to the parent. Effort
// selections are stored in canonical choice order, reasoning variants replace entries by type,
// and cleared nested fields remove empty media overrides. Condition edits remove empty child
// conditions while retaining the conditions record, so each control follows its own
// configured-value representation.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  // modelValue contains configured model capabilities. Missing keys inherit values, while an
  // empty reasoning-options array represents an explicitly unadjustable model.
  modelValue: ModelAttributes
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [ModelAttributes] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// uid namespaces generated field identifiers, while effortChoices includes the vendor-default
// null alongside supported named reasoning levels.
const uid = useId()
const effortChoices = [null, ...reasoningEfforts]

// customReasoning keeps an empty custom editor expanded even though its saved options array
// also represents unadjustable reasoning.
const customReasoning = ref(false)
// reasoningMode distinguishes inherited options, explicit unadjustability, and the custom panel
// using both saved options and local editing state.
const reasoningMode = computed(() => {
  const options = props.modelValue.reasoning_options
  if (options === undefined) return 'inherit'
  return options.length || customReasoning.value ? 'custom' : 'none'
})
// reasoningModes and adjacent selector lists translate inheritance, supported values, thinking
// conditions, and interleaved response-field choices.
const reasoningModes = computed(() => [
  { value: 'inherit', label: t('admin.modelAttributes.inherit') },
  { value: 'none', label: t('admin.modelAttributes.notAdjustable') },
  { value: 'custom', label: t('admin.modelAttributes.custom') },
])
const overrideModes = computed(() => [
  { value: 'inherit', label: t('admin.modelAttributes.inherit') },
  { value: 'custom', label: t('admin.modelAttributes.custom') },
])
const booleanModes = computed(() => [
  { value: '', label: t('admin.modelAttributes.inherit') },
  { value: 'true', label: t('admin.modelAttributes.supported') },
  { value: 'false', label: t('admin.modelAttributes.unsupported') },
])
const thinkingStates = computed(() => [
  { value: '', label: t('admin.modelAttributes.anyState') },
  { value: 'true', label: t('admin.modelAttributes.thinkingOn') },
  { value: 'false', label: t('admin.modelAttributes.thinkingOff') },
])
const interleavedOptions = computed(() => [
  { value: '', label: t('admin.modelAttributes.inherit') },
  { value: 'true', label: t('admin.modelAttributes.supported') },
  { value: 'reasoning_content', label: 'reasoning_content' },
  { value: 'reasoning_details', label: 'reasoning_details' },
])
// interleavedValue converts the stored boolean or field object into the selector string used by
// the interleaved control.
const interleavedValue = computed(() =>
  props.modelValue.interleaved === true ? 'true' : (props.modelValue.interleaved?.field ?? ''),
)
// selectedEfforts and budget locate the active reasoning variants, and reasoningCount
// summarizes configured reasoning overrides for the disclosure.
const selectedEfforts = computed(
  () =>
    props.modelValue.reasoning_options?.find((option) => option.type === 'effort')?.values ?? [],
)
const budget = computed(() =>
  props.modelValue.reasoning_options?.find((option) => option.type === 'budget_tokens'),
)
const reasoningCount = computed(
  () =>
    (['reasoning_options', 'interleaved', 'conditions'] as const).filter(
      (key) => props.modelValue[key] !== undefined,
    ).length,
)

// configuredText formats the number of configured override groups for a disclosure. Zero
// returns empty text, leaving the summary area clear when no detailed values have been
// configured.
function configuredText(count: number) {
  return count ? t('admin.modelAttributes.configuredCount', { count }) : ''
}
// effortLabel returns an effort identifier or the translated vendor-default label. Null
// identifies the vendor default choice and remains distinct from named reasoning effort levels
// in the draft.
function effortLabel(value: string | null) {
  return value ?? t('admin.modelAttributes.vendorDefault')
}
// checked reads the checkbox state from the dispatched input event.
function checked(event: Event) {
  return (event.target as HTMLInputElement).checked
}
// numberValue reads a numeric input as a number or undefined. Clearing the input returns
// undefined so nested override setters can remove the configured field and restore inheritance.
function numberValue(event: Event) {
  const value = (event.target as HTMLInputElement).value
  return value === '' ? undefined : Number(value)
}

// set emits a copied attribute object with the requested field update. Undefined removes the
// key, allowing controls to restore inheritance while retaining explicit false and empty
// collection values.
function set<K extends keyof ModelAttributes>(key: K, value: ModelAttributes[K]) {
  const next = { ...props.modelValue, [key]: value }
  if (value === undefined) delete next[key]
  emit('update:modelValue', next)
}
// hasOption reports whether the draft contains the requested reasoning option type.
function hasOption(type: ReasoningOption['type']) {
  return props.modelValue.reasoning_options?.some((option) => option.type === type) ?? false
}
// setReasoningMode selects the editor mode and publishes its saved reasoning representation.
// Callers pass inherit, none, or custom from the mode selector; switching modes clears
// the configured options. Inheritance removes reasoning_options, while the other modes publish
// an empty array. customReasoning distinguishes an empty custom editor from saved unadjustability
// when reasoningMode reads that array after the parent update. Setting the local flag before
// emitting lets custom mode remain open for adding options even though both choices serialize
// to the same empty array.
function setReasoningMode(mode: string) {
  // Record custom intent before publishing the shared empty-array representation so the
  // resulting parent update keeps the custom controls available for entering options.
  customReasoning.value = mode === 'custom'
  set('reasoning_options', mode === 'inherit' ? undefined : [])
}
// replaceOption replaces one reasoning variant by its type in the emitted options list.
// Omitting the replacement removes that variant, and supplied replacements append after the
// remaining option types.
function replaceOption(type: ReasoningOption['type'], option?: ReasoningOption) {
  const options = (props.modelValue.reasoning_options ?? []).filter((value) => value.type !== type)
  if (option) options.push(option)
  set('reasoning_options', options)
}
// toggleOption adds or removes a reasoning variant through the shared replacement function.
function toggleOption(type: ReasoningOption['type'], enabled: boolean) {
  replaceOption(type, enabled ? { type } : undefined)
}

// toggleEffort rebuilds selected effort values in the canonical choice order. Removing the last
// effort removes the effort variant, and the remaining reasoning option types retain their
// existing entries.
function toggleEffort(effort: string | null, enabled: boolean) {
  // Rebuild values from canonical choices so selecting the same efforts in another order
  // produces the same stored option list.
  const values = effortChoices.filter((choice) =>
    choice === effort ? enabled : selectedEfforts.value.includes(choice),
  )
  replaceOption('effort', values.length ? { type: 'effort', values } : undefined)
}
// setBudget updates one bound of the token-budget reasoning option. Undefined removes that
// bound while retaining the budget option and its other fields, allowing the user to configure
// bounds independently.
function setBudget(field: 'min' | 'max', value: number | undefined) {
  const next: ReasoningOption = { ...budget.value, type: 'budget_tokens', [field]: value }
  if (value === undefined) delete next[field]
  replaceOption('budget_tokens', next)
}
// toggleConditionEffort updates a capability condition using the canonical effort ordering.
function toggleConditionEffort(field: ConditionalCapability, effort: string, enabled: boolean) {
  const current = props.modelValue.conditions?.[field]?.efforts ?? []
  setCondition(
    field,
    'efforts',
    reasoningEfforts.filter((choice) => (choice === effort ? enabled : current.includes(choice))),
  )
}
// setCondition updates a nested parameter condition and emits the enclosing attributes.
// Undefined or empty-array values remove that condition field; an empty child condition is
// removed from the conditions record.
function setCondition(
  field: ConditionalCapability,
  key: keyof ParameterCondition,
  value: string[] | boolean | undefined,
) {
  const condition = { ...props.modelValue.conditions?.[field], [key]: value }
  if (value === undefined || (Array.isArray(value) && !value.length)) delete condition[key]
  const conditions = { ...props.modelValue.conditions, [field]: condition }
  if (!Object.keys(condition).length) delete conditions[field]
  set('conditions', conditions)
}
// setMedia updates a copied media capability object and publishes it through set. Undefined
// removes the selected media field, and clearing the final field removes the complete media
// override.
function setMedia<K extends keyof MediaCapabilities>(key: K, value: MediaCapabilities[K]) {
  const media = { ...props.modelValue.media, [key]: value }
  if (value === undefined) delete media[key]
  set('media', Object.keys(media).length ? media : undefined)
}
// setInterleaved converts the selector value into an interleaved-reasoning override. Recognized
// field names become field objects, true becomes the boolean capability, and every other value
// restores inheritance.
function setInterleaved(value: string) {
  if (value === 'reasoning_content' || value === 'reasoning_details')
    set('interleaved', { field: value })
  else set('interleaved', value === 'true' ? true : undefined)
}
</script>
