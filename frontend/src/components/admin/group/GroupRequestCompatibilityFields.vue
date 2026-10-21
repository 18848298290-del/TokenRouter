<template>
  <SettingsSection :title="t('admin.groups.settings.compatibility')">
    <SettingToggleRow
      v-for="feature in features"
      :id="`${idPrefix}-${feature.key}`"
      :key="feature.key"
      :label="t(`admin.groups.routingPolicy.${feature.label}`)"
      :hint="t(`admin.groups.routingPolicy.${feature.label}Hint`)"
      :model-value="featureEnabled(feature.key)"
      :setting="feature.key"
      @update:model-value="setFeature(feature.key, $event)"
    />
  </SettingsSection>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { GroupRoutingPolicy } from '@/api/groups.types'
import { cloneRoutingPolicy } from './routingPolicy'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'

// GroupRequestCompatibilityFields.vue edits Anthropic request compatibility flags inside a
// group routing policy. Existing boolean and per-platform settings are accepted for display,
// and edits copy the complete policy before replacing the selected feature's Anthropic value.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  idPrefix: string // field identifier prefix
  // modelValue supplies the parent-owned value edited by these controls. Updates are emitted to
  // the parent for publication back through this prop.
  modelValue: GroupRoutingPolicy
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [
    value: GroupRoutingPolicy, // replacement field value
  ] // replacement parent-owned field value
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// features lists the compatibility controls stored under routing features_config, with
// translation suffixes for their visible labels.
const features = [
  { key: 'web_search_emulation', label: 'webSearch' },
  { key: 'bedrock_cc_compat', label: 'bedrock' },
]

// featureEnabled reads a compatibility flag from its supported stored representations. Boolean
// values apply directly, while object values enable the control when their Anthropic property
// is true.
function featureEnabled(key: string) {
  const raw = props.modelValue.features_config[key]
  if (typeof raw === 'boolean') return raw
  return !!(raw && typeof raw === 'object' && (raw as Record<string, unknown>).anthropic === true)
}

// setFeature emits a complete policy with the selected Anthropic compatibility value. The
// policy is cloned before editing, and existing per-platform feature keys are copied into the
// replacement feature object.
function setFeature(key: string, enabled: boolean) {
  // Copy the complete routing policy before changing one platform flag so model rules and other
  // platform settings survive this edit.
  const policy = cloneRoutingPolicy(props.modelValue)
  const raw = policy.features_config[key]
  const values = raw && typeof raw === 'object' ? { ...(raw as Record<string, boolean>) } : {}
  policy.features_config[key] = { ...values, anthropic: enabled }
  emit('update:modelValue', policy)
}
</script>
