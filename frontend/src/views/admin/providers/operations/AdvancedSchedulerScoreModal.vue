<template>
  <BaseDialog
    :show="show"
    :title="t('admin.providers.advancedSchedulerScore.title')"
    width="extra-wide"
    @close="handleClose"
  >
    <div class="min-h-[320px] space-y-5">
      <div v-if="loadingOverview && !response" class="space-y-4" aria-busy="true">
        <div class="h-14 animate-pulse rounded-control bg-gray-100 dark:bg-dark-700"></div>
        <div class="h-10 animate-pulse rounded-control bg-gray-100 dark:bg-dark-700"></div>
        <div class="grid grid-cols-2 gap-3 md:grid-cols-5">
          <div
            v-for="index in 5"
            :key="index"
            class="h-20 animate-pulse rounded-control bg-gray-100 dark:bg-dark-700"
          ></div>
        </div>
      </div>

      <div
        v-else-if="errorMessage"
        class="flex min-h-[240px] flex-col items-center justify-center gap-3 text-center"
      >
        <Icon name="exclamationCircle" size="xl" class="text-red-500" />
        <p class="max-w-lg text-sm text-gray-600 dark:text-dark-300">{{ errorMessage }}</p>
        <button type="button" class="btn btn-secondary btn-sm" @click="refresh">
          <Icon
            name="refresh"
            size="sm"
            :class="loadingOverview || loadingDetail ? 'animate-spin' : ''"
          />
          {{ t('admin.providers.advancedSchedulerScore.retry') }}
        </button>
      </div>

      <template v-else-if="response">
        <header
          class="flex flex-col gap-3 border-b border-gray-200 pb-4 dark:border-dark-600 sm:flex-row sm:items-end sm:justify-between"
        >
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
              <h4 class="truncate text-base font-semibold text-gray-900 dark:text-gray-100">
                {{ response.provider.name }}
              </h4>
              <span class="font-mono text-xs text-gray-500 dark:text-dark-400"
                >#{{ response.provider.id }}</span
              >
              <span class="text-xs text-gray-500 dark:text-dark-400"
                >{{ response.provider.platform }} / {{ response.provider.type }}</span
              >
              <span
                :class="
                  response.provider.status === 'active'
                    ? 'text-emerald-600 dark:text-emerald-400'
                    : 'text-amber-600 dark:text-amber-400'
                "
                class="text-xs font-medium"
              >
                {{ response.provider.status }}
              </span>
            </div>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.providers.advancedSchedulerScore.generatedAt') }}:
              {{ formatTimestamp(response.generated_at) }}
              <span class="mx-1">·</span>
              {{ t('admin.providers.advancedSchedulerScore.calculationVersion') }}:
              {{ response.calculation_version }}
            </p>
          </div>
          <button
            type="button"
            class="flex shrink-0 rounded-control border border-gray-200 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:text-dark-300 dark:hover:bg-dark-700 dark:hover:text-white btn-icon-sm"
            :title="t('admin.providers.advancedSchedulerScore.refresh')"
            :aria-label="t('admin.providers.advancedSchedulerScore.refresh')"
            :disabled="loadingOverview || loadingDetail"
            @click="refresh"
          >
            <Icon
              name="refresh"
              size="sm"
              :class="loadingOverview || loadingDetail ? 'animate-spin' : ''"
            />
          </button>
        </header>

        <div
          v-if="groups.length === 0"
          class="flex min-h-[220px] flex-col items-center justify-center gap-2 text-center"
        >
          <Icon name="calculator" size="xl" class="text-gray-400 dark:text-dark-500" />
          <p class="text-sm text-gray-600 dark:text-dark-300">
            {{ t('admin.providers.advancedSchedulerScore.empty') }}
          </p>
        </div>

        <template v-else>
          <div class="-mx-1 overflow-x-auto px-1 pb-1">
            <div
              class="flex min-w-max gap-2"
              role="tablist"
              :aria-label="t('admin.providers.advancedSchedulerScore.groups')"
            >
              <button
                v-for="group in groups"
                :key="group.id"
                type="button"
                role="tab"
                :aria-selected="activeGroupID === group.id"
                :class="[
                  'flex min-h-12 flex-col justify-center rounded-control border px-3 text-left transition-colors',
                  activeGroupID === group.id
                    ? 'border-primary-500 bg-primary-50 text-primary-800 dark:border-primary-500/15 dark:bg-primary-500/8 dark:text-primary-500'
                    : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-300 dark:hover:border-dark-500',
                ]"
                @click="selectGroup(group.id)"
              >
                <span class="max-w-48 truncate text-sm font-medium">{{ group.name }}</span>
                <span class="mt-0.5 text-xs opacity-75">
                  {{
                    group.eligible
                      ? formatNumber(group.final_score)
                      : t('admin.providers.advancedSchedulerScore.filtered')
                  }}
                </span>
              </button>
            </div>
          </div>

          <div v-if="loadingDetail && !detail" class="space-y-4 py-3" aria-busy="true">
            <div class="h-12 animate-pulse rounded-control bg-gray-100 dark:bg-dark-700"></div>
            <div class="grid grid-cols-2 gap-3 md:grid-cols-5">
              <div
                v-for="index in 5"
                :key="index"
                class="h-20 animate-pulse rounded-control bg-gray-100 dark:bg-dark-700"
              ></div>
            </div>
            <div class="h-48 animate-pulse rounded-control bg-gray-100 dark:bg-dark-700"></div>
          </div>

          <template v-else-if="detail">
            <section class="border-b border-gray-200 pb-5 dark:border-dark-600">
              <button
                type="button"
                class="flex w-full items-center justify-between gap-3 text-left"
                :aria-expanded="showScenario"
                @click="showScenario = !showScenario"
              >
                <div>
                  <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                    {{ t('admin.providers.advancedSchedulerScore.scenario') }}
                  </h5>
                  <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                    {{
                      detail.context.baseline
                        ? t('admin.providers.advancedSchedulerScore.baseline')
                        : t('admin.providers.advancedSchedulerScore.simulationActive')
                    }}
                  </p>
                </div>
                <Icon
                  name="chevronDown"
                  size="sm"
                  :class="['transition-transform', showScenario && 'rotate-180']"
                  :animate-on-hover="false"
                />
              </button>
              <div
                v-if="showScenario"
                class="mt-4 grid gap-3 border-t border-gray-100 pt-4 dark:border-dark-700 md:grid-cols-3"
              >
                <label class="min-w-0">
                  <span class="input-label">{{
                    t('admin.providers.advancedSchedulerScore.requestedModel')
                  }}</span>
                  <input
                    v-model.trim="scenario.requested_model"
                    class="input"
                    :placeholder="
                      t('admin.providers.advancedSchedulerScore.requestedModelPlaceholder')
                    "
                  />
                </label>
                <label class="min-w-0">
                  <span class="input-label">{{
                    t('admin.providers.advancedSchedulerScore.sessionSticky')
                  }}</span>
                  <Select
                    v-model="scenario.sticky_provider_id"
                    :options="candidateOptions"
                    :placeholder="t('admin.providers.advancedSchedulerScore.notSpecified')"
                    clearable
                  />
                </label>
                <label class="min-w-0">
                  <span class="input-label">{{
                    t('admin.providers.advancedSchedulerScore.previousResponse')
                  }}</span>
                  <Select
                    v-model="scenario.previous_response_provider_id"
                    :options="candidateOptions"
                    :placeholder="t('admin.providers.advancedSchedulerScore.notSpecified')"
                    clearable
                  />
                </label>
                <div class="md:col-span-3 flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    class="btn btn-primary btn-sm"
                    :disabled="loadingDetail"
                    @click="preview"
                  >
                    <Icon name="refresh" size="sm" :class="loadingDetail ? 'animate-spin' : ''" />
                    {{ t('admin.providers.advancedSchedulerScore.recalculate') }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm"
                    :disabled="loadingDetail"
                    @click="restoreBaseline"
                  >
                    {{ t('admin.providers.advancedSchedulerScore.restoreBaseline') }}
                  </button>
                </div>
              </div>
            </section>

            <section
              v-if="!detail.eligible"
              class="border-b border-gray-200 py-5 dark:border-dark-600"
            >
              <div
                class="flex items-start gap-3 rounded-control border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
              >
                <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0" />
                <div>
                  <p class="font-medium">
                    {{ t('admin.providers.advancedSchedulerScore.ineligible') }}
                  </p>
                  <p class="mt-1 text-xs leading-5">
                    {{ hardFilterText(detail.hard_filter_reasons) }}
                  </p>
                </div>
              </div>
            </section>

            <template v-else-if="detail.score">
              <section class="border-b border-gray-200 py-5 dark:border-dark-600">
                <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                  {{ t('admin.providers.advancedSchedulerScore.scoreSummary') }}
                </h5>
                <div class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
                  <div
                    v-for="item in scoreSummaryItems"
                    :key="item.key"
                    class="rounded-control border border-gray-200 px-3 py-2.5 dark:border-dark-600"
                  >
                    <p class="text-xs text-gray-500 dark:text-dark-400">{{ item.label }}</p>
                    <p
                      class="mt-1 font-mono text-sm font-semibold text-gray-900 dark:text-gray-100"
                    >
                      {{ item.value }}
                    </p>
                  </div>
                </div>
                <p class="mt-3 text-xs text-gray-500 dark:text-dark-400">
                  {{ t('admin.providers.advancedSchedulerScore.selectionExplanation') }}
                </p>
              </section>

              <section class="border-b border-gray-200 py-5 dark:border-dark-600">
                <div class="flex items-center justify-between gap-3">
                  <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                    {{ t('admin.providers.advancedSchedulerScore.formula') }}
                  </h5>
                  <button
                    type="button"
                    class="flex shrink-0 rounded-control border border-gray-200 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:border-dark-600 dark:text-dark-300 dark:hover:bg-dark-700 dark:hover:text-white btn-icon-sm"
                    :title="t('admin.providers.advancedSchedulerScore.copyFormula')"
                    :aria-label="t('admin.providers.advancedSchedulerScore.copyFormula')"
                    @click="copyFormula(detail.score.formula)"
                  >
                    <Icon name="copy" size="sm" />
                  </button>
                </div>
                <div
                  class="mt-3 overflow-x-auto rounded-control border border-gray-200 bg-gray-50 px-3 py-3 dark:border-dark-600 dark:bg-dark-900"
                >
                  <code
                    class="block min-w-max whitespace-pre font-mono text-xs leading-6 text-gray-800 dark:text-dark-200"
                    >{{ detail.score.formula }}</code
                  >
                </div>
              </section>

              <section class="border-b border-gray-200 py-5 dark:border-dark-600">
                <div class="flex flex-wrap items-baseline justify-between gap-2">
                  <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                    {{ t('admin.providers.advancedSchedulerScore.metrics') }}
                  </h5>
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ normalizationRangeText(detail.candidate_pool.normalization_ranges) }}
                  </p>
                </div>
                <div class="mt-3 hidden overflow-x-auto lg:block">
                  <table class="w-full min-w-[860px] text-left text-xs">
                    <thead
                      class="border-b border-gray-200 text-gray-500 dark:border-dark-600 dark:text-dark-400"
                    >
                      <tr>
                        <th class="px-2 py-2 font-medium">
                          {{ t('admin.providers.advancedSchedulerScore.metricColumns.metric') }}
                        </th>
                        <th class="px-2 py-2 font-medium">
                          {{ t('admin.providers.advancedSchedulerScore.metricColumns.raw') }}
                        </th>
                        <th class="px-2 py-2 font-medium">
                          {{
                            t('admin.providers.advancedSchedulerScore.metricColumns.normalization')
                          }}
                        </th>
                        <th class="px-2 py-2 font-medium">
                          {{ t('admin.providers.advancedSchedulerScore.metricColumns.normalized') }}
                        </th>
                        <th class="px-2 py-2 font-medium">
                          {{ t('admin.providers.advancedSchedulerScore.metricColumns.weight') }}
                        </th>
                        <th class="px-2 py-2 font-medium">
                          {{
                            t('admin.providers.advancedSchedulerScore.metricColumns.contribution')
                          }}
                        </th>
                        <th class="px-2 py-2 font-medium">
                          {{ t('admin.providers.advancedSchedulerScore.metricColumns.source') }}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr
                        v-for="metric in detail.metrics"
                        :key="metric.key"
                        class="border-b border-gray-100 align-top dark:border-dark-700"
                      >
                        <td class="px-2 py-2.5 font-medium text-gray-900 dark:text-gray-100">
                          {{ metricLabel(metric.key) }}
                        </td>
                        <td class="px-2 py-2.5 text-gray-600 dark:text-dark-300">
                          {{ metric.raw_value }}
                        </td>
                        <td
                          class="max-w-xs px-2 py-2.5 font-mono text-xs leading-5 text-gray-600 dark:text-dark-300"
                        >
                          {{ metric.normalization }}
                        </td>
                        <td class="px-2 py-2.5 font-mono text-gray-900 dark:text-gray-100">
                          {{ formatNumber(metric.normalized_value) }}
                        </td>
                        <td class="px-2 py-2.5 font-mono text-gray-900 dark:text-gray-100">
                          {{ formatNumber(metric.weight) }}
                        </td>
                        <td class="px-2 py-2.5 font-mono text-gray-900 dark:text-gray-100">
                          {{ formatNumber(metric.weighted_contribution) }}
                        </td>
                        <td class="px-2 py-2.5 text-gray-500 dark:text-dark-400">
                          <span>{{ metricStatus(metric) }}</span>
                          <span v-if="metric.observed_at" class="block pt-0.5">{{
                            formatTimestamp(metric.observed_at)
                          }}</span>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <div class="mt-3 space-y-2 lg:hidden">
                  <Disclosure
                    summary-class="flex cursor-pointer list-none items-center justify-between gap-3 text-sm"
                    v-for="metric in detail.metrics"
                    :key="metric.key"
                    class="rounded-control border border-gray-200 px-3 py-2 dark:border-dark-600"
                  >
                    <template #summary>
                      <span class="font-medium text-gray-900 dark:text-gray-100">{{
                        metricLabel(metric.key)
                      }}</span>
                      <span class="font-mono text-xs text-gray-600 dark:text-dark-300">{{
                        formatNumber(metric.weighted_contribution)
                      }}</span>
                    </template>
                    <dl
                      class="mt-3 grid gap-2 border-t border-gray-100 pt-3 text-xs dark:border-dark-700"
                    >
                      <div>
                        <dt class="text-gray-500 dark:text-dark-400">
                          {{ t('admin.providers.advancedSchedulerScore.metricColumns.raw') }}
                        </dt>
                        <dd class="mt-0.5 text-gray-800 dark:text-dark-200">
                          {{ metric.raw_value }}
                        </dd>
                      </div>
                      <div>
                        <dt class="text-gray-500 dark:text-dark-400">
                          {{
                            t('admin.providers.advancedSchedulerScore.metricColumns.normalization')
                          }}
                        </dt>
                        <dd
                          class="mt-0.5 break-words font-mono text-xs leading-5 text-gray-800 dark:text-dark-200"
                        >
                          {{ metric.normalization }}
                        </dd>
                      </div>
                      <div class="grid grid-cols-3 gap-2">
                        <div>
                          <dt class="text-gray-500 dark:text-dark-400">
                            {{
                              t('admin.providers.advancedSchedulerScore.metricColumns.normalized')
                            }}
                          </dt>
                          <dd class="mt-0.5 font-mono">
                            {{ formatNumber(metric.normalized_value) }}
                          </dd>
                        </div>
                        <div>
                          <dt class="text-gray-500 dark:text-dark-400">
                            {{ t('admin.providers.advancedSchedulerScore.metricColumns.weight') }}
                          </dt>
                          <dd class="mt-0.5 font-mono">{{ formatNumber(metric.weight) }}</dd>
                        </div>
                        <div>
                          <dt class="text-gray-500 dark:text-dark-400">
                            {{
                              t('admin.providers.advancedSchedulerScore.metricColumns.contribution')
                            }}
                          </dt>
                          <dd class="mt-0.5 font-mono">
                            {{ formatNumber(metric.weighted_contribution) }}
                          </dd>
                        </div>
                      </div>
                    </dl>
                  </Disclosure>
                </div>
              </section>
            </template>

            <section class="border-b border-gray-200 py-5 dark:border-dark-600">
              <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                {{ t('admin.providers.advancedSchedulerScore.candidatePool') }}
              </h5>
              <div class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                <div
                  class="rounded-control border border-gray-200 px-3 py-2.5 dark:border-dark-600"
                >
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ t('admin.providers.advancedSchedulerScore.poolTotal') }}
                  </p>
                  <p class="mt-1 font-mono text-sm font-semibold">
                    {{ detail.candidate_pool.total_candidates }}
                  </p>
                </div>
                <div
                  class="rounded-control border border-gray-200 px-3 py-2.5 dark:border-dark-600"
                >
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ t('admin.providers.advancedSchedulerScore.poolEligible') }}
                  </p>
                  <p class="mt-1 font-mono text-sm font-semibold">
                    {{ detail.candidate_pool.eligible_candidates }}
                  </p>
                </div>
                <div
                  class="rounded-control border border-gray-200 px-3 py-2.5 dark:border-dark-600"
                >
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ t('admin.providers.advancedSchedulerScore.poolExcluded') }}
                  </p>
                  <p class="mt-1 font-mono text-sm font-semibold">
                    {{ detail.candidate_pool.excluded_candidates }}
                  </p>
                </div>
                <div
                  class="rounded-control border border-gray-200 px-3 py-2.5 dark:border-dark-600"
                >
                  <p class="text-xs text-gray-500 dark:text-dark-400">
                    {{ t('admin.providers.advancedSchedulerScore.poolTopK') }}
                  </p>
                  <p class="mt-1 font-mono text-sm font-semibold">
                    {{ detail.candidate_pool.top_k }}
                  </p>
                </div>
              </div>
              <p
                v-if="exclusionSummary"
                class="mt-3 text-xs leading-5 text-gray-500 dark:text-dark-400"
              >
                {{ exclusionSummary }}
              </p>
              <div
                v-if="detail.candidate_pool.candidates.length"
                class="mt-4 max-h-56 overflow-auto border-t border-gray-100 pt-3 dark:border-dark-700"
              >
                <div
                  v-for="candidate in detail.candidate_pool.candidates"
                  :key="candidate.id"
                  class="grid grid-cols-[2rem_minmax(0,1fr)_5rem_4rem] items-center gap-2 border-b border-gray-100 py-2 text-xs dark:border-dark-700"
                >
                  <span class="font-mono text-gray-500 dark:text-dark-400"
                    >#{{ candidate.rank }}</span
                  >
                  <span class="truncate text-gray-800 dark:text-dark-200"
                    >{{ candidate.name }}
                    <span class="text-gray-400">#{{ candidate.id }}</span></span
                  >
                  <span class="font-mono text-right text-gray-800 dark:text-dark-200">{{
                    formatNumber(candidate.final_score)
                  }}</span>
                  <span class="text-right text-gray-500 dark:text-dark-400">{{
                    candidate.in_top_k ? t('admin.providers.advancedSchedulerScore.inTopK') : ''
                  }}</span>
                </div>
              </div>
            </section>

            <section
              class="grid gap-5 py-5"
              :class="detail.policy_signals.length ? 'lg:grid-cols-2' : ''"
            >
              <div>
                <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                  {{ t('admin.providers.advancedSchedulerScore.effectiveSettings') }}
                </h5>
                <dl
                  data-testid="advanced-scheduler-effective-settings"
                  class="mt-3 grid grid-cols-1 gap-x-6 border-t border-gray-100 text-xs dark:border-dark-700 sm:grid-cols-2"
                >
                  <div
                    v-for="setting in detail.effective_settings"
                    :key="setting.key"
                    class="grid grid-cols-[minmax(0,1fr)_auto] gap-3 border-b border-gray-100 py-2 dark:border-dark-700"
                  >
                    <dt class="truncate text-gray-600 dark:text-dark-300">
                      {{ settingLabel(setting.key) }}
                    </dt>
                    <dd class="flex items-baseline justify-end gap-2 whitespace-nowrap text-right">
                      <span class="font-mono text-gray-900 dark:text-gray-100">{{
                        setting.value
                      }}</span
                      ><span class="text-gray-400">{{ settingSourceLabel(setting.source) }}</span>
                    </dd>
                  </div>
                </dl>
              </div>
              <div v-if="detail.policy_signals.length">
                <h5 class="text-sm font-semibold text-gray-900 dark:text-gray-100">
                  {{ t('admin.providers.advancedSchedulerScore.policySignals') }}
                </h5>
                <div class="mt-3 space-y-2">
                  <div
                    v-for="signal in detail.policy_signals"
                    :key="signal.key"
                    class="border-l-2 border-gray-200 pl-3 dark:border-dark-600"
                  >
                    <p class="text-xs font-medium text-gray-800 dark:text-dark-200">
                      {{ policyLabel(signal.key) }}
                      <span class="font-normal text-gray-400">{{
                        policyStateLabel(signal.state)
                      }}</span>
                    </p>
                    <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-dark-400">
                      {{ signal.detail }}
                    </p>
                  </div>
                </div>
              </div>
            </section>
          </template>
        </template>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import Disclosure from '@/components/common/Disclosure.vue'

