<template>
  <div :class="isTooltip ? 'space-y-3' : 'space-y-4 text-sm'">
    <p v-if="attributes.route_differences" class="flex items-start gap-1.5" :class="isTooltip ? 'text-amber-300' : 'text-amber-700 dark:text-amber-300'">
      <Icon name="exclamationTriangle" size="xs" class="mt-0.5 h-3.5 w-3.5" />
      <span>{{ t('admin.modelAttributes.routeDifferences') }}</span>
    </p>

    <!-- 数值上限保留文字标签，数字右对齐并使用等宽数字方便比较。 -->
    <dl :class="isTooltip ? 'space-y-1.5' : 'space-y-2'">
      <div v-for="field in textFields" :key="field" class="flex items-baseline justify-between gap-4">
        <dt class="shrink-0" :class="labelClass">{{ t(`admin.modelAttributes.fields.${field}`) }}</dt>
        <dd class="min-w-0 break-words text-right tabular-nums" :class="valueClass">{{ formatValue(attributes[field]) }}</dd>
      </div>
    </dl>

    <!-- 模态与模型卡片右上角一致：输入图标 -> 输出图标。 -->
    <div class="flex items-center justify-between gap-4 border-t pt-3" :class="dividerClass" data-testid="model-attributes-modalities">
      <span class="shrink-0" :class="labelClass">{{ t('admin.modelAttributes.fields.modalities') }}</span>
      <span class="flex min-w-0 flex-wrap items-center justify-end gap-1">
        <template v-if="inputModalities.length">
          <ModalityIcon v-for="modality in inputModalities" :key="`input-${modality}`" :modality="modality" :tone="modalityTone" />
        </template>
        <span v-else :class="mutedClass">{{ emptyModalityText(attributes.input_modalities) }}</span>
        <Icon name="moveRight" size="xs" :class="mutedClass" />
        <template v-if="outputModalities.length">
          <ModalityIcon v-for="modality in outputModalities" :key="`output-${modality}`" :modality="modality" :tone="modalityTone" />
        </template>
        <span v-else :class="mutedClass">{{ emptyModalityText(attributes.output_modalities) }}</span>
      </span>
    </div>

    <!-- 能力开关用「功能图标 + 名称 + 对勾/叉号」小标签按宽度换行，不支持和未知的项整体弱化。 -->
    <ul class="flex flex-wrap gap-1.5 border-t pt-3" :class="dividerClass" data-testid="model-attributes-capabilities">
      <li
        v-for="field in attributeCapabilities"
        :key="field"
        class="inline-flex items-center gap-1 rounded-compact px-1.5 py-0.5"
        :class="chipClass"
        :data-capability="field"
        :data-state="capabilityState(attributes[field], field)"
        :title="`${t(`admin.modelAttributes.fields.${field}`)} · ${capabilityText(attributes[field], field)}`"
      >
        <Icon :name="CAPABILITY_ICONS[field]" size="xs" class="h-3.5 w-3.5" :class="attributes[field] ? labelClass : mutedClass" />
        <span class="whitespace-nowrap" :class="attributes[field] ? valueClass : mutedClass">{{ t(`admin.modelAttributes.fields.${field}`) }}</span>
        <Icon
          :name="STATE_ICONS[capabilityState(attributes[field], field)]"
          size="xs"
          :stroke-width="2.5"
          :animate-on-hover="false"
          class="h-3.5 w-3.5"
          :class="attributes[field] ? checkClass : mutedClass"
          role="img"
          :aria-label="capabilityText(attributes[field], field)"
        />
      </li>
    </ul>
    <div v-if="attributes.reasoning_options !== undefined" class="space-y-2 border-t pt-3" :class="dividerClass">
      <p :class="labelClass">{{ t('admin.modelAttributes.reasoningOptions') }}</p>
      <p v-if="!attributes.reasoning_options.length" :class="mutedClass">{{ t('admin.modelAttributes.notAdjustable') }}</p>
      <p v-for="option in attributes.reasoning_options" :key="option.type" :class="valueClass">
        <template v-if="option.type === 'toggle'">{{ t('admin.modelAttributes.thinkingToggle') }}</template>
        <template v-else-if="option.type === 'effort'">{{ t('admin.modelAttributes.efforts') }}: {{ option.values?.map(value => value ?? 'default').join(', ') || t('admin.modelAttributes.none') }}</template>
        <template v-else>{{ t('admin.modelAttributes.tokenBudget') }}: {{ option.min ?? t('admin.modelAttributes.unknown') }} ~ {{ option.max ?? t('admin.modelAttributes.unknown') }}</template>
      </p>
    </div>
    <dl v-if="attributes.conditions && Object.keys(attributes.conditions).length" class="space-y-2 border-t pt-3" :class="dividerClass">
      <div v-for="(condition, field) in attributes.conditions" :key="field">
        <dt :class="labelClass">{{ t(`admin.modelAttributes.fields.${field}`) }} · {{ t('admin.modelAttributes.conditions') }}</dt>
        <dd :class="valueClass">{{ conditionText(condition) }}</dd>
      </div>
    </dl>
    <p v-if="attributes.media?.video_operations?.length" :class="valueClass">{{ t('admin.modelAttributes.mediaFields.video_operations') }}: {{ attributes.media.video_operations.join(', ') }}</p>
    <p v-if="attributes.media?.video_parameters?.length" :class="valueClass">{{ t('admin.modelAttributes.mediaFields.video_parameters') }}: {{ attributes.media.video_parameters.join(', ') }}</p>
    <p v-if="attributes.media?.image_sizes?.length" :class="valueClass">{{ t('admin.modelAttributes.media') }}: {{ attributes.media.image_sizes.join(', ') }}</p>
  </div>
