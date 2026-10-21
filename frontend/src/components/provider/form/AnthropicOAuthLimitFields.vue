<template>
  <SettingsSection
    :title="t('admin.providers.quotaControl.title')"
    :hint="t('admin.providers.quotaControl.hint')"
  >
    <SettingToggleRow
      :id="`${uid}-window-cost`"
      v-model="windowCostEnabled"
      :label="t('admin.providers.quotaControl.windowCost.label')"
      :hint="t('admin.providers.quotaControl.windowCost.hint')"
      testid="window-cost-toggle"
    />
    <Collapse :open="windowCostEnabled" unmount-on-hide>
      <SettingsSubpanel class="grid gap-4 space-y-0 md:grid-cols-2">
        <div>
          <label :for="`${uid}-window-cost-limit`" class="input-label">{{
            t('admin.providers.quotaControl.windowCost.limit')
          }}</label>
          <div class="input-icon-wrap">
            <span class="input-icon text-gray-500 dark:text-gray-400">$</span>
            <input
              :id="`${uid}-window-cost-limit`"
              v-model.number="windowCostLimit"
              type="number"
              min="0"
              step="1"
              class="input input-has-icon input-icon-text"
              :placeholder="t('admin.providers.quotaControl.windowCost.limitPlaceholder')"
            />
          </div>
          <p class="input-hint">{{ t('admin.providers.quotaControl.windowCost.limitHint') }}</p>
        </div>
        <div>
          <label :for="`${uid}-window-cost-reserve`" class="input-label">{{
            t('admin.providers.quotaControl.windowCost.stickyReserve')
          }}</label>
          <div class="input-icon-wrap">
            <span class="input-icon text-gray-500 dark:text-gray-400">$</span>
            <input
              :id="`${uid}-window-cost-reserve`"
              v-model.number="windowCostStickyReserve"
              type="number"
              min="0"
              step="1"
              class="input input-has-icon input-icon-text"
              :placeholder="t('admin.providers.quotaControl.windowCost.stickyReservePlaceholder')"
            />
          </div>
          <p class="input-hint">
            {{ t('admin.providers.quotaControl.windowCost.stickyReserveHint') }}
          </p>
        </div>
      </SettingsSubpanel>
    </Collapse>

    <SettingToggleRow
      :id="`${uid}-session-limit`"
      v-model="sessionLimitEnabled"
      :label="t('admin.providers.quotaControl.sessionLimit.label')"
      :hint="t('admin.providers.quotaControl.sessionLimit.hint')"
      testid="session-limit-toggle"
    />
    <Collapse :open="sessionLimitEnabled" unmount-on-hide>
      <SettingsSubpanel class="grid gap-4 space-y-0 md:grid-cols-2">
        <div>
          <label :for="`${uid}-max-sessions`" class="input-label">{{
            t('admin.providers.quotaControl.sessionLimit.maxSessions')
          }}</label>
          <input
            :id="`${uid}-max-sessions`"
            v-model.number="maxSessions"
            type="number"
            min="1"
            step="1"
            class="input"
            :placeholder="t('admin.providers.quotaControl.sessionLimit.maxSessionsPlaceholder')"
          />
          <p class="input-hint">
            {{ t('admin.providers.quotaControl.sessionLimit.maxSessionsHint') }}
          </p>
        </div>
        <div>
          <label :for="`${uid}-idle-timeout`" class="input-label">{{
            t('admin.providers.quotaControl.sessionLimit.idleTimeout')
          }}</label>
          <div class="input-icon-wrap">
            <input
              :id="`${uid}-idle-timeout`"
              v-model.number="sessionIdleTimeout"
              type="number"
              min="1"
              step="1"
              class="input pr-16"
              :placeholder="t('admin.providers.quotaControl.sessionLimit.idleTimeoutPlaceholder')"
            />
            <span class="input-icon-right text-sm text-gray-500 dark:text-gray-400">{{
              t('common.minutes')
            }}</span>
          </div>
          <p class="input-hint">
            {{ t('admin.providers.quotaControl.sessionLimit.idleTimeoutHint') }}
          </p>
        </div>
      </SettingsSubpanel>
    </Collapse>

    <SettingToggleRow
      :id="`${uid}-rpm`"
      v-model="rpmLimitEnabled"
      :label="t('admin.providers.quotaControl.rpmLimit.label')"
      :hint="t('admin.providers.quotaControl.rpmLimit.hint')"
      testid="rpm-limit-toggle"
    />
    <Collapse :open="rpmLimitEnabled" unmount-on-hide>
      <RpmLimitPanel
        v-model:base-rpm="baseRpm"
        v-model:strategy="rpmStrategy"
        v-model:sticky-buffer="rpmStickyBuffer"
      />
    </Collapse>

    <!-- User-message queue mode remains visible independently from the RPM enable switch because the parent stores it separately. -->
    <div class="space-y-2">
      <div>
        <span class="text-sm font-medium text-primary-900 dark:text-dark-50">{{
          t('admin.providers.quotaControl.rpmLimit.userMsgQueue')
        }}</span>
        <p class="input-hint">{{ t('admin.providers.quotaControl.rpmLimit.userMsgQueueHint') }}</p>
      </div>
      <SettingsSegmented
        v-model="userMsgQueueMode"
        :ariaLabel="t('admin.providers.quotaControl.rpmLimit.userMsgQueue')"
        :options="umqModeOptions"
      />
    </div>
  </SettingsSection>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Collapse from '@/components/common/Collapse.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSegmented from '@/components/common/settings/SettingsSegmented.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import RpmLimitPanel from './RpmLimitPanel.vue'