import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import { Icon } from '@/components/icons'
import { useClipboard } from '@/composables/useClipboard'
import { adminAPI } from '@/api/admin'
import { formatDateTime } from '@/utils/format'
import type {
  AdvancedSchedulerScoreDiagnosticDetail,
  AdvancedSchedulerScoreDiagnosticMetric,
  AdvancedSchedulerScoreDiagnosticResponse,
  AdvancedSchedulerScorePreviewRequest,
} from '@/api/admin/providers'
import type { Provider } from '@/api/admin/providers.types'

/**
 * AdvancedSchedulerScoreModal.vue displays scheduler score diagnostics for a provider across
 * its assigned groups and optional request scenarios. Baseline detail responses are cached by
 * group within the dialog, while preview requests use the current model and affinity inputs.
 * Refresh clears that cache and retains the selected group when it remains available.
 */

/**
 * props identifies the visible provider; showing diagnostics loads its overview while hiding
 * clears cache, selected group, and scenario input.
 */
const props = defineProps<{
  show: boolean // parent-controlled diagnostic dialog visibility
  provider: Provider | null // selected provider or no selection
}>()

/**
 * emit requests that the provider page close diagnostics, leaving selection and dialog
 * visibility under the parent ownership.
 */
const emit = defineEmits<{
  /**
   * close requests that the parent hide the scheduler diagnostics dialog.
   */
  (event: 'close'): void
}>()

