<template>
  <div>
    <ModelAttributeDisclosure :title="t('admin.modelAttributes.reasoningSection')" :meta="configuredText(reasoningCount)">
      <div class="space-y-4">
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.modelAttributes.fields.reasoning_options') }}</label>
            <Select :aria-label="t('admin.modelAttributes.fields.reasoning_options')" :model-value="reasoningMode" :options="reasoningModes" @update:model-value="setReasoningMode(String($event))" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.modelAttributes.fields.interleaved') }}</label>
            <Select :aria-label="t('admin.modelAttributes.fields.interleaved')" :model-value="interleavedValue" :options="interleavedOptions" @update:model-value="setInterleaved(String($event))" />
          </div>
        </div>
        <SettingsSubpanel v-if="reasoningMode === 'custom'">
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" class="rounded-compact" :checked="hasOption('toggle')" @change="toggleOption('toggle', checked($event))" />
            {{ t('admin.modelAttributes.thinkingToggle') }}
          </label>
          <fieldset>
            <legend class="input-label">{{ t('admin.modelAttributes.efforts') }}</legend>
            <div class="flex flex-wrap gap-x-4 gap-y-2">
              <label v-for="effort in effortChoices" :key="String(effort)" class="flex items-center gap-2 text-sm">
                <input type="checkbox" class="rounded-compact" :checked="selectedEfforts.includes(effort)" @change="toggleEffort(effort, checked($event))" />
                {{ effortLabel(effort) }}
              </label>
            </div>
          </fieldset>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" class="rounded-compact" :checked="hasOption('budget_tokens')" @change="toggleOption('budget_tokens', checked($event))" />
            {{ t('admin.modelAttributes.tokenBudget') }}
          </label>
          <div v-if="budget" class="grid gap-4 sm:grid-cols-2">
            <label v-for="field in ['min', 'max'] as const" :key="field">
              <span class="input-label">{{ t(`admin.modelAttributes.budget.${field}`) }}</span>
              <input class="input" type="number" :min="field === 'min' ? -1 : 0" step="1" :value="budget[field] ?? ''" @input="setBudget(field, numberValue($event))" />
            </label>
          </div>
        </SettingsSubpanel>
        <div>
          <label class="input-label">{{ t('admin.modelAttributes.fields.conditions') }}</label>
          <Select :aria-label="t('admin.modelAttributes.fields.conditions')" :model-value="modelValue.conditions === undefined ? 'inherit' : 'custom'" :options="overrideModes" @update:model-value="set('conditions', $event === 'inherit' ? undefined : {})" />
          <p class="input-hint">{{ t('admin.modelAttributes.conditionsHint') }}</p>
        </div>
        <SettingsSubpanel v-if="modelValue.conditions" class="divide-y divide-gray-200 dark:divide-dark-600">
          <div v-for="field in conditionalCapabilities" :key="field" role="group" :aria-label="t(`admin.modelAttributes.fields.${field}`)" class="space-y-3 pt-4 first:pt-0">
            <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <span class="text-sm font-medium text-gray-900 dark:text-dark-50">{{ t(`admin.modelAttributes.fields.${field}`) }}</span>
              <Select class="sm:w-56" :aria-label="`${t(`admin.modelAttributes.fields.${field}`)} · ${t('admin.modelAttributes.thinkingState')}`" :model-value="String(modelValue.conditions[field]?.thinking ?? '')" :options="thinkingStates" @update:model-value="setCondition(field, 'thinking', $event === '' ? undefined : $event === 'true')" />
            </div>
            <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
              <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.modelAttributes.efforts') }}</span>
              <label v-for="effort in reasoningEfforts" :key="effort" class="flex items-center gap-2 text-sm">
                <input type="checkbox" class="rounded-compact" :checked="modelValue.conditions[field]?.efforts?.includes(effort)" @change="toggleConditionEffort(field, effort, checked($event))" />
                {{ effort }}
              </label>
            </div>
          </div>
        </SettingsSubpanel>
      </div>
    </ModelAttributeDisclosure>
    <ModelAttributeDisclosure class="mt-4" :title="t('admin.modelAttributes.media')" :meta="configuredText(Object.keys(modelValue.media ?? {}).length)">
      <p class="input-hint mb-4">{{ t('admin.modelAttributes.listHint') }}</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <div v-for="field in mediaListFields" :key="field">
          <label class="input-label" :for="`${uid}-${field}`">{{ t(`admin.modelAttributes.mediaFields.${field}`) }}</label>
          <ModelAttributeListInput :id="`${uid}-${field}`" :model-value="modelValue.media?.[field]" :placeholder="t('admin.modelAttributes.inherit')" @update:model-value="setMedia(field, $event)" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.modelAttributes.mediaFields.input_fidelity') }}</label>
          <Select :aria-label="t('admin.modelAttributes.mediaFields.input_fidelity')" :model-value="String(modelValue.media?.input_fidelity ?? '')" :options="booleanModes" @update:model-value="setMedia('input_fidelity', $event === '' ? undefined : $event === 'true')" />
        </div>
        <label v-for="field in mediaLimitFields" :key="field">
          <span class="input-label">{{ t(`admin.modelAttributes.mediaFields.${field}`) }}</span>
          <input class="input" type="number" min="0" step="1" :value="modelValue.media?.[field] ?? ''" :placeholder="t('admin.modelAttributes.inherit')" @input="setMedia(field, numberValue($event))" />
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
} from '@/types/modelAttributes'

