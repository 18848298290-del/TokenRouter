<template>
  <SettingsSection>
    <SettingToggleRow
      :id="`${uid}-enabled`"
      v-model="enabled"
      :label="t('admin.providers.poolMode')"
      :hint="t('admin.providers.poolModeHint')"
      testid="pool-mode-toggle"
    />
    <Collapse :open="enabled" unmount-on-hide>
      <SettingsSubpanel>
        <SettingsNotice>{{ t('admin.providers.poolModeInfo') }}</SettingsNotice>
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label :for="`${uid}-retry-count`" class="input-label">{{
              t('admin.providers.poolModeRetryCount')
            }}</label>
            <input
              :id="`${uid}-retry-count`"
              v-model.number="retryCount"
              type="number"
              min="0"
              :max="MAX_POOL_MODE_RETRY_COUNT"
              step="1"
              class="input"
            />
            <p class="input-hint">
              {{
                t('admin.providers.poolModeRetryCountHint', {
                  default: DEFAULT_POOL_MODE_RETRY_COUNT,
                  max: MAX_POOL_MODE_RETRY_COUNT,
                })
              }}
            </p>
          </div>
          <div>
            <label :for="`${uid}-retry-codes`" class="input-label">{{
              t('admin.providers.poolModeRetryStatusCodes')
            }}</label>
            <input
              :id="`${uid}-retry-codes`"
              v-model="retryStatusCodes"
              type="text"
              class="input"
              :placeholder="DEFAULT_POOL_MODE_RETRY_STATUS_CODES.join(', ')"
            />
            <p class="input-hint">
              {{
                t('admin.providers.poolModeRetryStatusCodesHint', {
                  default: DEFAULT_POOL_MODE_RETRY_STATUS_CODES.join(', '),
                })
              }}
            </p>
          </div>
        </div>
      </SettingsSubpanel>
    </Collapse>
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
import {
  DEFAULT_POOL_MODE_RETRY_COUNT,
  DEFAULT_POOL_MODE_RETRY_STATUS_CODES,
  MAX_POOL_MODE_RETRY_COUNT,
} from './poolMode'

// PoolModeFields.vue edits pool retry enablement, retry count, and status-code text for
// provider forms. Shared constants supply input hints and bounds, while the owning form
// parses the text and normalizes counts when constructing its credential payload.

// enabled is the parent-owned feature switch whose value controls the visibility and
// application of these settings.
const enabled = defineModel<boolean>('enabled', { required: true })
// retryCount binds the parent-owned pool retry attempt count to the shared controls
// through its model update event.
const retryCount = defineModel<number>('retryCount', { required: true })
// retryStatusCodes binds the parent-owned comma-separated HTTP retry status draft to the
// shared controls through its model update event.
const retryStatusCodes = defineModel<string>('retryStatusCodes', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
</script>