/**
 * Translation and clipboard access support diagnostic labels and formula copying, while
 * response, loading, cache, and scenario state belong to this dialog.
 */
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const response = ref<AdvancedSchedulerScoreDiagnosticResponse | null>(null)
const detail = computed<AdvancedSchedulerScoreDiagnosticDetail | undefined>(
  () => response.value?.detail,
)
const activeGroupID = ref<number | null>(null)
const loadingOverview = ref(false)
const loadingDetail = ref(false)
const errorMessage = ref('')
const showScenario = ref(false)
const detailCache = new Map<number, AdvancedSchedulerScoreDiagnosticResponse>()
/**
 * scenario holds optional model and provider-affinity inputs used by preview requests; reset
 * operations restore their absent values before baseline loading.
 */
const scenario = reactive<{
  requested_model: string // optional scenario model identifier
  sticky_provider_id: number | null // optional sticky affinity provider
  previous_response_provider_id: number | null // optional continuation affinity provider
}>({
  requested_model: '',
  sticky_provider_id: null,
  previous_response_provider_id: null,
})

/**
 * Group and candidate choices derive from the current response, while score summaries keep
 * unavailable probabilities distinct from measured zero.
 */
const groups = computed(() => response.value?.groups ?? [])
const candidateOptions = computed<SelectOption[]>(() =>
  (detail.value?.candidate_pool.candidates ?? []).map((candidate) => ({
    value: candidate.id,
    label: `${candidate.name} #${candidate.id}`,
  })),
)
const scoreSummaryItems = computed(() => {
  const score = detail.value?.score
  if (!score) return []
  return [
    {
      key: 'base',
      label: t('admin.providers.advancedSchedulerScore.baseScore'),
      value: formatNumber(score.base_score),
    },
    {
      key: 'sticky',
      label: t('admin.providers.advancedSchedulerScore.stickyBonus'),
      value: formatNumber(score.sticky_bonus),
    },
    {
      key: 'final',
      label: t('admin.providers.advancedSchedulerScore.finalScore'),
      value: formatNumber(score.final_score),
    },
    {
      key: 'rank',
      label: t('admin.providers.advancedSchedulerScore.rank'),
      value: score.in_top_k
        ? `${score.rank} / Top-${detail.value?.candidate_pool.top_k ?? 0}`
        : `${score.rank}`,
    },
    {
      key: 'probability',
      label: t('admin.providers.advancedSchedulerScore.selectionProbability'),
      value:
        score.selection_probability === undefined
          ? '—'
          : formatPercent(score.selection_probability),
    },
  ]
})
/**
 * exclusionSummary combines the returned exclusion counts with translated reasons and remains
 * empty when diagnostics report no candidate exclusion entries.
 */
