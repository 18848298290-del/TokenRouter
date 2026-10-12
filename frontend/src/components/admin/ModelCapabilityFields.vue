<template>
  <div class="space-y-4">
    <fieldset class="space-y-4 rounded-control border border-gray-200 p-4 dark:border-dark-600">
      <legend class="px-1 text-sm font-medium">{{ t('admin.modelAttributes.reasoningOptions') }}</legend>
      <Select :aria-label="t('admin.modelAttributes.reasoningOptions')" :model-value="reasoningMode" :options="reasoningModes" @update:model-value="setReasoningMode(String($event))" />
      <div v-if="reasoningMode === 'custom'" class="grid gap-4 sm:grid-cols-2">
        <label class="flex items-center gap-2 text-sm sm:col-span-2">
          <input type="checkbox" class="rounded-compact" :checked="hasOption('toggle')" @change="toggleOption('toggle', ($event.target as HTMLInputElement).checked)" />
          {{ t('admin.modelAttributes.thinkingToggle') }}
        </label>
        <label class="sm:col-span-2"><span class="input-label">{{ t('admin.modelAttributes.efforts') }}</span>
          <input class="input" :value="efforts" placeholder="low, medium, high, xhigh, max" @input="setEfforts(($event.target as HTMLInputElement).value)" />
        </label>
        <label class="flex items-center gap-2 text-sm sm:col-span-2">
          <input type="checkbox" class="rounded-compact" :checked="hasOption('budget_tokens')" @change="toggleOption('budget_tokens', ($event.target as HTMLInputElement).checked)" />
          {{ t('admin.modelAttributes.tokenBudget') }}
        </label>
        <template v-if="hasOption('budget_tokens')">
          <label v-for="field in ['min', 'max'] as const" :key="field"><span class="input-label">{{ t(`admin.modelAttributes.budget.${field}`) }}</span>
            <input class="input" type="number" :min="field === 'min' ? -1 : 0" step="1" :value="budget?.[field] ?? ''" @input="setBudget(field, ($event.target as HTMLInputElement).value)" />
          </label>
        </template>
      </div>
    </fieldset>
    <fieldset class="space-y-4 rounded-control border border-gray-200 p-4 dark:border-dark-600">
      <legend class="px-1 text-sm font-medium">{{ t('admin.modelAttributes.conditions') }}</legend>
      <Select :aria-label="t('admin.modelAttributes.conditions')" :model-value="modelValue.conditions === undefined ? 'inherit' : 'custom'" :options="overrideModes" @update:model-value="set('conditions', $event === 'inherit' ? undefined : {})" />
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.modelAttributes.conditionsHint') }}</p>
      <div v-if="modelValue.conditions !== undefined" class="space-y-4">
        <div v-for="field in conditionalFields" :key="field" class="grid gap-4 sm:grid-cols-2">
          <label><span class="input-label">{{ t(`admin.modelAttributes.fields.${field}`) }} · {{ t('admin.modelAttributes.efforts') }}</span>
            <input class="input" :value="modelValue.conditions[field]?.efforts?.join(', ') ?? ''" placeholder="none" @input="setCondition(field, 'efforts', words(($event.target as HTMLInputElement).value))" />
          </label>
          <div><label class="input-label">{{ t('admin.modelAttributes.thinkingState') }}</label>
            <Select :model-value="String(modelValue.conditions[field]?.thinking ?? '')" :options="thinkingStates" @update:model-value="setCondition(field, 'thinking', $event === '' ? undefined : $event === 'true')" />
          </div>
        </div>
      </div>
    </fieldset>
    <div>
      <label class="input-label">{{ t('admin.modelAttributes.interleaved') }}</label>
      <Select :model-value="interleavedValue" :options="interleavedOptions" @update:model-value="setInterleaved(String($event))" />
    </div>
    <fieldset class="grid gap-4 rounded-control border border-gray-200 p-4 dark:border-dark-600 sm:grid-cols-2">
      <legend class="px-1 text-sm font-medium">{{ t('admin.modelAttributes.media') }}</legend>
      <label v-for="field in mediaLists" :key="field"><span class="input-label">{{ t(`admin.modelAttributes.mediaFields.${field}`) }}</span>
        <input class="input" :value="modelValue.media?.[field]?.join(', ') ?? ''" :placeholder="t('admin.modelAttributes.inherit')" @input="setMedia(field, ($event.target as HTMLInputElement).value ? words(($event.target as HTMLInputElement).value) : undefined)" />
      </label>
      <div><label class="input-label">{{ t('admin.modelAttributes.mediaFields.input_fidelity') }}</label>
        <Select :model-value="String(modelValue.media?.input_fidelity ?? '')" :options="booleanModes" @update:model-value="setMedia('input_fidelity', $event === '' ? undefined : $event === 'true')" />
      </div>
      <label v-for="field in ['max_reference_images', 'max_output_count'] as const" :key="field"><span class="input-label">{{ t(`admin.modelAttributes.mediaFields.${field}`) }}</span>
        <input class="input" type="number" min="0" step="1" :value="modelValue.media?.[field] ?? ''" :placeholder="t('admin.modelAttributes.inherit')" @input="setMedia(field, ($event.target as HTMLInputElement).value === '' ? undefined : Number(($event.target as HTMLInputElement).value))" />
      </label>
    </fieldset>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import type { ModelAttributes, ReasoningOption, ParameterCondition, MediaCapabilities } from '@/types/modelAttributes'

