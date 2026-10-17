<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-col-reverse gap-2 lg:flex-row lg:items-start lg:justify-between">
          <div class="min-w-0 flex-1">
            <ProviderTableFilters
              v-model:searchQuery="params.search"
              :filters="params"
              :groups="groups"
              @update:filters="(newFilters) => Object.assign(params, newFilters)"
              @change="debouncedReload"
              @update:searchQuery="debouncedReload"
            />
          </div>
          <ProviderTableActions
            class="shrink-0 lg:justify-end"
            :loading="loading"
            @refresh="handleManualRefresh"
            @create="openCreateProvider()"
          >
            <template #after>
              <!-- Auto Refresh Dropdown -->
              <div class="relative" ref="autoRefreshDropdownRef">
                <button
                  ref="autoRefreshButtonRef"
                  @click="toggleAutoRefreshDropdown"
                  class="btn btn-secondary shrink-0 justify-center btn-icon"
                  :title="autoRefreshButtonTitle"
                >
                  <Icon name="clock" size="sm" :class="autoRefreshEnabled ? 'text-primary-500' : ''" />
                </button>
                <MotionTransition name="dropdown-fade">
                  <div
                    v-if="showAutoRefreshDropdown" :inert="!(showAutoRefreshDropdown) || undefined"
                    class="dropdown fixed z-50 w-56 origin-top-left overflow-hidden py-0"
                    :style="autoRefreshDropdownStyle"
                  >
                    <div class="p-2">
                      <button
                        @click="setAutoRefreshEnabled(!autoRefreshEnabled)"
                        class="dropdown-item-sm justify-between rounded-control"
                      >
                        <span>{{ t('admin.providers.enableAutoRefresh') }}</span>
                        <Icon
                          v-if="autoRefreshEnabled"
                          name="check"
                          size="sm"
                          class="text-primary-500"
                          :animate-on-hover="false"
                        />
                      </button>
                      <div class="my-1 border-t border-gray-100 dark:border-dark-600"></div>
                      <button
                        v-for="sec in autoRefreshIntervals"
                        :key="sec"
                        @click="setAutoRefreshInterval(sec)"
                        class="dropdown-item-sm justify-between rounded-control"
                      >
                        <span>{{ autoRefreshIntervalLabel(sec) }}</span>
                        <Icon
                          v-if="autoRefreshIntervalSeconds === sec"
                          name="check"
                          size="sm"
                          class="text-primary-500"
                          :animate-on-hover="false"
                        />
                      </button>
                    </div>
                  </div>
                </MotionTransition>
              </div>

              <!-- 更多工具下拉菜单 -->
              <div class="relative" ref="providerToolsDropdownRef">
                <button
                  ref="providerToolsButtonRef"
                  @click="toggleProviderToolsDropdown"
                  class="btn btn-secondary justify-center lg:w-auto lg:px-3 lg:py-1.5 btn-icon"
                  :title="t('admin.providers.moreActions')"
                  :aria-expanded="showProviderToolsDropdown"
                >
                  <Icon name="more" size="sm" class="lg:hidden" />
                  <span class="hidden lg:inline">{{ t('admin.providers.moreActions') }}</span>
                  <Icon
                    name="chevronDown"
                    size="xs"
                    class="ml-1 hidden lg:inline"
                    :animate-on-hover="false"
                  />
                </button>
                <Teleport to="body">
                  <MotionTransition name="dropdown-fade">
                    <div
                      v-if="showProviderToolsDropdown" :inert="!(showProviderToolsDropdown) || undefined"
                      class="dropdown fixed z-teleport-tooltip origin-top-right overflow-hidden py-0"
                      :style="providerToolsDropdownStyle"
                      @click.stop
                    >
                      <div class="overflow-y-auto p-2" :style="{ maxHeight: `${providerToolsDropdownPosition.maxHeight}px` }">
                      <div class="px-2 py-2">
                        <div class="text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-gray-500">
                          {{ t('admin.providers.dataActions') }}
                        </div>
                      </div>
                      <button
                        class="dropdown-item-sm gap-3 rounded-control"
                        data-test="bulk-edit-filtered"
                        @click="openBulkEditFilteredFromMenu"
                      >
                        <span class="provider-tools-menu-icon bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300">
                          <Icon name="edit" size="sm" />
                        </span>
                        <span class="flex-1 text-left">{{ t('admin.providers.bulkEditFiltered') }}</span>
                      </button>
                      <button class="dropdown-item-sm gap-3 rounded-control" @click="openSyncFromCrs">
                        <span class="provider-tools-menu-icon bg-blue-50 text-blue-600 dark:bg-blue-900/30 dark:text-blue-300">
                          <Icon name="sync" size="sm" />
                        </span>
                        <span class="flex-1 text-left">{{ t('admin.providers.syncFromCrs') }}</span>
                      </button>
                      <button class="dropdown-item-sm gap-3 rounded-control" @click="openImportData">
                        <span class="provider-tools-menu-icon bg-emerald-50 text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-300">
                          <Icon name="upload" size="sm" />
                        </span>
                        <span class="flex-1 text-left">{{ t('admin.providers.dataImport') }}</span>
                      </button>
                      <button class="dropdown-item-sm gap-3 rounded-control" @click="openExportDataDialogFromMenu">
                        <span class="provider-tools-menu-icon bg-violet-50 text-violet-600 dark:bg-violet-900/30 dark:text-violet-300">
                          <Icon name="download" size="sm" />
                        </span>
                        <span class="flex-1 text-left">
                          {{ selIds.length ? t('admin.providers.dataExportSelected') : t('admin.providers.dataExport') }}
                        </span>
                        <span
                          v-if="selIds.length"
                          class="rounded-full bg-primary-100 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/40 dark:text-primary-300"
                        >
                          {{ t('admin.providers.selectedCount', { count: selIds.length }) }}
                        </span>
                      </button>

                      <div class="my-2 border-t border-gray-100 dark:border-dark-600"></div>
                      <div class="px-2 py-2">
                        <div class="text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-gray-500">
                          {{ t('admin.providers.toolActions') }}
                        </div>
                      </div>
                      <button class="dropdown-item-sm gap-3 rounded-control" @click="openErrorPassthrough">
                        <span class="provider-tools-menu-icon bg-amber-50 text-amber-600 dark:bg-amber-900/30 dark:text-amber-300">
                          <Icon name="shield" size="sm" />
                        </span>
                        <span class="flex-1 text-left">{{ t('admin.errorPassthrough.title') }}</span>
                      </button>
                      <button class="dropdown-item-sm gap-3 rounded-control" @click="openTLSFingerprintProfiles">
                        <span class="provider-tools-menu-icon bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-200">
                          <Icon name="lock" size="sm" />
                        </span>
                        <span class="flex-1 text-left">{{ t('admin.tlsFingerprintProfiles.title') }}</span>
                      </button>
                      <button class="dropdown-item-sm gap-3 rounded-control" @click="openTLSFingerprintRouters">
                        <span class="provider-tools-menu-icon bg-cyan-50 text-cyan-600 dark:bg-cyan-900/30 dark:text-cyan-300">
                          <Icon name="swap" size="sm" />
                        </span>
                        <span class="flex-1 text-left">{{ t('admin.tlsFingerprintRouters.title') }}</span>
                      </button>

                      <div class="my-2 border-t border-gray-100 dark:border-dark-600"></div>
                      <div class="px-2 py-2">
                        <div class="flex items-center justify-between gap-3">
                          <span class="text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-gray-500">
                            {{ t('admin.providers.viewColumns') }}
                          </span>
                          <Icon name="grid" size="sm" class="text-gray-400" />
                        </div>
                      </div>
                      <div class="grid grid-cols-1 gap-1">
                        <button
                          v-for="col in toggleableColumns"
                          :key="col.key"
                          @click="toggleColumn(col.key)"
                          class="dropdown-item-sm justify-between rounded-control"
                        >
                          <span class="truncate">{{ col.label }}</span>
                          <Icon
                            v-if="isColumnVisible(col.key)"
                            name="check"
                            size="sm"
                            class="text-primary-500"
                            :animate-on-hover="false"
                          />
                        </button>
                      </div>
                      </div>
                    </div>
                  </MotionTransition>
                </Teleport>
              </div>
            </template>
          </ProviderTableActions>
        </div>
        <div
          v-if="hasPendingListSync"
          class="mt-2 flex items-center justify-between rounded-control border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-700/40 dark:bg-amber-900/20 dark:text-amber-200"
        >
          <span>{{ t('admin.providers.listPendingSyncHint') }}</span>
          <button
            class="btn btn-secondary px-2 py-1 text-xs"
            @click="syncPendingListChanges"
          >
            {{ t('admin.providers.listPendingSyncAction') }}
          </button>
        </div>
        <ProviderBulkActionsBar
          class="mt-2"
          :selected-ids="selIds"
          :usage-loading="bulkUsageLoading"
          :upstream-usage-loading="upstreamUsageBulkLoading"
          :total-results="pagination.total"
          :selecting-all="selectingAllResults"
          :all-results-selected="allResultsSelected"
          @delete="handleBulkDelete"
          @reset-status="handleBulkResetStatus"
          @refresh-token="handleBulkRefreshToken"
          @query-usage="handleBulkQueryUsage"
          @query-upstream-usage="handleBulkQueryUpstreamUsage"
          @edit-selected="openBulkEditSelected"
          @clear="clearSelection"
          @select-page="selectPage"
          @select-all-results="handleSelectAllResults"
          @toggle-schedulable="handleBulkToggleSchedulable"
        />
      </template>
      <template #table>
        <div ref="providerTableRef" class="flex min-h-0 flex-1 flex-col overflow-hidden">
        <DataTable
          column-order-storage-key="admin-providers-column-order"
          ref="dataTableRef"
          :columns="cols"
          :data="providers"
          :loading="loading"
          row-key="id"
          :server-side-sort="true"
          @sort="handleSort"
          default-sort-key="name"
          default-sort-order="asc"
          :sort-storage-key="PROVIDER_SORT_STORAGE_KEY"
          :estimate-row-height="156"
          :overscan="5"
          :virtualize-threshold="50"
        >
          <template #header-select>
            <input
              type="checkbox"
              class="h-4 w-4 cursor-pointer rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="allVisibleSelected"
              @click.stop
              @change="toggleSelectAllVisible($event)"
            />
          </template>
          <template #cell-select="{ row }">
            <input
              type="checkbox"
              :checked="isSelected(row.id)"
              @change="toggleSel(row.id)"
              class="h-4 w-4 cursor-pointer rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </template>
          <template #cell-id="{ value }">
            <span class="font-mono text-xs text-gray-500 dark:text-gray-400">#{{ value }}</span>
          </template>
          <template #cell-name="{ row, value }">
            <div class="flex flex-col">
              <HelpTooltip
                v-if="providerHomepageUrl(row)"
                :content="providerHomepageUrl(row)"
                width-class="w-max max-w-sm break-all"
                class="-ml-1 self-start"
              >
                <template #trigger>
                  <a
                    :href="providerHomepageUrl(row)"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="border-b border-dotted border-gray-300 font-medium text-gray-900 dark:border-dark-500 dark:text-white"
                  >
                    {{ value }}
                  </a>
                </template>
              </HelpTooltip>
              <span v-else class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
              <span
                v-if="providerDisplayEmail(row)"
                class="text-xs text-gray-500 dark:text-gray-400 truncate max-w-[200px]"
                :title="providerDisplayEmail(row) + (row.parent_chatgpt_account_id ? ' · ' + row.parent_chatgpt_account_id : '')"
              >
                {{ providerDisplayEmail(row) }}
              </span>
            </div>
          </template>
          <template #cell-notes="{ value }">
            <span v-if="value" :title="value" class="block max-w-xs truncate text-sm text-gray-600 dark:text-gray-300">{{ value }}</span>
            <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
          </template>
          <template #cell-platform_type="{ row }">
            <div class="flex min-w-0 flex-col gap-1">
              <div class="flex flex-wrap items-center gap-1">
                <PlatformTypeBadge :platform="row.platform" :type="row.type"
                  :auth-mode="getOpenAIAuthMode(row)"
                  :plan-type="getProviderPlanType(row)"
                  :privacy-mode="row.extra?.privacy_mode || row.parent_privacy_mode"
                  :subscription-expires-at="row.credentials?.subscription_expires_at || row.parent_subscription_expires_at" />
                <span
                  v-if="getAntigravityTierLabel(row)"
                  :class="['inline-block rounded-compact px-1.5 py-0.5 text-xs font-medium', getAntigravityTierClass(row)]"
                >
                  {{ getAntigravityTierLabel(row) }}
                </span>
              </div>

            </div>
          </template>
          <template #cell-capacity="{ row }">
            <ProviderCapacityCell :provider="row" />
          </template>
          <template #cell-status="{ row }">
            <div class="flex items-center gap-1.5">
              <ProviderStatusIndicator :provider="row" @show-temp-unsched="handleShowTempUnsched" />
            </div>
          </template>
          <template #cell-schedulable="{ row }">
            <!-- 异步保存，值由 handleToggleSchedulable 写回列表。 -->
            <Toggle
              :model-value="row.schedulable"
              size="sm"
              :disabled="togglingSchedulable === row.id"
              off-tone="soft"
              :title="row.schedulable ? t('admin.providers.schedulableEnabled') : t('admin.providers.schedulableDisabled')"
              @update:model-value="handleToggleSchedulable(row)"
            />
          </template>
          <template #cell-today_stats="{ row }">
            <ProviderTodayStatsCell
              :stats="todayStatsByProviderId[String(row.id)] ?? null"
              :loading="todayStatsLoading"
              :error="todayStatsError"
            />
          </template>
          <template #cell-groups="{ row }">
            <ProviderGroupsCell :groups="row.groups" :max-display="4" />
          </template>
          <template #header-usage="{ column }">
            <div class="flex items-center">
              <span>{{ column.label }}</span>
              <HelpTooltip :content="t('admin.providers.usageWindowsHint')" width-class="w-72" />
            </div>
          </template>
          <template #cell-usage="{ row }">
            <ProviderUsageCell
              :provider="row"
              :today-stats="todayStatsByProviderId[String(row.id)] ?? null"
              :today-stats-loading="todayStatsLoading"
              :manual-refresh-token="usageManualRefreshToken"
              :batched-usage="usageBatchByProviderId[String(row.id)] ?? null"
              :batched-usage-error="usageBatchErrorByProviderId[String(row.id)] ?? null"
              :batched-usage-loading="usageBatchLoadingByProviderId[String(row.id)] === true"
              :request-batched-usage="isDesktopViewport ? queueBatchedUsage : null"
              :upstream-usage="upstreamUsageByProviderId[String(row.id)] ?? null"
              :upstream-usage-error="upstreamUsageErrorByProviderId[String(row.id)] ?? null"
              :upstream-usage-loading="upstreamUsageLoadingByProviderId[String(row.id)] === true"
              :request-upstream-usage="requestUpstreamUsage"
              @provider-updated="handleProviderUpdated"
              @usage-loaded="handleProviderUsageLoaded(row.id, $event)"
            />
          </template>
          <template #cell-proxy="{ row }">
            <div class="flex flex-col gap-1">
              <div v-if="row.proxy" class="flex items-center gap-2">
                <span class="text-sm text-gray-700 dark:text-gray-300">{{ row.proxy.name }}</span>
                <span v-if="row.proxy.country_code" class="text-xs text-gray-500 dark:text-gray-400">
                  ({{ row.proxy.country_code }})
                </span>
              </div>
              <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
              <div v-if="row.proxy && row.proxy.expires_at" class="flex items-center gap-2 text-xs">
                <span class="text-gray-600 dark:text-gray-300">{{ formatDateTime(row.proxy.expires_at) }}</span>
                <span :class="proxyExpiryBadge(row.proxy)">{{ proxyExpiryText(row.proxy) }}</span>
              </div>
              <div v-if="row.proxy_fallback_origin_id" class="flex items-center gap-1">
                <span class="inline-flex items-center px-1.5 py-0.5 rounded-compact text-xs font-medium bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200" :title="t('admin.providers.fallbackActiveTip', { origin: row.proxy_fallback_origin_name })">
                  {{ t('admin.providers.fallbackActive') }}
                </span>
                <button class="text-xs px-1.5 py-0.5 rounded-compact border border-gray-300 dark:border-dark-500 text-gray-600 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700" @click="onRevertFallback(row)">{{ t('admin.providers.revertProxy') }}</button>
              </div>
            </div>
          </template>
          <template #cell-rate_multiplier="{ row }">
            <span class="text-sm font-mono text-gray-700 dark:text-gray-300">
              {{ (row.rate_multiplier ?? 1).toFixed(2) }}x
            </span>
          </template>
          <template #cell-priority="{ value }">
            <span class="text-sm text-gray-700 dark:text-gray-300">{{ value }}</span>
          </template>
          <template #header-scheduler_score="{ column }">
            <div class="flex items-center">
              <span>{{ column.label }}</span>
              <HelpTooltip :content="t('admin.providers.schedulerScore.hint')" width-class="w-80" />
            </div>
          </template>
          <template #cell-scheduler_score="{ row }">
            <div v-if="getSchedulerScoreRows(row).length" class="flex min-w-[7rem] flex-col gap-0.5 font-mono text-xs leading-4">
              <div
                v-for="score in getSchedulerScoreRows(row)"
                :key="String(score.group_id)"
                class="flex items-center gap-1 whitespace-nowrap text-gray-700 dark:text-gray-300"
                :title="`${formatSchedulerScoreGroup(score)} / ${formatSchedulerScore(score.base_score)} / ${formatStickySchedulerScore(score)}`"
              >
                <span class="max-w-[4.75rem] truncate text-gray-500 dark:text-dark-400">{{ formatSchedulerScoreGroup(score) }}</span>
                <span class="text-gray-300 dark:text-gray-600">/</span>
                <span>{{ formatSchedulerScore(score.base_score) }}</span>
                <span class="text-gray-300 dark:text-gray-600">/</span>
                <span class="text-primary-700 dark:text-primary-300">{{ formatStickySchedulerScore(score) }}</span>
              </div>
            </div>
            <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
          </template>
          <template #cell-last_used_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatRelativeTime(value) }}</span>
          </template>
          <template #cell-created_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(value) }}</span>
          </template>
          <template #cell-expires_at="{ row, value }">
            <div class="flex flex-col items-start gap-1">
              <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatExpiresAt(value) }}</span>
              <div v-if="isExpired(value) || (row.auto_pause_on_expired && value)" class="flex items-center gap-1">
                <span
                  v-if="isExpired(value)"
                  class="inline-flex items-center rounded-compact bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
                >
                  {{ t('admin.providers.expired') }}
                </span>
                <span
                  v-if="row.auto_pause_on_expired && value"
                  class="inline-flex items-center rounded-compact bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
                >
                  {{ t('admin.providers.autoPauseOnExpired') }}
                </span>
              </div>
            </div>
          </template>
          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <button @click="handleEdit(row)" class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400">
                <Icon name="edit" size="sm" class="h-4 w-4" />
                <span class="text-xs">{{ t('common.edit') }}</span>
              </button>
              <button @click="openMenu(row, $event)" class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-700 dark:hover:text-white">
                <Icon name="more" size="sm" class="h-4 w-4" />
                <span class="text-xs">{{ t('common.more') }}</span>
              </button>
            </div>
          </template>
        </DataTable>
        </div>
      </template>
      <template #pagination><Pagination v-if="pagination.total > 0" :page="pagination.page" :total="pagination.total" :page-size="pagination.page_size" @update:page="handlePageChange" @update:pageSize="handlePageSizeChange" /></template>
    </TablePageLayout>
    <CreateProviderModal
      :show="showCreate"
      :initial-platform="createInitialPlatform"
      :proxies="proxies"
      :groups="groups"
      @close="showCreate = false"
      @created="reload"
    />
    <EditProviderModal :show="showEdit" :provider="edAcc" :proxies="proxies" :groups="groups" @close="showEdit = false" @updated="handleProviderUpdated" />
    <ReAuthProviderModal :show="showReAuth" :provider="reAuthAcc" @close="closeReAuthModal" @reauthorized="handleProviderUpdated" />
    <ProviderTestModal :show="showTest" :provider="testingAcc" @close="closeTestModal" />
    <ProviderStatsModal :show="showStats" :provider="statsAcc" @close="closeStatsModal" />
    <AdvancedSchedulerScoreModal :show="showAdvancedSchedulerScore" :provider="advancedSchedulerScoreAcc" @close="closeAdvancedSchedulerScoreModal" />
    <CodexInviteResetModal :show="showInviteReset" :provider="inviteResetAcc" @close="closeInviteResetModal" @updated="enterAutoRefreshSilentWindow" />
    <ScheduledTestsPanel :show="showSchedulePanel" :provider-id="scheduleAcc?.id ?? null" :model-options="scheduleModelOptions" @close="closeSchedulePanel" />
    <ProviderActionMenu :show="menu.show" :provider="menu.acc" :position="menu.pos" @close="menu.show = false" @test="handleTest" @stats="handleViewStats" @advanced-scheduler-score="handleAdvancedSchedulerScore" @schedule="handleSchedule" @duplicate="handleDuplicateProvider" @reauth="handleReAuth" @refresh-token="handleRefresh" @recover-state="handleRecoverState" @reset-quota="handleResetQuota" @set-privacy="handleSetPrivacy" @invite-reset="handleInviteReset" @create-spark-shadow="handleCreateSparkShadow" @delete="handleDelete" />
    <SyncFromCrsModal :show="showSync" @close="showSync = false" @synced="handleExternalProvidersChanged" />
    <ImportDataModal :show="showImportData" @close="showImportData = false" @imported="handleDataImported" />
    <BulkEditProviderModal
      :show="showBulkEdit"
      :provider-ids="selIds"
      :selected-platforms="selPlatforms"
      :selected-types="selTypes"
      :target="bulkEditTarget ?? undefined"
      :proxies="proxies"
      :groups="groups"
      @close="showBulkEdit = false"
      @updated="handleBulkUpdated"
    />
    <TempUnschedStatusModal :show="showTempUnsched" :provider="tempUnschedAcc" @close="showTempUnsched = false" @reset="handleTempUnschedReset" />
    <ConfirmDialog :show="showDeleteDialog" :title="t('admin.providers.deleteProvider')" :message="t('admin.providers.deleteConfirm', { name: deletingAcc?.name })" :confirm-text="t('common.delete')" :cancel-text="t('common.cancel')" :danger="true" @confirm="confirmDelete" @cancel="showDeleteDialog = false" />
    <ConfirmDialog :show="showCreateShadowDialog" :title="t('admin.providers.createSparkShadow')" :message="t('admin.providers.createSparkShadowConfirm', { name: creatingShadowAcc?.name })" @confirm="confirmCreateSparkShadow" @cancel="showCreateShadowDialog = false" />
    <ConfirmDialog :show="showExportDataDialog" :title="t('admin.providers.dataExport')" :message="t('admin.providers.dataExportConfirmMessage')" :confirm-text="t('admin.providers.dataExportConfirm')" :cancel-text="t('common.cancel')" @confirm="handleExportData" @cancel="showExportDataDialog = false">
      <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
        <input type="checkbox" class="h-4 w-4 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500" v-model="includeProxyOnExport" />
        <span>{{ t('admin.providers.dataExportIncludeProxies') }}</span>
      </label>
    </ConfirmDialog>
    <ErrorPassthroughRulesModal :show="showErrorPassthrough" @close="showErrorPassthrough = false" />
    <TLSFingerprintProfilesModal :show="showTLSFingerprintProfiles" @close="showTLSFingerprintProfiles = false" />
    <TLSFingerprintRoutersModal :show="showTLSFingerprintRouters" @close="showTLSFingerprintRouters = false" />
    <TotpStepUpDialog :controller="providerExportStepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { ref, reactive, computed, nextTick, onMounted, onUnmounted, toRaw, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { adminAPI } from '@/api/admin'
