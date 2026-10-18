<template>
  <div ref="rootRef" v-if="showUsageWindows">
    <!-- Anthropic OAuth and Setup Token providers: fetch real usage data -->
    <template
      v-if="
        provider.platform === 'anthropic' &&
        (provider.type === 'oauth' || provider.type === 'setup-token')
      "
    >
      <!-- Loading state -->
      <div v-if="loading" class="space-y-1.5">
        <!-- OAuth: 3 rows, Setup Token: 1 row -->
        <div class="flex items-center gap-1">
          <div class="skeleton h-3 w-[32px]"></div>
          <div class="skeleton h-1.5 w-8 rounded-full"></div>
          <div class="skeleton h-3 w-[32px]"></div>
        </div>
        <template v-if="provider.type === 'oauth'">
          <div class="flex items-center gap-1">
            <div class="skeleton h-3 w-[32px]"></div>
            <div class="skeleton h-1.5 w-8 rounded-full"></div>
            <div class="skeleton h-3 w-[32px]"></div>
          </div>
          <div class="flex items-center gap-1">
            <div class="skeleton h-3 w-[32px]"></div>
            <div class="skeleton h-1.5 w-8 rounded-full"></div>
            <div class="skeleton h-3 w-[32px]"></div>
          </div>
        </template>
      </div>

      <!-- Error state -->
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>

      <!-- Usage data -->
      <div v-else-if="usageInfo" class="space-y-1">
        <!-- API error (degraded response) -->
        <div v-if="usageInfo.error" class="text-xs text-amber-600 dark:text-amber-400 truncate max-w-[200px]" :title="usageInfo.error">
          {{ usageInfo.error }}
        </div>
        <!-- 5h Window -->
        <UsageProgressBar
          v-if="usageInfo.five_hour"
          label="5h"
          :utilization="usageInfo.five_hour.utilization"
          :resets-at="usageInfo.five_hour.resets_at"
          :window-stats="usageInfo.five_hour.window_stats"
          color="indigo"
        />

        <!-- 7d Window (OAuth only) -->
        <UsageProgressBar
          v-if="usageInfo.seven_day"
          label="7d"
          :utilization="usageInfo.seven_day.utilization"
          :resets-at="usageInfo.seven_day.resets_at"
          color="emerald"
        />

        <!-- 7d Sonnet Window (OAuth only) -->
        <UsageProgressBar
          v-if="usageInfo.seven_day_sonnet"
          label="7d S"
          :utilization="usageInfo.seven_day_sonnet.utilization"
          :resets-at="usageInfo.seven_day_sonnet.resets_at"
          color="purple"
        />

        <!-- Fable 专属 7d_oi 窗口 -->
        <UsageProgressBar
          v-if="usageInfo.seven_day_fable"
          label="7d F"
          :utilization="usageInfo.seven_day_fable.utilization"
          :resets-at="usageInfo.seven_day_fable.resets_at"
          color="amber"
        />

        <!-- Passive sampling label + active query button -->
        <div class="mt-0.5 flex items-center justify-end gap-1.5 lg:justify-start">
          <span
            v-if="usageInfo.source === 'passive'"
            class="text-xs text-gray-400 dark:text-gray-500 italic"
          >
            {{ t('admin.providers.usageWindow.passiveSampled') }}
          </span>
          <button
            type="button"
            class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 hover:bg-primary-50 dark:text-primary-500 dark:hover:bg-primary-500/8 transition-colors"
            :disabled="activeQueryLoading"
            @click="loadActiveUsage"
          >
            <Icon
              name="refresh"
              size="xs"
              :class="{ 'animate-spin': activeQueryLoading }"
              :stroke-width="2"
            />
            {{ t('admin.providers.usageWindow.activeQuery') }}
          </button>
        </div>
      </div>

      <!-- No data yet -->
      <div v-else class="space-y-1">
        <div class="text-xs text-gray-400">-</div>
        <!-- 即使还没有被动额度 header，也允许手动触发上游额度探测。 -->
        <GrokQuotaProbeCell :provider="provider" />
      </div>
    </template>

    <!-- OpenAI OAuth 提供商统一使用 /usage API 数据源 -->
    <template v-else-if="provider.platform === 'openai' && provider.type === 'oauth'">
      <div v-if="hasOpenAIUsageFallback" class="space-y-1">
        <UsageProgressBar
          v-if="usageInfo?.five_hour"
          label="5h"
          :utilization="usageInfo.five_hour.utilization"
          :resets-at="usageInfo.five_hour.resets_at"
          :window-stats="usageInfo.five_hour.window_stats"
          :show-now-when-idle="true"
          color="indigo"
        />
        <UsageProgressBar
          v-if="usageInfo?.seven_day"
          label="7d"
          :utilization="usageInfo.seven_day.utilization"
          :resets-at="usageInfo.seven_day.resets_at"
          :window-stats="usageInfo.seven_day.window_stats"
          :show-now-when-idle="true"
          color="emerald"
        />
        <OpenAIQuotaResetCell :provider="provider">
          <template #pre-actions>
            <button
              type="button"
              class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8"
              :disabled="activeQueryLoading"
              @click="loadActiveUsage"
            >
              <Icon
                name="refresh"
                size="xs"
                :class="{ 'animate-spin': activeQueryLoading }"
                :stroke-width="2"
              />
              {{ t('admin.providers.usageWindow.activeQuery') }}
            </button>
          </template>
          <span
            v-if="openAIQuotaAutoPaused"
            class="inline-flex items-center rounded-compact bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
          >
            {{ t('admin.providers.usageWindow.quotaAutoPaused') }}
          </span>
        </OpenAIQuotaResetCell>
      </div>
      <div v-else-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="skeleton h-3 w-[32px]"></div>
          <div class="skeleton h-1.5 w-8 rounded-full"></div>
          <div class="skeleton h-3 w-[32px]"></div>
        </div>
        <div class="flex items-center gap-1">
          <div class="skeleton h-3 w-[32px]"></div>
          <div class="skeleton h-1.5 w-8 rounded-full"></div>
          <div class="skeleton h-3 w-[32px]"></div>
        </div>
      </div>
      <div v-else>
        <div class="text-xs text-gray-400">-</div>
        <OpenAIQuotaResetCell :provider="provider" class="mt-1" />
      </div>
    </template>

    <!-- Antigravity OAuth providers: fetch usage from API -->
    <template v-else-if="provider.platform === 'antigravity' && provider.type === 'oauth'">
      <!-- 提供商类型徽章 -->
      <div v-if="antigravityTierLabel" class="mb-1 flex items-center gap-1">
        <span
          :class="[
            'inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium',
            antigravityTierClass
          ]"
        >
          {{ antigravityTierLabel }}
        </span>
        <!-- 不合格提供商警告图标 -->
        <span
          v-if="hasIneligibleTiers"
          class="group relative cursor-help"
        >
          <Icon name="exclamationCircle" size="xs" class="h-3.5 w-3.5 text-red-500" />
          <span
            class="pointer-events-none absolute left-0 top-full z-50 mt-1 w-80 whitespace-normal break-words tooltip-panel rounded-compact px-3 py-2 text-xs leading-relaxed opacity-0 shadow-lg transition-opacity group-hover:opacity-100"
          >
            {{ t('admin.providers.ineligibleWarning') }}
          </span>
        </span>
      </div>

      <!-- Forbidden state (403) -->
      <div v-if="isForbidden" class="space-y-1">
        <span
          :class="[
            'inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium',
            forbiddenBadgeClass
          ]"
        >
          {{ forbiddenLabel }}
        </span>
        <div v-if="validationURL" class="flex items-center gap-1">
          <a
            :href="validationURL"
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-primary-600 hover:text-primary-800 hover:underline dark:text-primary-500 dark:hover:text-primary-500"
            :title="t('admin.providers.openVerification')"
          >
            {{ t('admin.providers.openVerification') }}
          </a>
          <button
            type="button"
            class="text-xs text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"
            :title="t('admin.providers.copyLink')"
            @click="copyValidationURL"
          >
            {{ linkCopied ? t('admin.providers.linkCopied') : t('admin.providers.copyLink') }}
          </button>
        </div>
      </div>

      <!-- Needs reauth (401) -->
      <div v-else-if="needsReauth" class="space-y-1">
        <span class="inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium bg-orange-100 text-orange-700 dark:bg-orange-900/40 dark:text-orange-300">
          {{ t('admin.providers.needsReauth') }}
        </span>
      </div>

      <!-- Degraded error (non-403, non-401) -->
      <div v-else-if="usageInfo?.error" class="space-y-1">
        <span class="inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
          {{ usageErrorLabel }}
        </span>
      </div>

      <!-- Loading state -->
      <div v-else-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="skeleton h-3 w-[32px]"></div>
          <div class="skeleton h-1.5 w-8 rounded-full"></div>
          <div class="skeleton h-3 w-[32px]"></div>
        </div>
      </div>

      <!-- Error state -->
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>

      <!-- Usage data from API -->
      <div v-else-if="hasAntigravityQuotaFromAPI" class="space-y-1">
        <!-- Gemini 3 Pro -->
        <UsageProgressBar
          v-if="antigravity3ProUsageFromAPI !== null"
          :label="t('admin.providers.usageWindow.gemini3Pro')"
          :utilization="antigravity3ProUsageFromAPI.utilization"
          :resets-at="antigravity3ProUsageFromAPI.resetTime"
          color="indigo"
        />

        <!-- Gemini 3 Flash -->
        <UsageProgressBar
          v-if="antigravity3FlashUsageFromAPI !== null"
          :label="t('admin.providers.usageWindow.gemini3Flash')"
          :utilization="antigravity3FlashUsageFromAPI.utilization"
          :resets-at="antigravity3FlashUsageFromAPI.resetTime"
          color="emerald"
        />

        <!-- Gemini 3 Image -->
        <UsageProgressBar
          v-if="antigravity3ImageUsageFromAPI !== null"
          :label="t('admin.providers.usageWindow.gemini3Image')"
          :utilization="antigravity3ImageUsageFromAPI.utilization"
          :resets-at="antigravity3ImageUsageFromAPI.resetTime"
          color="purple"
        />

        <!-- Claude -->
        <UsageProgressBar
          v-if="antigravityClaudeUsageFromAPI !== null"
          :label="t('admin.providers.usageWindow.claude')"
          :utilization="antigravityClaudeUsageFromAPI.utilization"
          :resets-at="antigravityClaudeUsageFromAPI.resetTime"
          color="amber"
        />

        <div v-if="aiCreditsDisplay" class="mt-1 flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
          <Icon name="creditCard" size="xs" />
          {{ t('admin.providers.aiCreditsBalance') }}: {{ aiCreditsDisplay }}
        </div>
      </div>
      <div v-else-if="aiCreditsDisplay" class="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
        <Icon name="creditCard" size="xs" />
        {{ t('admin.providers.aiCreditsBalance') }}: {{ aiCreditsDisplay }}
      </div>
      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- Grok OAuth 提供商：被动 xAI 额度 header + 本地 TokenRouter 用量 -->
    <template v-else-if="provider.platform === 'grok' && provider.type === 'oauth'">
      <div v-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="skeleton h-3 w-[32px]"></div>
          <div class="skeleton h-1.5 w-8 rounded-full"></div>
          <div class="skeleton h-3 w-[32px]"></div>
        </div>
      </div>
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>
      <div v-else-if="needsReauth" class="space-y-1">
        <span class="inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium bg-orange-100 text-orange-700 dark:bg-orange-900/40 dark:text-orange-300">
          {{ t('admin.providers.needsReauth') }}
        </span>
      </div>
      <div v-else-if="isForbidden" class="space-y-1">
        <span class="inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300">
          {{ usageInfo?.grok_entitlement_status || t('admin.providers.forbidden') }}
        </span>
      </div>
      <div v-else-if="usageInfo" class="space-y-1">
        <!-- 免费层只显示滚动 24 小时软性门禁；付费层显示 7 天、30 天与预付余额。 -->
        <template v-if="grokIsFree">
          <UsageProgressBar
            v-if="grokFreeTokenBar"
            label="24h"
            :title="t('admin.providers.usageWindow.grokFreeQuota24hHint', { limit: formatCompactNumber(grokFreeTokenBar.limit) })"
            :utilization="grokFreeTokenBar.utilization"
            :window-stats="grokFreeQuotaUsage"
            :show-now-when-idle="true"
            color="emerald"
          />
          <div v-else-if="grokQuotaUnknown" class="text-xs text-gray-500 dark:text-gray-400">
            {{ grokQuotaUnknownLabel }}
          </div>
        </template>
        <template v-else>
          <UsageProgressBar
            v-if="grokWeeklyBillingBar"
            label="7d"
            :utilization="grokWeeklyBillingBar.utilization"
            :resets-at="grokWeeklyBillingBar.resetsAt"
            :window-stats="grokWeeklyBillingBar.windowStats"
            :show-now-when-idle="true"
            color="indigo"
          />
          <UsageProgressBar
            v-if="grokMonthlyBillingBar"
            label="30d"
            :utilization="grokMonthlyBillingBar.utilization"
            :resets-at="grokMonthlyBillingBar.resetsAt"
            :window-stats="grokMonthlyBillingBar.windowStats"
            :show-now-when-idle="true"
            color="indigo"
          />
          <div
            v-if="grokPrepaidMoneyLine"
            class="flex flex-wrap items-center gap-1 text-xs text-gray-500 dark:text-gray-400"
          >
            <span
              v-if="grokPrepaidMoneyLine.showPrepaid"
              class="rounded-compact bg-emerald-50 px-1 py-0.5 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
              :title="t('admin.providers.usageWindow.grokPrepaid')"
            >
              {{ t('admin.providers.usageWindow.grokPrepaid') }} ${{ grokPrepaidMoneyLine.prepaid }}
            </span>
            <span
              v-if="grokPrepaidMoneyLine.showUsedLimit"
              :title="t('admin.providers.usageWindow.grokMonthlyLimit')"
            >
              {{ t('admin.providers.usageWindow.grokUsed') }}
              {{ grokPrepaidMoneyLine.used }}/{{ grokPrepaidMoneyLine.limit }}
            </span>
          </div>
          <div v-if="grokQuotaUnknown" class="text-xs text-gray-500 dark:text-gray-400">
            {{ grokQuotaUnknownLabel }}
          </div>
        </template>
        <div v-if="usageInfo.error" class="truncate text-xs text-amber-600 dark:text-amber-400 max-w-[200px]" :title="usageInfo.error">
          {{ usageErrorLabel }}
        </div>
        <div v-if="grokRetryAfterLabel" class="text-xs text-amber-600 dark:text-amber-400">
          {{ t('admin.providers.usageWindow.grokRetryAfter', { time: grokRetryAfterLabel }) }}
        </div>
        <GrokQuotaProbeCell :provider="provider" compact @probed="handleGrokProbed" />
      </div>
      <div v-else class="space-y-1">
        <div class="text-xs text-gray-400">-</div>
        <GrokQuotaProbeCell :provider="provider" compact @probed="handleGrokProbed" />
      </div>
    </template>

    <!-- Qoder COSY 提供商：上游月度 credits -->
    <template v-else-if="provider.platform === 'qoder'">
      <div v-if="loading" class="space-y-1.5">
        <div class="flex items-center gap-1">
          <div class="skeleton h-3 w-[48px]"></div>
          <div class="skeleton h-1.5 w-8 rounded-full"></div>
          <div class="skeleton h-3 w-[40px]"></div>
        </div>
      </div>
      <div v-else-if="error" class="text-xs text-red-500">
        {{ error }}
      </div>
      <div v-else-if="usageInfo?.qoder_quota" class="space-y-1">
        <div v-if="usageInfo.error" class="truncate text-xs text-amber-600 dark:text-amber-400 max-w-[200px]" :title="usageInfo.error">
          {{ usageErrorLabel }}
        </div>
        <UsageProgressBar
          label="Credits"
          :utilization="qoderQuotaUsageBar.utilization"
          :resets-at="qoderQuotaUsageBar.resetsAt"
          wide-label
          color="indigo"
        />
        <div class="flex flex-wrap items-center justify-end gap-1.5 text-xs text-gray-500 dark:text-gray-400 lg:justify-start">
          <span class="whitespace-nowrap rounded-compact bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700">
            {{ qoderQuotaCreditsLabel }}
          </span>
          <span
            v-if="usageInfo.qoder_quota.is_quota_exceeded"
            class="rounded-compact bg-amber-100 px-1.5 py-0.5 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
          >
            exceeded
          </span>
          <span
            v-if="usageInfo.qoder_quota.snapshot_from_provider"
            class="whitespace-nowrap rounded-compact bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700"
          >
            cached
          </span>
        </div>
        <button
          type="button"
          class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8 transition-colors"
          :disabled="activeQueryLoading"
          @click="loadActiveUsage"
        >
          <Icon
            name="refresh"
            size="xs"
            :class="{ 'animate-spin': activeQueryLoading }"
            :stroke-width="2"
          />
          {{ t('admin.providers.usageWindow.activeQuery') }}
        </button>
      </div>
      <div v-else-if="usageInfo?.error" class="space-y-1">
        <div class="truncate text-xs text-amber-600 dark:text-amber-400 max-w-[200px]" :title="usageInfo.error">
          {{ usageErrorLabel }}
        </div>
        <button
          type="button"
          class="inline-flex items-center gap-0.5 rounded-compact px-1.5 py-0.5 text-xs font-medium text-primary-600 hover:bg-primary-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-500 dark:hover:bg-primary-500/8 transition-colors"
          :disabled="activeQueryLoading"
          @click="loadActiveUsage"
        >
          <Icon
            name="refresh"
            size="xs"
            :class="{ 'animate-spin': activeQueryLoading }"
            :stroke-width="2"
          />
          {{ t('admin.providers.usageWindow.activeQuery') }}
        </button>
      </div>
      <div v-else class="text-xs text-gray-400">-</div>
    </template>

    <!-- Gemini platform: show quota + local usage window -->
    <template v-else-if="provider.platform === 'gemini'">
      <ProviderUpstreamUsageCell
        v-if="provider.type === 'apikey'"
        :provider="provider"
        :result="upstreamUsage"
        :error="upstreamUsageError"
        :loading="upstreamUsageLoading"
        :show-query-button="false"
        :request="requestUpstreamUsage"
      />
      <!-- Auth Type + Tier Badge (first line) -->
      <div v-if="geminiAuthTypeLabel" class="mb-1 flex items-center gap-1">
        <span
          :class="[
            'inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium',
            geminiTierClass
          ]"
        >
          {{ geminiAuthTypeLabel }}
        </span>
        <!-- Help icon -->
        <span
          class="group relative cursor-help"
        >
          <Icon
            name="questionCircle"
            size="xs"
            class="h-3.5 w-3.5 text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300"
          />
          <span
            class="pointer-events-none absolute left-0 top-full z-50 mt-1 w-80 whitespace-normal break-words tooltip-panel rounded-compact px-3 py-2 text-xs leading-relaxed opacity-0 shadow-lg transition-opacity group-hover:opacity-100"
          >
            <div class="font-semibold mb-1">{{ t('admin.providers.gemini.quotaPolicy.title') }}</div>
            <div class="mb-2 text-gray-300">{{ t('admin.providers.gemini.quotaPolicy.note') }}</div>
            <div class="space-y-1">
              <div><strong>{{ geminiQuotaPolicyChannel }}:</strong></div>
              <div class="pl-2">• {{ geminiQuotaPolicyLimits }}</div>
              <div class="mt-2">
                <a :href="geminiQuotaPolicyDocsUrl" target="_blank" rel="noopener noreferrer" class="text-primary-400 hover:text-primary-300 underline">
                  {{ t('admin.providers.gemini.quotaPolicy.columns.docs') }} →
                </a>
              </div>
            </div>
          </span>
        </span>
      </div>

      <!-- Usage data or unlimited flow -->
      <div class="space-y-1">
        <ProviderUsageStatsChips v-if="showGeminiTodayStats && todayStats" :stats="todayStats" scope="today" />
        <div
          v-else-if="showGeminiTodayStats && todayStatsLoading"
          class="mb-0.5 flex items-center justify-end gap-1 lg:justify-start"
        >
          <div class="skeleton h-3 w-10"></div>
          <div class="skeleton h-3 w-8"></div>
          <div class="skeleton h-3 w-12"></div>
        </div>
        <div v-if="loading" class="space-y-1">
          <div class="flex items-center gap-1">
            <div class="skeleton h-3 w-[32px]"></div>
            <div class="skeleton h-1.5 w-8 rounded-full"></div>
            <div class="skeleton h-3 w-[32px]"></div>
          </div>
        </div>
        <div v-else-if="error" class="text-xs text-red-500">
          {{ error }}
        </div>
        <!-- Gemini: show daily usage bars when available -->
        <div v-else-if="geminiUsageAvailable" class="space-y-1">
          <UsageProgressBar
            v-for="bar in geminiUsageBars"
            :key="bar.key"
            :label="bar.label"
            :utilization="bar.utilization"
            :resets-at="bar.resetsAt"
            :window-stats="bar.windowStats"
            :color="bar.color"
          />
          <p class="mt-1 text-xs leading-tight text-gray-400 dark:text-gray-500 italic">
            * {{ t('admin.providers.gemini.quotaPolicy.simulatedNote') || 'Simulated quota' }}
          </p>
        </div>
        <!-- AI Studio Client OAuth: show unlimited flow (no usage tracking) -->
        <div v-else class="text-xs text-gray-400">
          {{ t('admin.providers.gemini.rateLimit.unlimited') }}
        </div>
      </div>
    </template>

    <!-- Other providers: no usage window -->
    <template v-else>
      <div
        v-if="provider.type !== 'apikey' || (!upstreamUsage && !upstreamUsageError && !upstreamUsageLoading && !upstreamUsageDisabled)"
        class="text-xs text-gray-400"
      >-</div>
    </template>
  </div>

  <!-- Non-OAuth/Setup-Token providers -->
  <div ref="rootRef" v-else>
    <div v-if="isGeminiThirdPartyProvider" class="space-y-1">
      <ProviderUpstreamUsageCell
        v-if="provider.type === 'apikey'"
        :provider="provider"
        :result="upstreamUsage"
        :error="upstreamUsageError"
        :loading="upstreamUsageLoading"
        :show-query-button="false"
        :request="requestUpstreamUsage"
      />
      <ProviderUsageStatsChips v-if="showGeminiTodayStats && todayStats" :stats="todayStats" scope="today" />
      <div v-else-if="showGeminiTodayStats && todayStatsLoading" class="mb-0.5 flex items-center justify-end gap-1 lg:justify-start">
        <div class="skeleton h-3 w-10"></div>
        <div class="skeleton h-3 w-8"></div>
        <div class="skeleton h-3 w-12"></div>
      </div>
      <div
        v-else-if="provider.type !== 'apikey'"
        class="text-xs text-gray-400"
      >-</div>
    </div>
    <!-- 非 API Key 的 Gemini 提供商展示本地配额视图。 -->
    <ProviderQuotaInfo
      v-else-if="provider.platform === 'gemini' && provider.type !== 'apikey'"
      :provider="provider"
    />
    <!-- API Key（含国产平台）统一展示上游用量、本地统计和本地配额。 -->
    <div v-else class="space-y-1">
      <ProviderUpstreamUsageCell
        v-if="provider.type === 'apikey'"
        :provider="provider"
        :result="upstreamUsage"
        :error="upstreamUsageError"
        :loading="upstreamUsageLoading"
        :show-query-button="false"
        :request="requestUpstreamUsage"
      />
      <ProviderQuotaInfo
        v-if="provider.platform === 'gemini' && provider.type === 'apikey'"
        :provider="provider"
      />
      <OllamaCloudUsageCell
        v-if="provider.ollama_cloud_usage?.eligible"
        :provider="provider"
        @updated="handleOllamaCloudUsageUpdated"
      />
      <!-- 今日统计：请求数、Token、提供商成本和用户扣费。 -->
      <ProviderUsageStatsChips v-if="todayStats" :stats="todayStats" scope="today" />
      <!-- Loading skeleton for today stats -->
      <div
        v-else-if="todayStatsLoading"
        class="mb-0.5 flex items-center justify-end gap-1 lg:justify-start"
      >
        <div class="skeleton h-3 w-10"></div>
        <div class="skeleton h-3 w-8"></div>
        <div class="skeleton h-3 w-12"></div>
      </div>

      <!-- API Key providers with quota limits: show progress bars -->
      <UsageProgressBar
        v-if="quotaDailyBar"
        label="1d"
        :utilization="quotaDailyBar.utilization"
        :resets-at="quotaDailyBar.resetsAt"
        color="indigo"
      />
      <UsageProgressBar
        v-if="quotaWeeklyBar"
        label="7d"
        :utilization="quotaWeeklyBar.utilization"
        :resets-at="quotaWeeklyBar.resetsAt"
        color="emerald"
      />
      <UsageProgressBar
        v-if="quotaMonthlyBar"
        :label="provider.quota_monthly_reset_mode === 'fixed' ? 'M' : '30d'"
        :utilization="quotaMonthlyBar.utilization"
        :resets-at="quotaMonthlyBar.resetsAt"
        color="emerald"
      />
      <UsageProgressBar
        v-if="quotaTotalBar"
        label="total"
        :utilization="quotaTotalBar.utilization"
        color="purple"
      />

      <!-- No data at all -->
      <div
        v-if="!todayStats && !todayStatsLoading && !hasApiKeyQuota && !provider.ollama_cloud_usage?.eligible &&
          provider.type !== 'apikey'"
        class="text-xs text-gray-400"
      >-</div>
    </div>
  </div>
  <!-- 查询入口放在本地统计和配额之后，由外层统一展示。 -->
  <div v-if="isUpstreamUsageQueryEnabled(provider)" class="mt-0.5 flex items-center justify-end gap-1.5 lg:justify-start">
    <ProviderUpstreamUsageQueryButton
      :provider="provider"
      :loading="upstreamUsageLoading"
      :request="requestUpstreamUsage"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { COPY_FEEDBACK_MS } from '@/constants/ui'