import { useUserMsgQueueModeOptions, type RpmStrategy } from './providerFormOptions'

// AnthropicOAuthLimitFields.vue groups spending-window, session-count, and request-rate
// limits for Anthropic OAuth and setup-token forms. Each control writes a parent-owned
// draft, while enable switches control which values the owning create or edit form
// includes in its payload.

// windowCostEnabled binds the parent-owned spending-window limit switch to the shared
// controls through its model update event.
const windowCostEnabled = defineModel<boolean>('windowCostEnabled', { required: true })
// windowCostLimit binds the parent-owned spending-window ceiling in USD to the shared
// controls through its model update event.
const windowCostLimit = defineModel<number | null>('windowCostLimit', { required: true })
// windowCostStickyReserve binds the parent-owned sticky-session reserve amount in USD to
// the shared controls through its model update event.
const windowCostStickyReserve = defineModel<number | null>('windowCostStickyReserve', {
  required: true,
})
// sessionLimitEnabled binds the parent-owned session-count limit switch to the shared
// controls through its model update event.
const sessionLimitEnabled = defineModel<boolean>('sessionLimitEnabled', { required: true })
// maxSessions binds the parent-owned maximum simultaneous tracked session count to the
// shared controls through its model update event.
const maxSessions = defineModel<number | null>('maxSessions', { required: true })
// sessionIdleTimeout binds the parent-owned session idle timeout in minutes to the shared
// controls through its model update event.
const sessionIdleTimeout = defineModel<number | null>('sessionIdleTimeout', { required: true })
// rpmLimitEnabled binds the parent-owned request-rate limit switch to the shared controls
// through its model update event.
const rpmLimitEnabled = defineModel<boolean>('rpmLimitEnabled', { required: true })
// baseRpm binds the parent-owned base requests-per-minute limit to the shared controls
// through its model update event.
const baseRpm = defineModel<number | null>('baseRpm', { required: true })
// rpmStrategy binds the parent-owned tiered or sticky-exempt rate strategy to the shared
// controls through its model update event.
const rpmStrategy = defineModel<RpmStrategy>('rpmStrategy', { required: true })
// rpmStickyBuffer binds the parent-owned sticky-session request-rate buffer to the shared
// controls through its model update event.
const rpmStickyBuffer = defineModel<number | null>('rpmStickyBuffer', { required: true })
// userMsgQueueMode binds the parent-owned off, throttle, or serialize queue mode to the
// shared controls through its model update event.
const userMsgQueueMode = defineModel<string>('userMsgQueueMode', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
// umqModeOptions supplies localized queue strategies from the shared option factory used
// by the parent provider forms.
const umqModeOptions = useUserMsgQueueModeOptions()
</script>