import { isUpstreamUsageQueryEnabled, supportsUpstreamUsageQuery } from '@/utils/upstreamUsage'
import { useTableLoader } from '@/composables/useTableLoader'
import { useSwipeSelect, type SwipeSelectVirtualContext } from '@/composables/useSwipeSelect'
import { useTableSelection } from '@/composables/useTableSelection'
import { useStepUp, isStepUpBlocked, isStepUpCancelled, stepUpBlockReason } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Toggle from '@/components/common/Toggle.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { CreateProviderModal, EditProviderModal, BulkEditProviderModal, SyncFromCrsModal, TempUnschedStatusModal } from '@/components/provider'
import ProviderTableActions from '@/components/admin/provider/ProviderTableActions.vue'
import ProviderTableFilters from '@/components/admin/provider/ProviderTableFilters.vue'
import ProviderBulkActionsBar from '@/components/admin/provider/ProviderBulkActionsBar.vue'
import ProviderActionMenu from '@/components/admin/provider/ProviderActionMenu.vue'
import ImportDataModal from '@/components/admin/provider/ImportDataModal.vue'
import ReAuthProviderModal from '@/components/admin/provider/ReAuthProviderModal.vue'
import ProviderTestModal from '@/components/admin/provider/ProviderTestModal.vue'
import ProviderStatsModal from '@/components/admin/provider/ProviderStatsModal.vue'
import AdvancedSchedulerScoreModal from '@/components/admin/provider/AdvancedSchedulerScoreModal.vue'
import CodexInviteResetModal from '@/components/admin/provider/CodexInviteResetModal.vue'
import ScheduledTestsPanel from '@/components/admin/provider/ScheduledTestsPanel.vue'
import type { SelectOption } from '@/components/common/Select.vue'
import ProviderStatusIndicator from '@/components/provider/ProviderStatusIndicator.vue'
import ProviderUsageCell from '@/components/provider/ProviderUsageCell.vue'
import ProviderTodayStatsCell from '@/components/provider/ProviderTodayStatsCell.vue'
import ProviderGroupsCell from '@/components/provider/ProviderGroupsCell.vue'
import ProviderCapacityCell from '@/components/provider/ProviderCapacityCell.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import ErrorPassthroughRulesModal from '@/components/admin/ErrorPassthroughRulesModal.vue'
import TLSFingerprintProfilesModal from '@/components/admin/TLSFingerprintProfilesModal.vue'
import TLSFingerprintRoutersModal from '@/components/admin/TLSFingerprintRoutersModal.vue'
import { fetchAllProviderIds } from '@/utils/providerSelection'
import { buildGrokUsageRefreshKey, buildOpenAIUsageRefreshKey } from '@/utils/providerUsageRefresh'
import { enqueueUsageRequest } from '@/utils/usageLoadQueue'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { proxyExpiryBadgeClass, proxyExpiryLabelKey } from '@/utils/proxyExpiry'
import { sanitizeUrl } from '@/utils/url'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { MIN_COMFORTABLE_PANEL_HEIGHT } from '@/constants/overlay'
import { TABLE_DESKTOP_MEDIA_QUERY } from '@/constants/layout'
import type {
  Provider,
  ProviderPlatform,
  ProviderSchedulerGroupScore,
  ProviderType,
  ProviderUsageInfo,
  UpstreamUsageQueryError,
  UpstreamUsageQueryResult,
  Proxy as ProviderProxy,
  AdminGroup,
  WindowStats,
  ClaudeModel
} from '@/types'

// ProvidersView.vue loads the administrator provider list and coordinates its
// editing, selection, testing, and usage-query actions. The table combines
// persisted provider settings with local statistics and separately fetched
// upstream observations. Provider updates invalidate cached observations so
// changed credentials and query settings cannot reuse earlier display data.
//
// Upstream queries run on administrator actions and retain successful results
// in session storage under the current administrator identity. Cache identities
// include the selected adapter, query address, forwarding address, and provider
// revision. Loading and restoring the table use those identities to decide
// whether a stored observation still belongs to the displayed provider.

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const proxies = ref<ProviderProxy[]>([])
const groups = ref<AdminGroup[]>([])
const providerTableRef = ref<HTMLElement | null>(null)
const dataTableRef = ref<InstanceType<typeof DataTable> | null>(null)
type ProviderBulkEditTarget =
  | {
      mode: 'selected'
      providerIds: number[]
      selectedPlatforms: ProviderPlatform[]
      selectedTypes: ProviderType[]
    }
  | {
      mode: 'filtered'
      filters: {
        platform?: string
        type?: string
        status?: string
        group?: string
        search?: string
        privacy_mode?: string
        sort_by?: string
        sort_order?: ProviderSortOrder
      }
      previewCount: number
      selectedPlatforms: ProviderPlatform[]
      selectedTypes: ProviderType[]
    }
type ProviderBulkEditFilterSnapshot = Extract<ProviderBulkEditTarget, { mode: 'filtered' }>['filters']
const BULK_EDIT_FILTER_METADATA_PAGE_SIZE = 500
const selPlatforms = computed<ProviderPlatform[]>(() => {
  const platforms = new Set(
    providers.value
      .filter(a => isSelected(a.id))
      .map(a => a.platform)
  )
  return [...platforms]
})
const selTypes = computed<ProviderType[]>(() => {
  const types = new Set(
    providers.value
      .filter(a => isSelected(a.id))
      .map(a => a.type)
  )
  return [...types]
})
const showCreate = ref(false)
const createInitialPlatform = ref<ProviderPlatform | undefined>(undefined)
const showEdit = ref(false)
const showSync = ref(false)
const showImportData = ref(false)
const showExportDataDialog = ref(false)
const includeProxyOnExport = ref(true)
const showBulkEdit = ref(false)
const bulkEditTarget = ref<ProviderBulkEditTarget | null>(null)
const showTempUnsched = ref(false)
const showDeleteDialog = ref(false)
const showCreateShadowDialog = ref(false)
const showReAuth = ref(false)
const showTest = ref(false)
const showStats = ref(false)
const showAdvancedSchedulerScore = ref(false)
const showInviteReset = ref(false)
const showErrorPassthrough = ref(false)
const showTLSFingerprintProfiles = ref(false)
const showTLSFingerprintRouters = ref(false)
const edAcc = ref<Provider | null>(null)
const tempUnschedAcc = ref<Provider | null>(null)
const deletingAcc = ref<Provider | null>(null)
const creatingShadowAcc = ref<Provider | null>(null)
const reAuthAcc = ref<Provider | null>(null)
const testingAcc = ref<Provider | null>(null)
const statsAcc = ref<Provider | null>(null)
const advancedSchedulerScoreAcc = ref<Provider | null>(null)
const inviteResetAcc = ref<Provider | null>(null)
const showSchedulePanel = ref(false)
const scheduleAcc = ref<Provider | null>(null)
const scheduleModelOptions = ref<SelectOption[]>([])
const togglingSchedulable = ref<number | null>(null)
const bulkUsageLoading = ref(false)
const menu = reactive<{show:boolean, acc:Provider|null, pos:{top:number, left:number}|null}>({ show: false, acc: null, pos: null })
const exportingData = ref(false)
const openCreateProvider = (platform?: ProviderPlatform) => {
  createInitialPlatform.value = platform
  showCreate.value = true
}