import type {
  Provider,
  ProviderUsageInfo,
  GeminiCredentials,
  WindowStats,
  UpstreamUsageQueryError,
  UpstreamUsageQueryResult
} from '@/types'
import { buildOpenAIUsageRefreshKey } from '@/utils/providerUsageRefresh'
import { isUpstreamUsageQueryEnabled } from '@/utils/upstreamUsage'
import { enqueueUsageRequest } from '@/utils/usageLoadQueue'
import { formatCompactNumber } from '@/utils/format'
import { TABLE_DESKTOP_MEDIA_QUERY } from '@/constants/layout'
import Icon from '@/components/icons/Icon.vue'
import UsageProgressBar from './UsageProgressBar.vue'
import ProviderQuotaInfo from './ProviderQuotaInfo.vue'
import OpenAIQuotaResetCell from './OpenAIQuotaResetCell.vue'
import GrokQuotaProbeCell from './GrokQuotaProbeCell.vue'
import OllamaCloudUsageCell from './OllamaCloudUsageCell.vue'
import ProviderUpstreamUsageCell from './ProviderUpstreamUsageCell.vue'
import ProviderUsageStatsChips from './ProviderUsageStatsChips.vue'
import ProviderUpstreamUsageQueryButton from './ProviderUpstreamUsageQueryButton.vue'