const exclusionSummary = computed(() => {
  const exclusions = detail.value?.candidate_pool.exclusion_reasons ?? {}
  const items = Object.entries(exclusions)
  if (items.length === 0) return ''
  return items.map(([reason, count]) => `${hardFilterLabel(reason)} ${count}`).join(' · ')
})

watch(
  () => [props.show, props.provider?.id] as const,
  ([visible]) => {
    if (visible) {
      void loadOverview()
    } else {
      resetState()
    }
  },
  { immediate: true },
)

/**
 * resetState clears diagnostic data, group selection, request feedback, cached details, and
 * scenario inputs. It resets local display state when hidden but does not cancel an outstanding
 * API request.
 */
function resetState() {
  response.value = null
  activeGroupID.value = null
  loadingOverview.value = false
  loadingDetail.value = false
  errorMessage.value = ''
  showScenario.value = false
  // Clear cached baselines before accepting a new overview or hidden-dialog lifecycle so
  // subsequent group loads request current diagnostic data.
  detailCache.clear()
  resetScenario()
}

/**
 * resetScenario clears the requested model and both provider-affinity scenario inputs.
 */
function resetScenario() {
  scenario.requested_model = ''
  scenario.sticky_provider_id = null
  scenario.previous_response_provider_id = null
}

/**
 * loadOverview refreshes the provider group overview and clears cached details. It retains a
 * valid preferred group or selects the first returned group, then loads its baseline diagnostic
 * detail.
 */
