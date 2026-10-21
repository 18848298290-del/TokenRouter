<template>
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-overload-cooldown"
    :title="t('admin.settings.overloadCooldown.title')"
    :description="t('admin.settings.overloadCooldown.description')"
  >
    <ContentSkeleton v-if="overloadCooldownLoading" variant="form" :rows="3" />
    <SettingsSection v-else>
      <SettingToggleRow
        id="overload-cooldown-enabled"
        v-model="overloadCooldownForm.enabled"
        :label="t('admin.settings.overloadCooldown.enabled')"
        :hint="t('admin.settings.overloadCooldown.enabledHint')"
      />
      <Collapse :open="overloadCooldownForm.enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingRow
            id="overload-cooldown-minutes"
            field
            label-for="overload-cooldown-minutes"
            :label="t('admin.settings.overloadCooldown.cooldownMinutes')"
            :hint="t('admin.settings.overloadCooldown.cooldownMinutesHint')"
          >
            <input
              id="overload-cooldown-minutes"
              v-model.number="overloadCooldownForm.cooldown_minutes"
              type="number"
              min="1"
              max="120"
              class="input"
            />
          </SettingRow>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>

  <!-- OpenAI OAuth permission failures and repeated-failure thresholds. -->
  <SettingsCard
    v-show="activeGatewaySection === 'openai'"
    v-content-reveal="activeGatewaySection === 'openai'"
    data-testid="gateway-card-openai-403-cooldown"
    :title="t('admin.settings.openAI403Cooldown.title')"
    :description="t('admin.settings.openAI403Cooldown.description')"
  >
    <ContentSkeleton v-if="openAI403CooldownLoading" variant="form" :rows="3" />
    <SettingsSection v-else>
      <SettingToggleRow
        id="openai-403-cooldown-enabled"
        v-model="openAI403CooldownForm.enabled"
        :label="t('admin.settings.openAI403Cooldown.enabled')"
        :hint="t('admin.settings.openAI403Cooldown.enabledHint')"
      />
      <Collapse :open="openAI403CooldownForm.enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingRow
            id="openai-403-cooldown-minutes"
            field
            label-for="openai-403-cooldown-minutes"
            :label="t('admin.settings.openAI403Cooldown.cooldownMinutes')"
            :hint="t('admin.settings.openAI403Cooldown.cooldownMinutesHint')"
          >
            <input
              id="openai-403-cooldown-minutes"
              v-model.number="openAI403CooldownForm.cooldown_minutes"
              type="number"
              min="1"
              max="120"
              class="input"
            />
          </SettingRow>
          <SettingToggleRow
            id="openai-403-error-on-threshold"
            v-model="openAI403CooldownForm.error_on_threshold_enabled"
            :label="t('admin.settings.openAI403Cooldown.errorOnThresholdEnabled')"
            :hint="t('admin.settings.openAI403Cooldown.errorOnThresholdEnabledHint')"
          />
          <template v-if="openAI403CooldownForm.error_on_threshold_enabled">
            <SettingRow
              id="openai-403-threshold-count"
              field
              label-for="openai-403-threshold-count"
              :label="t('admin.settings.openAI403Cooldown.thresholdCount')"
              :hint="t('admin.settings.openAI403Cooldown.thresholdCountHint')"
            >
              <input
                id="openai-403-threshold-count"
                v-model.number="openAI403CooldownForm.threshold_count"
                type="number"
                min="1"
                max="20"
                class="input"
              />
            </SettingRow>
            <SettingRow
              id="openai-403-threshold-window"
              field
              label-for="openai-403-threshold-window"
              :label="t('admin.settings.openAI403Cooldown.thresholdWindowMinutes')"
              :hint="t('admin.settings.openAI403Cooldown.thresholdWindowMinutesHint')"
            >
              <input
                id="openai-403-threshold-window"
                v-model.number="openAI403CooldownForm.threshold_window_minutes"
                type="number"
                min="1"
                max="1440"
                class="input"
              />
            </SettingRow>
          </template>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>

  <!-- Rate-limit cooldown shared across providers. -->
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-rate-limit-cooldown"
    :title="t('admin.settings.rateLimit429Cooldown.title')"
    :description="t('admin.settings.rateLimit429Cooldown.description')"
  >
    <ContentSkeleton v-if="rateLimit429CooldownLoading" variant="form" :rows="3" />
    <SettingsSection v-else>
      <SettingToggleRow
        id="rate-limit-429-cooldown-enabled"
        v-model="rateLimit429CooldownForm.enabled"
        :label="t('admin.settings.rateLimit429Cooldown.enabled')"
        :hint="t('admin.settings.rateLimit429Cooldown.enabledHint')"
      />
      <Collapse :open="rateLimit429CooldownForm.enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingRow
            id="rate-limit-429-cooldown-seconds"
            field
            label-for="rate-limit-429-cooldown-seconds"
            :label="t('admin.settings.rateLimit429Cooldown.cooldownSeconds')"
            :hint="t('admin.settings.rateLimit429Cooldown.cooldownSecondsHint')"
          >
            <input
              id="rate-limit-429-cooldown-seconds"
              v-model.number="rateLimit429CooldownForm.cooldown_seconds"
              type="number"
              min="1"
              max="7200"
              class="input"
            />
          </SettingRow>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>

  <!-- Stream timeout actions and escalation thresholds. -->
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-stream-timeout"
    :title="t('admin.settings.streamTimeout.title')"
    :description="t('admin.settings.streamTimeout.description')"
  >
    <ContentSkeleton v-if="streamTimeoutLoading" variant="form" :rows="4" />
    <SettingsSection v-else>
      <SettingToggleRow
        id="stream-timeout-enabled"
        v-model="streamTimeoutForm.enabled"
        :label="t('admin.settings.streamTimeout.enabled')"
        :hint="t('admin.settings.streamTimeout.enabledHint')"
      />
      <Collapse :open="streamTimeoutForm.enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingRow
            id="stream-timeout-action"
            field
            :label="t('admin.settings.streamTimeout.action')"
            :hint="t('admin.settings.streamTimeout.actionHint')"
          >
            <Select
              v-model="streamTimeoutForm.action"
              :options="streamTimeoutActionOptions"
              :aria-label="t('admin.settings.streamTimeout.action')"
            />
          </SettingRow>
          <!-- The temporary scheduling pause duration applies when the selected action is temp_unsched. -->
          <SettingRow
            v-if="streamTimeoutForm.action === 'temp_unsched'"
            id="stream-timeout-temp-unsched-minutes"
            field
            label-for="stream-timeout-temp-unsched-minutes"
            :label="t('admin.settings.streamTimeout.tempUnschedMinutes')"
            :hint="t('admin.settings.streamTimeout.tempUnschedMinutesHint')"
          >
            <input
              id="stream-timeout-temp-unsched-minutes"
              v-model.number="streamTimeoutForm.temp_unsched_minutes"
              type="number"
              min="1"
              max="60"
              class="input"
            />
          </SettingRow>
          <SettingRow
            id="stream-timeout-threshold-count"
            field
            label-for="stream-timeout-threshold-count"
            :label="t('admin.settings.streamTimeout.thresholdCount')"
            :hint="t('admin.settings.streamTimeout.thresholdCountHint')"
          >
            <input
              id="stream-timeout-threshold-count"
              v-model.number="streamTimeoutForm.threshold_count"
              type="number"
              min="1"
              max="10"
              class="input"
            />
          </SettingRow>
          <SettingRow
            id="stream-timeout-threshold-window"
            field
            label-for="stream-timeout-threshold-window"
            :label="t('admin.settings.streamTimeout.thresholdWindowMinutes')"
            :hint="t('admin.settings.streamTimeout.thresholdWindowMinutesHint')"
          >
            <input
              id="stream-timeout-threshold-window"
              v-model.number="streamTimeoutForm.threshold_window_minutes"
              type="number"
              min="1"
              max="60"
              class="input"
            />
          </SettingRow>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>

  <!-- Request rectification rules shared across platforms. -->
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-request-rectifier"
    :title="t('admin.settings.rectifier.title')"
    :description="t('admin.settings.rectifier.description')"
  >
    <ContentSkeleton v-if="rectifierLoading" variant="form" :rows="4" />
    <SettingsSection v-else>
      <SettingToggleRow
        id="rectifier-enabled"
        v-model="rectifierForm.enabled"
        :label="t('admin.settings.rectifier.enabled')"
        :hint="t('admin.settings.rectifier.enabledHint')"
      />
      <Collapse :open="rectifierForm.enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingToggleRow
            id="rectifier-thinking-signature"
            v-model="rectifierForm.thinking_signature_enabled"
            :label="t('admin.settings.rectifier.thinkingSignature')"
            :hint="t('admin.settings.rectifier.thinkingSignatureHint')"
          />
          <SettingToggleRow
            id="rectifier-thinking-budget"
            v-model="rectifierForm.thinking_budget_enabled"
            :label="t('admin.settings.rectifier.thinkingBudget')"
            :hint="t('admin.settings.rectifier.thinkingBudgetHint')"
          />
          <SettingToggleRow
            id="rectifier-apikey-signature"
            v-model="rectifierForm.apikey_signature_enabled"
            :label="t('admin.settings.rectifier.apikeySignature')"
            :hint="t('admin.settings.rectifier.apikeySignatureHint')"
          />
          <!-- Custom signature patterns apply when API Key signature rectification is enabled. -->
          <RuleListEditor
            v-if="rectifierForm.apikey_signature_enabled"
            :items="rectifierForm.apikey_signature_patterns"
            :title="t('admin.settings.rectifier.apikeyPatterns')"
            :hint="t('admin.settings.rectifier.apikeyPatternsHint')"
            :add-label="t('admin.settings.rectifier.addPattern')"
            :animated="false"
            test-id="rectifier-patterns"
            @add="rectifierForm.apikey_signature_patterns.push('')"
            @remove="rectifierForm.apikey_signature_patterns.splice($event, 1)"
          >
            <template #row="{ index }">
              <input
                v-model="rectifierForm.apikey_signature_patterns[index]"
                type="text"
                class="input"
                :placeholder="t('admin.settings.rectifier.apikeyPatternPlaceholder')"
              />
            </template>
          </RuleListEditor>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>

  <!-- Anthropic beta token policy rules. -->
  <SettingsCard
    v-show="activeGatewaySection === 'anthropic'"
    v-content-reveal="activeGatewaySection === 'anthropic'"
    data-testid="gateway-card-beta-policy"
    :title="t('admin.settings.betaPolicy.title')"
    :description="t('admin.settings.betaPolicy.description')"
  >
    <ContentSkeleton v-if="betaPolicyLoading" variant="form" :rows="5" />
    <SettingsSection v-else>
      <!-- Each returned beta token has one editable rule. -->
      <SettingsSubpanel v-for="rule in betaPolicyForm.rules" :key="rule.beta_token">
        <div class="flex items-center gap-2">
          <span class="text-sm font-semibold text-primary-900 dark:text-dark-50">
            {{ getBetaDisplayName(rule.beta_token) }}
          </span>
          <span
            class="rounded-compact bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-500 dark:bg-dark-700 dark:text-dark-300"
          >
            {{ rule.beta_token }}
          </span>
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label :for="`beta-policy-${rule.beta_token}-action`" class="input-label">
              {{ t('admin.settings.betaPolicy.action') }}
            </label>
            <Select
              :id="`beta-policy-${rule.beta_token}-action`"
              :modelValue="rule.action"
              @update:modelValue="rule.action = $event as any"
              :options="betaPolicyActionOptions"
            />
          </div>
          <div>
            <label :for="`beta-policy-${rule.beta_token}-scope`" class="input-label">
              {{ t('admin.settings.betaPolicy.scope') }}
            </label>
            <Select
              :id="`beta-policy-${rule.beta_token}-scope`"
              :modelValue="rule.scope"
              @update:modelValue="rule.scope = $event as any"
              :options="betaPolicyScopeOptions"
            />
          </div>
        </div>

        <!-- Block actions expose the message returned to the requesting client. -->
        <div v-if="rule.action === 'block'">
          <label class="input-label">{{ t('admin.settings.betaPolicy.errorMessage') }}</label>
          <input
            v-model="rule.error_message"
            type="text"
            class="input"
            :placeholder="t('admin.settings.betaPolicy.errorMessagePlaceholder')"
          />
          <p class="input-hint">
            {{ t('admin.settings.betaPolicy.errorMessageHint') }}
          </p>
        </div>

        <div v-if="betaPresets[rule.beta_token]?.length">
          <p class="input-label">
            {{ t('admin.settings.betaPolicy.quickPresets') }}
          </p>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="preset in betaPresets[rule.beta_token]"
              :key="preset.label"
              type="button"
              class="inline-flex items-center gap-1 rounded-compact border border-primary-200 bg-primary-50 px-2.5 py-1 text-xs font-medium text-primary-700 transition-colors hover:bg-primary-100 dark:border-primary-800 dark:bg-primary-900/30 dark:text-primary-300 dark:hover:bg-primary-900/50"
              @click="applyBetaPreset(rule, preset)"
              :title="preset.description"
            >
              {{ preset.label }}
            </button>
          </div>
        </div>

        <RuleListEditor
          :items="rule.model_whitelist || []"
          :title="t('admin.settings.betaPolicy.modelWhitelist')"
          :hint="t('admin.settings.betaPolicy.modelWhitelistHint')"
          :add-label="t('admin.settings.betaPolicy.addModelPattern')"
          :animated="false"
          @add="
            () => {
              if (!rule.model_whitelist) rule.model_whitelist = []
              rule.model_whitelist.push('')
            }
          "
          @remove="rule.model_whitelist!.splice($event, 1)"
        >
          <template #footer>
            <div class="flex flex-wrap items-center gap-1.5">
              <span class="text-xs text-gray-500 dark:text-dark-400"
                >{{ t('admin.settings.betaPolicy.commonPatterns') }}:</span
              >
              <button
                v-for="pattern in commonModelPatterns"
                :key="pattern"
                type="button"
                class="rounded-compact border border-gray-200 px-2 py-0.5 text-xs text-gray-600 transition-colors hover:border-primary-300 hover:bg-primary-50 hover:text-primary-700 dark:border-dark-600 dark:text-gray-400 dark:hover:border-primary-700 dark:hover:bg-primary-900/30 dark:hover:text-primary-300"
                @click="addQuickPattern(rule, pattern)"
              >
                {{ pattern }}
              </button>
            </div>
          </template>
          <template #row="{ index }">
            <input
              v-model="rule.model_whitelist![index]"
              type="text"
              class="input"
              :placeholder="t('admin.settings.betaPolicy.modelPatternPlaceholder')"
            />
          </template>
        </RuleListEditor>

        <!-- When the whitelist contains patterns, unmatched models use the selected fallback action. -->
        <div v-if="rule.model_whitelist && rule.model_whitelist.length > 0">
          <label :for="`beta-policy-${rule.beta_token}-fallback`" class="input-label">
            {{ t('admin.settings.betaPolicy.fallbackAction') }}
          </label>
          <Select
            :id="`beta-policy-${rule.beta_token}-fallback`"
            :modelValue="rule.fallback_action || 'pass'"
            @update:modelValue="rule.fallback_action = $event as any"
            :options="betaPolicyActionOptions"
          />
          <p class="input-hint">
            {{ t('admin.settings.betaPolicy.fallbackActionHint') }}
          </p>
          <div v-if="rule.fallback_action === 'block'" class="mt-2">
            <label class="input-label">{{ t('admin.settings.betaPolicy.errorMessage') }}</label>
            <input
              v-model="rule.fallback_error_message"
              type="text"
              class="input"
              :placeholder="t('admin.settings.betaPolicy.fallbackErrorMessagePlaceholder')"
            />
            <p class="input-hint">
              {{ t('admin.settings.betaPolicy.errorMessageHint') }}
            </p>
          </div>
        </div>
      </SettingsSubpanel>
    </SettingsSection>
  </SettingsCard>

  <!-- Fast and Flex rules are included in the bulk settings save after their initial load. -->
  <SettingsCard
    v-show="activeGatewaySection === 'openai'"
    v-content-reveal="activeGatewaySection === 'openai'"
    data-testid="gateway-card-openai-fast-policy"
    :title="t('admin.settings.openaiFastPolicy.title')"
    :description="t('admin.settings.openaiFastPolicy.description')"
  >
    <SettingsSection>
      <RuleListEditor
        :items="openaiFastPolicyForm.rules"
        variant="card"
        :item-label="
          (index) => t('admin.settings.openaiFastPolicy.ruleHeader', { index: index + 1 })
        "
        :add-label="t('admin.settings.openaiFastPolicy.addRule')"
        :remove-label="t('admin.settings.openaiFastPolicy.removeRule')"
        :empty-text="t('admin.settings.openaiFastPolicy.empty')"
        add-placement="footer"
        test-id="openai-fast-rules"
        @add="addOpenAIFastPolicyRule"
        @remove="removeOpenAIFastPolicyRule"
      >
        <template #row="{ item: rule, index: ruleIndex }">
          <div class="space-y-4">
            <div
              class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-dark-400"
              :data-testid="`openai-fast-policy-summary-${ruleIndex}`"
            >
              <span class="font-medium text-gray-700 dark:text-dark-200">
                {{
                  t(
                    hasOpenAIFastPolicyTargetModels(rule)
                      ? 'admin.settings.openaiFastPolicy.summaryTargetModels'
                      : 'admin.settings.openaiFastPolicy.summaryAllModels',
                  )
                }}
              </span>
              <span aria-hidden="true">→</span>
              <span
                class="inline-flex items-center rounded-compact bg-primary-50 px-2 py-0.5 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
              >
                {{ openaiFastPolicyActionSummary(rule.action) }}
              </span>
              <template v-if="hasOpenAIFastPolicyTargetModels(rule)">
                <span aria-hidden="true">·</span>
                <span class="font-medium text-gray-700 dark:text-dark-200">
                  {{ t('admin.settings.openaiFastPolicy.summaryOtherModels') }}
                </span>
                <span aria-hidden="true">→</span>
                <span
                  class="inline-flex items-center rounded-compact bg-gray-100 px-2 py-0.5 font-medium text-gray-700 dark:bg-dark-600 dark:text-gray-300"
                >
                  {{ openaiFastPolicyActionSummary(rule.fallback_action || 'pass') }}
                </span>
              </template>
            </div>
            <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
              <div>
                <label :for="`openai-fast-policy-tier-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.openaiFastPolicy.serviceTier') }}
                </label>
                <Select
                  :id="`openai-fast-policy-tier-${ruleIndex}`"
                  :modelValue="rule.service_tier"
                  @update:modelValue="rule.service_tier = $event as 'all' | 'priority' | 'flex'"
                  :options="openaiFastPolicyTierOptions"
                />
              </div>
              <div>
                <label :for="`openai-fast-policy-action-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.openaiFastPolicy.action') }}
                </label>
                <Select
                  :id="`openai-fast-policy-action-${ruleIndex}`"
                  :modelValue="rule.action"
                  @update:modelValue="
                    rule.action = $event as
                      'pass' | 'filter' | 'block' | 'force_priority' | 'force_ultrafast'
                  "
                  :options="openaiFastPolicyActionOptions"
                />
              </div>
              <div>
                <label :for="`openai-fast-policy-scope-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.openaiFastPolicy.scope') }}
                </label>
                <Select
                  :id="`openai-fast-policy-scope-${ruleIndex}`"
                  :modelValue="rule.scope"
                  @update:modelValue="rule.scope = $event as 'all' | 'oauth' | 'apikey' | 'bedrock'"
                  :options="openaiFastPolicyScopeOptions"
                />
              </div>
            </div>
            <div>
              <p class="input-label">
                {{ t('admin.settings.openaiFastPolicy.userIds') }}
              </p>
              <OpenAIFastPolicyUserSelector
                :model-value="rule.user_ids || []"
                @update:model-value="rule.user_ids = $event"
              />
              <p class="input-hint">
                {{ t('admin.settings.openaiFastPolicy.userIdsHint') }}
              </p>
            </div>
            <div v-if="rule.action === 'block'">
              <label class="input-label">{{
                t('admin.settings.openaiFastPolicy.errorMessage')
              }}</label>
              <input
                v-model="rule.error_message"
                type="text"
                class="input"
                :placeholder="t('admin.settings.openaiFastPolicy.errorMessagePlaceholder')"
              />
              <p class="input-hint">
                {{ t('admin.settings.openaiFastPolicy.errorMessageHint') }}
              </p>
            </div>
            <div
              role="group"
              :aria-labelledby="`openai-fast-policy-models-label-${ruleIndex}`"
              :aria-describedby="`openai-fast-policy-models-hint-${ruleIndex}`"
            >
              <p :id="`openai-fast-policy-models-label-${ruleIndex}`" class="input-label mb-0">
                {{ t('admin.settings.openaiFastPolicy.modelWhitelist') }}
              </p>
              <p :id="`openai-fast-policy-models-hint-${ruleIndex}`" class="input-hint mb-2">
                {{ t('admin.settings.openaiFastPolicy.modelWhitelistHint') }}
              </p>
              <RuleListEditor
                :items="rule.model_whitelist || []"
                :add-label="t('admin.settings.openaiFastPolicy.addModelPattern')"
                add-placement="footer"
                :animated="false"
                :test-id="`openai-fast-models-${ruleIndex}`"
                @add="addOpenAIFastPolicyModelPattern(rule)"
                @remove="removeOpenAIFastPolicyModelPattern(rule, $event)"
              >
                <template #row="{ index: patternIdx }">
                  <input
                    v-model="rule.model_whitelist![patternIdx]"
                    type="text"
                    class="input"
                    :placeholder="t('admin.settings.openaiFastPolicy.modelPatternPlaceholder')"
                  />
                </template>
              </RuleListEditor>
            </div>
            <div v-if="hasOpenAIFastPolicyTargetModels(rule)">
              <label :for="`openai-fast-policy-fallback-${ruleIndex}`" class="input-label">
                {{ t('admin.settings.openaiFastPolicy.fallbackAction') }}
              </label>
              <Select
                :id="`openai-fast-policy-fallback-${ruleIndex}`"
                :modelValue="rule.fallback_action || 'pass'"
                @update:modelValue="
                  rule.fallback_action = $event as
                    'pass' | 'filter' | 'block' | 'force_priority' | 'force_ultrafast'
                "
                :options="openaiFastPolicyActionOptions"
              />
              <p class="input-hint">
                {{ t('admin.settings.openaiFastPolicy.fallbackActionHint') }}
              </p>
              <div v-if="rule.fallback_action === 'block'" class="mt-2">
                <label class="input-label">{{
                  t('admin.settings.openaiFastPolicy.errorMessage')
                }}</label>
                <input
                  v-model="rule.fallback_error_message"
                  type="text"
                  class="input"
                  :placeholder="
                    t('admin.settings.openaiFastPolicy.fallbackErrorMessagePlaceholder')
                  "
                />
              </div>
            </div>
          </div>
        </template>
      </RuleListEditor>
      <p class="input-hint">{{ t('admin.settings.openaiFastPolicy.saveHint') }}</p>
    </SettingsSection>
  </SettingsCard>
