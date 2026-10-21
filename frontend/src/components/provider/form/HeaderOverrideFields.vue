<template>
  <SettingsSection>
    <SettingToggleRow
      :id="`${uid}-enabled`"
      v-model="enabled"
      :label="t('admin.providers.headerOverride.title')"
      :hint="t('admin.providers.headerOverride.hint')"
      :testid="testid"
    />
    <Collapse :open="enabled" unmount-on-hide>
      <SettingsSubpanel>
        <SettingsNotice>
          <p>{{ t('admin.providers.headerOverride.info') }}</p>
          <slot name="notice" />
        </SettingsNotice>
        <HeaderOverrideEditor :rows="rows" @update:rows="rows = $event" />
      </SettingsSubpanel>
    </Collapse>
    <slot name="disabled" v-if="!enabled" />
  </SettingsSection>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Collapse from '@/components/common/Collapse.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import type { HeaderOverrideRow } from '@/components/provider/form/headerOverrides'
import HeaderOverrideEditor from '../HeaderOverrideEditor.vue'

// HeaderOverrideFields.vue groups the header override switch and shared row editor for
// provider forms. The parent owns the row objects and enabled state, while a supplemental
// slot supplies context such as bulk replacement instructions next to the same controls.
defineProps<{
  testid?: string // Stable test selector value.
}>()
// enabled is the parent-owned feature switch whose value controls the visibility and
// application of these settings.
const enabled = defineModel<boolean>('enabled', { required: true })
// rows holds parent-owned header drafts that the nested editor updates before the form
// builds credentials.
const rows = defineModel<HeaderOverrideRow[]>('rows', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
</script>