// 提供商工具下拉菜单
const showProviderToolsDropdown = ref(false)
const providerToolsDropdownRef = ref<HTMLElement | null>(null)
const providerToolsButtonRef = ref<HTMLElement | null>(null)
const providerToolsDropdownPosition = reactive({
  top: null as number | null,
  bottom: null as number | null,
  left: 16,
  width: 320,
  maxHeight: 0
})
const providerToolsDropdownStyle = computed(() => ({
  top: providerToolsDropdownPosition.top == null ? 'auto' : `${providerToolsDropdownPosition.top}px`,
  bottom: providerToolsDropdownPosition.bottom == null ? 'auto' : `${providerToolsDropdownPosition.bottom}px`,
  left: `${providerToolsDropdownPosition.left}px`,
  width: `${providerToolsDropdownPosition.width}px`
}))
const hiddenColumns = reactive<Set<string>>(new Set())
const DEFAULT_HIDDEN_COLUMNS = ['today_stats', 'proxy', 'notes', 'scheduler_score', 'rate_multiplier']
const HIDDEN_COLUMNS_KEY = 'provider-hidden-columns'
// 迁移列配置时默认隐藏调度权值，该列展示会触发后端打分。
const HIDDEN_COLUMNS_VERSION_KEY = 'provider-hidden-columns-version'
const HIDDEN_COLUMNS_CURRENT_VERSION = 'scheduler-score-hidden-by-default'

