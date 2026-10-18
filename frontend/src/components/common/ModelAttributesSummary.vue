<template>
  <div :class="isTooltip ? 'space-y-3' : 'space-y-4 text-sm'">
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
          <ModalityIcon
            v-for="modality in inputModalities"
            :key="`input-${modality}`"
            :data-modality="`input-${modality}`"
            :modality="modality"
            :tone="modalityTone"
            :state="modalityState(modality, 'input_modalities')"
          />
        </template>
        <span v-else :class="mutedClass">{{ emptyModalityText(attributes.input_modalities) }}</span>
        <Icon name="moveRight" size="xs" :class="mutedClass" />
        <template v-if="outputModalities.length">
          <ModalityIcon
            v-for="modality in outputModalities"
            :key="`output-${modality}`"
            :data-modality="`output-${modality}`"
            :modality="modality"
            :tone="modalityTone"
            :state="modalityState(modality, 'output_modalities')"
          />
        </template>
        <span v-else :class="mutedClass">{{ emptyModalityText(attributes.output_modalities) }}</span>
      </span>
    </div>

    <p v-if="hasUnknownModalities" :class="mutedClass">{{ t('admin.modelAttributes.unknown') }}</p>

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
          :class="stateClass(capabilityState(attributes[field], field))"
          role="img"
          :aria-label="capabilityText(attributes[field], field)"
        />
      </li>
    </ul>
    <!-- 推理、参数条件和媒体参数只列出已知的项。 -->
    <dl v-if="detailRows.length" class="border-t pt-3" :class="[dividerClass, isTooltip ? 'space-y-1.5' : 'space-y-2']" data-testid="model-attributes-details">
      <div v-for="row in detailRows" :key="row.key" class="flex items-baseline justify-between gap-4">
        <dt class="shrink-0" :class="labelClass">{{ row.label }}</dt>
        <dd class="min-w-0 break-words text-right" :class="row.muted ? mutedClass : valueClass">{{ row.value }}</dd>
      </div>
    </dl>
    <div v-if="detailVariants.length" class="space-y-3 border-t pt-3" :class="dividerClass" data-testid="model-attributes-variants">
      <section v-for="(rows, index) in detailVariants" :key="index" class="space-y-1.5">
        <p :class="labelClass">{{ t('marketplace.attributeVariant', { index: index + 1 }) }}</p>
        <dl class="space-y-1.5">
          <div v-for="row in rows" :key="row.key" class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0" :class="labelClass">{{ row.label }}</dt>
            <dd class="min-w-0 break-words text-right" :class="row.muted ? mutedClass : valueClass">{{ row.value }}</dd>
          </div>
        </dl>
      </section>
    </div>
    <slot />
    <p v-if="attributes.route_differences" class="flex items-start gap-1.5" :class="isTooltip ? 'text-amber-300' : 'text-amber-700 dark:text-amber-300'">
      <Icon name="exclamationTriangle" size="xs" class="mt-0.5 h-3.5 w-3.5" />
      <span>{{ t(routeDifferencesKey) }}</span>
    </p>
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
import { attributeCapabilities, attributeLimits, attributeModalities, conditionalCapabilities, mediaLimitFields, mediaListFields, type ConditionalCapability, type ModelAttributes, type ModelAttributePresentation, type ParameterCondition, type ReasoningOption } from '@/types/modelAttributes'

// ModelAttributesSummary.vue renders model limits, modalities, and capability
// details in tooltips and administrative views. Unknown values remain distinct
// from unsupported capabilities. Protocol selection supplies active attributes
// alongside the model's overall modality baseline, keeping every advertised icon
// in place while its selected-route state changes. Callers choose the explanation
// for route differences according to their aggregation rules.
//
// Marketplace presentations can retain alternative attribute combinations when
// request conditions or reasoning controls differ. Linked values are shown in
// separate groups so opposite thinking requirements and disjoint budget ranges
// remain readable as alternatives. Media lists and ceilings use their supplied
// summary values, and administrative consumers omit these optional display inputs.

const props = withDefaults(defineProps<{
  attributes: ModelAttributePresentation
  // ModalityBaseline retains the overall icons while selected protocol attributes
  // determine their support state. Omission renders the supplied attributes directly.
  modalityBaseline?: ModelAttributes
  // RouteDifferencesKey selects the explanation appropriate to the aggregation consumer.
  routeDifferencesKey?: string
  // tooltip 变体用于深色浮层：反色文字，并省略标题旁已展示的显示名。
  variant?: 'default' | 'tooltip'
}>(), {
  variant: 'default',
  routeDifferencesKey: 'admin.modelAttributes.routeDifferences',
})
const { t } = useI18n()