async function loadOverview(preferredGroupID?: number | null) {
  if (!props.provider) return
  loadingOverview.value = true
  errorMessage.value = ''
  // Clear cached baselines before accepting a new overview or hidden-dialog lifecycle so
  // subsequent group loads request current diagnostic data.
  detailCache.clear()
  try {
    const next = await adminAPI.providers.getAdvancedSchedulerScore(props.provider.id)
    response.value = next
    const targetGroupID =
      preferredGroupID && next.groups.some((group) => group.id === preferredGroupID)
        ? preferredGroupID
        : (next.groups[0]?.id ?? null)
    activeGroupID.value = targetGroupID
    if (targetGroupID) {
      await loadDetail(targetGroupID, false)
    }
  } catch (error: any) {
    errorMessage.value = error?.message || t('admin.providers.advancedSchedulerScore.loadFailed')
  } finally {
    loadingOverview.value = false
  }
}

/**
 * selectGroup selects a diagnostic group and clears scenario inputs and prior errors. An
 * already selected group with detail needs no reload; other selections reuse baseline cache
 * when available.
 */
async function selectGroup(groupID: number) {
  if (activeGroupID.value === groupID && detail.value) return
  activeGroupID.value = groupID
  resetScenario()
  showScenario.value = false
  errorMessage.value = ''
  await loadDetail(groupID, false)
}