// ProviderUsageCell.vue presents provider usage snapshots and local quota progress.
// Platform and authentication type select upstream measurements, billing details,
// refresh actions, and quota views. The local spending controls use the limits
// and effective usage supplied by the administration API, keeping their progress
// separate from upstream account observations. Each periodic quota has its own
// reset timestamp and can be displayed when the other dimensions are disabled.
//
// Fixed local quotas use the server's published reset time. Elapsed-time quotas
// derive their deadline from the stored start and their configured period;
// monthly quotas use thirty days so the displayed countdown matches billing.

// 模块级缓存供所有 ProviderUsageCell 实例共享
const _usageCache = new Map<number, { data: ProviderUsageInfo; ts: number }>()
const USAGE_CACHE_TTL = 5 * 60 * 1000 // 5 分钟

const props = withDefaults(
  defineProps<{
    provider: Provider
    todayStats?: WindowStats | null
    todayStatsLoading?: boolean
    manualRefreshToken?: number
    batchedUsage?: ProviderUsageInfo | null
    batchedUsageError?: string | null
    batchedUsageLoading?: boolean
    requestBatchedUsage?: ((provider: Provider, options?: { force?: boolean }) => void) | null
    upstreamUsage?: UpstreamUsageQueryResult | null
    upstreamUsageError?: UpstreamUsageQueryError | null
    upstreamUsageLoading?: boolean
    requestUpstreamUsage?: ((provider: Provider, options?: { force?: boolean }) => void) | null
  }>(),
  {
    todayStats: null,
    todayStatsLoading: false,
    manualRefreshToken: 0,
    batchedUsage: null,
    batchedUsageError: null,
    batchedUsageLoading: false,
    requestBatchedUsage: null,
    upstreamUsage: null,
    upstreamUsageError: null,
    upstreamUsageLoading: false,
    requestUpstreamUsage: null
  }
)