</template>

<script setup lang="ts">
import { vContentReveal } from '@/directives/contentReveal'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import Collapse from '@/components/common/Collapse.vue'
import SettingRow from '@/components/common/settings/SettingRow.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import OpenAIFastPolicyUserSelector from '@/views/admin/settings/OpenAIFastPolicyUserSelector.vue'
import type { useGatewaySettings } from './useGatewaySettings'
import { toRef } from 'vue'
import type { GatewaySection } from './useNavigationSettings'

/**
 * GatewayPolicySettings.vue renders provider cooldown, request rectification, and
 * tier policy cards from the page-owned gateway controller. Cards select their
 * category with v-show so navigation retains the rule editors and their draft rows;
 * endpoint loading flags determine when each dedicated form replaces its skeleton.
 */
// controller holds the page-owned refs and actions used by these cards.
// activeGatewaySection changes their visibility while mounted editors retain draft state
// and their membership in the parent save sequence.
const props = defineProps<{
  controller: ReturnType<typeof useGatewaySettings> // Page-owned gateway actions and state.
  activeGatewaySection: GatewaySection // Current visible gateway category.
}>()
// Template bindings retain the controller refs, so edits participate in the page save snapshots.
const {
  overloadCooldownLoading,
  overloadCooldownForm,
  openAI403CooldownLoading,
  openAI403CooldownForm,
  rateLimit429CooldownLoading,
  rateLimit429CooldownForm,
  streamTimeoutLoading,
  streamTimeoutForm,
  streamTimeoutActionOptions,
  rectifierLoading,
  rectifierForm,
  betaPolicyLoading,
  betaPolicyForm,
  openaiFastPolicyForm,
  betaPolicyActionOptions,
  betaPolicyScopeOptions,
  betaPresets,
  commonModelPatterns,
  getBetaDisplayName,
  applyBetaPreset,
  addQuickPattern,
  openaiFastPolicyTierOptions,
  openaiFastPolicyActionOptions,
  openaiFastPolicyActionSummary,
  hasOpenAIFastPolicyTargetModels,
  openaiFastPolicyScopeOptions,
  addOpenAIFastPolicyRule,
  removeOpenAIFastPolicyRule,
  addOpenAIFastPolicyModelPattern,
  removeOpenAIFastPolicyModelPattern,
} = props.controller
// Navigation remains reactive when the parent changes category, including while this wrapper is hidden.
const activeGatewaySection = toRef(props, 'activeGatewaySection')
// Translation lookup follows locale changes for the labels and hints rendered by this panel.
const { t } = useI18n()
</script>
