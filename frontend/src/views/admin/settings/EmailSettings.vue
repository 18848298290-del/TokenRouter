<template>
  <SettingsNotice v-if="!form.email_verify_enabled">
    <p class="font-medium">{{ t('admin.settings.emailTabDisabledTitle') }}</p>
    <p>{{ t('admin.settings.emailTabDisabledHint') }}</p>
  </SettingsNotice>

  <Collapse :open="form.email_verify_enabled" unmount-on-hide>
    <SettingsCard
      :title="t('admin.settings.smtp.title')"
      :description="t('admin.settings.smtp.description')"
    >
      <template #actions>
        <button
          type="button"
          :disabled="testingSmtp || loadFailed"
          class="btn btn-secondary btn-sm h-9"
          @click="testSmtpConnection"
        >
          <Icon
            v-if="testingSmtp"
            name="loader"
            size="sm"
            :animate-on-hover="false"
            class="h-4 w-4 animate-spin"
          />
          {{
            testingSmtp ? t('admin.settings.smtp.testing') : t('admin.settings.smtp.testConnection')
          }}
        </button>
      </template>
      <SettingsSection>
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label for="smtp-host" class="input-label">{{ t('admin.settings.smtp.host') }}</label>
            <input
              id="smtp-host"
              v-model="form.smtp_host"
              type="text"
              class="input"
              :placeholder="t('admin.settings.smtp.hostPlaceholder')"
            />
          </div>
          <div>
            <label for="smtp-port" class="input-label">{{ t('admin.settings.smtp.port') }}</label>
            <input
              id="smtp-port"
              v-model.number="form.smtp_port"
              type="number"
              min="1"
              max="65535"
              class="input"
              :placeholder="t('admin.settings.smtp.portPlaceholder')"
            />
          </div>
          <div>
            <label for="smtp-username" class="input-label">{{
              t('admin.settings.smtp.username')
            }}</label>
            <input
              id="smtp-username"
              v-model="form.smtp_username"
              type="text"
              class="input"
              :placeholder="t('admin.settings.smtp.usernamePlaceholder')"
            />
          </div>
          <div>
            <label for="smtp-password" class="input-label">{{
              t('admin.settings.smtp.password')
            }}</label>
            <input
              id="smtp-password"
              v-model="form.smtp_password"
              type="password"
              class="input"
              autocomplete="new-password"
              autocapitalize="off"
              spellcheck="false"
              :placeholder="
                form.smtp_password_configured
                  ? t('admin.settings.smtp.passwordConfiguredPlaceholder')
                  : t('admin.settings.smtp.passwordPlaceholder')
              "
              @keydown="smtpPasswordManuallyEdited = true"
              @paste="smtpPasswordManuallyEdited = true"
            />
            <p class="input-hint">
              {{
                form.smtp_password_configured
                  ? t('admin.settings.smtp.passwordConfiguredHint')
                  : t('admin.settings.smtp.passwordHint')
              }}
            </p>
          </div>
          <div>
            <label for="smtp-from-email" class="input-label">{{
              t('admin.settings.smtp.fromEmail')
            }}</label>
            <input
              id="smtp-from-email"
              v-model="form.smtp_from_email"
              type="email"
              class="input"
              :placeholder="t('admin.settings.smtp.fromEmailPlaceholder')"
            />
          </div>
          <div>
            <LocalizedEditor
              :label="t('admin.settings.smtp.fromName')"
              :placeholder="t('admin.settings.smtp.fromNamePlaceholder')"
              v-model="form.localized_settings.smtp_from_name"
              :default-source-locale="getLocale()"
            />
          </div>
        </div>
        <SettingToggleRow
          id="smtp-use-tls"
          v-model="form.smtp_use_tls"
          :label="t('admin.settings.smtp.useTls')"
          :hint="t('admin.settings.smtp.useTlsHint')"
        />
      </SettingsSection>
    </SettingsCard>
  </Collapse>

  <Collapse :open="form.email_verify_enabled" unmount-on-hide>
    <SettingsCard
      :title="t('admin.settings.testEmail.title')"
      :description="t('admin.settings.testEmail.description')"
    >
      <SettingsSection>
        <div>
          <label for="test-email-address" class="input-label">
            {{ t('admin.settings.testEmail.recipientEmail') }}
          </label>
          <div class="flex gap-2">
            <input
              id="test-email-address"
              v-model="testEmailAddress"
              type="email"
              class="input"
              :placeholder="t('admin.settings.testEmail.recipientEmailPlaceholder')"
            />
            <button
              type="button"
              :disabled="sendingTestEmail || !testEmailAddress || loadFailed"
              class="btn btn-secondary shrink-0"
              @click="sendTestEmail"
            >
              <Icon
                v-if="sendingTestEmail"
                name="loader"
                size="sm"
                :animate-on-hover="false"
                class="h-4 w-4 animate-spin"
              />
              {{
                sendingTestEmail
                  ? t('admin.settings.testEmail.sending')
                  : t('admin.settings.testEmail.sendTestEmail')
              }}
            </button>
          </div>
        </div>
      </SettingsSection>
    </SettingsCard>
  </Collapse>

  <!-- Subscription expiry notifications use the shared bulk draft. -->
  <SettingsCard
    :title="t('admin.settings.subscriptionExpiryNotify.title')"
    :description="t('admin.settings.subscriptionExpiryNotify.description')"
  >
    <SettingsSection>
      <SettingToggleRow
        id="subscription-expiry-notify-enabled"
        v-model="form.subscription_expiry_notify_enabled"
        :label="t('admin.settings.subscriptionExpiryNotify.enabled')"
        :hint="t('admin.settings.subscriptionExpiryNotify.enabledHint')"
      />
    </SettingsSection>
  </SettingsCard>

  <EmailTemplateEditor />

  <!-- Low-balance controls combine a threshold with localized recharge guidance. -->
  <SettingsCard
    :title="t('admin.settings.balanceNotify.title')"
    :description="t('admin.settings.balanceNotify.description')"
  >
    <SettingsSection>
      <SettingToggleRow
        id="balance-low-notify-enabled"
        v-model="form.balance_low_notify_enabled"
        :label="t('admin.settings.balanceNotify.enabled')"
      />
      <Collapse :open="form.balance_low_notify_enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingRow
            id="balance-low-notify-threshold"
            field
            label-for="balance-low-notify-threshold"
            :label="t('admin.settings.balanceNotify.threshold')"
            :hint="t('admin.settings.balanceNotify.thresholdHint')"
          >
            <div class="input-icon-wrap">
              <span
                class="input-icon input-icon-text text-sm text-gray-400 dark:text-dark-400"
                aria-hidden="true"
                >{{ previewBalanceUnitSymbol }}</span
              >
              <input
                id="balance-low-notify-threshold"
                v-model.number="form.balance_low_notify_threshold"
                type="number"
                min="0"
                step="0.01"
                class="input input-has-icon input-icon-text"
              />
            </div>
          </SettingRow>
        </SettingsSubpanel>
      </Collapse>
      <div>
        <LocalizedEditor
          :label="t('admin.settings.balanceNotify.rechargeUrl')"
          :placeholder="currentOrigin"
          v-model="form.localized_settings.balance_low_notify_recharge_url"
          :default-source-locale="getLocale()"
        />
        <p class="input-hint">{{ t('admin.settings.balanceNotify.rechargeUrlHint') }}</p>
      </div>
    </SettingsSection>
  </SettingsCard>

  <!-- Administrator-managed recipient rows receive provider quota notifications. -->
  <SettingsCard
    :title="t('admin.settings.quotaNotify.title')"
    :description="t('admin.settings.quotaNotify.description')"
  >
    <SettingsSection>
      <SettingToggleRow
        id="provider-quota-notify-enabled"
        v-model="form.provider_quota_notify_enabled"
        :label="t('admin.settings.quotaNotify.enabled')"
      />
      <Collapse :open="form.provider_quota_notify_enabled" unmount-on-hide>
        <SettingsSubpanel>
          <RuleListEditor
            :items="form.provider_quota_notify_emails || []"
            :title="t('admin.settings.quotaNotify.emails')"
            :hint="t('admin.settings.quotaNotify.emailsHint')"
            :add-label="t('admin.settings.quotaNotify.addEmail')"
            test-id="quota-notify-emails"
            @add="addQuotaNotifyEmail"
            @remove="form.provider_quota_notify_emails.splice($event, 1)"
          >
            <template #row="{ item: entry }">
              <div class="flex items-center gap-3">
                <Toggle
                  :model-value="!entry.disabled"
                  size="md"
                  :aria-label="entry.email || t('admin.settings.quotaNotify.emailPlaceholder')"
                  @update:model-value="entry.disabled = !entry.disabled"
                />
                <input
                  v-model="entry.email"
                  type="email"
                  class="input min-w-0 flex-1"
                  :aria-label="t('admin.settings.quotaNotify.emails')"
                  :placeholder="t('admin.settings.quotaNotify.emailPlaceholder')"
                />
              </div>
            </template>
          </RuleListEditor>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>