</template>

<script setup lang="ts">
import { getLocale } from '@/i18n'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import ModalityIcon from '@/components/common/ModalityIcon.vue'
import type { IconName } from '@/components/icons/registry'
import type { ModelModality } from '@/utils/modelCapabilities'
import { attributeCapabilities, attributeLimits, attributeModalities, type ModelAttributes, type ParameterCondition } from '@/types/modelAttributes'

const props = withDefaults(defineProps<{
  attributes: ModelAttributes
  // tooltip 变体用于深色浮层：反色文字，并省略标题旁已展示的显示名。
  variant?: 'default' | 'tooltip'
}>(), {
  variant: 'default',
})
const { t } = useI18n()

type CapabilityState = 'supported' | 'unsupported' | 'unknown' | 'conditional'

const CAPABILITY_ICONS: Record<typeof attributeCapabilities[number], IconName> = {
  reasoning: 'brain',
  tool_call: 'toolCall',
  structured_output: 'structuredOutput',
  temperature: 'temperature',
  top_p: 'temperature',
  attachment: 'attachment',
}

const STATE_ICONS: Record<CapabilityState, IconName> = {
  supported: 'check',
  unsupported: 'x',
  unknown: 'minus',
  conditional: 'minus',
}

const isTooltip = computed(() => props.variant === 'tooltip')
const textFields = computed(() => [
  ...(isTooltip.value ? [] : ['display_name'] as const),
  ...attributeLimits,
] as const)

// 提示浮层在两种主题下都是深底，只使用深色配色；默认变体跟随主题。
const labelClass = computed(() => isTooltip.value ? 'text-gray-400 dark:text-dark-400' : 'text-gray-500 dark:text-dark-400')
const valueClass = computed(() => isTooltip.value ? 'text-white dark:text-dark-100' : 'text-gray-900 dark:text-dark-50')
const mutedClass = computed(() => isTooltip.value ? 'text-gray-500 dark:text-dark-500' : 'text-gray-400 dark:text-dark-500')
const dividerClass = computed(() => isTooltip.value ? 'border-white/10 dark:border-dark-700' : 'border-gray-100 dark:border-dark-700')
const chipClass = computed(() => isTooltip.value ? 'bg-white/5 dark:bg-dark-800' : 'bg-gray-50 dark:bg-dark-800')
const checkClass = computed(() => isTooltip.value ? 'text-emerald-400' : 'text-emerald-600 dark:text-emerald-400')
const modalityTone = computed(() => isTooltip.value ? 'inverse' : 'auto')

// 按已知模态渲染图标，未知取值跳过。
const inputModalities = computed(() => knownModalities(props.attributes.input_modalities))
const outputModalities = computed(() => knownModalities(props.attributes.output_modalities))

function knownModalities(values: string[] | undefined): ModelModality[] {
  return (values ?? []).filter((value): value is ModelModality => (attributeModalities as readonly string[]).includes(value))
}

function formatValue(value: string | number | undefined) {
  if (value === undefined || value === null) return t('admin.modelAttributes.unknown')
  return typeof value === 'number' ? value.toLocaleString(getLocale()) : value
}

// 未返回模态表示未知，显式空数组表示无。
function emptyModalityText(value: string[] | undefined) {
  return t(value === undefined ? 'admin.modelAttributes.unknown' : 'admin.modelAttributes.none')
}

function capabilityState(value: boolean | undefined, field?: typeof attributeCapabilities[number]): CapabilityState {
  if (value === undefined || value === null) return 'unknown'
  if (value && (field === 'temperature' || field === 'top_p' || field === 'tool_call') && props.attributes.conditions?.[field]) return 'conditional'
  return value ? 'supported' : 'unsupported'
}

function capabilityText(value: boolean | undefined, field?: typeof attributeCapabilities[number]) {
  return t(`admin.modelAttributes.${capabilityState(value, field)}`)
}
function conditionText(condition: ParameterCondition | undefined) {
  if (!condition) return ''
  return [condition.efforts?.length ? `${t('admin.modelAttributes.efforts')}: ${condition.efforts.join(', ')}` : '', condition.thinking === undefined ? '' : t(`admin.modelAttributes.${condition.thinking ? 'thinkingOn' : 'thinkingOff'}`)].filter(Boolean).join(' · ')
}
</script>