const emit = defineEmits<{
  'provider-updated': [provider: Provider]
}>()

const { t } = useI18n()
const canUseMatchMedia =
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
const getDesktopViewportMatches = () =>
  canUseMatchMedia ? window.matchMedia(TABLE_DESKTOP_MEDIA_QUERY).matches : true

const unmounted = ref(false)
onBeforeUnmount(() => { unmounted.value = true })

const loading = ref(false)
const activeQueryLoading = ref(false)
const error = ref<string | null>(null)
const usageInfo = ref<ProviderUsageInfo | null>(null)
const rootRef = ref<HTMLElement | null>(null)
const isDesktopViewport = ref(getDesktopViewportMatches())
const hasEnteredViewport = ref(false)
const pendingAutoLoad = ref(false)
const pendingAutoLoadSource = ref<'passive' | 'active' | undefined>(undefined)

let desktopViewportMediaQuery: MediaQueryList | null = null
let desktopViewportListener: ((event: MediaQueryListEvent) => void) | null = null
let visibilityObserver: IntersectionObserver | null = null

const isGeminiThirdPartyProvider = computed(() => {
  if (props.provider.platform !== 'gemini' || props.provider.type !== 'apikey') return false
  const credentials = props.provider.credentials as GeminiCredentials | undefined
  return credentials?.provider_type === 'third_party'
})

const upstreamUsageDisabled = computed(() => {
  if (props.provider.type !== 'apikey') return false
  const config = props.provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
  return config?.enabled === false
})

// Show usage windows for OAuth and Setup Token providers
const showUsageWindows = computed(() => {
  // API Key 的上游余额由独立子组件按需查询；不能沿用 OAuth/Gemini
  // 用量模型在列表加载或进入视口时主动请求上游。
  if (props.provider.type === 'apikey') return false
  // 第三方 Gemini 提供商没有官方等级，不能展示或请求本地模拟配额。
  if (props.provider.platform === 'gemini') return !isGeminiThirdPartyProvider.value
  if (props.provider.platform === 'qoder') return props.provider.type === 'cosy'
  return props.provider.type === 'oauth' || props.provider.type === 'setup-token'
})

const shouldFetchUsage = computed(() => {
  if (props.provider.platform === 'anthropic') {
    return props.provider.type === 'oauth' || props.provider.type === 'setup-token'
  }
  if (props.provider.platform === 'gemini') {
    return props.provider.type !== 'apikey' && !isGeminiThirdPartyProvider.value
  }
  if (props.provider.platform === 'antigravity') {
    return props.provider.type === 'oauth'
  }
  if (props.provider.platform === 'grok') {
    return props.provider.type === 'oauth'
  }
  if (props.provider.platform === 'qoder') {
    return props.provider.type === 'cosy'
  }
  if (props.provider.platform === 'openai') {
    return props.provider.type === 'oauth'
  }
  return false
})

const isBatchManaged = computed(() => typeof props.requestBatchedUsage === 'function')

const showGeminiTodayStats = computed(() => {
  return props.provider.platform === 'gemini' &&
    (props.provider.type === 'service_account' || props.provider.type === 'apikey')
})

const geminiUsageAvailable = computed(() => {
  return (
    !!usageInfo.value?.gemini_shared_daily ||
    !!usageInfo.value?.gemini_pro_daily ||
    !!usageInfo.value?.gemini_flash_daily ||
    !!usageInfo.value?.gemini_shared_minute ||
    !!usageInfo.value?.gemini_pro_minute ||
    !!usageInfo.value?.gemini_flash_minute
  )
})

const hasOpenAIUsageFallback = computed(() => {
  if (props.provider.platform !== 'openai' || props.provider.type !== 'oauth') return false
  return !!usageInfo.value?.five_hour || !!usageInfo.value?.seven_day
})

const openAIQuotaAutoPaused = computed(() => {
  if (props.provider.platform !== 'openai' || props.provider.type !== 'oauth') return false
  return usageInfo.value?.quota_auto_paused ?? props.provider.quota_auto_paused ?? false
})

const openAIUsageRefreshKey = computed(() => buildOpenAIUsageRefreshKey(props.provider))

const shouldAutoLoadUsageOnMount = computed(() => {
  return shouldFetchUsage.value
})

const shouldLazyLoadOnMobile = computed(() => {
  return shouldFetchUsage.value && !isDesktopViewport.value
})

// Antigravity quota types (用于 API 返回的数据)
interface AntigravityUsageResult {
  utilization: number
  resetTime: string | null
}