</template>

<script setup lang="ts">
import { currentOrigin } from './settingsDefaults'
import { toRef } from 'vue'
import Collapse from '@/components/common/Collapse.vue'
import SettingRow from '@/components/common/settings/SettingRow.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import LocalizedEditor from '@/components/common/LocalizedEditor.vue'
import { getLocale } from '@/i18n'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import Toggle from '@/components/common/Toggle.vue'
import EmailTemplateEditor from '@/views/admin/settings/EmailTemplateEditor.vue'
import type { useEmailSettings, EmailSettingsForm } from './useEmailSettings'

/**
 * EmailSettings.vue renders SMTP and notification drafts alongside the
 * independently saved email template editor. SMTP tests use the page
 * controller, while localized sender-name edits update the shared source and
 * translations that the page later includes in its bulk save.
 */

// props supply the page-owned controller and draft used by every editable
// control in this panel.
const props = defineProps<{
  // controller retains action state across tab visibility changes and also
  // serves the parent settings page.
  controller: ReturnType<typeof useEmailSettings>
  // form is the parent-owned mutable draft; nested field edits participate in
  // the page save and discard actions.
  form: EmailSettingsForm
  loadFailed: boolean // Parent settings load failure.
  previewBalanceUnitSymbol: string // Current balance display symbol.
}>()
// Controller bindings expose shared reactive action state and handlers
// directly to the template controls in this panel.
const {
  testingSmtp,
  sendingTestEmail,
  smtpPasswordManuallyEdited,
  testEmailAddress,
  addQuotaNotifyEmail,
  testSmtpConnection,
  sendTestEmail,
} = props.controller
// Draft and display bindings keep parent updates visible while this mounted
// panel is hidden by the tab container.
const { form } = props
const loadFailed = toRef(props, 'loadFailed')
const previewBalanceUnitSymbol = toRef(props, 'previewBalanceUnitSymbol')
const { t } = useI18n()
</script>
