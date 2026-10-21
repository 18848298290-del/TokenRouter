<template>
  <BaseDialog
    :show="show"
    :title="t('team.statisticsTitle', { name: team?.name || '' })"
    width="extra-wide"
    @close="emit('close')"
  >
    <div class="space-y-6">
      <div
        v-if="team"
        class="flex flex-wrap items-center justify-between gap-3 rounded-control border border-gray-200 bg-gray-50 px-4 py-3 dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="min-w-0">
          <p class="truncate font-semibold text-gray-900 dark:text-white">{{ team.name }}</p>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('team.last30DaysStatistics') }} ·
            {{ t('team.memberCount', { count: team.member_count }) }}
          </p>
        </div>
        <span :class="['badge', team.status === 'active' ? 'badge-success' : 'badge-danger']">
          {{ team.status === 'active' ? t('team.statusActive') : t('team.statusSuspended') }}
        </span>
      </div>

      <div
        v-if="loading && !summary"
        class="space-y-6"
        role="status"
        :aria-label="t('common.loading')"
        aria-busy="true"
        data-loading-skeleton
      >
        <div class="grid grid-cols-2 lg:grid-cols-4 gap-4" aria-hidden="true">
          <div v-for="card in 4" :key="card" class="card min-w-0 space-y-3 p-4">
            <Skeleton width="55%" :height="12" />
            <Skeleton width="75%" :height="28" />
            <Skeleton width="65%" :height="12" />
          </div>
        </div>
        <TeamMemberUsageCharts :series="[]" :loading="true" />
      </div>

      <template v-else>
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <div class="metric-card">
            <p class="metric-label">{{ t('team.totalCost') }}</p>
            <BalanceAmount
              :amount="summary?.actual_cost || 0"
              :fraction-digits="4"
              icon-size="md"
              class="mt-2 text-xl font-semibold"
            />
          </div>
          <div class="metric-card">
            <p class="metric-label">{{ t('team.requests') }}</p>
            <p class="metric-value">{{ formatNumber(summary?.request_count || 0) }}</p>
          </div>
          <div class="metric-card">
            <p class="metric-label">{{ t('team.inputTokens') }}</p>
            <p class="metric-value">{{ formatNumber(summary?.input_tokens || 0) }}</p>
          </div>
          <div class="metric-card">
            <p class="metric-label">{{ t('team.outputTokens') }}</p>
            <p class="metric-value">{{ formatNumber(summary?.output_tokens || 0) }}</p>
          </div>
        </div>

        <TeamMemberUsageCharts :series="memberSeries" :loading="loading" />
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import Skeleton from '@/components/common/Skeleton.vue'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminTeam } from '@/api/admin/teams'
import type { TeamUsageSummary } from '@/api/team'
import BaseDialog from '@/components/common/BaseDialog.vue'
import BalanceAmount from '@/components/common/BalanceAmount.vue'
import TeamMemberUsageCharts from '@/components/charts/TeamMemberUsageCharts.vue'
import { useAppStore } from '@/stores/app'

// TeamStatsModal.vue loads team totals and per-member usage for the statistics dialog.
// Member identities and usage responses are matched by their shared request order so
// chart labels correspond to the correct totals. Each load clears the visible data
// before starting the requests.
//
// A monotonically increasing sequence identifies the newest load. Closing the dialog or
// switching the selected team invalidates earlier work, and only the current sequence
// can publish summaries, report errors, or clear loading. Member requests are awaited
// together after the member list arrives; a rejected request leaves that load without a
// published summary.

// props supplies the selected team and visibility that trigger summary loading and
// invalidate earlier work when the dialog closes.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
  // team selects the statistics subject; loading waits when it is null and
  // visibility changes clear earlier displayed results.
  team: AdminTeam | null
}>()

// emit requests that the parent close the statistics dialog and update the visibility
// it supplies to this component.
const emit = defineEmits<{
  (event: 'close'): void // Requests parent-owned dialog closure.
}>()

// The translation and notification services provide the statistics labels and the
// visible error message when the current load fails.
const { t } = useI18n()
const appStore = useAppStore()
// loading, summary, and memberSeries describe one statistics load: the pending flag
// controls feedback while accepted totals and member rows feed the charts.
const loading = ref(false)
const summary = ref<TeamUsageSummary | null>(null)
const memberSeries = ref<
  Array<{
    userID: number // Chart member identifier.
    label: string // Member chart label.
    summary: TeamUsageSummary // Member usage totals.
  }>
>([])
// requestSequence invalidates older statistics requests when another team is loaded or
// the dialog is hidden by its parent.
let requestSequence = 0

// formatNumber renders numeric totals with locale separators and substitutes zero for
// falsy input. It is used by the request and token statistic cards when a summary is
// absent.
const formatNumber = (value: number) => Number(value || 0).toLocaleString()

// loadStatistics loads the selected team's summary, members, and each member's usage.
// It captures the team and a sequence before starting work, clearing the previous
// results for the pending dialog.
//
// Member summaries retain Promise.all input order, allowing each response to be paired
// with its member label. Sequence checks guard both success and failure publication; a
// newer load or a hidden dialog invalidates this work. Only the current sequence clears
// the shared loading flag.
const loadStatistics = async () => {
  const team = props.team
  if (!team) return
  // Claim a generation before starting requests so closing or loading another team can
  // invalidate both successful and failed completions together.
  const sequence = ++requestSequence
  loading.value = true
  summary.value = null
  memberSeries.value = []
  try {
    const [members, teamSummary] = await Promise.all([
      adminAPI.teams.members(team.id),
      adminAPI.teams.usage(team.id),
    ])
    // Keep member requests in list order so each returned summary can be paired with
    // the same member's identity and display label.
    const memberSummaries = await Promise.all(
      members.map((member) =>
        adminAPI.teams.usage(team.id, {
          member_id: member.user_id, // Requested member identifier.
        }),
      ),
    )
    // A newer selection or closure invalidates this generation, so its completion must
    // not publish data or feedback into the current dialog.
    if (sequence !== requestSequence) return
    summary.value = teamSummary
    memberSeries.value = members.map((member, index) => ({
      userID: member.user_id, // Chart member identifier.
      label: member.username || member.email, // Member chart label.
      summary: memberSummaries[index], // Member usage totals.
    }))
  } catch (error: any) {
    // An invalidated generation must not show an error for the team now displayed, so
    // failures use the same ownership check as successful data.
    if (sequence !== requestSequence) return
    appStore.showError(error?.message || t('team.loadFailed'))
  } finally {
    if (sequence === requestSequence) loading.value = false
  }
}

// Opening or changing teams starts another generation; hiding invalidates unfinished
// requests before clearing the displayed totals and member charts.
watch(
  () => [props.show, props.team?.id] as const,
  ([visible]) => {
    if (visible && props.team) void loadStatistics()
    else {
      requestSequence++
      summary.value = null
      memberSeries.value = []
    }
  },
  {
    immediate: true, // Runs on watcher registration.
  },
)
</script>

<style scoped>
.metric-card {
  @apply rounded-surface border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900;
}

.metric-label {
  @apply text-xs font-medium text-gray-500 dark:text-gray-400;
}

.metric-value {
  @apply mt-2 text-xl font-semibold text-gray-900 dark:text-white;
}
</style>
