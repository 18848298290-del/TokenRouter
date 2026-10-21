<template>
  <SettingsCard
    :title="t('admin.settings.defaults.title')"
    :description="t('admin.settings.defaults.description')"
  >
    <SettingsSection>
      <div class="grid gap-4 md:grid-cols-2">
        <div>
          <label for="default-balance" class="input-label">
            {{ t('admin.settings.defaults.defaultBalance') }}
          </label>
          <input
            id="default-balance"
            v-model.number="form.default_balance"
            type="number"
            step="0.01"
            min="0"
            class="input"
            placeholder="0.00"
          />
          <p class="input-hint">{{ t('admin.settings.defaults.defaultBalanceHint') }}</p>
        </div>
        <div>
          <label for="default-concurrency" class="input-label">
            {{ t('admin.settings.defaults.defaultConcurrency') }}
          </label>
          <input
            id="default-concurrency"
            v-model.number="form.default_concurrency"
            type="number"
            min="1"
            class="input"
            placeholder="1"
          />
          <p class="input-hint">
            {{ t('admin.settings.defaults.defaultConcurrencyHint') }}
          </p>
        </div>
        <div>
          <label for="default-user-rpm-limit" class="input-label">
            {{ t('admin.settings.defaults.defaultUserRpmLimit') }}
          </label>
          <input
            id="default-user-rpm-limit"
            v-model.number="form.default_user_rpm_limit"
            type="number"
            min="0"
            :max="MAX_USER_API_KEY_LIMIT"
            step="1"
            class="input"
            placeholder="0"
          />
          <p class="input-hint">
            {{ t('admin.settings.defaults.defaultUserRpmLimitHint') }}
          </p>
        </div>
        <div>
          <label for="default-user-api-key-limit" class="input-label">
            {{ t('admin.settings.defaults.defaultUserApiKeyLimit') }}
          </label>
          <input
            id="default-user-api-key-limit"
            v-model.number="form.default_user_api_key_limit"
            type="number"
            min="0"
            step="1"
            class="input"
            data-test="default-user-api-key-limit"
            placeholder="100"
          />
          <p class="input-hint">
            {{ t('admin.settings.defaults.defaultUserApiKeyLimitHint') }}
          </p>
        </div>
      </div>
    </SettingsSection>

    <SettingsSection>
      <RuleListEditor
        :items="form.default_subscriptions"
        :title="t('admin.settings.defaults.defaultSubscriptions')"
        :hint="t('admin.settings.defaults.defaultSubscriptionsHint')"
        :add-label="t('admin.settings.defaults.addDefaultSubscription')"
        :empty-text="t('admin.settings.defaults.defaultSubscriptionsEmpty')"
        :add-disabled="subscriptionPlans.length === 0"
        test-id="default-subscriptions"
        @add="addDefaultSubscription"
        @remove="removeDefaultSubscription"
      >
        <template #row="{ item }">
          <Select
            v-model="item.plan_id"
            :aria-label="t('admin.settings.defaults.subscriptionGroup')"
            :options="defaultSubscriptionPlanOptions"
            :placeholder="t('admin.settings.defaults.subscriptionGroup')"
          />
        </template>
      </RuleListEditor>
    </SettingsSection>
  </SettingsCard>

  <!-- Default grants for each authentication source. -->
  <SettingsCard
    :title="t('admin.settings.authSourceDefaults.title')"
    :description="t('admin.settings.authSourceDefaults.description')"
  >
    <SettingsSection>
      <SettingToggleRow
        id="force-email-on-third-party-signup"
        v-model="form.force_email_on_third_party_signup"
        :label="t('admin.settings.authSourceDefaults.requireEmailLabel')"
        :hint="t('admin.settings.authSourceDefaults.requireEmailHint')"
      />
    </SettingsSection>

    <SettingsSection>
      <template v-for="authSource in authSourceDefaultsMeta" :key="authSource.source">
        <SettingToggleRow
          :id="`auth-source-${authSource.source}-grant-on-signup`"
          v-model="authSourceDefaults[authSource.source].grant_on_signup"
          :label="authSource.title"
          :hint="authSource.description"
          :testid="`auth-source-${authSource.source}-enabled`"
        />
        <Collapse :open="authSourceDefaults[authSource.source].grant_on_signup" unmount-on-hide>
          <SettingsSubpanel :data-testid="`auth-source-${authSource.source}-panel`">
            <p class="input-hint mt-0">
              {{ t('admin.settings.authSourceDefaults.enabledHint') }}
            </p>
            <div class="grid gap-4 md:grid-cols-2">
              <div>
                <label :for="`auth-source-${authSource.source}-balance`" class="input-label">
                  {{ t('admin.settings.defaults.defaultBalance') }}
                </label>
                <input
                  :id="`auth-source-${authSource.source}-balance`"
                  v-model.number="authSourceDefaults[authSource.source].balance"
                  type="number"
                  step="0.01"
                  min="0"
                  class="input"
                  placeholder="0.00"
                />
              </div>
              <div>
                <label :for="`auth-source-${authSource.source}-concurrency`" class="input-label">
                  {{ t('admin.settings.defaults.defaultConcurrency') }}
                </label>
                <input
                  :id="`auth-source-${authSource.source}-concurrency`"
                  v-model.number="authSourceDefaults[authSource.source].concurrency"
                  type="number"
                  min="1"
                  class="input"
                  placeholder="5"
                />
              </div>
            </div>
            <SettingToggleRow
              :id="`auth-source-${authSource.source}-grant-on-first-bind`"
              v-model="authSourceDefaults[authSource.source].grant_on_first_bind"
              :label="t('admin.settings.authSourceDefaults.grantOnFirstBindLabel')"
              :hint="t('admin.settings.authSourceDefaults.grantOnFirstBindHint')"
            />
            <RuleListEditor
              :items="authSourceDefaults[authSource.source].subscriptions"
              :title="t('admin.settings.authSourceDefaults.defaultSubscriptionsLabel')"
              :hint="t('admin.settings.authSourceDefaults.defaultSubscriptionsHint')"
              :add-label="t('admin.settings.defaults.addDefaultSubscription')"
              :empty-text="t('admin.settings.authSourceDefaults.noSourceSubscriptions')"
              :add-disabled="subscriptionPlans.length === 0"
              :test-id="`auth-source-${authSource.source}-subscriptions`"
              @add="addAuthSourceDefaultSubscription(authSource.source)"
              @remove="removeAuthSourceDefaultSubscription(authSource.source, $event)"
            >
              <template #row="{ item }">
                <Select
                  v-model="item.plan_id"
                  :aria-label="t('admin.settings.defaults.subscriptionGroup')"
                  :options="defaultSubscriptionPlanOptions"
                  :placeholder="t('admin.settings.defaults.subscriptionGroup')"
                />
              </template>
            </RuleListEditor>
          </SettingsSubpanel>
        </Collapse>
      </template>
    </SettingsSection>
  </SettingsCard>