/**
 * loadDetail loads one provider/group diagnostic response or uses its cached baseline unless
 * forced. Successful responses replace visible data and cache; failures report an error while
 * retaining prior response state.
 */
async function loadDetail(groupID: number, force: boolean) {
  if (!props.provider) return
  if (!force) {
    const cached = detailCache.get(groupID)
    if (cached) {
      response.value = cached
      return
    }
  }
  loadingDetail.value = true
  try {
    const next = await adminAPI.providers.getAdvancedSchedulerScore(props.provider.id, groupID)
    detailCache.set(groupID, next)
    response.value = next
  } catch (error: any) {
    errorMessage.value = error?.message || t('admin.providers.advancedSchedulerScore.loadFailed')
  } finally {
    loadingDetail.value = false
  }
}

/**
 * preview requests diagnostics using the active group and nonempty scenario inputs. Its
 * response replaces visible detail but is not stored in the baseline cache, allowing a later
 * baseline reload.
 */
async function preview() {
  if (!props.provider || !activeGroupID.value) return
  loadingDetail.value = true
  errorMessage.value = ''
  const payload: AdvancedSchedulerScorePreviewRequest = {
    group_id: activeGroupID.value,
  }
  if (scenario.requested_model) payload.requested_model = scenario.requested_model
  if (scenario.sticky_provider_id) payload.sticky_provider_id = scenario.sticky_provider_id
  if (scenario.previous_response_provider_id)
    payload.previous_response_provider_id = scenario.previous_response_provider_id
  try {
    response.value = await adminAPI.providers.previewAdvancedSchedulerScore(
      props.provider.id,
      payload,
    )
  } catch (error: any) {
    errorMessage.value = error?.message || t('admin.providers.advancedSchedulerScore.previewFailed')
  } finally {
    loadingDetail.value = false
  }
}

