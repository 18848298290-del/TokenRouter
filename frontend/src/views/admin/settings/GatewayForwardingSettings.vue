<template>
  <SettingsCard
    v-show="activeGatewaySection === 'anthropic'"
    v-content-reveal="activeGatewaySection === 'anthropic'"
    data-testid="gateway-card-claude-code"
    :title="t('admin.settings.claudeCode.title')"
    :description="t('admin.settings.claudeCode.description')"
  >
    <SettingsSection>
      <SettingRow
        id="claude-code-min-version"
        field
        label-for="claude-code-min-version"
        :label="t('admin.settings.claudeCode.minVersion')"
        :hint="t('admin.settings.claudeCode.minVersionHint')"
      >
        <input
          id="claude-code-min-version"
          v-model="form.min_claude_code_version"
          type="text"
          class="input font-mono text-sm"
          :placeholder="t('admin.settings.claudeCode.minVersionPlaceholder')"
        />
      </SettingRow>
      <SettingRow
        id="claude-code-max-version"
        field
        label-for="claude-code-max-version"
        :label="t('admin.settings.claudeCode.maxVersion')"
        :hint="t('admin.settings.claudeCode.maxVersionHint')"
      >
        <input
          id="claude-code-max-version"
          v-model="form.max_claude_code_version"
          type="text"
          class="input font-mono text-sm"
          :placeholder="t('admin.settings.claudeCode.maxVersionPlaceholder')"
        />
      </SettingRow>
    </SettingsSection>
  </SettingsCard>

  <!-- Ollama Cloud polling and usage refresh controls. -->
  <SettingsCard
    v-show="activeGatewaySection === 'ollamaCloud'"
    v-content-reveal="activeGatewaySection === 'ollamaCloud'"
    data-testid="ollama-cloud-usage-global-settings"
    :title="t('admin.settings.ollamaCloudUsage.title')"
    :description="t('admin.settings.ollamaCloudUsage.description')"
  >
    <ContentSkeleton v-if="ollamaCloudUsageLoading" variant="form" :rows="3" />
    <SettingsSection v-else>
      <SettingToggleRow
        id="ollama-cloud-usage-enabled"
        v-model="ollamaCloudUsageForm.enabled"
        :label="t('admin.settings.ollamaCloudUsage.enabled')"
        :hint="t('admin.settings.ollamaCloudUsage.enabledHint')"
        testid="ollama-cloud-usage-global-enabled"
      />
      <Collapse :open="ollamaCloudUsageForm.enabled" unmount-on-hide>
        <SettingsSubpanel>
          <SettingRow
            id="ollama-cloud-usage-debounce"
            field
            label-for="ollama-cloud-usage-debounce"
            :label="t('admin.settings.ollamaCloudUsage.debounceMinutes')"
            :hint="t('admin.settings.ollamaCloudUsage.debounceHint')"
          >
            <input
              id="ollama-cloud-usage-debounce"
              v-model.number="ollamaCloudUsageForm.debounce_minutes"
              type="number"
              min="1"
              max="60"
              class="input"
              data-testid="ollama-cloud-usage-global-debounce"
              @keydown.enter.prevent="saveOllamaCloudUsageSettings"
            />
          </SettingRow>
          <SettingRow
            id="ollama-cloud-usage-interval"
            field
            label-for="ollama-cloud-usage-interval"
            :label="t('admin.settings.ollamaCloudUsage.intervalMinutes')"
            :hint="t('admin.settings.ollamaCloudUsage.intervalHint')"
          >
            <input
              id="ollama-cloud-usage-interval"
              v-model.number="ollamaCloudUsageForm.interval_minutes"
              type="number"
              min="15"
              max="1440"
              class="input"
              data-testid="ollama-cloud-usage-global-interval"
              @keydown.enter.prevent="saveOllamaCloudUsageSettings"
            />
          </SettingRow>
        </SettingsSubpanel>
      </Collapse>
    </SettingsSection>
  </SettingsCard>

  <!-- General scheduling and latency measurement controls. -->
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-scheduling"
    :title="t('admin.settings.scheduling.title')"
    :description="t('admin.settings.scheduling.description')"
  >
    <div class="settings-section space-y-6" data-testid="gateway-scheduling-general">
      <!-- The global choice controls upstream TTFT for every text platform and protocol. -->
      <SettingRow
        id="ttft-mode"
        field
        :label="t('admin.settings.gatewayForwarding.ttftMode')"
        :hint="t('admin.settings.gatewayForwarding.ttftModeHint')"
      >
        <Select
          v-model="form.ttft_mode"
          :options="ttftModeOptions"
          :aria-label="t('admin.settings.gatewayForwarding.ttftMode')"
          data-testid="ttft-mode"
        />
      </SettingRow>

      <SettingsSection
        :title="t('admin.settings.scheduling.providerSchedulingThresholdsTitle')"
        :hint="t('admin.settings.scheduling.providerSchedulingThresholdsDescription')"
      >
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <div v-for="platform in schedulingThresholdPlatforms" :key="platform">
            <label :for="`provider-scheduling-threshold-${platform}`" class="input-label font-mono">
              {{ platform }}
            </label>
            <div class="input-icon-wrap">
              <input
                :id="`provider-scheduling-threshold-${platform}`"
                v-model.number="form.provider_scheduling_thresholds[platform]"
                type="number"
                min="1"
                max="100"
                step="1"
                class="input input-has-icon-right"
                :data-testid="`provider-scheduling-threshold-${platform}`"
                placeholder="100"
              />
              <span
                class="input-icon-right text-sm text-gray-400 dark:text-dark-400"
                aria-hidden="true"
                >%</span
              >
            </div>
          </div>
        </div>
        <div class="space-y-1">
          <p class="input-hint">
            {{ t('admin.settings.scheduling.providerSchedulingThresholdsRangeHint') }}
          </p>
          <p class="input-hint">
            {{ t('admin.settings.scheduling.providerSchedulingThresholdsDisabledHint') }}
          </p>
          <p class="input-hint">
            {{ t('admin.settings.scheduling.providerSchedulingThresholdsGlobalHint') }}
          </p>
        </div>
      </SettingsSection>

      <SettingsSection
        :title="t('admin.settings.scheduling.stickyEscapeTitle')"
        :hint="t('admin.settings.scheduling.stickyEscapeDescription')"
      >
        <SettingToggleRow
          id="advanced-scheduler-sticky-escape-enabled"
          v-model="form.advanced_scheduler_sticky_escape_enabled"
          :label="t('admin.settings.scheduling.stickyEscapeEnabled')"
        />
        <Collapse :open="form.advanced_scheduler_sticky_escape_enabled">
          <SettingsSubpanel>
            <SettingRow
              id="advanced-scheduler-sticky-escape-ttft"
              field
              label-for="advanced-scheduler-sticky-escape-ttft"
              :label="t('admin.settings.scheduling.stickyEscapeTTFT')"
            >
              <input
                id="advanced-scheduler-sticky-escape-ttft"
                v-model="form.advanced_scheduler_sticky_escape_ttft_ms"
                class="input"
                inputmode="numeric"
                type="text"
                :placeholder="
                  advancedSchedulerPlaceholder(
                    'advanced_scheduler_effective_sticky_escape_ttft_ms',
                    '15000',
                  )
                "
              />
            </SettingRow>
            <SettingRow
              id="advanced-scheduler-sticky-escape-error-rate"
              field
              label-for="advanced-scheduler-sticky-escape-error-rate"
              :label="t('admin.settings.scheduling.stickyEscapeErrorRate')"
            >
              <input
                id="advanced-scheduler-sticky-escape-error-rate"
                v-model="form.advanced_scheduler_sticky_escape_error_rate"
                class="input"
                inputmode="decimal"
                type="text"
                :placeholder="
                  advancedSchedulerPlaceholder(
                    'advanced_scheduler_effective_sticky_escape_error_rate',
                    '0.5',
                  )
                "
              />
            </SettingRow>
          </SettingsSubpanel>
        </Collapse>
      </SettingsSection>
    </div>

    <div class="settings-section space-y-6" data-testid="gateway-scheduling-general-advanced">
      <SettingsSection
        :title="t('admin.settings.scheduling.advancedTitle')"
        :hint="t('admin.settings.scheduling.advancedDescription')"
      >
        <template #actions>
          <HelpTooltip
            trigger="both"
            placement="bottom"
            width-class="w-max max-w-[calc(100vw-1.5rem)] sm:w-96"
            class="shrink-0"
            data-testid="advanced-scheduler-help"
          >
            <template #trigger>
              <button
                type="button"
                class="inline-flex h-7 w-7 items-center justify-center rounded-control text-gray-400 transition-colors hover:bg-gray-100 hover:text-primary-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:text-gray-500 dark:hover:bg-dark-800 dark:hover:text-primary-400"
                :aria-label="t('admin.settings.scheduling.advancedHelp.trigger')"
                :title="t('admin.settings.scheduling.advancedHelp.trigger')"
              >
                <Icon name="questionCircle" size="sm" :stroke-width="1.75" />
              </button>
            </template>
            <div class="space-y-2.5">
              <p class="font-semibold text-white">
                {{ t('admin.settings.scheduling.advancedHelp.title') }}
              </p>
              <p class="text-gray-200">
                {{ t('admin.settings.scheduling.advancedHelp.summary') }}
              </p>
              <p class="rounded-control bg-white/5 px-2 py-1.5 text-xs text-gray-200">
                {{ t('admin.settings.scheduling.advancedHelp.formula') }}
              </p>
              <ol class="list-decimal space-y-2 pl-4 text-gray-200">
                <li>
                  <span class="font-medium text-white">
                    {{ t('admin.settings.scheduling.advancedHelp.hardFilterTitle') }}
                  </span>
                  {{ t('admin.settings.scheduling.advancedHelp.hardFilter') }}
                </li>
                <li>
                  <span class="font-medium text-white">
                    {{ t('admin.settings.scheduling.advancedHelp.scoreTitle') }}
                  </span>
                  {{ t('admin.settings.scheduling.advancedHelp.score') }}
                </li>
                <li>
                  <span class="font-medium text-white">
                    {{ t('admin.settings.scheduling.advancedHelp.feedbackTitle') }}
                  </span>
                  {{ t('admin.settings.scheduling.advancedHelp.feedback') }}
                </li>
                <li>
                  <span class="font-medium text-white">
                    {{ t('admin.settings.scheduling.advancedHelp.selectionTitle') }}
                  </span>
                  {{ t('admin.settings.scheduling.advancedHelp.selection') }}
                </li>
              </ol>
              <p class="border-t border-white/10 pt-2 text-gray-300">
                {{ t('admin.settings.scheduling.advancedHelp.boundary') }}
              </p>
            </div>
          </HelpTooltip>
        </template>
        <SettingToggleRow
          id="advanced-scheduler-sticky-weighted"
          v-model="form.advanced_scheduler_sticky_weighted_enabled"
          :label="t('admin.settings.scheduling.stickyWeightedTitle')"
          :hint="t('admin.settings.scheduling.stickyWeightedDescription')"
        />
        <SettingToggleRow
          id="advanced-scheduler-subscription-priority"
          v-model="form.advanced_scheduler_subscription_priority_enabled"
          :label="t('admin.settings.scheduling.subscriptionPriorityTitle')"
          :hint="t('admin.settings.scheduling.subscriptionPriorityDescription')"
        />
      </SettingsSection>

      <SettingsSection
        :title="t('admin.settings.scheduling.ewmaTitle')"
        :hint="t('admin.settings.scheduling.ewmaDescription')"
      >
        <SettingRow
          id="advanced-scheduler-ewma-error-rate"
          field
          label-for="advanced-scheduler-ewma-error-rate"
          :label="t('admin.settings.scheduling.ewmaErrorRateAlpha')"
        >
          <input
            id="advanced-scheduler-ewma-error-rate"
            v-model="form.advanced_scheduler_ewma_error_rate_alpha"
            class="input"
            inputmode="decimal"
            type="text"
            :placeholder="
              advancedSchedulerPlaceholder(
                'advanced_scheduler_effective_ewma_error_rate_alpha',
                '0.2',
              )
            "
          />
        </SettingRow>
        <SettingRow
          id="advanced-scheduler-ewma-ttft"
          field
          label-for="advanced-scheduler-ewma-ttft"
          :label="t('admin.settings.scheduling.ewmaTTFTAlpha')"
        >
          <input
            id="advanced-scheduler-ewma-ttft"
            v-model="form.advanced_scheduler_ewma_ttft_alpha"
            class="input"
            inputmode="decimal"
            type="text"
            :placeholder="
              advancedSchedulerPlaceholder('advanced_scheduler_effective_ewma_ttft_alpha', '0.2')
            "
          />
        </SettingRow>
      </SettingsSection>

      <SettingsSection
        :title="t('admin.settings.scheduling.weightsTitle')"
        :hint="t('admin.settings.scheduling.weightsDescription')"
      >
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-5">
          <div v-for="field in advancedSchedulerWeightFields" :key="field.key">
            <label :for="`advanced-scheduler-weight-${field.key}`" class="input-label">
              {{ field.label }}
            </label>
            <input
              :id="`advanced-scheduler-weight-${field.key}`"
              v-model="form[field.key]"
              class="input"
              inputmode="decimal"
              :placeholder="field.placeholder"
              type="text"
            />
          </div>
        </div>
      </SettingsSection>
    </div>
  </SettingsCard>

  <!-- OpenAI quota exhaustion thresholds. -->
  <SettingsCard
    v-show="activeGatewaySection === 'openai'"
    v-content-reveal="activeGatewaySection === 'openai'"
    data-testid="gateway-card-openai-scheduling"
    :title="t('admin.settings.openaiScheduling.title')"
    :description="t('admin.settings.openaiScheduling.description')"
  >
    <SettingsSection
      :title="t('admin.settings.openaiQuotaAutoPause.title')"
      :hint="t('admin.settings.openaiQuotaAutoPause.description')"
    >
      <SettingRow
        id="openai-quota-auto-pause-5h"
        field
        label-for="openai-quota-auto-pause-5h"
        :label="t('admin.settings.openaiQuotaAutoPause.default5h')"
      >
        <input
          id="openai-quota-auto-pause-5h"
          v-model.number="openAIQuotaAutoPause5hPercent"
          type="number"
          min="0"
          max="100"
          step="0.1"
          class="input"
          data-testid="settings-openai-quota-auto-pause-5h"
        />
      </SettingRow>
      <SettingRow
        id="openai-quota-auto-pause-7d"
        field
        label-for="openai-quota-auto-pause-7d"
        :label="t('admin.settings.openaiQuotaAutoPause.default7d')"
        :hint="t('admin.settings.openaiQuotaAutoPause.thresholdHint')"
      >
        <input
          id="openai-quota-auto-pause-7d"
          v-model.number="openAIQuotaAutoPause7dPercent"
          type="number"
          min="0"
          max="100"
          step="0.1"
          class="input"
          data-testid="settings-openai-quota-auto-pause-7d"
        />
      </SettingRow>
    </SettingsSection>
  </SettingsCard>

  <!-- Grok default model and endpoint controls. -->
  <SettingsCard
    v-show="activeGatewaySection === 'grok'"
    v-content-reveal="activeGatewaySection === 'grok'"
    data-testid="gateway-forwarding-grok"
    :title="t('admin.settings.gatewayForwarding.grokTitle')"
    :description="t('admin.settings.gatewayForwarding.grokDescription')"
  >
    <SettingsSection>
      <SettingRow
        id="grok-default-text-model"
        field
        label-for="grok-default-text-model"
        :label="t('admin.settings.gatewayForwarding.grokDefaultTextModel')"
        :hint="t('admin.settings.gatewayForwarding.grokDefaultTextModelHint')"
      >
        <input
          id="grok-default-text-model"
          v-model.trim="form.grok_default_text_model"
          type="text"
          class="input"
          list="grok-default-text-model-options"
          data-testid="grok-default-text-model"
          placeholder="grok-4.5"
        />
        <datalist id="grok-default-text-model-options">
          <option value="grok-4.5" />
          <option value="grok-4.3" />
          <option value="grok-build-0.1" />
        </datalist>
      </SettingRow>
      <SettingRow
        id="grok-default-base-url-mode"
        field
        :label="t('admin.settings.gatewayForwarding.grokDefaultBaseURLMode')"
        :hint="t('admin.settings.gatewayForwarding.grokDefaultBaseURLModeHint')"
      >
        <Select
          id="grok-default-base-url-mode"
          v-model="form.grok_default_base_url_mode"
          :options="grokDefaultBaseURLOptions"
          :aria-label="t('admin.settings.gatewayForwarding.grokDefaultBaseURLMode')"
          data-testid="grok-default-base-url-mode"
        />
      </SettingRow>
    </SettingsSection>
  </SettingsCard>

  <!-- Anthropic forwarding and prompt injection controls. -->
  <SettingsCard
    v-show="activeGatewaySection === 'anthropic'"
    v-content-reveal="activeGatewaySection === 'anthropic'"
    data-testid="gateway-forwarding-anthropic"
    :title="t('admin.settings.gatewayForwarding.anthropicTitle')"
    :description="t('admin.settings.gatewayForwarding.anthropicDescription')"
  >
    <SettingsSection>
      <SettingToggleRow
        id="enable-fingerprint-unification"
        v-model="form.enable_fingerprint_unification"
        :label="t('admin.settings.gatewayForwarding.fingerprintUnification')"
        :hint="t('admin.settings.gatewayForwarding.fingerprintUnificationHint')"
      />
      <SettingToggleRow
        id="enable-metadata-passthrough"
        v-model="form.enable_metadata_passthrough"
        :label="t('admin.settings.gatewayForwarding.metadataPassthrough')"
        :hint="t('admin.settings.gatewayForwarding.metadataPassthroughHint')"
      />
      <SettingToggleRow
        id="enable-anthropic-cache-ttl-1h-injection"
        v-model="form.enable_anthropic_cache_ttl_1h_injection"
        :label="t('admin.settings.gatewayForwarding.anthropicCacheTTL1hInjection')"
        :hint="t('admin.settings.gatewayForwarding.anthropicCacheTTL1hInjectionHint')"
      />
      <SettingToggleRow
        id="rewrite-message-cache-control"
        v-model="form.rewrite_message_cache_control"
        :label="t('admin.settings.gatewayForwarding.rewriteMessageCacheControl')"
        :hint="t('admin.settings.gatewayForwarding.rewriteMessageCacheControlHint')"
      />
      <SettingToggleRow
        id="enable-client-dateline-normalization"
        v-model="form.enable_client_dateline_normalization"
        :label="t('admin.settings.gatewayForwarding.clientDatelineNormalization')"
        :hint="t('admin.settings.gatewayForwarding.clientDatelineNormalizationHint')"
      />
    </SettingsSection>
    <SettingsSection>
      <!-- Prompt injection applies to Anthropic OAuth and Setup Token requests. -->
      <SettingToggleRow
        id="enable-claude-oauth-system-prompt-injection"
        v-model="form.enable_claude_oauth_system_prompt_injection"
        :label="t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptInjection')"
        :hint="t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptInjectionHint')"
      />
      <div class="grid gap-4 md:grid-cols-2">
        <div>
          <label for="claude-oauth-system-prompt" class="input-label">
            {{ t('admin.settings.gatewayForwarding.claudeOAuthSystemPrompt') }}
          </label>
          <textarea
            id="claude-oauth-system-prompt"
            v-model="form.claude_oauth_system_prompt"
            rows="6"
            class="input min-h-32 font-mono text-sm"
            :placeholder="t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptPlaceholder')"
          />
          <p class="input-hint">
            {{ t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptHint') }}
          </p>
        </div>
        <div>
          <label for="claude-oauth-system-prompt-blocks" class="input-label">
            {{ t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptBlocks') }}
          </label>
          <textarea
            id="claude-oauth-system-prompt-blocks"
            v-model="form.claude_oauth_system_prompt_blocks"
            rows="6"
            class="input min-h-32 font-mono text-sm"
            :placeholder="
              t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptBlocksPlaceholder')
            "
          />
          <p class="input-hint">
            {{ t('admin.settings.gatewayForwarding.claudeOAuthSystemPromptBlocksHint') }}
          </p>
        </div>
      </div>
    </SettingsSection>
  </SettingsCard>

  <!-- Antigravity client identity controls. -->
  <SettingsCard
    v-show="activeGatewaySection === 'antigravity'"
    v-content-reveal="activeGatewaySection === 'antigravity'"
    data-testid="gateway-forwarding-antigravity"
    :title="t('admin.settings.gatewayForwarding.antigravityTitle')"
    :description="t('admin.settings.gatewayForwarding.antigravityDescription')"
  >
    <SettingsSection>
      <SettingRow
        id="antigravity-user-agent-version"
        field
        label-for="antigravity-user-agent-version"
        :label="t('admin.settings.gatewayForwarding.antigravityUserAgentVersion')"
        :hint="t('admin.settings.gatewayForwarding.antigravityUserAgentVersionHint')"
      >
        <input
          id="antigravity-user-agent-version"
          v-model="form.antigravity_user_agent_version"
          type="text"
          class="input font-mono text-sm"
          :placeholder="
            t('admin.settings.gatewayForwarding.antigravityUserAgentVersionPlaceholder')
          "
        />
      </SettingRow>
    </SettingsSection>
  </SettingsCard>

  <ResponsesWSSettings
    v-show="activeGatewaySection === 'openai'"
    v-content-reveal="activeGatewaySection === 'openai'"
  />
  <!-- OpenAI and Codex client forwarding controls. -->
  <SettingsCard
    v-show="activeGatewaySection === 'openai'"
    v-content-reveal="activeGatewaySection === 'openai'"
    data-testid="gateway-forwarding-openai"
    :title="t('admin.settings.gatewayForwarding.openaiTitle')"
    :description="t('admin.settings.gatewayForwarding.openaiDescription')"
  >
    <SettingsSection>
      <!-- This global switch controls Codex plugin access from Claude Code. -->
      <SettingToggleRow
        id="openai-allow-claude-code-codex-plugin"
        v-model="form.openai_allow_claude_code_codex_plugin"
        :label="t('admin.settings.gatewayForwarding.openaiAllowClaudeCodeCodexPlugin')"
        :hint="t('admin.settings.gatewayForwarding.openaiAllowClaudeCodeCodexPluginDesc')"
      />
      <div>
        <label for="openai-codex-user-agent" class="input-label">
          {{ t('admin.settings.gatewayForwarding.openaiCodexUserAgent') }}
        </label>
        <input
          id="openai-codex-user-agent"
          v-model="form.openai_codex_user_agent"
          type="text"
          class="input font-mono text-sm"
          :placeholder="t('admin.settings.gatewayForwarding.openaiCodexUserAgentPlaceholder')"
        />
        <p class="input-hint">
          {{ t('admin.settings.gatewayForwarding.openaiCodexUserAgentHint') }}
        </p>
      </div>
    </SettingsSection>
  </SettingsCard>

  <!-- User prompt replacement rules shared across platforms. -->
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-user-prompt-replacement"
    :title="t('admin.settings.userPromptReplacement.title')"
    :description="t('admin.settings.userPromptReplacement.description')"
  >
    <template #actions>
      <Toggle
        v-model="form.user_prompt_replacement_config.enabled"
        size="md"
        :aria-label="t('admin.settings.userPromptReplacement.title')"
      />
    </template>
    <SettingsSection>
      <RuleListEditor
        :items="form.user_prompt_replacement_config.rules"
        variant="card"
        :item-label="(index) => t('common.ruleIndex', { index: index + 1 })"
        :add-label="t('admin.settings.userPromptReplacement.addRule')"
        :empty-text="t('admin.settings.userPromptReplacement.empty')"
        test-id="prompt-replacement-rules"
        @add="addUserPromptReplacementRule"
        @remove="removeUserPromptReplacementRule"
      >
        <template #header-actions>
          <button type="button" class="btn btn-secondary" @click="resetUserPromptReplacementRules">
            {{ t('admin.settings.userPromptReplacement.resetDefault') }}
          </button>
        </template>
        <template #row="{ item: rule, index: ruleIndex }">
          <div class="space-y-4">
            <div class="flex min-w-0 items-center gap-3">
              <Toggle
                v-model="rule.enabled"
                size="md"
                :aria-label="rule.name || t('common.ruleIndex', { index: ruleIndex + 1 })"
              />
              <input
                v-model="rule.name"
                type="text"
                class="input min-w-0 flex-1"
                :aria-label="t('admin.settings.userPromptReplacement.namePlaceholder')"
                :placeholder="t('admin.settings.userPromptReplacement.namePlaceholder')"
              />
            </div>
            <div class="grid gap-4 lg:grid-cols-6">
              <div class="lg:col-span-4">
                <label :for="`prompt-replacement-pattern-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.userPromptReplacement.pattern') }}
                </label>
                <textarea
                  :id="`prompt-replacement-pattern-${ruleIndex}`"
                  v-model="rule.pattern"
                  rows="3"
                  class="input font-mono text-xs"
                  :placeholder="t('admin.settings.userPromptReplacement.patternPlaceholder')"
                />
              </div>
              <div>
                <label :for="`prompt-replacement-group-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.userPromptReplacement.targetGroup') }}
                </label>
                <input
                  :id="`prompt-replacement-group-${ruleIndex}`"
                  v-model.number="rule.target_group"
                  type="number"
                  min="0"
                  step="1"
                  class="input"
                />
              </div>
              <div>
                <label :for="`prompt-replacement-type-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.userPromptReplacement.replacementType') }}
                </label>
                <Select
                  :id="`prompt-replacement-type-${ruleIndex}`"
                  v-model="rule.replacement_type"
                  :options="userPromptReplacementTypeOptions"
                />
              </div>
            </div>
            <div class="grid gap-4 md:grid-cols-3">
              <div v-if="rule.replacement_type === 'static'">
                <label :for="`prompt-replacement-static-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.userPromptReplacement.staticText') }}
                </label>
                <input
                  :id="`prompt-replacement-static-${ruleIndex}`"
                  v-model="rule.static_text"
                  type="text"
                  class="input"
                  :placeholder="t('admin.settings.userPromptReplacement.staticTextPlaceholder')"
                />
              </div>
              <div v-if="rule.replacement_type !== 'static'">
                <label :for="`prompt-replacement-timezone-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.userPromptReplacement.timezone') }}
                </label>
                <Select
                  :id="`prompt-replacement-timezone-${ruleIndex}`"
                  v-model="rule.timezone"
                  :options="userPromptReplacementTimezoneOptions"
                  searchable
                  creatable
                  :creatable-prefix="t('admin.settings.userPromptReplacement.useTimezone')"
                />
              </div>
              <div v-if="rule.replacement_type === 'current_time'">
                <label :for="`prompt-replacement-format-${ruleIndex}`" class="input-label">
                  {{ t('admin.settings.userPromptReplacement.timeFormat') }}
                </label>
                <input
                  :id="`prompt-replacement-format-${ruleIndex}`"
                  v-model="rule.time_format"
                  type="text"
                  class="input font-mono text-sm"
                  placeholder="2006-01-02"
                />
              </div>
            </div>
          </div>
        </template>
      </RuleListEditor>
    </SettingsSection>
  </SettingsCard>

  <!-- The global save sequence submits search emulation through its dedicated endpoint. -->
  <WebSearchSettings :controller="webSearch" :active-gateway-section="activeGatewaySection" />

  <!-- Usage record visibility for users. -->
  <SettingsCard
    v-show="activeGatewaySection === 'general'"
    v-content-reveal="activeGatewaySection === 'general'"
    data-testid="gateway-card-usage-records"
    :title="t('admin.settings.usageRecords.title')"
    :description="t('admin.settings.usageRecords.description')"
  >
    <SettingsSection>
      <SettingToggleRow
        id="allow-user-view-error-requests"
        v-model="form.allow_user_view_error_requests"
        :label="t('admin.settings.user_error_view.label')"
        :hint="t('admin.settings.user_error_view.description')"
      />
    </SettingsSection>
  </SettingsCard>
</template>

<script setup lang="ts">
import { vContentReveal } from '@/directives/contentReveal'
import ResponsesWSSettings from '@/views/admin/settings/ResponsesWSSettings.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import Collapse from '@/components/common/Collapse.vue'
import SettingRow from '@/components/common/settings/SettingRow.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import SettingsCard from '@/components/common/settings/SettingsCard.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Select from '@/components/common/Select.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import Toggle from '@/components/common/Toggle.vue'
import type { useGatewaySettings, GatewaySettingsForm } from './useGatewaySettings'
import { toRef } from 'vue'
import type { GatewaySection } from './useNavigationSettings'
import WebSearchSettings from './WebSearchSettings.vue'
import type { useWebSearchSettings } from './useWebSearchSettings'

/**
 * GatewayForwardingSettings.vue renders scheduling, forwarding identity, prompt
 * replacement, and usage visibility controls against the shared gateway draft. It
 * also hosts the independently registered Responses WebSocket editor and the search
 * configuration panel, whose mounted positions determine their participation in the
 * page save flow.
 */
// controller and form refer to the page-owned gateway actions and bulk draft, while
// webSearch holds the separate search draft. activeGatewaySection changes visibility;
// all nested editors remain mounted so their save registration and local state survive.
const props = defineProps<{
  controller: ReturnType<typeof useGatewaySettings> // Page-owned gateway actions and state.
  form: GatewaySettingsForm // Shared editable gateway field references.
  activeGatewaySection: GatewaySection // Current visible gateway category.
  webSearch: ReturnType<typeof useWebSearchSettings> // Search draft and dedicated endpoint actions.
}>()
// Template bindings retain the controller refs, so edits participate in the page save snapshots.
const {
  ttftModeOptions,
  ollamaCloudUsageLoading,
  ollamaCloudUsageForm,
  schedulingThresholdPlatforms,
  openAIQuotaAutoPause5hPercent,
  openAIQuotaAutoPause7dPercent,
  grokDefaultBaseURLOptions,
  userPromptReplacementTypeOptions,
  userPromptReplacementTimezoneOptions,
  addUserPromptReplacementRule,
  removeUserPromptReplacementRule,
  resetUserPromptReplacementRules,
  advancedSchedulerPlaceholder,
  advancedSchedulerWeightFields,
  saveOllamaCloudUsageSettings,
} = props.controller
// The bulk draft is mutated in place so the root tracker observes controls across category changes.
const { form } = props
// Navigation remains reactive when the parent changes category, including while this wrapper is hidden.
const activeGatewaySection = toRef(props, 'activeGatewaySection')
// The search editor uses its separate endpoint while sharing the page-wide save and discard controls.
const { webSearch } = props
// Translation lookup follows locale changes for the labels and hints rendered by this panel.
const { t } = useI18n()
</script>
