<template>
  <fieldset class="grid gap-4 rounded-control border border-gray-200 p-4 dark:border-dark-600 sm:grid-cols-2">
    <legend class="px-1 text-sm font-medium">{{ t('admin.modelAttributes.routeMatch') }}</legend>
    <div v-for="field in fields" :key="field">
      <label class="input-label">{{ t(`admin.modelAttributes.routeFields.${field}`) }}</label>
      <Select :aria-label="t(`admin.modelAttributes.routeFields.${field}`)" :model-value="modelValue?.[field] ?? ''" :options="optionsFor(field)" @update:model-value="set(field, String($event))" />
    </div>
    <p v-if="protocolCatalogError" class="text-xs text-red-600 sm:col-span-2">{{ t('common.error') }}</p>
  </fieldset>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { loadProtocolCatalog, protocolCatalog, protocolCatalogError } from '@/api/admin/protocolCapabilities'
import type { AttributeRouteMatch } from '@/api/admin/modelAttributes'

const props = defineProps<{ modelValue?: AttributeRouteMatch }>()
const emit = defineEmits<{ 'update:modelValue': [AttributeRouteMatch | undefined] }>()
const { t } = useI18n()
const fields = ['platform', 'provider_type', 'provider_mode', 'site', 'client_protocol', 'upstream_protocol'] as const
const platforms = ['openai', 'anthropic', 'gemini', 'antigravity', 'grok', 'qoder', 'kimi', 'zhipu', 'deepseek', 'jev']
const types = ['apikey', 'oauth', 'setup-token', 'bedrock', 'service_account', 'cosy', 'upstream']
function optionsFor(field: keyof AttributeRouteMatch) {
  let options: { value: string; label: string }[]
  if (field === 'client_protocol' || field === 'upstream_protocol') {
    options = (protocolCatalog.value?.protocols ?? []).filter(item => field === 'upstream_protocol' || !item.upstream_only).map(item => ({ value: item.id, label: item.name }))
  } else {
    const values = field === 'platform' ? platforms : field === 'provider_type' ? types : field === 'provider_mode' ? ['payg', 'coding'] : ['cn', 'global']
    options = values.map(value => ({ value, label: value }))
  }
  const selected = props.modelValue?.[field]
  if (selected && !options.some(option => option.value === selected)) options.push({ value: selected, label: selected })
  return [{ value: '', label: t('admin.modelAttributes.allRoutes') }, ...options]
}
function set(field: keyof AttributeRouteMatch, value: string) {
  const match = { ...props.modelValue, [field]: value }
  if (!value) delete match[field]
  emit('update:modelValue', Object.keys(match).length ? match : undefined)
}
onMounted(() => { void loadProtocolCatalog().catch(() => {}) })
</script>
