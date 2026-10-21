<template>
  <SettingsSection>
    <SettingToggleRow
      v-if="!hideToggle"
      :id="`${uid}-enabled`"
      v-model="enabled"
      :label="t('admin.providers.quotaControl.tlsFingerprint.label')"
      :hint="t('admin.providers.quotaControl.tlsFingerprint.hint')"
      :testid="testIdPrefix && `${testIdPrefix}-toggle`"
    />
    <Collapse :open="hideToggle || enabled" unmount-on-hide>
      <SettingsSubpanel>
        <div>
          <label :for="`${uid}-profile`" class="input-label">{{
            t('admin.providers.quotaControl.tlsFingerprint.profile')
          }}</label>
          <Select
            :id="`${uid}-profile`"
            v-model="profileId"
            :data-testid="testIdPrefix && `${testIdPrefix}-profile`"
            :options="profileOptions"
          />
        </div>
        <div v-if="routerOptions">
          <label :for="`${uid}-router`" class="input-label">{{
            t('admin.providers.quotaControl.tlsFingerprint.router')
          }}</label>
          <Select
            :id="`${uid}-router`"
            v-model="routerId"
            :data-testid="testIdPrefix && `${testIdPrefix}-router`"
            :options="routerOptions"
          />
          <p class="input-hint">
            {{ t('admin.providers.quotaControl.tlsFingerprint.routerHint') }}
          </p>
        </div>
      </SettingsSubpanel>
    </Collapse>
  </SettingsSection>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Collapse from '@/components/common/Collapse.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'

// TLSFingerprintFields.vue edits fingerprint profile and optional router selections for
// provider authentication forms. The parent supplies available choices and owns the
// enabled flag; router controls appear when router options are supplied, and hidden
// controls retain their draft values.
defineProps<{
  profileOptions: SelectOption[] // Available TLS profile choices.

  // Supplying router choices reveals the optional router selector even when the list is
  // empty.
  routerOptions?: SelectOption[]
  testIdPrefix?: string // Test selector ID prefix.

  // The enclosing bulk application field supplies enablement when the local toggle is
  // hidden.
  hideToggle?: boolean
}>()
// enabled is the parent-owned feature switch whose value controls the visibility and
// application of these settings.
const enabled = defineModel<boolean>('enabled', { default: false })
// profileId binds the parent-owned selected TLS fingerprint profile ID to the shared
// controls through its model update event.
const profileId = defineModel<number | null>('profileId', { required: true })
// routerId binds the parent-owned selected TLS fingerprint router ID to the shared
// controls through its model update event.
const routerId = defineModel<number | null>('routerId', { default: null })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
</script>