// Sorting settings
const PROVIDER_SORT_STORAGE_KEY = 'provider-table-sort'
type ProviderSortOrder = 'asc' | 'desc'
type ProviderSortState = {
  sort_by: string
  sort_order: ProviderSortOrder
}
const PROVIDER_SORTABLE_KEYS = new Set([
  'id',
  'name',
  'status',
  'schedulable',
  'priority',
  'rate_multiplier',
  'last_used_at',
  'created_at',
  'expires_at'
])
const loadInitialProviderSortState = (): ProviderSortState => {
  const fallback: ProviderSortState = { sort_by: 'name', sort_order: 'asc' }
  try {
    const raw = localStorage.getItem(PROVIDER_SORT_STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as { key?: string; order?: string }
    const key = typeof parsed.key === 'string' ? parsed.key : ''
    if (!PROVIDER_SORTABLE_KEYS.has(key)) return fallback
    return {
      sort_by: key,
      sort_order: parsed.order === 'desc' ? 'desc' : 'asc'
    }
  } catch {
    return fallback
  }
}
const sortState = reactive<ProviderSortState>(loadInitialProviderSortState())

// Auto refresh settings
const showAutoRefreshDropdown = ref(false)
const autoRefreshDropdownRef = ref<HTMLElement | null>(null)
const autoRefreshButtonRef = ref<HTMLElement | null>(null)
const autoRefreshDropdownStyle = ref<Record<string, string>>({})
const AUTO_REFRESH_STORAGE_KEY = 'provider-auto-refresh'
const autoRefreshIntervals = [5, 10, 15, 30] as const
const autoRefreshEnabled = ref(false)
const autoRefreshIntervalSeconds = ref<(typeof autoRefreshIntervals)[number]>(30)
const autoRefreshCountdown = ref(0)
const autoRefreshETag = ref<string | null>(null)
const autoRefreshFetching = ref(false)
const AUTO_REFRESH_SILENT_WINDOW_MS = 15000
const autoRefreshSilentUntil = ref(0)
const autoRefreshButtonTitle = computed(() => {
  if (!autoRefreshEnabled.value) return t('admin.providers.autoRefresh')
  return t('admin.providers.autoRefreshCountdown', { seconds: autoRefreshCountdown.value })
})
const hasPendingListSync = ref(false)
const todayStatsByProviderId = ref<Record<string, WindowStats>>({})
const todayStatsLoading = ref(false)
const todayStatsError = ref<string | null>(null)
const todayStatsReqSeq = ref(0)
const pendingTodayStatsRefresh = ref(false)
const usageManualRefreshToken = ref(0)

const isDesktopViewport = ref(
  typeof window === 'undefined' ? true : window.matchMedia(TABLE_DESKTOP_MEDIA_QUERY).matches
)
let desktopViewportMediaQuery: MediaQueryList | null = null
let desktopViewportListener: ((event: MediaQueryListEvent) => void) | null = null

const usageBatchByProviderId = ref<Record<string, ProviderUsageInfo | null>>({})
const usageBatchErrorByProviderId = ref<Record<string, string | null>>({})
const usageBatchLoadingByProviderId = ref<Record<string, boolean>>({})
const usageBatchRequestTokenByProviderId = ref<Record<string, number>>({})
const usageBatchCache = new Map<number, { data: ProviderUsageInfo; ts: number }>()
const USAGE_BATCH_CACHE_TTL = 5 * 60 * 1000
const pendingUsageBatchIds = new Set<number>()
let usageBatchFlushTimer: ReturnType<typeof setTimeout> | null = null
let queuedUsageBatchForce = false
let usageBatchRequestToken = 0

const upstreamUsageByProviderId = ref<Record<string, UpstreamUsageQueryResult | null>>({})
const upstreamUsageErrorByProviderId = ref<Record<string, UpstreamUsageQueryError | null>>({})
const upstreamUsageLoadingByProviderId = ref<Record<string, boolean>>({})
const upstreamUsageRequestTokenByProviderId = ref<Record<string, number>>({})
const upstreamUsageBulkLoading = ref(false)
const UPSTREAM_USAGE_CACHE_TTL = 5 * 60 * 1000
const UPSTREAM_USAGE_BATCH_SIZE = 100
const UPSTREAM_USAGE_BATCH_CONCURRENCY = 4
let upstreamUsageRequestToken = 0
let hydratedUpstreamUsageAdminID: number | null | undefined
const UPSTREAM_USAGE_CACHE_PREFIX = 'tokenrouter:admin:upstream-usage:v1:'

const buildDefaultTodayStats = (): WindowStats => ({
  requests: 0,
  tokens: 0,
  cost: 0,
  standard_cost: 0,
  user_cost: 0
})

const providerSupportsBatchUsage = (provider: Provider) => {
  if (provider.platform === 'anthropic') {
    return provider.type === 'oauth' || provider.type === 'setup-token'
  }
  if (provider.platform === 'gemini') {
    const credentials = provider.credentials as Record<string, unknown> | undefined
    return !(provider.type === 'apikey' && credentials?.provider_type === 'third_party')
  }
  if (provider.platform === 'antigravity') return provider.type === 'oauth'
  if (provider.platform === 'openai') return provider.type === 'oauth'
  if (provider.platform === 'grok') return provider.type === 'oauth'
  return false
}

// effectiveUpstreamUsageAdapter resolves the format used for cache identity.
// CN platform and mode select their fixed formats; manual relay choices retain
// registered adapter names and otherwise use the Sub2API default.
const effectiveUpstreamUsageAdapter = (provider: Provider) => {
  if (provider.platform === 'kimi') {
    return provider.credentials?.provider_mode === 'coding' ? 'kimi_coding' : 'kimi_balance'
  }
  if (provider.platform === 'zhipu') {
    return provider.credentials?.provider_mode === 'coding' ? 'zhipu_coding' : ''
  }
  if (provider.platform === 'deepseek') return 'deepseek_balance'
  const rawConfig = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
  return rawConfig?.adapter === 'new_api' || rawConfig?.adapter === 'zivv' || rawConfig?.adapter === 'derouter'
    ? rawConfig.adapter
    : 'sub2api'
}

// 缓存键需要区分不同 Base URL，但不应把可能包含内部路径信息的原文写进浏览器存储。
const upstreamUsageCacheIdentity = (value: string) => {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(16)
}

const upstreamUsageCacheKey = (provider: Provider) => {
  const adminID = typeof authStore.user?.id === 'number' && Number.isSafeInteger(authStore.user.id) && authStore.user.id > 0
    ? String(authStore.user.id)
    : null
  if (!adminID) return ''
  const rawConfig = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
  const config = {
    enabled: rawConfig?.enabled !== false,
    adapter: effectiveUpstreamUsageAdapter(provider) ||
      (typeof rawConfig?.adapter === 'string' && rawConfig.adapter.trim()
        ? rawConfig.adapter.trim()
        : 'sub2api'),
    base_url: upstreamUsageCacheIdentity(typeof rawConfig?.base_url === 'string' ? rawConfig.base_url.trim() : '')
  }
  const providerBaseURL = typeof provider.credentials?.base_url === 'string'
    ? provider.credentials.base_url.trim()
    : ''
  const proxyUpdatedAt = provider.proxy?.updated_at ?? ''
  return `${UPSTREAM_USAGE_CACHE_PREFIX}${adminID}:${provider.id}:${provider.platform}:${provider.type}:${provider.updated_at}:${provider.proxy_id ?? ''}:${proxyUpdatedAt}:${upstreamUsageCacheIdentity(providerBaseURL)}:${JSON.stringify(config)}`
}

// sessionStorage 可被浏览器扩展或旧版本页面写入任意 JSON；恢复前只接受
// 检查适配器返回的数值、时间和模式，过滤损坏的快照。
const isFiniteOptionalNumber = (value: unknown, nonNegative = false) =>
  value == null || (typeof value === 'number' && Number.isFinite(value) && (!nonNegative || value >= 0))

const isValidUpstreamUsageAmount = (value: unknown) => {
  if (!value || typeof value !== 'object') return false
  const amount = value as Record<string, unknown>
  return isFiniteOptionalNumber(amount.used, true) &&
    isFiniteOptionalNumber(amount.total, true) &&
    isFiniteOptionalNumber(amount.remaining) &&
    [amount.used, amount.total, amount.remaining].some(item => typeof item === 'number')
}

const isValidUpstreamUsageLimit = (value: unknown) => {
  if (!value || typeof value !== 'object') return false
  const limit = value as Record<string, unknown>
  const resetAt = limit.reset_at
  return typeof limit.name === 'string' && limit.name.trim() !== '' &&
    isFiniteOptionalNumber(limit.used, true) && isFiniteOptionalNumber(limit.limit, true) &&
    isFiniteOptionalNumber(limit.remaining) &&
    [limit.used, limit.limit, limit.remaining].some(item => typeof item === 'number') &&
    (resetAt == null || (typeof resetAt === 'string' && Number.isFinite(Date.parse(resetAt))))
}

const isValidUpstreamUsageInfo = (value: unknown) => {
  if (!value || typeof value !== 'object') return false
  const usage = value as Record<string, unknown>
  const unit = usage.unit
  const mode = usage.mode
  const limits = usage.limits
  const subscription = usage.subscription
  if (typeof usage.provider !== 'string' || usage.provider.trim() === '' || typeof mode !== 'string' ||
    !['balance', 'quota', 'limits', 'subscription'].includes(mode) ||
    (unit != null && !['USD', 'CNY', 'TOKENS', 'PERCENT'].includes(String(unit))) ||
    (usage.expires_at != null && (typeof usage.expires_at !== 'string' || !Number.isFinite(Date.parse(usage.expires_at)))) ||
    (usage.balance != null && !isValidUpstreamUsageAmount(usage.balance)) ||
    (usage.balances != null && (!Array.isArray(usage.balances) || !usage.balances.every(item => {
      if (!item || typeof item !== 'object') return false
      const balance = item as Record<string, unknown>
      return typeof balance.currency === 'string' && balance.currency.trim() !== '' &&
        typeof balance.remaining === 'number' && Number.isFinite(balance.remaining)
    }))) ||
    (limits != null && (!Array.isArray(limits) || !limits.every(isValidUpstreamUsageLimit)))) return false
  if (subscription != null) {
    if (typeof subscription !== 'object') return false
    const item = subscription as Record<string, unknown>
    if (typeof item.plan_name !== 'string' || item.plan_name.trim() === '' ||
      (item.unlimited != null && typeof item.unlimited !== 'boolean') ||
      !isFiniteOptionalNumber(item.remaining) ||
      (item.expires_at != null && (typeof item.expires_at !== 'string' || !Number.isFinite(Date.parse(item.expires_at)))) ||
      (item.limits != null && (!Array.isArray(item.limits) || !item.limits.every(isValidUpstreamUsageLimit)))) return false
    if (item.unlimited === true && (typeof item.remaining === 'number' ||
      (Array.isArray(item.limits) && item.limits.length > 0))) return false
    if (item.unlimited !== true && typeof item.remaining !== 'number' &&
      (!Array.isArray(item.limits) || item.limits.length === 0)) return false
  }
  if ((mode === 'balance' || mode === 'quota') && !isValidUpstreamUsageAmount(usage.balance)) return false
  if (mode === 'limits' &&
    (!Array.isArray(limits) || limits.length === 0) &&
    (!subscription || typeof subscription !== 'object' ||
      !Array.isArray((subscription as Record<string, unknown>).limits) ||
      ((subscription as Record<string, unknown>).limits as unknown[]).length === 0)) return false
  if (mode === 'subscription' && (!subscription || typeof subscription !== 'object')) return false
  return true
}

const isValidUpstreamUsageResult = (provider: Provider, data: unknown): data is UpstreamUsageQueryResult => {
  if (!data || typeof data !== 'object') return false
  const result = data as UpstreamUsageQueryResult
  const rawConfig = provider.extra?.upstream_usage_query as Record<string, unknown> | undefined
  const expectedAdapter = effectiveUpstreamUsageAdapter(provider)
  if (result.provider_id !== provider.id ||
    rawConfig?.enabled === false ||
    result.adapter !== expectedAdapter ||
    typeof result.observed_at !== 'string' || !Number.isFinite(Date.parse(result.observed_at))) return false
  return result.usage
    ? isValidUpstreamUsageInfo(result.usage)
    : isValidUpstreamUsageInfo({
        provider: result.provider,
        mode: result.mode,
        unit: result.unit,
        balance: result.balance,
        balances: result.balances,
        available: result.available,
        limits: result.limits,
        subscription: result.subscription,
        expires_at: result.expires_at
      })
}

const readUpstreamUsageCache = (provider: Provider): UpstreamUsageQueryResult | null => {
  const key = upstreamUsageCacheKey(provider)
  if (!key) return null
  try {
    const raw = sessionStorage.getItem(key)
    if (!raw) return null
    const parsed = JSON.parse(raw) as { data?: UpstreamUsageQueryResult; ts?: number }
    const now = Date.now()
    if (
      !isValidUpstreamUsageResult(provider, parsed.data) ||
      typeof parsed.ts !== 'number' ||
      !Number.isFinite(parsed.ts) ||
      parsed.ts > now + 60 * 1000 ||
      now - parsed.ts >= UPSTREAM_USAGE_CACHE_TTL
    ) {
      sessionStorage.removeItem(key)
      return null
    }
    return parsed.data
  } catch {
    try {
      sessionStorage.removeItem(key)
    } catch {
      // 浏览器存储不可用时无法继续清理。
    }
    return null
  }
}

const invalidateUpstreamUsageCache = (providerID: number) => {
  const key = String(providerID)
  upstreamUsageRequestTokenByProviderId.value = {
    ...upstreamUsageRequestTokenByProviderId.value,
    [key]: ++upstreamUsageRequestToken
  }
  setUpstreamUsageState(providerID, null, null, false)
  try {
    const adminID = typeof authStore.user?.id === 'number' && Number.isSafeInteger(authStore.user.id) && authStore.user.id > 0
      ? String(authStore.user.id)
      : null
    if (!adminID) return
    const prefix = `${UPSTREAM_USAGE_CACHE_PREFIX}${adminID}:${providerID}:`
    for (let index = sessionStorage.length - 1; index >= 0; index -= 1) {
      const storageKey = sessionStorage.key(index)
      if (storageKey?.startsWith(prefix)) sessionStorage.removeItem(storageKey)
    }
  } catch {
    // 浏览器存储不可用时，内存状态已经完成失效。
  }
}

const invalidateAllUpstreamUsageCache = () => {
  upstreamUsageRequestToken++
  upstreamUsageByProviderId.value = {}
  upstreamUsageErrorByProviderId.value = {}
  upstreamUsageLoadingByProviderId.value = {}
  upstreamUsageRequestTokenByProviderId.value = {}
  try {
    const adminID = typeof authStore.user?.id === 'number' && Number.isSafeInteger(authStore.user.id) && authStore.user.id > 0
      ? String(authStore.user.id)
      : null
    if (!adminID) return
    const prefix = `${UPSTREAM_USAGE_CACHE_PREFIX}${adminID}:`
    for (let index = sessionStorage.length - 1; index >= 0; index -= 1) {
      const storageKey = sessionStorage.key(index)
      if (storageKey?.startsWith(prefix)) sessionStorage.removeItem(storageKey)
    }
  } catch {
    // 浏览器存储不可用时，内存状态已经完成失效。
  }
}

const writeUpstreamUsageCache = (provider: Provider, data: UpstreamUsageQueryResult) => {
  try {
    if (!isValidUpstreamUsageResult(provider, data)) return
    const key = upstreamUsageCacheKey(provider)
    if (!key) return
    sessionStorage.setItem(key, JSON.stringify({ data, ts: Date.now() }))
  } catch {
    // 浏览器存储不可用时仍保留当前页面结果。
  }
}

const normalizeUpstreamUsageError = (error: unknown): UpstreamUsageQueryError => {
  const value = (error && typeof error === 'object') ? error as Record<string, unknown> : {}
  return {
    code: typeof value.reason === 'string' ? value.reason : typeof value.code === 'string' ? value.code : undefined,
    message: typeof value.message === 'string' ? value.message : undefined,
    status: typeof value.status === 'number' ? value.status : undefined
  }
}

const setUpstreamUsageState = (
  providerID: number,
  result: UpstreamUsageQueryResult | null,
  error: UpstreamUsageQueryError | null,
  loadingState: boolean
) => {
  const key = String(providerID)
  upstreamUsageByProviderId.value = { ...upstreamUsageByProviderId.value, [key]: result }
  upstreamUsageErrorByProviderId.value = { ...upstreamUsageErrorByProviderId.value, [key]: error }
  upstreamUsageLoadingByProviderId.value = { ...upstreamUsageLoadingByProviderId.value, [key]: loadingState }
}

const hydrateUpstreamUsageCache = () => {
  const adminID = authStore.user?.id ?? null
  if (hydratedUpstreamUsageAdminID !== adminID) {
    upstreamUsageByProviderId.value = {}
    upstreamUsageErrorByProviderId.value = {}
    upstreamUsageLoadingByProviderId.value = {}
    upstreamUsageRequestTokenByProviderId.value = {}
    upstreamUsageRequestToken++
    hydratedUpstreamUsageAdminID = adminID
  }
  for (const provider of providers.value) {
    if (!isUpstreamUsageQueryEnabled(provider)) continue
    const cached = readUpstreamUsageCache(provider)
    if (cached) {
      setUpstreamUsageState(provider.id, cached, null, false)
    } else if (!upstreamUsageLoadingByProviderId.value[String(provider.id)]) {
      setUpstreamUsageState(provider.id, null, null, false)
    }
  }
}

const requestUpstreamUsage = async (provider: Provider, options?: { force?: boolean }) => {
  if (!isUpstreamUsageQueryEnabled(provider)) return
  const key = String(provider.id)
  const force = options?.force === true
  if (!force) {
    const cached = readUpstreamUsageCache(provider)
    if (cached) {
      setUpstreamUsageState(provider.id, cached, null, false)
      return
    }
  }
  const requestToken = ++upstreamUsageRequestToken
  upstreamUsageRequestTokenByProviderId.value = {
    ...upstreamUsageRequestTokenByProviderId.value,
    [key]: requestToken
  }
  setUpstreamUsageState(provider.id, null, null, true)
  try {
    const result = await adminAPI.providers.queryUpstreamUsage(provider.id)
    if (upstreamUsageRequestTokenByProviderId.value[key] !== requestToken) return
    if (!isValidUpstreamUsageResult(provider, result)) {
      setUpstreamUsageState(provider.id, null, { code: 'UPSTREAM_USAGE_INVALID_RESPONSE' }, false)
      return
    }
    writeUpstreamUsageCache(provider, result)
    setUpstreamUsageState(provider.id, result, null, false)
  } catch (error) {
    if (upstreamUsageRequestTokenByProviderId.value[key] !== requestToken) return
    setUpstreamUsageState(provider.id, null, normalizeUpstreamUsageError(error), false)
  }
}

const queryUpstreamUsageChunk = async (selectedProviders: Provider[]) => {
  const ids = selectedProviders.map(provider => provider.id)
  if (ids.length === 0) return { success: 0, failed: 0 }
  const tokens = new Map(selectedProviders.map(provider => [provider.id, upstreamUsageRequestTokenByProviderId.value[String(provider.id)] ?? 0]))
  let success = 0
  let failed = 0
  let result: Awaited<ReturnType<typeof adminAPI.providers.queryBatchUpstreamUsage>>
  try {
    result = await adminAPI.providers.queryBatchUpstreamUsage(ids)
  } catch (error) {
    for (const provider of selectedProviders) {
      const key = String(provider.id)
      if (upstreamUsageRequestTokenByProviderId.value[key] !== tokens.get(provider.id)) continue
      setUpstreamUsageState(provider.id, null, normalizeUpstreamUsageError(error), false)
      failed++
    }
    return { success, failed }
  }
  for (const provider of selectedProviders) {
    const key = String(provider.id)
    if (upstreamUsageRequestTokenByProviderId.value[key] !== tokens.get(provider.id)) continue
    const data = result.usage?.[key]
    const queryError = result.errors?.[key]
    if (data) {
      if (!isValidUpstreamUsageResult(provider, data)) {
        setUpstreamUsageState(provider.id, null, { code: 'UPSTREAM_USAGE_INVALID_RESPONSE' }, false)
        failed++
        continue
      }
      writeUpstreamUsageCache(provider, data)
      setUpstreamUsageState(provider.id, data, null, false)
      success++
    } else {
      setUpstreamUsageState(provider.id, null, queryError ?? { code: 'UPSTREAM_USAGE_REQUEST_FAILED' }, false)
      failed++
    }
  }
  return { success, failed }
}

const queryUpstreamUsageChunks = async (selectedProviders: Provider[]) => {
  const chunks: Provider[][] = []
  for (let index = 0; index < selectedProviders.length; index += UPSTREAM_USAGE_BATCH_SIZE) {
    chunks.push(selectedProviders.slice(index, index + UPSTREAM_USAGE_BATCH_SIZE))
  }
  let nextChunk = 0
  let success = 0
  let failed = 0
  // 每个后端批量请求最多携带 100 个 ID，同时最多发出 4 个请求。
  // 后端仍以全局并发槽位限制真正访问上游的提供商数。
  const worker = async () => {
    while (nextChunk < chunks.length) {
      const chunkIndex = nextChunk
      nextChunk += 1
      const chunkResult = await queryUpstreamUsageChunk(chunks[chunkIndex])
      success += chunkResult.success
      failed += chunkResult.failed
    }
  }
  const workers = Array.from(
    { length: Math.min(UPSTREAM_USAGE_BATCH_CONCURRENCY, chunks.length) },
    () => worker()
  )
  await Promise.all(workers)
  return { success, failed }
}

const handleBulkQueryUpstreamUsage = async () => {
  if (upstreamUsageBulkLoading.value || selIds.value.length === 0) return
  upstreamUsageBulkLoading.value = true
  try {
    const currentProvidersById = new Map(providers.value.map(provider => [provider.id, provider]))
    const selectedProviderResults = await Promise.allSettled(
      selIds.value.map(id => currentProvidersById.get(id) ? Promise.resolve(currentProvidersById.get(id)!) : adminAPI.providers.getById(id))
    )
    const selectedProviders = selectedProviderResults
      .filter((item): item is PromiseFulfilledResult<Provider> => item.status === 'fulfilled')
      .map(item => item.value)
      .filter(isUpstreamUsageQueryEnabled)
    if (selectedProviders.length === 0) {
      appStore.showWarning(t('admin.providers.upstreamUsage.noSupportedSelection'))
      return
    }
    for (const provider of selectedProviders) {
      const key = String(provider.id)
      upstreamUsageRequestTokenByProviderId.value = {
        ...upstreamUsageRequestTokenByProviderId.value,
        [key]: ++upstreamUsageRequestToken
      }
      setUpstreamUsageState(provider.id, null, null, true)
    }
    let failed = selectedProviderResults.filter(item => item.status === 'rejected').length
    const chunkResult = await queryUpstreamUsageChunks(selectedProviders)
    const success = chunkResult.success
    failed += chunkResult.failed
    if (failed > 0) {
      appStore.showError(t('admin.providers.upstreamUsage.partialSuccess', { success, failed }))
    } else {
      appStore.showSuccess(t('admin.providers.upstreamUsage.success', { count: success }))
    }
  } catch (error) {
    console.error('Failed to bulk query upstream usage:', error)
    for (const providerID of selIds.value) {
      if (upstreamUsageLoadingByProviderId.value[String(providerID)]) {
        setUpstreamUsageState(providerID, null, normalizeUpstreamUsageError(error), false)
      }
    }
    appStore.showError(t('admin.providers.upstreamUsage.queryFailed'))
  } finally {
    upstreamUsageBulkLoading.value = false
  }
}

const setUsageBatchLoading = (providerID: number, loadingState: boolean) => {
  usageBatchLoadingByProviderId.value = {
    ...usageBatchLoadingByProviderId.value,
    [String(providerID)]: loadingState
  }
}

const setUsageBatchState = (providerID: number, usage: ProviderUsageInfo | null, error: string | null) => {
  const key = String(providerID)
  usageBatchByProviderId.value = {
    ...usageBatchByProviderId.value,
    [key]: usage
  }
  usageBatchErrorByProviderId.value = {
    ...usageBatchErrorByProviderId.value,
    [key]: error
  }
}

const handleProviderUsageLoaded = (providerID: number, usage: ProviderUsageInfo) => {
  if (usageBatchByProviderId.value[String(providerID)] === usage) return
  setUsageBatchState(providerID, usage, null)
}

const flushQueuedUsageBatch = async () => {
  usageBatchFlushTimer = null
  const providerIDs = Array.from(pendingUsageBatchIds)
  const force = queuedUsageBatchForce
  pendingUsageBatchIds.clear()
  queuedUsageBatchForce = false

  if (providerIDs.length === 0) return

  const requestTokensByProvider = providerIDs.reduce<Record<string, number>>((acc, providerID) => {
    acc[String(providerID)] = usageBatchRequestTokenByProviderId.value[String(providerID)] ?? 0
    return acc
  }, {})

  try {
    const result = await adminAPI.providers.getBatchUsage(providerIDs, force)

    const usageMap = result.usage ?? {}
    const errorMap = result.errors ?? {}
    const now = Date.now()
    const nextUsage = { ...usageBatchByProviderId.value }
    const nextErrors = { ...usageBatchErrorByProviderId.value }
    const nextLoading = { ...usageBatchLoadingByProviderId.value }

    for (const providerID of providerIDs) {
      const key = String(providerID)
      if ((usageBatchRequestTokenByProviderId.value[key] ?? 0) !== requestTokensByProvider[key]) {
        continue
      }
      const usage = usageMap[key] ?? null
      nextUsage[key] = usage
      nextErrors[key] = errorMap[key] ?? null
      nextLoading[key] = false
      if (usage) {
        usageBatchCache.set(providerID, { data: usage, ts: now })
      } else {
        usageBatchCache.delete(providerID)
      }
    }

    usageBatchByProviderId.value = nextUsage
    usageBatchErrorByProviderId.value = nextErrors
    usageBatchLoadingByProviderId.value = nextLoading
  } catch (error) {
    const nextErrors = { ...usageBatchErrorByProviderId.value }
    const nextLoading = { ...usageBatchLoadingByProviderId.value }
    for (const providerID of providerIDs) {
      const key = String(providerID)
      if ((usageBatchRequestTokenByProviderId.value[key] ?? 0) !== requestTokensByProvider[key]) {
        continue
      }
      nextErrors[key] = 'Failed'
      nextLoading[key] = false
    }
    usageBatchErrorByProviderId.value = nextErrors
    usageBatchLoadingByProviderId.value = nextLoading
    console.error('Failed to load provider usage batch:', error)
  }
}

const queueBatchedUsage = (provider: Provider, options?: { force?: boolean }) => {
  if (!isDesktopViewport.value) return
  if (!providerSupportsBatchUsage(provider)) return

  const force = options?.force === true
  const cacheKey = provider.id
  const key = String(cacheKey)

  if (force) {
    usageBatchCache.delete(cacheKey)
  } else {
    const cached = usageBatchCache.get(cacheKey)
    if (cached && Date.now() - cached.ts < USAGE_BATCH_CACHE_TTL) {
      setUsageBatchState(cacheKey, cached.data, null)
      setUsageBatchLoading(cacheKey, false)
      return
    }
  }

  usageBatchErrorByProviderId.value = {
    ...usageBatchErrorByProviderId.value,
    [key]: null
  }
  usageBatchRequestTokenByProviderId.value = {
    ...usageBatchRequestTokenByProviderId.value,
    [key]: ++usageBatchRequestToken
  }
  setUsageBatchLoading(cacheKey, true)
  pendingUsageBatchIds.add(cacheKey)
  queuedUsageBatchForce = queuedUsageBatchForce || force

  if (usageBatchFlushTimer !== null) return
  usageBatchFlushTimer = setTimeout(() => {
    void flushQueuedUsageBatch()
  }, 0)
}

const refreshTodayStatsBatch = async () => {
  // Why this checks both columns:
  // - today_stats column shows dedicated today's metrics.
  // - usage column also embeds today's stats for Key/Bedrock rows.
  // So we only skip fetching when BOTH columns are hidden.
  if (hiddenColumns.has('today_stats') && hiddenColumns.has('usage')) {
    todayStatsLoading.value = false
    todayStatsError.value = null
    return
  }

  const providerIDs = providers.value.map(provider => provider.id)
  const reqSeq = ++todayStatsReqSeq.value
  if (providerIDs.length === 0) {
    todayStatsByProviderId.value = {}
    todayStatsError.value = null
    todayStatsLoading.value = false
    return
  }

  todayStatsLoading.value = true
  todayStatsError.value = null

  try {
    const result = await adminAPI.providers.getBatchTodayStats(providerIDs)
    if (reqSeq !== todayStatsReqSeq.value) return
    const serverStats = result.stats ?? {}
    const nextStats: Record<string, WindowStats> = {}
    for (const providerID of providerIDs) {
      const key = String(providerID)
      nextStats[key] = serverStats[key] ?? buildDefaultTodayStats()
    }
    todayStatsByProviderId.value = nextStats
  } catch (error) {
    if (reqSeq !== todayStatsReqSeq.value) return
    todayStatsError.value = 'Failed'
    console.error('Failed to load provider today stats:', error)
  } finally {
    if (reqSeq === todayStatsReqSeq.value) {
      todayStatsLoading.value = false
    }
  }
}

const autoRefreshIntervalLabel = (sec: number) => {
  if (sec === 5) return t('admin.providers.refreshInterval5s')
  if (sec === 10) return t('admin.providers.refreshInterval10s')
  if (sec === 15) return t('admin.providers.refreshInterval15s')
  if (sec === 30) return t('admin.providers.refreshInterval30s')
  return `${sec}s`
}

const formatSchedulerScore = (value: unknown): string => {
  const num = Number(value)
  if (!Number.isFinite(num)) return '-'
  return num.toFixed(6).replace(/\.?0+$/, '')
}

const formatStickySchedulerScore = (score: ProviderSchedulerGroupScore): string => {
  if (!score) return '-'
  if (score.sticky_score_infinity) return '+∞'
  return formatSchedulerScore(score.sticky_score)
}

const getSchedulerScoreRows = (provider: Provider): ProviderSchedulerGroupScore[] => {
  const groupRows = Array.isArray(provider.scheduler_scores)
    ? provider.scheduler_scores.filter(score => score.group_id != null)
    : []
  if (groupRows.length) return groupRows
  // 未分组提供商没有分组维度分数，回退展示后端返回的基础分
  if (provider.scheduler_score) {
    return [{ group_id: null, ...provider.scheduler_score }]
  }
  return []
}

const formatSchedulerScoreGroup = (score: ProviderSchedulerGroupScore): string => {
  if ('group_name' in score && score.group_name) return score.group_name
  if ('group_id' in score && score.group_id != null) return `#${score.group_id}`
  return t('admin.providers.schedulerScore.ungrouped')
}

const loadSavedColumns = () => {
  try {
    const saved = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    if (saved) {
      const parsed = JSON.parse(saved) as string[]
      parsed.forEach(key => {
        hiddenColumns.add(key)
      })
      // 旧列配置可能默认显示 scheduler_score，仅迁移一次到新的安全默认值。
      if (localStorage.getItem(HIDDEN_COLUMNS_VERSION_KEY) !== HIDDEN_COLUMNS_CURRENT_VERSION) {
        hiddenColumns.add('scheduler_score')
        localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...hiddenColumns]))
        localStorage.setItem(HIDDEN_COLUMNS_VERSION_KEY, HIDDEN_COLUMNS_CURRENT_VERSION)
      }
    } else {
      DEFAULT_HIDDEN_COLUMNS.forEach(key => {
        hiddenColumns.add(key)
      })
      localStorage.setItem(HIDDEN_COLUMNS_VERSION_KEY, HIDDEN_COLUMNS_CURRENT_VERSION)
    }
  } catch (e) {
    console.error('Failed to load saved columns:', e)
    DEFAULT_HIDDEN_COLUMNS.forEach(key => {
      hiddenColumns.add(key)
    })
  }
}