// ===== Antigravity quota from API (usageInfo.antigravity_quota) =====

// 检查是否有从 API 获取的配额数据
const hasAntigravityQuotaFromAPI = computed(() => {
  return usageInfo.value?.antigravity_quota && Object.keys(usageInfo.value.antigravity_quota).length > 0
})

// 从 API 配额数据中获取使用率（多模型取最高使用率）
const getAntigravityUsageFromAPI = (
  modelNames: string[]
): AntigravityUsageResult | null => {
  const quota = usageInfo.value?.antigravity_quota
  if (!quota) return null

  let maxUtilization = 0
  let earliestReset: string | null = null

  for (const model of modelNames) {
    const modelQuota = quota[model]
    if (!modelQuota) continue

    if (modelQuota.utilization > maxUtilization) {
      maxUtilization = modelQuota.utilization
    }
    if (modelQuota.reset_time) {
      if (!earliestReset || modelQuota.reset_time < earliestReset) {
        earliestReset = modelQuota.reset_time
      }
    }
  }

  // 如果没有找到任何匹配的模型
  if (maxUtilization === 0 && earliestReset === null) {
    const hasAnyData = modelNames.some((m) => quota[m])
    if (!hasAnyData) return null
  }

  return {
    utilization: maxUtilization,
    resetTime: earliestReset
  }
}

// Gemini 3 Pro from API
const antigravity3ProUsageFromAPI = computed(() =>
  getAntigravityUsageFromAPI(['gemini-3-pro-low', 'gemini-3-pro-high', 'gemini-3-pro-preview'])
)

// Gemini 3 Flash from API
const antigravity3FlashUsageFromAPI = computed(() => getAntigravityUsageFromAPI(['gemini-3-flash']))

// Gemini Image from API
const antigravity3ImageUsageFromAPI = computed(() =>
  getAntigravityUsageFromAPI(['gemini-2.5-flash-image', 'gemini-3.1-flash-image', 'gemini-3-pro-image'])
)

// Claude from API (all Claude model variants)
const antigravityClaudeUsageFromAPI = computed(() =>
  getAntigravityUsageFromAPI([
    'claude-fable-5-1',
    'claude-fable-5',
    'claude-sonnet-4-5', 'claude-opus-4-5-thinking',
    'claude-sonnet-4-6', 'claude-opus-4-6', 'claude-opus-4-6-thinking',
    'claude-opus-4-7', 'claude-opus-4-8',
  ])
)

const aiCreditsDisplay = computed(() => {
  const credits = usageInfo.value?.ai_credits
  if (!credits || credits.length === 0) return null
  const total = credits.reduce((sum, credit) => sum + (credit.amount ?? 0), 0)
  if (total <= 0) return null
  return total.toFixed(0)
})

// Antigravity 提供商类型（从 load_code_assist 响应中提取）
const antigravityTier = computed(() => {
  const extra = props.provider.extra as Record<string, unknown> | undefined
  if (!extra) return null

  const loadCodeAssist = extra.load_code_assist as Record<string, unknown> | undefined
  if (!loadCodeAssist) return null

  // 优先取 paidTier，否则取 currentTier
  const paidTier = loadCodeAssist.paidTier as Record<string, unknown> | undefined
  if (paidTier && typeof paidTier.id === 'string') {
    return paidTier.id
  }

  const currentTier = loadCodeAssist.currentTier as Record<string, unknown> | undefined
  if (currentTier && typeof currentTier.id === 'string') {
    return currentTier.id
  }

  return null
})

// Gemini 提供商类型（从 credentials 中提取）
const geminiTier = computed(() => {
  if (props.provider.platform !== 'gemini') return null
  const creds = props.provider.credentials as GeminiCredentials | undefined
  return creds?.tier_id || null
})

const geminiOAuthType = computed(() => {
  if (props.provider.platform !== 'gemini') return null
  const creds = props.provider.credentials as GeminiCredentials | undefined
  return (creds?.oauth_type || '').trim() || null
})

// Gemini 是否为 Code Assist OAuth
const isGeminiCodeAssist = computed(() => {
  if (props.provider.platform !== 'gemini') return false
  const creds = props.provider.credentials as GeminiCredentials | undefined
  return creds?.oauth_type === 'code_assist' || (!creds?.oauth_type && !!creds?.project_id)
})

const geminiChannelShort = computed((): 'ai studio' | 'gcp' | 'google one' | 'client' | null => {
  if (props.provider.platform !== 'gemini') return null

  // API Key providers are AI Studio.
  if (props.provider.type === 'apikey') return 'ai studio'

  if (geminiOAuthType.value === 'google_one') return 'google one'
  if (isGeminiCodeAssist.value) return 'gcp'
  if (geminiOAuthType.value === 'ai_studio') return 'client'

  // Fallback (unknown legacy data): treat as AI Studio.
  return 'ai studio'
})

const geminiUserLevel = computed((): string | null => {
  if (props.provider.platform !== 'gemini') return null

  const tier = (geminiTier.value || '').toString().trim()
  const tierLower = tier.toLowerCase()
  const tierUpper = tier.toUpperCase()

  // Google One: free / pro / ultra
  if (geminiOAuthType.value === 'google_one') {
    if (tierLower === 'google_one_free') return 'free'
    if (tierLower === 'google_ai_pro') return 'pro'
    if (tierLower === 'google_ai_ultra') return 'ultra'

    // Backward compatibility (legacy tier markers)
    if (tierUpper === 'AI_PREMIUM' || tierUpper === 'GOOGLE_ONE_STANDARD') return 'pro'
    if (tierUpper === 'GOOGLE_ONE_UNLIMITED') return 'ultra'
    if (tierUpper === 'FREE' || tierUpper === 'GOOGLE_ONE_BASIC' || tierUpper === 'GOOGLE_ONE_UNKNOWN' || tierUpper === '') return 'free'

    return null
  }

  // GCP Code Assist: standard / enterprise
  if (isGeminiCodeAssist.value) {
    if (tierLower === 'gcp_enterprise') return 'enterprise'
    if (tierLower === 'gcp_standard') return 'standard'

    // Backward compatibility
    if (tierUpper.includes('ULTRA') || tierUpper.includes('ENTERPRISE')) return 'enterprise'
    return 'standard'
  }

  // AI Studio (API Key) and Client OAuth: free / paid
  if (props.provider.type === 'apikey' || geminiOAuthType.value === 'ai_studio') {
    if (tierLower === 'aistudio_paid') return 'paid'
    if (tierLower === 'aistudio_free') return 'free'

    // Backward compatibility
    if (tierUpper.includes('PAID') || tierUpper.includes('PAYG') || tierUpper.includes('PAY')) return 'paid'
    if (tierUpper.includes('FREE')) return 'free'
    if (props.provider.type === 'apikey') return 'free'
    return null
  }

  return null
})

// Gemini 认证类型（按要求：授权方式简称 + 用户等级）
const geminiAuthTypeLabel = computed(() => {
  if (props.provider.platform !== 'gemini') return null
  if (!geminiChannelShort.value) return null
  return geminiUserLevel.value ? `${geminiChannelShort.value} ${geminiUserLevel.value}` : geminiChannelShort.value
})

// Gemini 提供商类型徽章样式（统一样式）
const geminiTierClass = computed(() => {
  // Use channel+level to choose a stable color without depending on raw tier_id variants.
  const channel = geminiChannelShort.value
  const level = geminiUserLevel.value

  if (channel === 'client' || channel === 'ai studio') {
    return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
  }

  if (channel === 'google one') {
    if (level === 'ultra') return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    if (level === 'pro') return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-200'
  }

  if (channel === 'gcp') {
    if (level === 'enterprise') return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
  }

  return ''
})

// Gemini 配额政策信息
const geminiQuotaPolicyChannel = computed(() => {
  if (geminiOAuthType.value === 'google_one') {
    return t('admin.providers.gemini.quotaPolicy.rows.googleOne.channel')
  }
  if (isGeminiCodeAssist.value) {
    return t('admin.providers.gemini.quotaPolicy.rows.gcp.channel')
  }
  return t('admin.providers.gemini.quotaPolicy.rows.aiStudio.channel')
})