const props = defineProps<{ modelValue: ModelAttributes }>()
const emit = defineEmits<{ 'update:modelValue': [ModelAttributes] }>()
const { t } = useI18n()
const customReasoning = ref(false)
const conditionalFields = ['temperature', 'top_p', 'tool_call'] as const
const mediaLists = ['image_sizes', 'aspect_ratios', 'qualities', 'background_options', 'output_formats', 'video_operations', 'video_parameters'] as const
const reasoningMode = computed(() => props.modelValue.reasoning_options === undefined ? 'inherit' : props.modelValue.reasoning_options.length || customReasoning.value ? 'custom' : 'none')
const reasoningModes = computed(() => ['inherit', 'none', 'custom'].map(value => ({ value, label: t(`admin.modelAttributes.${value === 'none' ? 'notAdjustable' : value}`) })))
const overrideModes = computed(() => ['inherit', 'custom'].map(value => ({ value, label: t(`admin.modelAttributes.${value}`) })))
const booleanModes = computed(() => [{ value: '', label: t('admin.modelAttributes.inherit') }, { value: 'true', label: t('admin.modelAttributes.supported') }, { value: 'false', label: t('admin.modelAttributes.unsupported') }])
const thinkingStates = computed(() => [{ value: '', label: t('admin.modelAttributes.anyState') }, { value: 'true', label: t('admin.modelAttributes.thinkingOn') }, { value: 'false', label: t('admin.modelAttributes.thinkingOff') }])
const efforts = computed(() => props.modelValue.reasoning_options?.find(option => option.type === 'effort')?.values?.map(value => value ?? 'default').join(', ') ?? '')
const budget = computed(() => props.modelValue.reasoning_options?.find(option => option.type === 'budget_tokens'))
const interleavedValue = computed(() => props.modelValue.interleaved === true ? 'true' : props.modelValue.interleaved?.field ?? '')
const interleavedOptions = computed(() => [{ value: '', label: t('admin.modelAttributes.inherit') }, { value: 'true', label: t('admin.modelAttributes.supported') }, ...['reasoning_content', 'reasoning_details'].map(value => ({ value, label: value }))])
function words(value: string) { return [...new Set(value.split(/[,，]/).map(item => item.trim()).filter(Boolean))] }
function set<K extends keyof ModelAttributes>(key: K, value: ModelAttributes[K]) {
  const next = { ...props.modelValue, [key]: value }
  if (value === undefined) delete next[key]
  emit('update:modelValue', next)
}
function hasOption(type: ReasoningOption['type']) { return props.modelValue.reasoning_options?.some(option => option.type === type) ?? false }
function setReasoningMode(mode: string) {
  customReasoning.value = mode === 'custom'
  set('reasoning_options', mode === 'inherit' ? undefined : [])
}
function replaceOption(type: ReasoningOption['type'], option?: ReasoningOption) {
  const values = (props.modelValue.reasoning_options ?? []).filter(value => value.type !== type)
  if (option) values.push(option)
  set('reasoning_options', values)
}
function toggleOption(type: ReasoningOption['type'], enabled: boolean) { replaceOption(type, enabled ? { type } : undefined) }
function setEfforts(value: string) {
  const values = words(value)
  replaceOption('effort', values.length ? { type: 'effort', values } : undefined)
}
function setBudget(field: 'min' | 'max', value: string) {
  const next = { ...budget.value, type: 'budget_tokens' as const, [field]: value === '' ? undefined : Number(value) }
  if (value === '') delete next[field]
  replaceOption('budget_tokens', next)
}
function setCondition(field: typeof conditionalFields[number], key: keyof ParameterCondition, value: string[] | boolean | undefined) {
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
  set('interleaved', value === 'true' ? true : value === 'reasoning_content' || value === 'reasoning_details' ? { field: value } : undefined)
}
</script>
