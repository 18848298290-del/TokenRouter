<template>
  <ModelAttributeDisclosure :title="t('admin.modelAttributes.routeMatch')" :meta="matchSummary">
    <div class="grid gap-4 sm:grid-cols-3">
      <div v-for="field in fields" :key="field">
        <label class="input-label">{{ t(`admin.modelAttributes.routeFields.${field}`) }}</label>
        <Select
          :aria-label="t(`admin.modelAttributes.routeFields.${field}`)"
          :model-value="modelValue?.[field] ?? ''"
          :options="optionsFor(field)"
          @update:model-value="set(field, String($event))"
        />
      </div>
    </div>
    <p v-if="protocolCatalogError" class="input-hint text-amber-700 dark:text-amber-300">
      {{ t('admin.modelAttributes.protocolsUnavailable') }}
    </p>
  </ModelAttributeDisclosure>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import ModelAttributeDisclosure from './ModelAttributeDisclosure.vue'
import {
  loadProtocolCatalog,
  protocolCatalog,
  protocolCatalogError,
} from '@/api/admin/protocolCapabilities'
import type { AttributeRouteMatch } from '@/api/admin/modelAttributes'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

// ModelAttributeRouteFields.vue edits the route dimensions that limit an attribute rule.
// Provider choices use fixed domain values and protocol choices use the shared catalog. Saved
// protocol identifiers remain selectable when absent from the catalog, and clearing every
// dimension emits an inherited route match.

// RouteField identifies one route dimension accepted by an attribute rule.
type RouteField = keyof AttributeRouteMatch
// Option pairs a route selector value with its displayed label.
type Option = {
  value: string // stored selector value
  label: string // visible option label
}

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  // modelValue restricts an attribute rule to configured route dimensions. Undefined or an empty
  // match leaves the rule applicable to all routes.
  modelValue?: AttributeRouteMatch
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [AttributeRouteMatch | undefined] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// fields fixes the order of provider and protocol dimensions shown in the route match editor
// and summary.
const fields = [
  'platform',
  'provider_type',
  'provider_mode',
  'site',
  'client_protocol',
  'upstream_protocol',
] as const

// fixedOptions supplies supported provider dimensions, while protocol dimensions are read from
// the shared capability catalog by knownOptions.
const fixedOptions = computed<
  Record<'platform' | 'provider_type' | 'provider_mode' | 'site', Option[]>
>(() => ({
  platform: [...CONCRETE_PLATFORM_OPTIONS],
  provider_type: [
    { value: 'apikey', label: t('admin.providers.apiKey') },
    { value: 'oauth', label: t('admin.providers.oauthType') },
    { value: 'setup-token', label: t('admin.providers.setupToken') },
    { value: 'service_account', label: t('admin.providers.serviceAccount') },
    { value: 'bedrock', label: 'AWS Bedrock' },
    { value: 'cosy', label: t('admin.providers.types.qoderCosy') },
    { value: 'upstream', label: t('admin.providers.types.upstream') },
  ],
  provider_mode: [
    { value: 'payg', label: t('admin.providers.cnProviders.providerMode.payg') },
    { value: 'coding', label: t('admin.providers.cnProviders.providerMode.coding') },
  ],
  site: [
    { value: 'cn', label: t('admin.providers.qoder.site.cn') },
    { value: 'global', label: t('admin.providers.qoder.site.global') },
  ],
}))

// knownOptions returns choices for the requested route dimension. Fixed provider fields use
// local options, while client protocols exclude upstream-only entries from the shared catalog
// and upstream protocols include them.
function knownOptions(field: RouteField): Option[] {
  if (field !== 'client_protocol' && field !== 'upstream_protocol') return fixedOptions.value[field]
  return (protocolCatalog.value?.protocols ?? [])
    .filter((item) => field === 'upstream_protocol' || !item.upstream_only)
    .map((item) => ({ value: item.id, label: item.name }))
}
// valueLabel resolves a route value through the current dimension options. Unknown values
// display their raw identifier so saved conditions remain understandable before or after
// catalog loading.
function valueLabel(field: RouteField, value: string) {
  return knownOptions(field).find((option) => option.value === value)?.label ?? value
}

// optionsFor adds an unrestricted choice and preserves the currently selected route value. If
// the current value is absent from known options, it is appended with its raw identifier as the
// label.
function optionsFor(field: RouteField) {
  const options = [...knownOptions(field)]
  const selected = props.modelValue?.[field]
  if (selected && !options.some((option) => option.value === selected))
    options.push({ value: selected, label: selected })
  return [{ value: '', label: t('admin.modelAttributes.allRoutes') }, ...options]
}
// matchSummary joins configured route labels so the collapsed disclosure still identifies the
// dimensions restricting the rule.
const matchSummary = computed(() => {
  const labels = fields.flatMap((field) => {
    const value = props.modelValue?.[field]
    return value ? [valueLabel(field, value)] : []
  })
  return labels.length ? labels.join(' · ') : t('admin.modelAttributes.allRoutes')
})

// set publishes one changed route dimension in a copied match object. Clearing a value deletes
// its key, and removing the final dimension emits undefined to match all routes.
function set(field: RouteField, value: string) {
  const match = { ...props.modelValue, [field]: value }
  if (!value) delete match[field]
  emit('update:modelValue', Object.keys(match).length ? match : undefined)
}
// Load the shared protocol catalog after mounting; its own error state drives feedback while
// saved raw protocol values remain selectable.
onMounted(() => {
  void loadProtocolCatalog().catch(() => {})
})
</script>