const saveColumnsToStorage = () => {
  try {
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...hiddenColumns]))
    localStorage.setItem(HIDDEN_COLUMNS_VERSION_KEY, HIDDEN_COLUMNS_CURRENT_VERSION)
  } catch (e) {
    console.error('Failed to save columns:', e)
  }
}

const loadSavedAutoRefresh = () => {
  try {
    const saved = localStorage.getItem(AUTO_REFRESH_STORAGE_KEY)
    if (!saved) return
    const parsed = JSON.parse(saved) as { enabled?: boolean; interval_seconds?: number }
    autoRefreshEnabled.value = parsed.enabled === true
    const interval = Number(parsed.interval_seconds)
    if (autoRefreshIntervals.includes(interval as any)) {
      autoRefreshIntervalSeconds.value = interval as any
    }
  } catch (e) {
    console.error('Failed to load saved auto refresh settings:', e)
  }
}

const saveAutoRefreshToStorage = () => {
  try {
    localStorage.setItem(
      AUTO_REFRESH_STORAGE_KEY,
      JSON.stringify({
        enabled: autoRefreshEnabled.value,
        interval_seconds: autoRefreshIntervalSeconds.value
      })
    )
  } catch (e) {
    console.error('Failed to save auto refresh settings:', e)
  }
}

const clampDropdownLeft = (left: number, width: number) => {
  const margin = 16
  return Math.min(
    Math.max(left, margin),
    Math.max(margin, window.innerWidth - width - margin)
  )
}

const buildTopDropdownStyle = (trigger: HTMLElement | null, width: number, align: 'left' | 'right' = 'right'): Record<string, string> => {
  if (!trigger) return {}
  const rect = trigger.getBoundingClientRect()
  // 顶部工具菜单按触发按钮固定定位，并限制在视口范围内。
  const rawLeft = align === 'left' ? rect.left : rect.right - width
  const left = clampDropdownLeft(rawLeft, width)
  return {
    top: `${rect.bottom + 8}px`,
    left: `${left}px`,
    maxHeight: `${Math.max(MIN_COMFORTABLE_PANEL_HEIGHT, window.innerHeight - rect.bottom - 24)}px`
  }
}

const updateTopDropdownPositions = () => {
  if (showProviderToolsDropdown.value) {
    const trigger = providerToolsButtonRef.value
    if (trigger) {
      Object.assign(providerToolsDropdownPosition, getFloatingPanelPosition(
        trigger.getBoundingClientRect(),
        document.documentElement.clientWidth || window.innerWidth,
        window.innerHeight
      ))
    }
  }
  if (showAutoRefreshDropdown.value) {
    autoRefreshDropdownStyle.value = buildTopDropdownStyle(autoRefreshButtonRef.value, 224, 'left')
  }
}

const toggleProviderToolsDropdown = () => {
  showProviderToolsDropdown.value = !showProviderToolsDropdown.value
  showAutoRefreshDropdown.value = false
  updateTopDropdownPositions()
}

const toggleAutoRefreshDropdown = () => {
  showAutoRefreshDropdown.value = !showAutoRefreshDropdown.value
  showProviderToolsDropdown.value = false
  updateTopDropdownPositions()
}

if (typeof window !== 'undefined') {
  loadSavedColumns()
  loadSavedAutoRefresh()
}

const setAutoRefreshEnabled = (enabled: boolean) => {
  autoRefreshEnabled.value = enabled
  saveAutoRefreshToStorage()
  if (enabled) {
    autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
    resumeAutoRefresh()
  } else {
    pauseAutoRefresh()
    autoRefreshCountdown.value = 0
  }
}

const setAutoRefreshInterval = (seconds: (typeof autoRefreshIntervals)[number]) => {
  autoRefreshIntervalSeconds.value = seconds
  saveAutoRefreshToStorage()
  if (autoRefreshEnabled.value) {
    autoRefreshCountdown.value = seconds
  }
}

const toggleColumn = (key: string) => {
  const wasHidden = hiddenColumns.has(key)
  if (hiddenColumns.has(key)) {
    hiddenColumns.delete(key)
  } else {
    hiddenColumns.add(key)
  }
  saveColumnsToStorage()
  if ((key === 'today_stats' || key === 'usage') && wasHidden) {
    refreshTodayStatsBatch().catch((error) => {
      console.error('Failed to load provider today stats after showing column:', error)
    })
  }
  if (key === 'scheduler_score') {
    // 服务端仅在该列可见时返回调度权值，因此切换后立即重载当前页。
    syncProviderListDerivedParams()
    load().catch((error) => {
      console.error('Failed to reload providers after toggling scheduler score column:', error)
    })
  }
}

const isColumnVisible = (key: string) => !hiddenColumns.has(key)
const shouldIncludeSchedulerScore = () => isColumnVisible('scheduler_score')
const syncProviderListDerivedParams = () => {
  // 让自动刷新、排序等所有加载路径都与当前列可见性保持一致。
  const requestParams = params as any
  requestParams.include_scheduler_score = shouldIncludeSchedulerScore() ? '1' : '0'
}

const {
  items: providers,
  loading,
  params,
  pagination,
  load: baseLoad,
  reload: baseReload,
  debouncedReload: baseDebouncedReload,
  handlePageChange: baseHandlePageChange,
  handlePageSizeChange: baseHandlePageSizeChange
} = useTableLoader<Provider, any>({
  fetchFn: adminAPI.providers.list,
  initialParams: {
    platform: '',
    type: '',
    status: '',
    privacy_mode: '',
    group: '',
    search: '',
    include_scheduler_score: shouldIncludeSchedulerScore() ? '1' : '0',
    sort_by: sortState.sort_by,
    sort_order: sortState.sort_order
  }
})

const {
  selectedSet,
  selectedIds: selIds,
  allVisibleSelected,
  isSelected,
  setSelectedIds,
  select,
  deselect,
  toggle: toggleSel,
  clear: clearSelectedIds,
  removeMany: removeSelectedProviders,
  toggleVisible,
  selectVisible: selectCurrentPage,
  batchUpdate
} = useTableSelection<Provider>({
  rows: providers,
  getId: (provider) => provider.id
})

const selectingAllResults = ref(false)
const selectedAllResultIDs = ref<Set<number> | null>(null)
const selectionRequestVersion = ref(0)
const allResultsSelected = computed(() => {
  const snapshot = selectedAllResultIDs.value
  if (
    !snapshot ||
    snapshot.size === 0 ||
    snapshot.size !== pagination.total ||
    snapshot.size !== selectedSet.value.size
  ) {
    return false
  }
  return Array.from(snapshot).every(id => selectedSet.value.has(id))
})

const clearSelection = () => {
  selectionRequestVersion.value++
  selectingAllResults.value = false
  selectedAllResultIDs.value = null
  clearSelectedIds()
}

const selectPage = () => {
  selectCurrentPage()
}

const swipeVirtualContext: SwipeSelectVirtualContext = {
  getVirtualizer: () => dataTableRef.value?.virtualizer ?? null,
  getSortedData: () => dataTableRef.value?.sortedData ?? providers.value,
  getRowId: (row: any) => row.id,
}

useSwipeSelect(providerTableRef, {
  isSelected,
  select,
  deselect,
  batchUpdate
}, swipeVirtualContext)

const resetAutoRefreshCache = () => {
  autoRefreshETag.value = null
}

const isFirstLoad = ref(true)

const load = async () => {
  const requestParams = params as any
  syncProviderListDerivedParams()
  hasPendingListSync.value = false
  resetAutoRefreshCache()
  pendingTodayStatsRefresh.value = false
  if (isFirstLoad.value) {
    requestParams.lite = '1'
  }
  await baseLoad()
  if (isFirstLoad.value) {
    isFirstLoad.value = false
    delete requestParams.lite
  }
  hydrateUpstreamUsageCache()
  await refreshTodayStatsBatch()
}

const reload = async () => {
  syncProviderListDerivedParams()
  hasPendingListSync.value = false
  resetAutoRefreshCache()
  pendingTodayStatsRefresh.value = false
  await baseReload()
  hydrateUpstreamUsageCache()
  await refreshTodayStatsBatch()
}

const debouncedReload = () => {
  clearSelection()
  syncProviderListDerivedParams()
  hasPendingListSync.value = false
  resetAutoRefreshCache()
  pendingTodayStatsRefresh.value = true
  baseDebouncedReload()
}

const handlePageChange = (page: number) => {
  syncProviderListDerivedParams()
  hasPendingListSync.value = false
  resetAutoRefreshCache()
  pendingTodayStatsRefresh.value = true
  baseHandlePageChange(page)
}

const handlePageSizeChange = (size: number) => {
  syncProviderListDerivedParams()
  hasPendingListSync.value = false
  resetAutoRefreshCache()
  pendingTodayStatsRefresh.value = true
  baseHandlePageSizeChange(size)
}

const handleSort = (key: string, order: ProviderSortOrder) => {
  sortState.sort_by = key
  sortState.sort_order = order
  const requestParams = params as any
  requestParams.sort_by = key
  requestParams.sort_order = order
  syncProviderListDerivedParams()
  pagination.page = 1
  hasPendingListSync.value = false
  resetAutoRefreshCache()
  pendingTodayStatsRefresh.value = true
  load()
}

watch(loading, (isLoading, wasLoading) => {
  if (wasLoading && !isLoading && pendingTodayStatsRefresh.value) {
    pendingTodayStatsRefresh.value = false
    refreshTodayStatsBatch().catch((error) => {
      console.error('Failed to refresh provider today stats after table load:', error)
    })
  }
})

const isAnyModalOpen = computed(() => {
  return (
    showCreate.value ||
    showEdit.value ||
    showSync.value ||
    showImportData.value ||
    showExportDataDialog.value ||
    showBulkEdit.value ||
    showTempUnsched.value ||
    showDeleteDialog.value ||
    showReAuth.value ||
    showTest.value ||
    showStats.value ||
    showInviteReset.value ||
    showSchedulePanel.value ||
    showErrorPassthrough.value ||
    showTLSFingerprintProfiles.value ||
    showTLSFingerprintRouters.value
  )
})

const enterAutoRefreshSilentWindow = () => {
  autoRefreshSilentUntil.value = Date.now() + AUTO_REFRESH_SILENT_WINDOW_MS
  autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
}

const inAutoRefreshSilentWindow = () => {
  return Date.now() < autoRefreshSilentUntil.value
}

