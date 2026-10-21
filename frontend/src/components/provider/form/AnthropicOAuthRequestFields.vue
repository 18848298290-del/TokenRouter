<template>
  <SettingsSection>
    <SettingToggleRow
      :id="`${uid}-session-id-masking`"
      v-model="sessionIdMaskingEnabled"
      :label="t('admin.providers.quotaControl.sessionIdMasking.label')"
      :hint="t('admin.providers.quotaControl.sessionIdMasking.hint')"
      testid="session-id-masking-toggle"
    />

    <SettingToggleRow
      :id="`${uid}-cache-ttl`"
      v-model="cacheTTLOverrideEnabled"
      :label="t('admin.providers.quotaControl.cacheTTLOverride.label')"
      :hint="t('admin.providers.quotaControl.cacheTTLOverride.hint')"
      testid="cache-ttl-override-toggle"
    />
    <Collapse :open="cacheTTLOverrideEnabled" unmount-on-hide>
      <SettingsSubpanel>
        <div>
          <label :for="`${uid}-cache-ttl-target`" class="input-label">{{
            t('admin.providers.quotaControl.cacheTTLOverride.target')
          }}</label>
          <Select
            :id="`${uid}-cache-ttl-target`"
            v-model="cacheTTLOverrideTarget"
            :options="CACHE_TTL_OVERRIDE_TARGET_OPTIONS"
          />
          <p class="input-hint">
            {{ t('admin.providers.quotaControl.cacheTTLOverride.targetHint') }}
          </p>
        </div>
      </SettingsSubpanel>
    </Collapse>

    <SettingToggleRow
      :id="`${uid}-custom-base-url`"
      v-model="customBaseUrlEnabled"
      :label="t('admin.providers.quotaControl.customBaseUrl.label')"
      :hint="t('admin.providers.quotaControl.customBaseUrl.hint')"
      testid="custom-base-url-toggle"
    />
    <Collapse :open="customBaseUrlEnabled" unmount-on-hide>
      <SettingsSubpanel>
        <input
          v-model="customBaseUrl"
          type="text"
          class="input"
          :aria-label="t('admin.providers.quotaControl.customBaseUrl.label')"
          :placeholder="t('admin.providers.quotaControl.customBaseUrl.urlHint')"
        />
      </SettingsSubpanel>
    </Collapse>
  </SettingsSection>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Collapse from '@/components/common/Collapse.vue'
import Select from '@/components/common/Select.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import { CACHE_TTL_OVERRIDE_TARGET_OPTIONS } from './providerFormOptions'

// AnthropicOAuthRequestFields.vue groups Anthropic request customization drafts for
// session identity, cache duration, and an upstream address. The owning provider form
// decides which enabled values enter credentials, and hiding the nested controls retains
// their draft values for later editing.

// sessionIdMaskingEnabled binds the parent-owned session identifier masking switch to the
// shared controls through its model update event.
const sessionIdMaskingEnabled = defineModel<boolean>('sessionIdMaskingEnabled', { required: true })
// cacheTTLOverrideEnabled binds the parent-owned cache TTL rewrite switch to the shared
// controls through its model update event.
const cacheTTLOverrideEnabled = defineModel<boolean>('cacheTtlEnabled', { required: true })
// cacheTTLOverrideTarget binds the parent-owned five-minute or one-hour cache TTL choice
// to the shared controls through its model update event.
const cacheTTLOverrideTarget = defineModel<string>('cacheTtlTarget', { required: true })
// customBaseUrlEnabled binds the parent-owned custom upstream address switch to the
// shared controls through its model update event.
const customBaseUrlEnabled = defineModel<boolean>('customBaseUrlEnabled', { required: true })
// customBaseUrl binds the parent-owned custom upstream address draft to the shared
// controls through its model update event.
const customBaseUrl = defineModel<string>('customBaseUrl', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
</script>