type CapabilityState = 'supported' | 'unsupported' | 'unknown' | 'conditional'

const CAPABILITY_ICONS: Record<typeof attributeCapabilities[number], IconName> = {
  reasoning: 'brain',
  tool_call: 'toolCall',
  structured_output: 'structuredOutput',
  temperature: 'temperature',
  top_p: 'sliders',
  attachment: 'attachment',
}

const STATE_ICONS: Record<CapabilityState, IconName> = {
  supported: 'check',
  unsupported: 'x',
  unknown: 'minus',
  conditional: 'exclamationCircle',
}

const isTooltip = computed(() => props.variant === 'tooltip')
const textFields = computed(() => [
  ...(isTooltip.value ? [] : ['display_name'] as const),
  ...attributeLimits,
] as const)

// 提示浮层在两种主题下都是深底，只使用深色配色；默认变体跟随主题。
const labelClass = computed(() => isTooltip.value ? 'text-gray-400 dark:text-dark-400' : 'text-gray-500 dark:text-dark-400')
const valueClass = computed(() => isTooltip.value ? 'text-white dark:text-dark-100' : 'text-gray-900 dark:text-dark-50')
const mutedClass = computed(() => isTooltip.value ? 'text-gray-400 dark:text-dark-400' : 'text-gray-400 dark:text-dark-500')
const dividerClass = computed(() => isTooltip.value ? 'border-white/10 dark:border-dark-600' : 'border-gray-100 dark:border-dark-700')
const chipClass = computed(() => isTooltip.value ? 'bg-white/5 dark:bg-dark-800' : 'bg-gray-50 dark:bg-dark-800')
const checkClass = computed(() => isTooltip.value ? 'text-emerald-400' : 'text-emerald-600 dark:text-emerald-400')
const conditionalClass = computed(() => isTooltip.value ? 'text-amber-300' : 'text-amber-600 dark:text-amber-300')
const modalityTone = computed(() => isTooltip.value ? 'inverse' : 'auto')

// Modality lists use the overall baseline when present, retaining icon positions
// while the selected attributes determine support, rejection, or missing evidence.
const inputModalities = computed(() => knownModalities((props.modalityBaseline ?? props.attributes).input_modalities))
const outputModalities = computed(() => knownModalities((props.modalityBaseline ?? props.attributes).output_modalities))

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

type CapabilityField = typeof attributeCapabilities[number]

function isConditional(field: CapabilityField): field is ConditionalCapability {
  return (conditionalCapabilities as readonly string[]).includes(field)
}

// capabilityState keeps request-dependent support visible when a marketplace
// union retains alternative restrictions. Missing evidence remains unknown, while
// explicit false values retain the unsupported state used by administrative views.
function capabilityState(value: boolean | undefined, field: CapabilityField): CapabilityState {
  if (value === undefined || value === null) return 'unknown'
  if (value && isConditional(field) && (props.attributes.conditions?.[field] || props.attributes.variants?.some(route => route[field] && route.conditions?.[field]))) return 'conditional'
  return value ? 'supported' : 'unsupported'
}

function capabilityText(value: boolean | undefined, field: CapabilityField) {
  return t(`admin.modelAttributes.${capabilityState(value, field)}`)
}

function stateClass(state: CapabilityState) {
  if (state === 'supported') return checkClass.value
  return state === 'conditional' ? conditionalClass.value : mutedClass.value
}

interface DetailRow { key: string; label: string; value: string; muted?: boolean }