const geminiQuotaPolicyLimits = computed(() => {
  const tierLower = (geminiTier.value || '').toString().trim().toLowerCase()

  if (geminiOAuthType.value === 'google_one') {
    if (tierLower === 'google_ai_ultra' || geminiUserLevel.value === 'ultra') {
      return t('admin.providers.gemini.quotaPolicy.rows.googleOne.limitsUltra')
    }
    if (tierLower === 'google_ai_pro' || geminiUserLevel.value === 'pro') {
      return t('admin.providers.gemini.quotaPolicy.rows.googleOne.limitsPro')
    }
    return t('admin.providers.gemini.quotaPolicy.rows.googleOne.limitsFree')
  }

  if (isGeminiCodeAssist.value) {
    if (tierLower === 'gcp_enterprise' || geminiUserLevel.value === 'enterprise') {
      return t('admin.providers.gemini.quotaPolicy.rows.gcp.limitsEnterprise')
    }
    return t('admin.providers.gemini.quotaPolicy.rows.gcp.limitsStandard')
  }

  // AI Studio (API Key / custom OAuth)
  if (tierLower === 'aistudio_paid' || geminiUserLevel.value === 'paid') {
    return t('admin.providers.gemini.quotaPolicy.rows.aiStudio.limitsPaid')
  }
  return t('admin.providers.gemini.quotaPolicy.rows.aiStudio.limitsFree')
})

const geminiQuotaPolicyDocsUrl = computed(() => {
  if (geminiOAuthType.value === 'google_one' || isGeminiCodeAssist.value) {
    return 'https://developers.google.com/gemini-code-assist/resources/quotas'
  }
  return 'https://ai.google.dev/pricing'
})

const geminiUsesSharedDaily = computed(() => {
  if (props.provider.platform !== 'gemini') return false
  // Per requirement: Google One & GCP are shared RPD pools (no per-model breakdown).
  return (
    !!usageInfo.value?.gemini_shared_daily ||
    !!usageInfo.value?.gemini_shared_minute ||
    geminiOAuthType.value === 'google_one' ||
    isGeminiCodeAssist.value
  )
})

const geminiUsageBars = computed(() => {
  if (props.provider.platform !== 'gemini') return []
  if (!usageInfo.value) return []

  const bars: Array<{
    key: string
    label: string
    utilization: number
    resetsAt: string | null
    windowStats?: WindowStats | null
    color: 'indigo' | 'emerald'
  }> = []

  if (geminiUsesSharedDaily.value) {
    const sharedDaily = usageInfo.value.gemini_shared_daily
    if (sharedDaily) {
      bars.push({
        key: 'shared_daily',
        label: '1d',
        utilization: sharedDaily.utilization,
        resetsAt: sharedDaily.resets_at,
        windowStats: sharedDaily.window_stats,
        color: 'indigo'
      })
    }
    return bars
  }

  const pro = usageInfo.value.gemini_pro_daily
  if (pro) {
    bars.push({
      key: 'pro_daily',
      label: 'pro',
      utilization: pro.utilization,
      resetsAt: pro.resets_at,
      windowStats: pro.window_stats,
      color: 'indigo'
      })
  }

  const flash = usageInfo.value.gemini_flash_daily
  if (flash) {
    bars.push({
      key: 'flash_daily',
      label: 'flash',
      utilization: flash.utilization,
      resetsAt: flash.resets_at,
      windowStats: flash.window_stats,
      color: 'emerald'
    })
  }

  return bars
})

interface GrokQuotaBarInfo {
  utilization: number
  resetsAt: string | null
  windowStats?: WindowStats | null
}

type QoderQuotaProgressLike = NonNullable<NonNullable<ProviderUsageInfo['qoder_quota']>['user_quota']>

const qoderQuotaPoolCapacity = (pool: QoderQuotaProgressLike) => {
  if (pool.total && pool.total > 0) return pool.total
  if (pool.cap && pool.cap > 0) return pool.cap
  return (pool.used ?? 0) + (pool.remaining ?? 0)
}

const qoderQuotaPoolHasCapacity = (pool: QoderQuotaProgressLike) => {
  return pool.total != null || pool.cap != null || pool.used != null || pool.remaining != null
}

const qoderQuotaPools = computed<QoderQuotaProgressLike[]>(() => {
  const quota = usageInfo.value?.qoder_quota
  return [quota?.user_quota, quota?.add_on_quota, quota?.org_resource_package].filter(
    (pool): pool is QoderQuotaProgressLike => !!pool
  )
})

const qoderQuotaAggregate = computed(() => {
  const pools = qoderQuotaPools.value
  if (pools.length === 0) return null

  const used = pools.reduce((sum, pool) => sum + (pool.used ?? 0), 0)
  const remaining = pools.reduce((sum, pool) => sum + (pool.remaining ?? 0), 0)
  const total = pools.reduce((sum, pool) => sum + qoderQuotaPoolCapacity(pool), 0)
  const totalKnown = pools.some(qoderQuotaPoolHasCapacity)
  const unit = pools.find((pool) => pool.unit)?.unit || 'credits'
  const utilization = total > 0
    ? (used / total) * 100
    : (usageInfo.value?.qoder_quota?.total_usage_percentage ?? pools[0]?.percentage ?? 0)

  return { used, remaining, total, totalKnown, unit, utilization }
})

const qoderQuotaUsageBar = computed(() => {
  const quota = usageInfo.value?.qoder_quota
  const utilization = qoderQuotaAggregate.value?.utilization ?? quota?.total_usage_percentage ?? 0
  return {
    utilization: Math.max(0, Math.min(100, utilization)),
    resetsAt: quota?.expires_at || null
  }
})
const qoderQuotaCreditsLabel = computed(() => {
  const quota = qoderQuotaAggregate.value
  if (!quota) return '-'
  const used = formatCompactNumber(quota.used)
  const total = quota.totalKnown ? formatCompactNumber(quota.total) : 'unknown'
  const unit = quota.unit
  return `${used}/${total} ${unit}`
})
const grokBilling = computed(() => usageInfo.value?.grok_billing || null)
const grokLocalUsage7d = computed(() => (
  usageInfo.value?.grok_local_usage_7d || usageInfo.value?.seven_day?.window_stats || null
))
const grokLocalUsageMonthly = computed(() => (
  usageInfo.value?.grok_local_usage_monthly || usageInfo.value?.thirty_day?.window_stats || null
))
const grokWeeklyBillingBar = computed((): GrokQuotaBarInfo | null => {
  const billing = grokBilling.value
  if (billing?.period_type?.toLowerCase() !== 'weekly' || billing.usage_percent == null) {
    return null
  }
  return {
    utilization: Math.min(100, Math.max(0, billing.usage_percent)),
    resetsAt: billing.period_end || null,
    windowStats: grokLocalUsage7d.value
  }
})
// 根据账单探测结果计算月度已用与限额百分比，优先采用 used_percent，否则根据美分推导。
const grokMonthlyBillingBar = computed((): GrokQuotaBarInfo | null => {
  const billing = grokBilling.value
  if (!billing) return null
  let utilization: number | null = null
  if (billing.used_percent != null && Number.isFinite(billing.used_percent)) {
    utilization = billing.used_percent
  } else if (
    billing.monthly_limit_cents != null &&
    billing.monthly_limit_cents > 0 &&
    billing.used_cents != null
  ) {
    utilization = (billing.used_cents / billing.monthly_limit_cents) * 100
  }
  if (utilization == null) return null
  // 仅有周度数据时跳过重复的周度进度条。
  if (billing.period_type?.toLowerCase() === 'weekly' && billing.monthly_limit_cents == null) {
    return null
  }
  return {
    utilization: Math.min(100, Math.max(0, utilization)),
    resetsAt: billing.billing_period_end || billing.period_end || null,
    windowStats: grokLocalUsageMonthly.value
  }
})
const formatGrokMoney = (value?: number | null) => {
  if (value == null || Number.isNaN(value)) return '0'
  if (value >= 1000) return formatCompactNumber(value)
  if (value >= 100) return value.toFixed(0)
  if (value >= 10) return value.toFixed(1)
  return value.toFixed(2)
}
// 付费 Grok 提供商仅在对应数值有效且为正时显示预付或 used/limit 信息。
const grokPrepaidMoneyLine = computed(() => {
  const billing = grokBilling.value
  if (!billing) return null
  const prepaid = billing.prepaid_balance
  const showPrepaid = prepaid != null && Number.isFinite(prepaid) && prepaid > 0
  const limitRaw =
    billing.monthly_limit != null
      ? billing.monthly_limit
      : billing.monthly_limit_cents != null
        ? billing.monthly_limit_cents / 100
        : null
  const showUsedLimit = limitRaw != null && Number.isFinite(limitRaw) && limitRaw > 0
  if (!showPrepaid && !showUsedLimit) return null
  const used =
    billing.monthly_used != null
      ? billing.monthly_used
      : billing.used_cents != null
        ? billing.used_cents / 100
        : 0
  return {
    showPrepaid,
    showUsedLimit,
    prepaid: showPrepaid ? formatGrokMoney(prepaid) : null,
    used: showUsedLimit ? formatGrokMoney(used) : null,
    limit: showUsedLimit ? formatGrokMoney(limitRaw) : null
  }
})
const grokPlanLabelIsFree = (value: string) => value.includes('free') || value.includes('basic')
const grokPlanLabelIsPaid = (value: string) => {
  return value !== '' && !grokPlanLabelIsFree(value) && !value.includes('unknown')
}
const grokIsFree = computed(() => {
  if (props.provider.platform !== 'grok' || props.provider.type !== 'oauth') return false
  const billing = grokBilling.value
  const plan = (billing?.plan || '').trim().toLowerCase()
  const tier = (usageInfo.value?.subscription_tier || '').trim().toLowerCase()
  const entitlement = (usageInfo.value?.grok_entitlement_status || '').toLowerCase()
  if (grokPlanLabelIsFree(tier)) return true
  if (grokPlanLabelIsPaid(tier)) return false
  if (
    billing?.usage_percent != null ||
    billing?.used_percent != null ||
    (billing?.monthly_limit_cents != null && billing.monthly_limit_cents > 0)
  ) return false
  if (grokPlanLabelIsPaid(plan)) return false
  if (
    grokPlanLabelIsFree(plan) ||
    grokPlanLabelIsFree(entitlement)
  ) return true
  return billing != null
})
const grokFreeQuotaUsage = computed(() => usageInfo.value?.grok_local_usage_24h || null)
const grokFreeTokenBar = computed(() => {
  if (!grokIsFree.value || !grokFreeQuotaUsage.value) return null
  const limit = usageInfo.value?.grok_free_token_limit
  if (typeof limit !== 'number' || limit <= 0) return null
  const used = Math.max(0, grokFreeQuotaUsage.value.tokens || 0)
  return { utilization: Math.min(100, (used / limit) * 100), limit }
})
const grokQuotaUnknown = computed(() => {
  if (props.provider.platform !== 'grok') return false
  if (grokIsFree.value) {
    return !grokFreeTokenBar.value
  }
  if (grokWeeklyBillingBar.value || grokMonthlyBillingBar.value || grokPrepaidMoneyLine.value) {
    return false
  }
  return usageInfo.value?.grok_quota_snapshot_state !== 'observed'
})
const grokQuotaUnknownLabel = computed(() => {
  return usageInfo.value?.grok_quota_snapshot_state === 'no_headers'
    ? t('admin.providers.usageWindow.grokNoHeaders')
    : t('admin.providers.usageWindow.grokUnknown')
})
const grokRetryAfterLabel = computed(() => {
  const seconds = usageInfo.value?.grok_retry_after_seconds
  if (seconds == null || seconds <= 0) return null
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.ceil(seconds / 60)
  return `${minutes}m`
})

