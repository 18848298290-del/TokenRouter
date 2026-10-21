<template>
  <SettingsSection>
    <SettingToggleRow
      :id="`${uid}-enabled`"
      v-model="enabled"
      :label="t('admin.providers.tempUnschedulable.title')"
      :hint="t('admin.providers.tempUnschedulable.hint')"
      testid="temp-unsched-toggle"
    />
    <Collapse :open="enabled" unmount-on-hide>
      <TempUnschedRulesEditor v-model="rules" data-provider-field="temp-unsched" />
    </Collapse>
  </SettingsSection>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Collapse from '@/components/common/Collapse.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import TempUnschedRulesEditor, { type TempUnschedRuleForm } from '../TempUnschedRulesEditor.vue'

// TempUnschedFields.vue groups the temporary unschedulability switch and ordered rule
// editor used by provider forms. The parent owns both drafts and converts rule keywords
// and durations into the persisted policy when its create or edit operation is submitted.

// enabled is the parent-owned feature switch whose value controls the visibility and
// application of these settings.
const enabled = defineModel<boolean>('enabled', { required: true })
// rules holds the ordered parent-owned cooldown drafts that the nested editor mutates and
// replaces through its events.
const rules = defineModel<TempUnschedRuleForm[]>('rules', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
</script>
