<template>
  <ModelAttributeDisclosure :title="t('admin.modelAttributes.routeMatch')" :meta="matchSummary">
    <div class="grid gap-4 sm:grid-cols-3">
      <div v-for="field in fields" :key="field">
        <label class="input-label">{{ t(`admin.modelAttributes.routeFields.${field}`) }}</label>
        <Select :aria-label="t(`admin.modelAttributes.routeFields.${field}`)" :model-value="modelValue?.[field] ?? ''" :options="optionsFor(field)" @update:model-value="set(field, String($event))" />
      </div>
    </div>
    <p v-if="protocolCatalogError" class="input-hint text-amber-700 dark:text-amber-300">{{ t('admin.modelAttributes.protocolsUnavailable') }}</p>
  </ModelAttributeDisclosure>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import ModelAttributeDisclosure from './ModelAttributeDisclosure.vue'
import { loadProtocolCatalog, protocolCatalog, protocolCatalogError } from '@/api/admin/protocolCapabilities'
import type { AttributeRouteMatch } from '@/api/admin/modelAttributes'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

type RouteField = keyof AttributeRouteMatch
type Option = { value: string; label: string }

const props = defineProps<{ modelValue?: AttributeRouteMatch }>()
const emit = defineEmits<{ 'update:modelValue': [AttributeRouteMatch | undefined] }>()
const { t } = useI18n()
const fields = ['platform', 'provider_type', 'provider_mode', 'site', 'client_protocol', 'upstream_protocol'] as const

// 取值和后端 ModelAttributeMatch.Validate 的白名单一致，文案沿用提供商页面。
const fixedOptions = computed<Record<'platform' | 'provider_type' | 'provider_mode' | 'site', Option[]>>(() => ({
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

function knownOptions(field: RouteField): Option[] {
  if (field !== 'client_protocol' && field !== 'upstream_protocol') return fixedOptions.value[field]
  return (protocolCatalog.value?.protocols ?? [])
    .filter(item => field === 'upstream_protocol' || !item.upstream_only)
    .map(item => ({ value: item.id, label: item.name }))
}
function valueLabel(field: RouteField, value: string) {
  return knownOptions(field).find(option => option.value === value)?.label ?? value
}
// 协议目录加载失败时，已保存的值仍作为选项保留。
function optionsFor(field: RouteField) {
  const options = [...knownOptions(field)]
  const selected = props.modelValue?.[field]
  if (selected && !options.some(option => option.value === selected)) options.push({ value: selected, label: selected })
  return [{ value: '', label: t('admin.modelAttributes.allRoutes') }, ...options]
}
const matchSummary = computed(() => {
  const labels = fields.flatMap(field => {
    const value = props.modelValue?.[field]
    return value ? [valueLabel(field, value)] : []
  })
  return labels.length ? labels.join(' · ') : t('admin.modelAttributes.allRoutes')
})

function set(field: RouteField, value: string) {
  const match = { ...props.modelValue, [field]: value }
  if (!value) delete match[field]
  emit('update:modelValue', Object.keys(match).length ? match : undefined)
}
onMounted(() => { void loadProtocolCatalog().catch(() => {}) })
</script>