// 提供商类型显示标签
const antigravityTierLabel = computed(() => {
  switch (antigravityTier.value) {
    case 'free-tier':
      return t('admin.providers.tier.free')
    case 'g1-pro-tier':
      return t('admin.providers.tier.pro')
    case 'g1-ultra-tier':
      return t('admin.providers.tier.ultra')
    default:
      return null
  }
})

// 提供商类型徽章样式
const antigravityTierClass = computed(() => {
  switch (antigravityTier.value) {
    case 'free-tier':
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-200'
    case 'g1-pro-tier':
      return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    case 'g1-ultra-tier':
      return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    default:
      return ''
  }
})

// 检测提供商是否有不合格状态（ineligibleTiers）
const hasIneligibleTiers = computed(() => {
  const extra = props.provider.extra as Record<string, unknown> | undefined
  if (!extra) return false

  const loadCodeAssist = extra.load_code_assist as Record<string, unknown> | undefined
  if (!loadCodeAssist) return false

  const ineligibleTiers = loadCodeAssist.ineligibleTiers as unknown[] | undefined
  return Array.isArray(ineligibleTiers) && ineligibleTiers.length > 0
})

// Antigravity 403 forbidden 状态
const isForbidden = computed(() => !!usageInfo.value?.is_forbidden)
const forbiddenType = computed(() => usageInfo.value?.forbidden_type || 'forbidden')
const validationURL = computed(() => usageInfo.value?.validation_url || '')

// 需要重新授权（401）
const needsReauth = computed(() => !!usageInfo.value?.needs_reauth)

// 降级错误标签（rate_limited / network_error）
const usageErrorLabel = computed(() => {
  const code = usageInfo.value?.error_code
  if (code === 'rate_limited') return t('admin.providers.rateLimited')
  return t('admin.providers.usageError')
})

const forbiddenLabel = computed(() => {
  switch (forbiddenType.value) {
    case 'validation':
      return t('admin.providers.forbiddenValidation')
    case 'violation':
      return t('admin.providers.forbiddenViolation')
    default:
      return t('admin.providers.forbidden')
  }
})

const forbiddenBadgeClass = computed(() => {
  if (forbiddenType.value === 'validation') {
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/40 dark:text-yellow-300'
  }
  return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
})

const linkCopied = ref(false)
const copyValidationURL = async () => {
  if (!validationURL.value) return
  try {
    await navigator.clipboard.writeText(validationURL.value)
    linkCopied.value = true
    setTimeout(() => { linkCopied.value = false }, COPY_FEEDBACK_MS)
  } catch {
    // fallback: ignore
  }
}

const isAnthropicOAuthOrSetupToken = computed(() => {
  return props.provider.platform === 'anthropic' && (props.provider.type === 'oauth' || props.provider.type === 'setup-token')
})

const requestParentBatchUsage = (options?: { force?: boolean }) => {
  if (!isBatchManaged.value || !shouldFetchUsage.value) return
  props.requestBatchedUsage?.(props.provider, options)
}

const syncManagedUsageState = () => {
  if (!isBatchManaged.value) return
  usageInfo.value = props.batchedUsage ?? null
  error.value = props.batchedUsageError ?? null
  loading.value = props.batchedUsageLoading === true
}

const loadUsage = async (options?: { source?: 'passive' | 'active'; bypassCache?: boolean }) => {
  if (!shouldFetchUsage.value) return
  if (isBatchManaged.value) {
    requestParentBatchUsage({ force: options?.bypassCache === true })
    return
  }

  // 命中缓存时返回上次请求结果，供列表各次渲染复用。
  if (!options?.bypassCache) {
    const cached = _usageCache.get(props.provider.id)
    if (cached && Date.now() - cached.ts < USAGE_CACHE_TTL) {
      usageInfo.value = cached.data
      loading.value = false
      return
    }
  }

  loading.value = true
  error.value = null

  try {
    const fetchFn = () =>
      options?.source
        ? adminAPI.providers.getUsage(props.provider.id, options.source)
        : adminAPI.providers.getUsage(props.provider.id)
    const result = await enqueueUsageRequest(props.provider, fetchFn)
    if (!unmounted.value) {
      usageInfo.value = result
      _usageCache.set(props.provider.id, { data: result, ts: Date.now() })
    }
  } catch (e: any) {
    if (!unmounted.value) {
      error.value = t('common.error')
      console.error('Failed to load usage:', e)
    }
  } finally {
    if (!unmounted.value) loading.value = false
  }
}

const flushPendingAutoLoad = () => {
  if (!pendingAutoLoad.value) return
  const source = pendingAutoLoadSource.value
  pendingAutoLoad.value = false
  pendingAutoLoadSource.value = undefined
  loadUsage({ source }).catch((e) => {
    console.error('Failed to load deferred usage:', e)
  })
}

const requestAutoLoad = (source?: 'passive' | 'active') => {
  if (!shouldFetchUsage.value) return
  if (shouldLazyLoadOnMobile.value && !hasEnteredViewport.value) {
    pendingAutoLoad.value = true
    pendingAutoLoadSource.value = source
    return
  }
  loadUsage({ source }).catch((e) => {
    console.error('Failed to auto load usage:', e)
  })
}

const detachVisibilityObserver = () => {
  visibilityObserver?.disconnect()
  visibilityObserver = null
}