</template>

<script setup lang="ts">
import Collapse from '@/components/common/Collapse.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import { MAX_USER_API_KEY_LIMIT } from '@/constants/user'
import type { useUsersSettings, UsersSettingsForm } from './useUsersSettings'

/**
 * UserDefaultsSettings.vue edits global signup defaults and grants for individual
 * authentication sources. Subscription rows select loaded plan identifiers, while
 * source-specific controls mutate the controller draft that the page validates and
 * includes in its bulk save.
 */

/**
 * props supplies the page-owned controller and mutable draft used by this panel
 * throughout its mounted lifetime.
 */
const props = defineProps<{
  /** controller retains reactive fields and actions created once by the parent during page setup. */
  controller: ReturnType<typeof useUsersSettings>
  /** form shares editable settings with the parent; field writes enter its bulk dirty snapshot. */
  form: UsersSettingsForm
}>()
/**
 * Controller bindings retain Vue refs and action functions so template edits and
 * buttons use the parent instance.
 */
const {
  subscriptionPlans,
  authSourceDefaults,
  authSourceDefaultsMeta,
  defaultSubscriptionPlanOptions,
  addDefaultSubscription,
  removeDefaultSubscription,
  addAuthSourceDefaultSubscription,
  removeAuthSourceDefaultSubscription,
} = props.controller
/**
 * form retains the shared draft object so nested field edits reach the page save
 * target.
 */
const { form } = props
/**
 * t resolves settings labels and hints from the active interface locale during
 * rendering.
 */
const { t } = useI18n()
</script>