/**
 * restoreBaseline clears scenario input and forces fresh baseline detail for the active group.
 * With no active group it does nothing, leaving diagnostic response and control state
 * unchanged.
 */
async function restoreBaseline() {
  if (!activeGroupID.value) return
  resetScenario()
  await loadDetail(activeGroupID.value, true)
}

/**
 * refresh clears scenario input and reloads the overview with the current group preference.
 * Overview loading clears cached baselines and chooses another group only if the previous one
 * is unavailable.
 */
async function refresh() {
  const selected = activeGroupID.value
  resetScenario()
  await loadOverview(selected)
}

/**
 * handleClose requests parent-controlled dismissal of the scheduler diagnostics dialog.
 */
function handleClose() {
  emit('close')
}

/**
 * copyFormula copies a displayed scoring formula with localized success feedback.
 */
function copyFormula(formula: string) {
  void copyToClipboard(formula, t('admin.providers.advancedSchedulerScore.formulaCopied'))
}

/**
 * formatNumber displays finite scores with up to four fractional digits and trims trailing
 * zeros. Undefined or nonfinite observations use an em dash, keeping unavailable diagnostics
 * distinct from measured zero.
 */
function formatNumber(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return '—'
  return value.toFixed(4).replace(/\.?0+$/, '')
}