const shouldReplaceAutoRefreshRow = (current: Provider, next: Provider) => {
  const upstreamConnectionChanged = supportsUpstreamUsageQuery(current) || supportsUpstreamUsageQuery(next)
    ? current.type !== next.type || current.platform !== next.platform ||
      current.proxy_id !== next.proxy_id ||
      current.proxy?.updated_at !== next.proxy?.updated_at ||
      current.credentials?.base_url !== next.credentials?.base_url ||
      JSON.stringify(current.extra?.upstream_usage_query ?? null) !== JSON.stringify(next.extra?.upstream_usage_query ?? null)
    : false
  return (
    current.updated_at !== next.updated_at ||
    current.current_concurrency !== next.current_concurrency ||
    current.current_window_cost !== next.current_window_cost ||
    current.active_sessions !== next.active_sessions ||
    current.schedulable !== next.schedulable ||
    current.status !== next.status ||
    current.rate_limit_reset_at !== next.rate_limit_reset_at ||
    current.overload_until !== next.overload_until ||
    current.temp_unschedulable_until !== next.temp_unschedulable_until ||
    current.proxy?.updated_at !== next.proxy?.updated_at ||
    upstreamConnectionChanged ||
    buildOpenAIUsageRefreshKey(current) !== buildOpenAIUsageRefreshKey(next) ||
    buildGrokUsageRefreshKey(current) !== buildGrokUsageRefreshKey(next)
  )
}

const syncProviderRefs = (nextProvider: Provider) => {
  if (edAcc.value?.id === nextProvider.id) edAcc.value = nextProvider
  if (reAuthAcc.value?.id === nextProvider.id) reAuthAcc.value = nextProvider
  if (tempUnschedAcc.value?.id === nextProvider.id) tempUnschedAcc.value = nextProvider
  if (deletingAcc.value?.id === nextProvider.id) deletingAcc.value = nextProvider
  if (menu.acc?.id === nextProvider.id) menu.acc = nextProvider
}

const mergeProvidersIncrementally = (nextRows: Provider[]) => {
  const currentRows = providers.value
  const currentByID = new Map(currentRows.map(row => [row.id, row]))
  const nextIDs = new Set(nextRows.map(row => row.id))
  for (const currentRow of currentRows) {
    if (supportsUpstreamUsageQuery(currentRow) && !nextIDs.has(currentRow.id)) {
      // 自动刷新发现提供商已从列表消失时，立即删除对应浏览器快照。
      invalidateUpstreamUsageCache(currentRow.id)
    }
  }
  let changed = nextRows.length !== currentRows.length
  const mergedRows = nextRows.map((nextRow) => {
    const currentRow = currentByID.get(nextRow.id)
    if (!currentRow) {
      changed = true
      return nextRow
    }
    if (shouldReplaceAutoRefreshRow(currentRow, nextRow)) {
      if ((supportsUpstreamUsageQuery(currentRow) || supportsUpstreamUsageQuery(nextRow)) &&
        upstreamUsageCacheKey(currentRow) !== upstreamUsageCacheKey(nextRow)) {
        invalidateUpstreamUsageCache(nextRow.id)
      }
      changed = true
      syncProviderRefs(nextRow)
      return nextRow
    }
    return currentRow
  })
  if (!changed) {
    for (let i = 0; i < mergedRows.length; i += 1) {
      if (mergedRows[i].id !== currentRows[i]?.id) {
        changed = true
        break
      }
    }
  }
  if (changed) {
    providers.value = mergedRows
  }
}

const refreshProvidersIncrementally = async () => {
  if (autoRefreshFetching.value) return
  syncProviderListDerivedParams()
  autoRefreshFetching.value = true
  try {
    const result = await adminAPI.providers.listWithEtag(
      pagination.page,
      pagination.page_size,
      toRaw(params) as {
        platform?: string
        type?: string
        status?: string
        privacy_mode?: string
        group?: string
        search?: string
        sort_by?: string
        sort_order?: ProviderSortOrder

      },
      { etag: autoRefreshETag.value }
    )

    if (result.etag) {
      autoRefreshETag.value = result.etag
    }
    if (!result.notModified && result.data) {
      pagination.total = result.data.total || 0
      pagination.pages = result.data.pages || 0
      mergeProvidersIncrementally(result.data.items || [])
      hasPendingListSync.value = false
    }

    // 自动刷新从成功缓存恢复当前页面的上游用量。
    hydrateUpstreamUsageCache()
    await refreshTodayStatsBatch()
  } catch (error) {
    console.error('Auto refresh failed:', error)
  } finally {
    autoRefreshFetching.value = false
  }
}

const handleManualRefresh = async () => {
  await load()
  // Force usage cells to refetch /usage on explicit user refresh.
  usageManualRefreshToken.value += 1
}

const closeProviderToolsDropdown = () => {
  showProviderToolsDropdown.value = false
}

// 按当前筛选条件批量编辑全部结果，入口在工具菜单里，不依赖勾选。
const openBulkEditFilteredFromMenu = () => {
  closeProviderToolsDropdown()
  void openBulkEditFiltered()
}

const openSyncFromCrs = () => {
  closeProviderToolsDropdown()
  showSync.value = true
}

const openImportData = () => {
  closeProviderToolsDropdown()
  showImportData.value = true
}

const openExportDataDialogFromMenu = () => {
  closeProviderToolsDropdown()
  openExportDataDialog()
}

const openErrorPassthrough = () => {
  closeProviderToolsDropdown()
  showErrorPassthrough.value = true
}

const openTLSFingerprintProfiles = () => {
  closeProviderToolsDropdown()
  showTLSFingerprintProfiles.value = true
}

const openTLSFingerprintRouters = () => {
  closeProviderToolsDropdown()
  showTLSFingerprintRouters.value = true
}

const syncPendingListChanges = async () => {
  hasPendingListSync.value = false
  await load()
  // Keep behavior consistent with manual refresh.
  usageManualRefreshToken.value += 1
}

const { pause: pauseAutoRefresh, resume: resumeAutoRefresh } = useIntervalFn(
  async () => {
    if (!autoRefreshEnabled.value) return
    if (document.hidden) return
    if (loading.value || autoRefreshFetching.value) return
    if (isAnyModalOpen.value) return
    if (menu.show || showProviderToolsDropdown.value || showAutoRefreshDropdown.value) return
    if (inAutoRefreshSilentWindow()) {
      autoRefreshCountdown.value = Math.max(
        0,
        Math.ceil((autoRefreshSilentUntil.value - Date.now()) / 1000)
      )
      return
    }

    if (autoRefreshCountdown.value <= 0) {
      autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
      await refreshProvidersIncrementally()
      return
    }

    autoRefreshCountdown.value -= 1
  },
  1000,
  { immediate: false }
)

const GROK_QUOTA_SIGNAL_MAX_AGE_MS = 24 * 60 * 60 * 1000
const GROK_QUOTA_SIGNAL_MAX_FUTURE_SKEW_MS = 5 * 60 * 1000

function firstNonBlankString(...values: unknown[]): string | undefined {
  return values.find((value): value is string => (
    typeof value === 'string' && value.trim().length > 0
  ))
}

function normalizeGrokPlanKey(value: unknown): string {
  if (typeof value !== 'string') return ''
  return value
    .trim()
    .toLowerCase()
    .replace(/[\s_-]+/g, '')
}

function grokPersistedQuotaSnapshot(extra: Record<string, any>): Record<string, any> | undefined {
  const usage = extra.grok_usage_snapshot
  if (usage && typeof usage === 'object' && !Array.isArray(usage)) {
    return usage as Record<string, any>
  }
  const legacy = extra.grok_quota_snapshot
  if (legacy && typeof legacy === 'object' && !Array.isArray(legacy)) {
    return legacy as Record<string, any>
  }
  return undefined
}

function isGrokQuotaTimestampFresh(raw: unknown): boolean {
  const value = String(raw || '').trim()
  if (!value) return false
  const observedAt = Date.parse(value)
  if (!Number.isFinite(observedAt)) return false
  const age = Date.now() - observedAt
  return age <= GROK_QUOTA_SIGNAL_MAX_AGE_MS && age >= -GROK_QUOTA_SIGNAL_MAX_FUTURE_SKEW_MS
}