// linkedVariants retains distinct combinations of conditions and reasoning controls.
// Variants differing only in modalities or ceilings do not duplicate detail sections.
const linkedVariants = computed(() => {
  const seen = new Set<string>()
  return (props.attributes.variants ?? []).filter(value => {
    const key = JSON.stringify([value.reasoning_options, value.interleaved, value.conditions,
      ...conditionalCapabilities.map(field => value[field])])
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
})

// detailRows separates aggregate media fields from linked route-specific controls.
// Alternative groups take ownership of the reasoning and condition rows below.
const detailRows = computed<DetailRow[]>(() => [
  ...(linkedVariants.value.length > 1 ? [] : [
    ...reasoningRows(props.attributes.reasoning_options),
    ...interleavedRows(props.attributes.interleaved),
    ...conditionalCapabilities.flatMap(field => conditionRows(field, props.attributes.conditions?.[field])),
  ]),
  ...mediaRows(props.attributes.media),
])

// detailVariants displays exact linked controls rather than combining incompatible
// thinking requirements or filling gaps between separate reasoning budget intervals.
const detailVariants = computed(() => linkedVariants.value.length > 1 ? linkedVariants.value.map(value => [
  ...reasoningRows(value.reasoning_options),
  ...interleavedRows(value.interleaved),
  ...conditionalCapabilities.flatMap(field => [
    { key: `capability.${field}`, label: t(`admin.modelAttributes.fields.${field}`),
      value: t(`admin.modelAttributes.${value[field] === undefined ? 'unknown' : value[field] ? 'supported' : 'unsupported'}`) },
    ...conditionRows(field, value.conditions?.[field]),
  ]),
]) : [])

// modalityState distinguishes unknown route evidence from an explicitly rejected
// choice. Positive support in any retained variant makes that modality available.
function modalityState(modality: ModelModality, field: 'input_modalities' | 'output_modalities'): 'supported' | 'unsupported' | 'unknown' {
  const values = props.attributes[field]
  if (values?.includes(modality)) return 'supported'
  const variants = props.attributes.variants ?? [props.attributes]
  return variants.some(value => value[field] === undefined) ? 'unknown' : 'unsupported'
}

// hasUnknownModalities adds a visible label when neutral icons represent missing
// route evidence, complementing each icon's accessible state and dashed outline.
const hasUnknownModalities = computed(() => inputModalities.value.some(value => modalityState(value, 'input_modalities') === 'unknown') ||
  outputModalities.value.some(value => modalityState(value, 'output_modalities') === 'unknown'))

// reasoningRows formats declared controls while preserving separate budget records.
// Missing controls have no rows; an explicit empty list reports that controls
// cannot be adjusted, keeping it distinct from unknown catalogue information.
function reasoningRows(options: ReasoningOption[] | undefined): DetailRow[] {
  if (options === undefined) return []
  if (!options.length) return [{ key: 'reasoning_options', label: t('admin.modelAttributes.fields.reasoning_options'), value: t('admin.modelAttributes.notAdjustable'), muted: true }]
  return options.map((option, index) => {
    const key = `reasoning.${option.type}.${index}`
    if (option.type === 'toggle') return { key, label: t('admin.modelAttributes.thinkingToggle'), value: t('admin.modelAttributes.supported') }
    if (option.type === 'effort') {
      const values = option.values?.map(value => value ?? t('admin.modelAttributes.vendorDefault')).join(', ')
      return { key, label: t('admin.modelAttributes.efforts'), value: values || t('admin.modelAttributes.none') }
    }
    return { key, label: t('admin.modelAttributes.tokenBudget'), value: `${formatValue(option.min)} ~ ${formatValue(option.max)}` }
  })
}

function interleavedRows(value: ModelAttributes['interleaved']): DetailRow[] {
  if (value === undefined) return []
  return [{ key: 'interleaved', label: t('admin.modelAttributes.fields.interleaved'), value: value === true ? t('admin.modelAttributes.supported') : value.field }]
}

function conditionRows(field: ConditionalCapability, condition: ParameterCondition | undefined): DetailRow[] {
  const parts = [
    condition?.efforts?.length ? `${t('admin.modelAttributes.efforts')} ${condition.efforts.join(', ')}` : '',
    condition?.thinking === undefined ? '' : t(`admin.modelAttributes.${condition.thinking ? 'thinkingOn' : 'thinkingOff'}`),
  ].filter(Boolean)
  if (!parts.length) return []
  return [{ key: `conditions.${field}`, label: `${t(`admin.modelAttributes.fields.${field}`)} · ${t('admin.modelAttributes.fields.conditions')}`, value: parts.join(' · ') }]
}

function mediaRows(media: ModelAttributes['media']): DetailRow[] {
  if (!media) return []
  const rows: DetailRow[] = []
  for (const field of mediaListFields) {
    const values = media[field]
    if (values?.length) rows.push({ key: `media.${field}`, label: t(`admin.modelAttributes.mediaFields.${field}`), value: values.join(', ') })
  }
  for (const field of mediaLimitFields) {
    const value = media[field]
    if (value !== undefined) rows.push({ key: `media.${field}`, label: t(`admin.modelAttributes.mediaFields.${field}`), value: formatValue(value) })
  }
  if (media.input_fidelity !== undefined) {
    rows.push({ key: 'media.input_fidelity', label: t('admin.modelAttributes.mediaFields.input_fidelity'), value: t(`admin.modelAttributes.${media.input_fidelity ? 'supported' : 'unsupported'}`) })
  }
  return rows
}
</script>