/**
 * formatPercent converts a probability fraction to a percentage with up to two decimals.
 */
function formatPercent(value: number): string {
  // Convert the diagnostic probability fraction into a percent label.
  return `${(value * 100).toFixed(2).replace(/\.?0+$/, '')}%`
}

/**
 * formatTimestamp delegates a present diagnostic timestamp to the shared date formatter.
 * Missing values display an em dash, keeping absent observations distinguishable from the dates
 * shown for available diagnostics.
 */
function formatTimestamp(value: string | undefined): string {
  if (!value) return '—'
  return formatDateTime(value)
}

/**
 * metricLabel translates a scheduler metric identifier for its diagnostic row.
 */
function metricLabel(key: string): string {
  return t(`admin.providers.advancedSchedulerScore.metricNames.${key}`)
}

/**
 * settingLabel translates a scheduler setting identifier for its diagnostic row.
 */
function settingLabel(key: string): string {
  return t(`admin.providers.advancedSchedulerScore.settingNames.${key}`)
}

/**
 * policyLabel translates a scheduler policy identifier for its diagnostic row.
 */
function policyLabel(key: string): string {
  return t(`admin.providers.advancedSchedulerScore.policyNames.${key}`)
}

/**
 * policyStateLabel translates a scheduler policy-state identifier for its diagnostic row.
 */
function policyStateLabel(state: string): string {
  return t(`admin.providers.advancedSchedulerScore.policyStates.${state}`)
}

/**
 * settingSourceLabel translates the source of an effective scheduler setting.
 */
function settingSourceLabel(source: string): string {
  return t(`admin.providers.advancedSchedulerScore.settingSources.${source}`)
}

/**
 * hardFilterLabel translates a candidate exclusion reason for scheduler diagnostic summaries.
 */
function hardFilterLabel(reason: string): string {
  return t(`admin.providers.advancedSchedulerScore.filterReasons.${reason}`)
}

/**
 * hardFilterText joins translated exclusion reasons or supplies the no-candidate message when
 * none exist. The input order is retained so the summary follows the order returned by
 * diagnostics.
 */
function hardFilterText(reasons: string[] | undefined): string {
  if (!reasons?.length) return t('admin.providers.advancedSchedulerScore.noCandidate')
  return reasons.map(hardFilterLabel).join(' · ')
}

/**
 * metricStatus gives neutral observations precedence over availability, then returns the
 * reported source. Unavailable nonneutral observations receive a localized status instead of
 * exposing an absent source as the metric explanation.
 */
function metricStatus(metric: AdvancedSchedulerScoreDiagnosticMetric): string {
  if (metric.neutral) return t('admin.providers.advancedSchedulerScore.neutral')
  if (!metric.available) return t('admin.providers.advancedSchedulerScore.unavailable')
  return metric.source
}

/**
 * normalizationRangeText formats the candidate normalization ranges for diagnostic display.
 * Priority and queue observations are always included, while latency milliseconds and reset
 * seconds require both endpoints of their respective ranges.
 */
function normalizationRangeText(
  ranges: AdvancedSchedulerScoreDiagnosticDetail['candidate_pool']['normalization_ranges'],
): string {
  const parts = [
    `${t('admin.providers.advancedSchedulerScore.rangePriority')}: ${ranges.priority_min}–${ranges.priority_max}`,
    `${t('admin.providers.advancedSchedulerScore.rangeQueue')}: ${ranges.max_waiting_count}`,
  ]
  if (ranges.ttft_min_ms !== undefined && ranges.ttft_max_ms !== undefined) {
    parts.push(
      `${t('admin.providers.advancedSchedulerScore.rangeTTFT')}: ${formatNumber(ranges.ttft_min_ms)}–${formatNumber(ranges.ttft_max_ms)} ms`,
    )
  }
  if (ranges.reset_min_seconds !== undefined && ranges.reset_max_seconds !== undefined) {
    parts.push(
      `${t('admin.providers.advancedSchedulerScore.rangeReset')}: ${formatNumber(ranges.reset_min_seconds)}–${formatNumber(ranges.reset_max_seconds)} s`,
    )
  }
  return parts.join(' · ')
}
</script>