function isGrok45ResponsesQuotaModel(model: unknown): boolean {
  const value = String(model || '')
    .trim()
    .toLowerCase()
    .replace(/^(x-ai|xai)\//, '')
  return value === 'grok-4.5' || value.startsWith('grok-4.5-')
}

function grokQuotaLooksHeavy(snapshot: Record<string, any> | undefined): boolean {
  const req = Number(snapshot?.requests?.limit ?? 0)
  const tok = Number(snapshot?.tokens?.limit ?? 0)
  return req >= 8300 || tok >= 53_000_000
}

function grok45ResponsesPlanIsHeavy(snapshot: Record<string, any> | undefined): boolean {
  if (!snapshot) return false
  const hint = normalizeGrokPlanKey(snapshot.plan_from_45_responses)
  if (hint === 'supergrokheavy' && isGrokQuotaTimestampFresh(snapshot.plan_from_45_responses_at)) {
    return true
  }
  const observedAt = snapshot.last_headers_seen_at || snapshot.updated_at
  return (
    isGrok45ResponsesQuotaModel(snapshot.model) &&
    isGrokQuotaTimestampFresh(observedAt) &&
    grokQuotaLooksHeavy(snapshot)
  )
}

// JWT 中明确的档位优先于快照；SuperGrokPro 同时覆盖 SuperGrok 与 Heavy，
// 只有来自 grok-4.5 Responses 的 8300/53M 窗口或延续提示才能升级为 Heavy。
function getProviderPlanType(row: any): string | undefined {
  if (!row) return undefined
  if (row.platform === 'grok') {
    const extra = (row.extra || {}) as Record<string, any>
    const billing = extra.grok_billing_snapshot as Record<string, any> | undefined
    const usage = extra.grok_usage_snapshot as Record<string, any> | undefined
    const legacyQuota = extra.grok_quota_snapshot as Record<string, any> | undefined
    const quota = grokPersistedQuotaSnapshot(extra)
    const cred = firstNonBlankString(row.credentials?.subscription_tier)
    const credKey = normalizeGrokPlanKey(cred)
    if (credKey && credKey !== 'supergrokpro') {
      return cred
    }
    if (
      grok45ResponsesPlanIsHeavy(quota) &&
      (credKey === 'supergrokpro' ||
        normalizeGrokPlanKey(billing?.plan) === 'supergrok' ||
        normalizeGrokPlanKey(billing?.plan) === 'supergrokpro')
    ) {
      return 'SuperGrok Heavy'
    }
    if (credKey === 'supergrokpro') {
      return firstNonBlankString(billing?.plan) || 'SuperGrok'
    }
    return firstNonBlankString(
      billing?.plan,
      usage?.subscription_tier,
      legacyQuota?.subscription_tier,
      extra.subscription_tier,
      row.credentials?.plan_type,
      row.parent_plan_type
    )
  }
  return firstNonBlankString(row.credentials?.plan_type, row.parent_plan_type)
}

function getOpenAIAuthMode(row: any): string | undefined {
  if (!row || row.platform !== 'openai' || row.type !== 'oauth') return undefined
  const authMode = row.credentials?.auth_mode
  return typeof authMode === 'string' && authMode.trim() ? authMode : undefined
}

// Antigravity 订阅等级辅助函数
function getAntigravityTierFromRow(row: any): string | null {
  if (row.platform !== 'antigravity') return null
  const extra = row.extra as Record<string, unknown> | undefined
  if (!extra) return null
  const lca = extra.load_code_assist as Record<string, unknown> | undefined
  if (!lca) return null
  const paid = lca.paidTier as Record<string, unknown> | undefined
  if (paid && typeof paid.id === 'string') return paid.id
  const current = lca.currentTier as Record<string, unknown> | undefined
  if (current && typeof current.id === 'string') return current.id
  return null
}

function getAntigravityTierLabel(row: any): string | null {
  const tier = getAntigravityTierFromRow(row)
  switch (tier) {
    case 'free-tier': return t('admin.providers.tier.free')
    case 'g1-pro-tier': return t('admin.providers.tier.pro')
    case 'g1-ultra-tier': return t('admin.providers.tier.ultra')
    default: return null
  }
}

// 提供商显示邮箱:优先提供商自身(extra/credentials),影子提供商回退母提供商 parent_email。
// 名称单元格的 v-if、标题和文本共用此回退结果。
function providerDisplayEmail(row: any): string {
  return row.extra?.email_address || row.extra?.email || row.credentials?.email || row.parent_email || ''
}

// API Key 提供商的站点外链由协议、主机和端口构成。
function providerHomepageUrl(row: Provider): string {
  if (row.type !== 'apikey' || typeof row.credentials?.base_url !== 'string') return ''
  const baseUrl = sanitizeUrl(row.credentials.base_url)
  return baseUrl ? new URL(baseUrl).origin : ''
}

function getAntigravityTierClass(row: any): string {
  const tier = getAntigravityTierFromRow(row)
  switch (tier) {
    case 'free-tier': return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-200'
    case 'g1-pro-tier': return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    case 'g1-ultra-tier': return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    default: return ''
  }
}

// All available columns
const allColumns = computed(() => {
  const c = [
    { key: 'select', label: '', sortable: false },
    { key: 'name', label: t('admin.providers.columns.name'), sortable: true },
    { key: 'id', label: t('admin.providers.columns.id'), sortable: true },
    { key: 'platform_type', label: t('admin.providers.columns.platformType'), sortable: false },
    { key: 'capacity', label: t('admin.providers.columns.capacity'), sortable: false },
    { key: 'status', label: t('admin.providers.columns.status'), sortable: true },
    { key: 'schedulable', label: t('admin.providers.columns.schedulable'), sortable: true },
    { key: 'today_stats', label: t('admin.providers.columns.todayStats'), sortable: false }
  ]
  c.push({ key: 'groups', label: t('admin.providers.columns.groups'), sortable: false })
  c.push({ key: 'usage', label: t('admin.providers.columns.usageWindows'), sortable: false })
  c.push(
    { key: 'proxy', label: t('admin.providers.columns.proxy'), sortable: false },
    { key: 'priority', label: t('admin.providers.columns.priority'), sortable: true },
    { key: 'scheduler_score', label: t('admin.providers.columns.schedulerScore'), sortable: false },
    { key: 'rate_multiplier', label: t('admin.providers.columns.billingRateMultiplier'), sortable: true },
    { key: 'last_used_at', label: t('admin.providers.columns.lastUsed'), sortable: true },
    { key: 'created_at', label: t('admin.providers.columns.createdAt'), sortable: true },
    { key: 'expires_at', label: t('admin.providers.columns.expiresAt'), sortable: true },
    { key: 'notes', label: t('admin.providers.columns.notes'), sortable: false },
    { key: 'actions', label: t('admin.providers.columns.actions'), sortable: false }
  )
  return c
})

// Columns that can be toggled (exclude select, name, and actions)
const toggleableColumns = computed(() =>
  allColumns.value.filter(col => col.key !== 'select' && col.key !== 'name' && col.key !== 'actions')
)

// Filtered columns based on visibility
const cols = computed(() =>
  allColumns.value.filter(col =>
    col.key === 'select' || col.key === 'name' || col.key === 'actions' || !hiddenColumns.has(col.key)
  )
)

const handleEdit = (a: Provider) => { edAcc.value = a; showEdit.value = true }
const openMenu = async (a: Provider, e: MouseEvent) => {
  menu.acc = a

  const target = e.currentTarget as HTMLElement
  if (target) {
    const rect = target.getBoundingClientRect()
    const menuWidth = 208
    const menuHeight = 280
    const padding = 8
    const viewportWidth = window.innerWidth
    const viewportHeight = window.innerHeight

    let left: number
    let top: number

    if (viewportWidth < 768) {
      // 居中显示,水平位置
      left = Math.max(padding, Math.min(
        rect.left + rect.width / 2 - menuWidth / 2,
        viewportWidth - menuWidth - padding
      ))

      // 优先显示在按钮下方
      top = rect.bottom + 4

      // 如果下方空间不够,显示在上方
      if (top + menuHeight > viewportHeight - padding) {
        top = rect.top - menuHeight - 4
        // 如果上方也不够,就贴在视口顶部
        if (top < padding) {
          top = padding
        }
      }
    } else {
      left = Math.max(padding, Math.min(
        e.clientX - menuWidth,
        viewportWidth - menuWidth - padding
      ))
      top = e.clientY
      if (top + menuHeight > viewportHeight - padding) {
        top = viewportHeight - menuHeight - padding
      }
    }

    menu.pos = { top, left }
  } else {
    menu.pos = { top: e.clientY, left: e.clientX - 200 }
  }

  menu.show = true
  await nextTick()
  // 各提供商的菜单项数量不同，渲染后按实际高度校正，确保底部删除入口始终可达。
  const menuElement = document.querySelector<HTMLElement>('.action-menu-content')
  if (menu.show && menu.acc?.id === a.id && menu.pos && menuElement) {
    menu.pos.top = Math.max(8, Math.min(menu.pos.top, window.innerHeight - menuElement.offsetHeight - 8))
  }
}
const toggleSelectAllVisible = (event: Event) => {
  const target = event.target as HTMLInputElement
  toggleVisible(target.checked)
}
const handleBulkDelete = async () => {
  const providerIds = [...selIds.value]
  if (!confirm(t('admin.providers.bulkActions.confirmDelete', { count: providerIds.length }))) return
  try {
    for (const providerID of providerIds) invalidateUpstreamUsageCache(providerID)
    const result = await adminAPI.providers.batchDelete(providerIds)
    if (result.failed > 0) {
      appStore.showError(t('admin.providers.bulkActions.partialSuccess', {
        success: result.success,
        failed: result.failed
      }))
      setSelectedIds(result.failed_ids?.length ? result.failed_ids : providerIds)
    } else {
      appStore.showSuccess(t('admin.providers.bulkActions.deleteSuccess', { count: result.success }))
      clearSelection()
    }
    await reload()
  } catch (error) {
    console.error('Failed to bulk delete providers:', error)
    appStore.showError(String(error))
  }
}
const handleBulkResetStatus = async () => {
  if (!confirm(t('common.confirm'))) return
  try {
    const result = await adminAPI.providers.batchClearError(selIds.value)
    if (result.failed > 0) {
      appStore.showError(t('admin.providers.bulkActions.partialSuccess', { success: result.success, failed: result.failed }))
    } else {
      appStore.showSuccess(t('admin.providers.bulkActions.resetStatusSuccess', { count: result.success }))
      clearSelection()
    }
    reload()
  } catch (error) {
    console.error('Failed to bulk reset status:', error)
    appStore.showError(String(error))
  }
}
const handleBulkRefreshToken = async () => {
  if (!confirm(t('common.confirm'))) return
  try {
    for (const providerID of selIds.value) invalidateUpstreamUsageCache(providerID)
    const result = await adminAPI.providers.batchRefresh(selIds.value)
    if (result.failed > 0) {
      appStore.showError(t('admin.providers.bulkActions.partialSuccess', { success: result.success, failed: result.failed }))
    } else {
      appStore.showSuccess(t('admin.providers.bulkActions.refreshTokenSuccess', { count: result.success }))
      clearSelection()
    }
    reload()
  } catch (error) {
    console.error('Failed to bulk refresh token:', error)
    appStore.showError(String(error))
  }
}
const canQueryProviderUsage = (provider: Provider) => {
  // 支持后端 /usage 主动查询的提供商可参加批量查询。
  return (
    (provider.platform === 'anthropic' && (provider.type === 'oauth' || provider.type === 'setup-token')) ||
    (provider.platform === 'openai' && provider.type === 'oauth') ||
    (provider.platform === 'gemini' && provider.type !== 'apikey') ||
    (provider.platform === 'antigravity' && provider.type === 'oauth') ||
    (provider.platform === 'qoder' && provider.type === 'cosy')
  )
}
const handleBulkQueryUsage = async () => {
  if (bulkUsageLoading.value) return
  const providerIds = [...selIds.value]
  if (providerIds.length === 0) return

  bulkUsageLoading.value = true
  try {
    const currentProvidersById = new Map(providers.value.map(provider => [provider.id, provider]))
    // 跨页选择时，当前页外的提供商需要先拉取详情，保证批量查询作用于所有已选 ID。
    const selectedProviderResults = await Promise.allSettled(
      providerIds.map(providerId => {
        const provider = currentProvidersById.get(providerId)
        return provider ? Promise.resolve(provider) : adminAPI.providers.getById(providerId)
      })
    )
    const selectedProviders = selectedProviderResults
      .filter((result): result is PromiseFulfilledResult<Provider> => result.status === 'fulfilled')
      .map(result => result.value)
    const metadataFailed = selectedProviderResults.length - selectedProviders.length
    const queryableProviders = selectedProviders.filter(canQueryProviderUsage)
    if (queryableProviders.length === 0) {
      appStore.showWarning(t('admin.providers.bulkActions.queryUsageNoSupported'))
      return
    }

    const results = await Promise.allSettled(
      queryableProviders.map(provider =>
        enqueueUsageRequest(provider, () => adminAPI.providers.getUsage(provider.id, 'active', true))
      )
    )
    const success = results.filter(result => result.status === 'fulfilled').length
    const failed = metadataFailed + results.length - success
    if (success > 0) {
      usageManualRefreshToken.value += 1
    }
    if (failed > 0) {
      appStore.showError(t('admin.providers.bulkActions.queryUsagePartialSuccess', { success, failed }))
      return
    }
    appStore.showSuccess(t('admin.providers.bulkActions.queryUsageSuccess', { count: success }))
  } catch (error) {
    console.error('Failed to bulk query usage:', error)
    appStore.showError(String(error))
  } finally {
    bulkUsageLoading.value = false
  }
}

const updateSchedulableInList = (providerIds: number[], schedulable: boolean) => {
  if (providerIds.length === 0) return
  const idSet = new Set(providerIds)
  providers.value = providers.value.map((provider) => (idSet.has(provider.id) ? { ...provider, schedulable } : provider))
}
const normalizeBulkSchedulableResult = (
  result: {
    success?: number
    failed?: number
    success_ids?: number[]
    failed_ids?: number[]
    results?: Array<{ provider_id: number; success: boolean }>
  },
  providerIds: number[]
) => {
  const responseSuccessIds = Array.isArray(result.success_ids) ? result.success_ids : []
  const responseFailedIds = Array.isArray(result.failed_ids) ? result.failed_ids : []
  if (responseSuccessIds.length > 0 || responseFailedIds.length > 0) {
    return {
      successIds: responseSuccessIds,
      failedIds: responseFailedIds,
      successCount: typeof result.success === 'number' ? result.success : responseSuccessIds.length,
      failedCount: typeof result.failed === 'number' ? result.failed : responseFailedIds.length,
      hasIds: true,
      hasCounts: true
    }
  }

  const results = Array.isArray(result.results) ? result.results : []
  if (results.length > 0) {
    const successIds = results.filter(item => item.success).map(item => item.provider_id)
    const failedIds = results.filter(item => !item.success).map(item => item.provider_id)
    return {
      successIds,
      failedIds,
      successCount: typeof result.success === 'number' ? result.success : successIds.length,
      failedCount: typeof result.failed === 'number' ? result.failed : failedIds.length,
      hasIds: true,
      hasCounts: true
    }
  }

  const hasExplicitCounts = typeof result.success === 'number' || typeof result.failed === 'number'
  const successCount = typeof result.success === 'number' ? result.success : 0
  const failedCount = typeof result.failed === 'number' ? result.failed : 0
  if (hasExplicitCounts && failedCount === 0 && successCount === providerIds.length && providerIds.length > 0) {
    return {
      successIds: providerIds,
      failedIds: [],
      successCount,
      failedCount,
      hasIds: true,
      hasCounts: true
    }
  }

  return {
    successIds: [],
    failedIds: [],
    successCount,
    failedCount,
    hasIds: false,
    hasCounts: hasExplicitCounts
  }
}
const handleBulkToggleSchedulable = async (schedulable: boolean) => {
  const providerIds = [...selIds.value]
  try {
    const result = await adminAPI.providers.bulkUpdate(providerIds, { schedulable })
    const { successIds, failedIds, successCount, failedCount, hasIds, hasCounts } = normalizeBulkSchedulableResult(result, providerIds)
    if (!hasIds && !hasCounts) {
      appStore.showError(t('admin.providers.bulkSchedulableResultUnknown'))
      setSelectedIds(providerIds)
      load().catch((error) => {
        console.error('Failed to refresh providers:', error)
      })
      return
    }
    if (successIds.length > 0) {
      updateSchedulableInList(successIds, schedulable)
    }
    if (successCount > 0 && failedCount === 0) {
      const message = schedulable
        ? t('admin.providers.bulkSchedulableEnabled', { count: successCount })
        : t('admin.providers.bulkSchedulableDisabled', { count: successCount })
      appStore.showSuccess(message)
    }
    if (failedCount > 0) {
      const message = hasCounts || hasIds
        ? t('admin.providers.bulkSchedulablePartial', { success: successCount, failed: failedCount })
        : t('admin.providers.bulkSchedulableResultUnknown')
      appStore.showError(message)
      setSelectedIds(failedIds.length > 0 ? failedIds : providerIds)
    } else {
      if (hasIds) clearSelection()
      else setSelectedIds(providerIds)
    }
  } catch (error) {
    console.error('Failed to bulk toggle schedulable:', error)
    appStore.showError(t('common.error'))
  }
}
const buildBulkEditFilterSnapshot = (): ProviderBulkEditFilterSnapshot => {
  const rawParams = toRaw(params) as Record<string, unknown>
  const sortOrder: ProviderSortOrder = rawParams.sort_order === 'desc' ? 'desc' : 'asc'
  return {
    platform: typeof rawParams.platform === 'string' ? rawParams.platform : '',
    type: typeof rawParams.type === 'string' ? rawParams.type : '',
    status: typeof rawParams.status === 'string' ? rawParams.status : '',
    group: typeof rawParams.group === 'string' ? rawParams.group : '',
    search: typeof rawParams.search === 'string' ? rawParams.search : '',
    privacy_mode: typeof rawParams.privacy_mode === 'string' ? rawParams.privacy_mode : '',
    sort_by: typeof rawParams.sort_by === 'string' ? rawParams.sort_by : '',
    sort_order: sortOrder
  }
}

const handleSelectAllResults = async () => {
  if (selectingAllResults.value || pagination.total === 0) return

  const requestVersion = ++selectionRequestVersion.value
  const filters = buildBulkEditFilterSnapshot()
  selectingAllResults.value = true
  try {
    const ids = await fetchAllProviderIds(
      (page, pageSize, requestFilters) => adminAPI.providers.list(page, pageSize, requestFilters),
      filters
    )
    if (requestVersion !== selectionRequestVersion.value) return

    setSelectedIds(ids)
    selectedAllResultIDs.value = new Set(ids)
  } catch (error) {
    if (requestVersion !== selectionRequestVersion.value) return
    console.error('Failed to select all provider results:', error)
    appStore.showError(t('admin.providers.bulkActions.selectAllFailed'))
  } finally {
    if (requestVersion === selectionRequestVersion.value) {
      selectingAllResults.value = false
    }
  }
}

const collectSelectionMetadata = (rows: Provider[]) => {
  const selectedPlatforms = Array.from(new Set(rows.map(provider => provider.platform)))
  const selectedTypes = Array.from(new Set(rows.map(provider => provider.type)))
  return { selectedPlatforms, selectedTypes }
}

const loadBulkEditFilterMetadata = async (filters: ProviderBulkEditFilterSnapshot) => {
  const rows: Provider[] = []
  let page = 1
  while (true) {
    const response = await adminAPI.providers.list(page, BULK_EDIT_FILTER_METADATA_PAGE_SIZE, {
      ...filters,
      lite: '1'
    })
    const total = response.total
    rows.push(...response.items)

    if (rows.length >= total || response.items.length === 0) {
      return {
        previewCount: total,
        ...collectSelectionMetadata(rows)
      }
    }
    page += 1
  }
}

const openBulkEditSelected = () => {
  bulkEditTarget.value = {
    mode: 'selected',
    providerIds: [...selIds.value],
    selectedPlatforms: [...selPlatforms.value],
    selectedTypes: [...selTypes.value]
  }
  showBulkEdit.value = true
}

const openBulkEditFiltered = async () => {
  const filters = buildBulkEditFilterSnapshot()
  const metadata = await loadBulkEditFilterMetadata(filters)
  bulkEditTarget.value = {
    mode: 'filtered',
    filters,
    ...metadata
  }
  showBulkEdit.value = true
}

const handleBulkUpdated = () => {
  invalidateAllUpstreamUsageCache()
  showBulkEdit.value = false
  bulkEditTarget.value = null
  clearSelection()
  reload()
}
const handleExternalProvidersChanged = () => {
  invalidateAllUpstreamUsageCache()
  reload()
}
const handleDataImported = () => {
  invalidateAllUpstreamUsageCache()
  showImportData.value = false
  reload()
}
const PROVIDER_UNGROUPED_GROUP_QUERY_VALUE = 'ungrouped'
const PROVIDER_PRIVACY_MODE_UNSET_QUERY_VALUE = '__unset__'
const buildProviderQueryFilters = () => ({
  platform: params.platform || '',
  type: params.type || '',
  status: params.status || '',
  group: params.group || '',
  privacy_mode: params.privacy_mode || '',
  search: params.search || '',
  sort_by: sortState.sort_by,
  sort_order: sortState.sort_order
})
const providerMatchesCurrentFilters = (provider: Provider) => {
  const filters = buildProviderQueryFilters()
  if (filters.platform && provider.platform !== filters.platform) return false
  if (filters.type && provider.type !== filters.type) return false
  if (filters.status) {
    const now = Date.now()
    const rateLimitResetAt = provider.rate_limit_reset_at ? new Date(provider.rate_limit_reset_at).getTime() : Number.NaN
    const isRateLimited = Number.isFinite(rateLimitResetAt) && rateLimitResetAt > now
    const tempUnschedUntil = provider.temp_unschedulable_until ? new Date(provider.temp_unschedulable_until).getTime() : Number.NaN
    const isTempUnschedulable = Number.isFinite(tempUnschedUntil) && tempUnschedUntil > now

    if (filters.status === 'active') {
      if (provider.status !== 'active' || isRateLimited || isTempUnschedulable || !provider.schedulable) return false
    } else if (filters.status === 'rate_limited') {
      if (provider.status !== 'active' || !isRateLimited || isTempUnschedulable) return false
    } else if (filters.status === 'temp_unschedulable') {
      if (provider.status !== 'active' || !isTempUnschedulable || isRateLimited) return false
    } else if (filters.status === 'unschedulable') {
      if (provider.status !== 'active' || provider.schedulable || isRateLimited || isTempUnschedulable) return false
    } else if (provider.status !== filters.status) {
      return false
    }
  }
  if (filters.group) {
    const groupIds = provider.group_ids ?? provider.groups?.map((group) => group.id) ?? []
    if (filters.group === PROVIDER_UNGROUPED_GROUP_QUERY_VALUE) {
      if (groupIds.length > 0) return false
    } else if (!groupIds.includes(Number(filters.group))) {
      return false
    }
  }
  const privacyMode = typeof provider.extra?.privacy_mode === 'string' ? provider.extra.privacy_mode : ''
  if (filters.privacy_mode) {
    if (filters.privacy_mode === PROVIDER_PRIVACY_MODE_UNSET_QUERY_VALUE) {
      if (privacyMode.trim() !== '') return false
    } else if (privacyMode !== filters.privacy_mode) {
      return false
    }
  }
  const search = String(filters.search || '').trim().toLowerCase()
  if (search && !provider.name.toLowerCase().includes(search)) return false
  return true
}
const mergeRuntimeFields = (oldProvider: Provider, updatedProvider: Provider): Provider => ({
  ...updatedProvider,
  current_concurrency: updatedProvider.current_concurrency ?? oldProvider.current_concurrency,
  current_window_cost: updatedProvider.current_window_cost ?? oldProvider.current_window_cost,
  active_sessions: updatedProvider.active_sessions ?? oldProvider.active_sessions
})

const syncPaginationAfterLocalRemoval = () => {
  const nextTotal = Math.max(0, pagination.total - 1)
  pagination.total = nextTotal
  pagination.pages = nextTotal > 0 ? Math.ceil(nextTotal / pagination.page_size) : 0

  const maxPage = Math.max(1, pagination.pages || 1)

  if (pagination.page > maxPage) {
    pagination.page = maxPage
  }
  // 行被本地移除后不立刻全量补页，改为提示用户手动同步。
  hasPendingListSync.value = nextTotal > 0
}

const patchProviderInList = (updatedProvider: Provider) => {
  const index = providers.value.findIndex(provider => provider.id === updatedProvider.id)
  if (index === -1) return
  const mergedProvider = mergeRuntimeFields(providers.value[index], updatedProvider)
  if (!providerMatchesCurrentFilters(mergedProvider)) {
    providers.value = providers.value.filter(provider => provider.id !== mergedProvider.id)
    syncPaginationAfterLocalRemoval()
    removeSelectedProviders([mergedProvider.id])
    if (menu.acc?.id === mergedProvider.id) {
      menu.show = false
      menu.acc = null
    }
    return
  }
  const nextProviders = [...providers.value]
  nextProviders[index] = mergedProvider
  providers.value = nextProviders
  syncProviderRefs(mergedProvider)
}
const handleProviderUpdated = (updatedProvider: Provider) => {
  invalidateUpstreamUsageCache(updatedProvider.id)
  patchProviderInList(updatedProvider)
  enterAutoRefreshSilentWindow()
}
const formatExportTimestamp = () => {
  const now = new Date()
  const pad2 = (value: number) => String(value).padStart(2, '0')
  return `${now.getFullYear()}${pad2(now.getMonth() + 1)}${pad2(now.getDate())}${pad2(now.getHours())}${pad2(now.getMinutes())}${pad2(now.getSeconds())}`
}
const openExportDataDialog = () => {
  includeProxyOnExport.value = true
  showExportDataDialog.value = true
}
const handleExportData = async () => {
  if (exportingData.value) return
  exportingData.value = true
  try {
    const dataPayload = await providerExportStepUp.run(() => adminAPI.providers.exportData(
      selIds.value.length > 0
        ? { ids: selIds.value, includeProxies: includeProxyOnExport.value }
        : {
            includeProxies: includeProxyOnExport.value,
            filters: buildProviderQueryFilters()
          }
    ))
    const timestamp = formatExportTimestamp()
    const filename = `tokenrouter-provider-${timestamp}.json`
    const blob = new Blob([JSON.stringify(dataPayload, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = filename
    link.click()
    URL.revokeObjectURL(url)
    // spark 影子提供商被后端排除出备份(其凭据透传母提供商、调度配置不可经凭据型导入重建);
    // 存在跳过项时提示用户下载文件缺少的提供商数量。
    if (dataPayload.skipped_shadows && dataPayload.skipped_shadows > 0) {
      appStore.showWarning(t('admin.providers.dataExportedSkippedShadows', { count: dataPayload.skipped_shadows }))
    } else {
      appStore.showSuccess(t('admin.providers.dataExported'))
    }
  } catch (error: any) {
    if (isStepUpCancelled(error)) {
      // 用户主动取消 step-up 验证，静默返回，不弹错误提示。
    } else if (isStepUpBlocked(error)) {
      appStore.showError(
        stepUpBlockReason(error) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
          ? t('stepUp.adminApiKeyForbidden')
          : t('stepUp.notEnabled')
      )
    } else {
      appStore.showError(error?.message || t('admin.providers.dataExportFailed'))
    }
  } finally {
    exportingData.value = false
    showExportDataDialog.value = false
  }
}
const providerExportStepUp = useStepUp()
const closeTestModal = () => { showTest.value = false; testingAcc.value = null }
const closeStatsModal = () => { showStats.value = false; statsAcc.value = null }
const closeAdvancedSchedulerScoreModal = () => { showAdvancedSchedulerScore.value = false; advancedSchedulerScoreAcc.value = null }
const closeInviteResetModal = () => { showInviteReset.value = false; inviteResetAcc.value = null }
const closeReAuthModal = () => { showReAuth.value = false; reAuthAcc.value = null }
const handleTest = (a: Provider) => { testingAcc.value = a; showTest.value = true }
const handleViewStats = (a: Provider) => { statsAcc.value = a; showStats.value = true }
const handleAdvancedSchedulerScore = (a: Provider) => { advancedSchedulerScoreAcc.value = a; showAdvancedSchedulerScore.value = true }
const handleInviteReset = (a: Provider) => { inviteResetAcc.value = a; showInviteReset.value = true }
const handleSchedule = async (a: Provider) => {
  scheduleAcc.value = a
  scheduleModelOptions.value = []
  showSchedulePanel.value = true
  try {
    const models = await adminAPI.providers.getAvailableModels(a.id)
    scheduleModelOptions.value = models.map((m: ClaudeModel) => ({ value: m.id, label: m.id }))
  } catch {
    scheduleModelOptions.value = []
  }
}
const closeSchedulePanel = () => { showSchedulePanel.value = false; scheduleAcc.value = null; scheduleModelOptions.value = [] }
const handleReAuth = (a: Provider) => { reAuthAcc.value = a; showReAuth.value = true }
const duplicatingProviderIDs = new Set<number>()
const handleDuplicateProvider = async (a: Provider) => {
  if (duplicatingProviderIDs.has(a.id)) return
  duplicatingProviderIDs.add(a.id)
  try {
    const duplicate = await adminAPI.providers.duplicate(a.id)
    appStore.showSuccess(t('admin.providers.duplicateSuccess', { name: duplicate.name }))
    reload()
  } catch (error: any) {
    console.error('Failed to duplicate provider:', error)
    appStore.showError(error?.message || t('admin.providers.duplicateFailed'))
  } finally {
    duplicatingProviderIDs.delete(a.id)
  }
}
const handleRefresh = async (a: Provider) => {
  try {
    invalidateUpstreamUsageCache(a.id)
    const updated = await adminAPI.providers.refreshCredentials(a.id)
    patchProviderInList(updated)
    enterAutoRefreshSilentWindow()
  } catch (error) {
    console.error('Failed to refresh credentials:', error)
  }
}
const handleRecoverState = async (a: Provider) => {
  try {
    const updated = await adminAPI.providers.recoverState(a.id)
    patchProviderInList(updated)
    enterAutoRefreshSilentWindow()
    appStore.showSuccess(t('admin.providers.recoverStateSuccess'))
  } catch (error: any) {
    console.error('Failed to recover provider state:', error)
    appStore.showError(error?.message || t('admin.providers.recoverStateFailed'))
  }
}
const handleResetQuota = async (a: Provider) => {
  try {
    const updated = await adminAPI.providers.resetProviderQuota(a.id)
    patchProviderInList(updated)
    enterAutoRefreshSilentWindow()
    appStore.showSuccess(t('common.success'))
  } catch (error) {
    console.error('Failed to reset quota:', error)
  }
}
const privacyResultMessageKey = (provider: Provider): { type: 'success' | 'error'; key: string } => {
  const mode = typeof provider.extra?.privacy_mode === 'string' ? provider.extra.privacy_mode : ''
  if (provider.platform === 'openai') {
    switch (mode) {
      case 'training_off':
        return { type: 'success', key: 'admin.providers.privacyTrainingOff' }
      case 'training_set_cf_blocked':
        return { type: 'error', key: 'admin.providers.privacyCfBlocked' }
      default:
        return { type: 'error', key: 'admin.providers.privacyFailed' }
    }
  }
  if (provider.platform === 'antigravity') {
    if (mode === 'privacy_set') {
      return { type: 'success', key: 'admin.providers.privacyAntigravitySet' }
    }
    return { type: 'error', key: 'admin.providers.privacyAntigravityFailed' }
  }
  return { type: 'error', key: 'admin.providers.privacyFailed' }
}
const handleSetPrivacy = async (a: Provider) => {
  try {
    const updated = await adminAPI.providers.setPrivacy(a.id)
    patchProviderInList(updated)
    enterAutoRefreshSilentWindow()
    const result = privacyResultMessageKey(updated)
    if (result.type === 'success') {
      appStore.showSuccess(t(result.key))
    } else {
      appStore.showError(t(result.key))
    }
  } catch (error: any) {
    console.error('Failed to set privacy:', error)
    appStore.showError(error?.response?.data?.message || t('admin.providers.privacyFailed'))
  }
}
const onRevertFallback = async (a: Provider) => {
  try {
    await adminAPI.providers.revertProxyFallback(a.id)
    appStore.showSuccess(t('admin.providers.revertProxySuccess'))
    reload()
  } catch (error: any) {
    console.error('Failed to revert proxy fallback:', error)
    appStore.showError(error?.response?.data?.message || t('admin.providers.revertProxyFailed'))
  }
}
const handleCreateSparkShadow = (a: Provider) => {
  creatingShadowAcc.value = a
  showCreateShadowDialog.value = true
}
const confirmCreateSparkShadow = async () => {
  const a = creatingShadowAcc.value
  if (!a) return
  try {
    await adminAPI.providers.createSparkShadow(a.id, { name: `${a.name} (Spark)` })
    showCreateShadowDialog.value = false
    creatingShadowAcc.value = null
    appStore.showSuccess(t('admin.providers.createSparkShadowSuccess'))
    reload()
  } catch (error: any) {
    console.error('Failed to create spark shadow:', error)
    appStore.showError(error?.response?.data?.message || t('admin.providers.createSparkShadowFailed'))
  }
}
const handleDelete = (a: Provider) => { deletingAcc.value = a; showDeleteDialog.value = true }
const confirmDelete = async () => {
  if (!deletingAcc.value) return
  const providerID = deletingAcc.value.id
  try {
    invalidateUpstreamUsageCache(providerID)
    await adminAPI.providers.delete(providerID)
    showDeleteDialog.value = false
    deletingAcc.value = null
    reload()
  } catch (error) {
    console.error('Failed to delete provider:', error)
  }
}
const handleToggleSchedulable = async (a: Provider) => {
  const nextSchedulable = !a.schedulable
  togglingSchedulable.value = a.id
  try {
    const updated = await adminAPI.providers.setSchedulable(a.id, nextSchedulable)
    updateSchedulableInList([a.id], updated?.schedulable ?? nextSchedulable)
    enterAutoRefreshSilentWindow()
  } catch (error) {
    console.error('Failed to toggle schedulable:', error)
    appStore.showError(t('admin.providers.failedToToggleSchedulable'))
  } finally {
    togglingSchedulable.value = null
  }
}
const handleShowTempUnsched = (a: Provider) => { tempUnschedAcc.value = a; showTempUnsched.value = true }
const handleTempUnschedReset = async (updated: Provider) => {
  showTempUnsched.value = false
  tempUnschedAcc.value = null
  patchProviderInList(updated)
  enterAutoRefreshSilentWindow()
}
const formatExpiresAt = (value: number | null) => {
  if (!value) return '-'
  return formatDateTime(
    new Date(value * 1000),
    {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false
    },
    'sv-SE'
  )
}
const isExpired = (value: number | null) => {
  if (!value) return false
  return value * 1000 <= Date.now()
}
// 所绑定代理的有效期(逻辑同 /admin/proxies,见 utils/proxyExpiry)
const proxyExpiryBadge = (p: ProviderProxy): string => proxyExpiryBadgeClass(p.expires_at, p.status)
const proxyExpiryText = (p: ProviderProxy): string => {
  const { key, params } = proxyExpiryLabelKey(p.expires_at, p.status)
  return params ? t(key, params) : t(key)
}

// 滚动时关闭行内操作菜单，顶部下拉菜单保持打开但同步位置。
const handleScroll = () => {
  menu.show = false
  updateTopDropdownPositions()
}

const handleViewportResize = () => {
  updateTopDropdownPositions()
}

// 点击外部关闭顶部下拉菜单
const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as HTMLElement
  if (providerToolsDropdownRef.value && !providerToolsDropdownRef.value.contains(target)) {
    showProviderToolsDropdown.value = false
  }
  if (autoRefreshDropdownRef.value && !autoRefreshDropdownRef.value.contains(target)) {
    showAutoRefreshDropdown.value = false
  }
}

onMounted(async () => {
  if (typeof window !== 'undefined') {
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

  load()
  const [proxiesResult, groupsResult] = await Promise.allSettled([
    adminAPI.proxies.getAll(),
    adminAPI.groups.getAllIncludingInactive()
  ])
  if (proxiesResult.status === 'fulfilled') {
    proxies.value = proxiesResult.value
  } else {
    console.error('Failed to load proxies:', proxiesResult.reason)
  }
  if (groupsResult.status === 'fulfilled') {
    groups.value = groupsResult.value
  } else {
    console.error('Failed to load groups:', groupsResult.reason)
  }
  window.addEventListener('scroll', handleScroll, true)
  window.addEventListener('resize', handleViewportResize)
  document.addEventListener('click', handleClickOutside)

  if (autoRefreshEnabled.value) {
    autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
    resumeAutoRefresh()
  } else {
    pauseAutoRefresh()
  }
})

onUnmounted(() => {
  if (usageBatchFlushTimer !== null) {
    clearTimeout(usageBatchFlushTimer)
    usageBatchFlushTimer = null
  }
  pendingUsageBatchIds.clear()
  window.removeEventListener('scroll', handleScroll, true)
  window.removeEventListener('resize', handleViewportResize)
  document.removeEventListener('click', handleClickOutside)
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

<style scoped>
.provider-tools-menu-icon {
  @apply inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-control;
}
</style>