const props = defineProps<{ modelValue: ModelAttributes }>()
const emit = defineEmits<{ 'update:modelValue': [ModelAttributes] }>()
const { t } = useI18n()
const uid = useId()
const effortChoices = [null, ...reasoningEfforts]

// 自定义推理且一项都没勾选时，保存值和“不可调节”同为空数组，这个标记让编辑中的面板保持展开。
const customReasoning = ref(false)
const reasoningMode = computed(() => {
  const options = props.modelValue.reasoning_options
  if (options === undefined) return 'inherit'
  return options.length || customReasoning.value ? 'custom' : 'none'
})
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
const interleavedValue = computed(() => props.modelValue.interleaved === true ? 'true' : props.modelValue.interleaved?.field ?? '')
const selectedEfforts = computed(() => props.modelValue.reasoning_options?.find(option => option.type === 'effort')?.values ?? [])
const budget = computed(() => props.modelValue.reasoning_options?.find(option => option.type === 'budget_tokens'))
const reasoningCount = computed(() => (['reasoning_options', 'interleaved', 'conditions'] as const)
  .filter(key => props.modelValue[key] !== undefined).length)

function configuredText(count: number) {
  return count ? t('admin.modelAttributes.configuredCount', { count }) : ''
}
function effortLabel(value: string | null) {
  return value ?? t('admin.modelAttributes.vendorDefault')
}
function checked(event: Event) {
  return (event.target as HTMLInputElement).checked
}
function numberValue(event: Event) {
  const value = (event.target as HTMLInputElement).value
  return value === '' ? undefined : Number(value)
}

// 删除属性键代表继承。
function set<K extends keyof ModelAttributes>(key: K, value: ModelAttributes[K]) {
  const next = { ...props.modelValue, [key]: value }
  if (value === undefined) delete next[key]
  emit('update:modelValue', next)
}
function hasOption(type: ReasoningOption['type']) {
  return props.modelValue.reasoning_options?.some(option => option.type === type) ?? false
}
function setReasoningMode(mode: string) {
  customReasoning.value = mode === 'custom'
  set('reasoning_options', mode === 'inherit' ? undefined : [])
}
function replaceOption(type: ReasoningOption['type'], option?: ReasoningOption) {
  const options = (props.modelValue.reasoning_options ?? []).filter(value => value.type !== type)
  if (option) options.push(option)
  set('reasoning_options', options)
}
function toggleOption(type: ReasoningOption['type'], enabled: boolean) {
  replaceOption(type, enabled ? { type } : undefined)
}
// 档位按固定顺序保存，勾选顺序不影响结果。
function toggleEffort(effort: string | null, enabled: boolean) {
  const values = effortChoices.filter(choice => choice === effort ? enabled : selectedEfforts.value.includes(choice))
  replaceOption('effort', values.length ? { type: 'effort', values } : undefined)
}
function setBudget(field: 'min' | 'max', value: number | undefined) {
  const next: ReasoningOption = { ...budget.value, type: 'budget_tokens', [field]: value }
  if (value === undefined) delete next[field]
  replaceOption('budget_tokens', next)
}
function toggleConditionEffort(field: ConditionalCapability, effort: string, enabled: boolean) {
  const current = props.modelValue.conditions?.[field]?.efforts ?? []
  setCondition(field, 'efforts', reasoningEfforts.filter(choice => choice === effort ? enabled : current.includes(choice)))
}
function setCondition(field: ConditionalCapability, key: keyof ParameterCondition, value: string[] | boolean | undefined) {
  const condition = { ...props.modelValue.conditions?.[field], [key]: value }
  if (value === undefined || Array.isArray(value) && !value.length) delete condition[key]
  const conditions = { ...props.modelValue.conditions, [field]: condition }
  if (!Object.keys(condition).length) delete conditions[field]
  set('conditions', conditions)
}
function setMedia<K extends keyof MediaCapabilities>(key: K, value: MediaCapabilities[K]) {
  const media = { ...props.modelValue.media, [key]: value }
  if (value === undefined) delete media[key]
  set('media', Object.keys(media).length ? media : undefined)
}
function setInterleaved(value: string) {
  if (value === 'reasoning_content' || value === 'reasoning_details') set('interleaved', { field: value })
  else set('interleaved', value === 'true' ? true : undefined)
}
</script>