const attachVisibilityObserver = () => {
  detachVisibilityObserver()
  if (!shouldLazyLoadOnMobile.value || hasEnteredViewport.value) return
  if (typeof window === 'undefined' || typeof IntersectionObserver === 'undefined') {
    hasEnteredViewport.value = true
    flushPendingAutoLoad()
    return
  }
  if (!rootRef.value) return

  visibilityObserver = new IntersectionObserver((entries) => {
    if (!entries.some((entry) => entry.isIntersecting)) return
    hasEnteredViewport.value = true
    detachVisibilityObserver()
    flushPendingAutoLoad()
  }, {
    root: null,
    rootMargin: '200px 0px',
    threshold: 0.01
  })
  visibilityObserver.observe(rootRef.value)
}

const loadActiveUsage = async () => {
  activeQueryLoading.value = true
  error.value = null
  try {
    const result = await adminAPI.providers.getUsage(props.provider.id, 'active', true)
    usageInfo.value = result
    _usageCache.set(props.provider.id, { data: result, ts: Date.now() })
  } catch (e: any) {
    console.error('Failed to load active usage:', e)
  } finally {
    activeQueryLoading.value = false
  }
}

// 探测会持久化上游额度状态，因此刷新当前单元格，使紧凑进度条与权益状态反映最新快照。
const handleGrokProbed = async () => {
  await loadUsage({ source: 'active', bypassCache: true })
}

// ===== API Key quota progress bars =====

// QuotaBarInfo describes local spending utilization and its optional reset
// timestamp.
interface QuotaBarInfo {
  utilization: number
  resetsAt: string | null
}

// makeQuotaBar calculates progress and the deadline for one local spending quota.
// Fixed schedules use server metadata; rolling windows use the stored start plus
// elapsed days, with a thirty-day duration for monthly limits. Missing starts
// leave the reset timestamp empty until accounting initializes a period.
// Progress uses the effective amount returned by the API, which already treats
// expired usage as zero; deriving a deadline here does not mutate that amount.
const makeQuotaBar = (
  used: number,
  limit: number,
  startKey?: string
): QuotaBarInfo => {
  const utilization = limit > 0 ? (used / limit) * 100 : 0
  let resetsAt: string | null = null
  if (startKey) {
    const extra = props.provider.extra as Record<string, unknown> | undefined
    const prefix = startKey.replace(/_start$/, '')
    const mode = (extra?.[`${prefix}_reset_mode`] as string) || 'rolling'

    if (mode === 'fixed') {
      // Use pre-computed next reset time for fixed mode
      const resetAtKey = `${prefix}_reset_at`
      resetsAt = (extra?.[resetAtKey] as string) || null
    } else {
      // Rolling mode: compute from start + period
      const startStr = extra?.[startKey] as string | undefined
      if (startStr) {
        const startDate = new Date(startStr)
        // Rolling windows use elapsed days, matching the backend quota expiry rules.
        const days = prefix === 'quota_daily' ? 1 : prefix === 'quota_weekly' ? 7 : 30
        const periodMs = days * 24 * 60 * 60 * 1000
        resetsAt = new Date(startDate.getTime() + periodMs).toISOString()
      }
    }
  }
  return { utilization, resetsAt }
}

// hasApiKeyQuota reports whether an API Key or Bedrock provider has any positive local spending limit, including monthly-only limits.
const hasApiKeyQuota = computed(() => {
  if (props.provider.type !== 'apikey' && props.provider.type !== 'bedrock') return false
  return (
    (props.provider.quota_daily_limit ?? 0) > 0 ||
    (props.provider.quota_weekly_limit ?? 0) > 0 ||
    (props.provider.quota_monthly_limit ?? 0) > 0 ||
    (props.provider.quota_limit ?? 0) > 0
  )
})

const quotaDailyBar = computed((): QuotaBarInfo | null => {
  const limit = props.provider.quota_daily_limit ?? 0
  if (limit <= 0) return null
  return makeQuotaBar(props.provider.quota_daily_used ?? 0, limit, 'quota_daily_start')
})

const quotaWeeklyBar = computed((): QuotaBarInfo | null => {
  const limit = props.provider.quota_weekly_limit ?? 0
  if (limit <= 0) return null
  return makeQuotaBar(props.provider.quota_weekly_used ?? 0, limit, 'quota_weekly_start')
})

// quotaMonthlyBar exposes monthly progress when its configured USD ceiling is
// positive.
const quotaMonthlyBar = computed((): QuotaBarInfo | null => {
  const limit = props.provider.quota_monthly_limit ?? 0
  if (limit <= 0) return null
  return makeQuotaBar(props.provider.quota_monthly_used ?? 0, limit, 'quota_monthly_start')
})

const quotaTotalBar = computed((): QuotaBarInfo | null => {
  const limit = props.provider.quota_limit ?? 0
  if (limit <= 0) return null
  return makeQuotaBar(props.provider.quota_used ?? 0, limit)
})

const handleOllamaCloudUsageUpdated = (state: NonNullable<Provider['ollama_cloud_usage']>) => {
  emit('provider-updated', { ...props.provider, ollama_cloud_usage: state })
}

onMounted(() => {
  if (canUseMatchMedia) {
    desktopViewportMediaQuery = window.matchMedia(TABLE_DESKTOP_MEDIA_QUERY)
    isDesktopViewport.value = desktopViewportMediaQuery.matches
    desktopViewportListener = (event: MediaQueryListEvent) => {
      isDesktopViewport.value = event.matches
    }
    if (typeof desktopViewportMediaQuery.addEventListener === 'function') {
      desktopViewportMediaQuery.addEventListener('change', desktopViewportListener)
    } else {
      desktopViewportMediaQuery.addListener(desktopViewportListener)
    }
  }

  if (isBatchManaged.value) {
    syncManagedUsageState()
    requestParentBatchUsage()
    return
  }

  if (!shouldAutoLoadUsageOnMount.value) return
  const source = isAnthropicOAuthOrSetupToken.value ? 'passive' : undefined
  requestAutoLoad(source)
})

watch(
  () => [props.batchedUsage, props.batchedUsageError, props.batchedUsageLoading, isBatchManaged.value] as const,
  () => {
    syncManagedUsageState()
  },
  { immediate: true, deep: true }
)

watch(isBatchManaged, (managed, wasManaged) => {
  if (managed && !wasManaged) {
    syncManagedUsageState()
    requestParentBatchUsage()
  }
})

watch(
  () => [props.provider.id, props.provider.platform, props.provider.type, isBatchManaged.value] as const,
  ([providerID, platform, providerType, managed], [previousProviderID, previousPlatform, previousProviderType]) => {
    if (
      providerID === previousProviderID &&
      platform === previousPlatform &&
      providerType === previousProviderType
    ) {
      return
    }
    if (!managed || !shouldFetchUsage.value) return
    syncManagedUsageState()
    requestParentBatchUsage()
  },
  { flush: 'post' }
)

watch(openAIUsageRefreshKey, (nextKey, prevKey) => {
  if (!prevKey || nextKey === prevKey) return
  if (props.provider.platform !== 'openai' || props.provider.type !== 'oauth') return

	_usageCache.delete(props.provider.id)
	usageInfo.value = null
	requestAutoLoad()
})

watch(
  () => props.manualRefreshToken,
  (nextToken, prevToken) => {
    if (nextToken === prevToken) return
    if (!shouldFetchUsage.value) return

    if (isBatchManaged.value) {
      requestParentBatchUsage({ force: true })
      return
    }

    const source = isAnthropicOAuthOrSetupToken.value ? 'passive' : undefined
    _usageCache.delete(props.provider.id)
    loadUsage({ source, bypassCache: true }).catch((e) => {
      console.error('Failed to refresh usage after manual refresh:', e)
    })
  }
)

watch(
  [rootRef, shouldLazyLoadOnMobile],
  () => {
    if (shouldLazyLoadOnMobile.value) {
      attachVisibilityObserver()
      return
    }
    detachVisibilityObserver()
  },
  { immediate: true, flush: 'post' }
)

watch(isDesktopViewport, (isDesktop) => {
  if (isDesktop) {
    detachVisibilityObserver()
    hasEnteredViewport.value = true
    flushPendingAutoLoad()
    return
  }
  hasEnteredViewport.value = false
  attachVisibilityObserver()
})

onUnmounted(() => {
  detachVisibilityObserver()
  if (desktopViewportMediaQuery && desktopViewportListener) {
    if (typeof desktopViewportMediaQuery.removeEventListener === 'function') {
      desktopViewportMediaQuery.removeEventListener('change', desktopViewportListener)
    } else {
      desktopViewportMediaQuery.removeListener(desktopViewportListener)
    }
  }
  desktopViewportListener = null
  desktopViewportMediaQuery = null
})
</script>
