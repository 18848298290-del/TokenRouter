<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-col gap-2">
          <div class="flex flex-col gap-2 2xl:flex-row 2xl:items-center 2xl:justify-between">
            <div
              class="grid w-full grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-[260px_160px_144px_152px] 2xl:w-auto"
            >
              <div class="min-w-0">
                <SearchInput
                  v-model="filters.taskName"
                  :placeholder="t('batchImage.filters.searchTaskName')"
                  class="w-full"
                  @search="applyFilters"
                />
              </div>
              <Select
                v-model="filters.apiKeyId"
                :options="apiKeyFilterOptions"
                class="w-full"
                @change="applyFilters"
              />
              <Select
                v-model="filters.status"
                :options="statusFilterOptions"
                class="w-full"
                @change="applyFilters"
              />
              <Select
                v-model="filters.downloaded"
                :options="downloadFilterOptions"
                class="w-full"
                @change="applyFilters"
              />
            </div>
            <div
              class="flex flex-wrap items-center justify-start gap-2 sm:justify-end 2xl:flex-shrink-0"
            >
              <button
                type="button"
                class="btn btn-secondary"
                :disabled="loadingJobs"
                @click="resetFilters"
              >
                {{ t('common.reset') }}
              </button>
              <button
                type="button"
                class="btn btn-secondary"
                :disabled="loadingKeys || loadingJobs"
                :title="t('common.refresh')"
                @click="refreshPage"
              >
                <Icon
                  name="refresh"
                  size="sm"
                  :class="loadingKeys || loadingJobs ? 'animate-spin' : ''"
                />
              </button>
              <button type="button" class="btn btn-secondary" @click="showGuideModal = true">
                <Icon name="book" size="sm" class="mr-2" />
                {{ t('batchImage.actions.usageGuide') }}
              </button>
              <button type="button" class="btn btn-primary" @click="openCreateModal">
                <Icon name="plus" size="sm" class="mr-2" />
                {{ t('batchImage.actions.createJob') }}
              </button>
            </div>
          </div>

          <div
            v-if="selectedJobIds.size"
            class="flex flex-wrap items-center justify-between gap-2 rounded-control border border-gray-200 bg-white px-3 py-2 shadow-sm dark:border-dark-700 dark:bg-dark-800"
          >
            <i18n-t
              keypath="batchImage.list.selectedJobs"
              tag="span"
              scope="global"
              :plural="selectedJobIds.size"
              class="text-sm text-gray-600 dark:text-gray-300"
            >
              <template #count>
                <span class="font-medium text-gray-900 dark:text-white">{{
                  selectedJobIds.size
                }}</span>
              </template>
            </i18n-t>
            <div class="flex flex-wrap items-center gap-2">
              <button
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="bulkDownloading || selectedDownloadableRows.length === 0"
                @click="downloadSelectedJobs"
              >
                <Icon
                  :name="bulkDownloading ? 'refresh' : 'download'"
                  size="sm"
                  class="mr-1.5"
                  :class="bulkDownloading ? 'animate-spin' : ''"
                />
                {{ t('batchImage.actions.downloadSelected') }}
              </button>
              <button
                type="button"
                class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400"
                :disabled="bulkDeleting"
                @click="deleteSelectedJobs"
              >
                <Icon
                  :name="bulkDeleting ? 'refresh' : 'trash'"
                  size="sm"
                  class="mr-1.5"
                  :class="bulkDeleting ? 'animate-spin' : ''"
                />
                {{ t('batchImage.actions.deleteRecords') }}
              </button>
            </div>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          column-order-storage-key="batch-image-column-order"
          :columns="columns"
          :data="visibleBatchJobs"
          :loading="loadingKeys || loadingJobs"
          :expandable-actions="false"
          row-key="id"
        >
          <template #header-select>
            <input
              type="checkbox"
              class="h-4 w-4 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="allVisibleSelected"
              :indeterminate="someVisibleSelected"
              @change="toggleAllVisible(($event.target as HTMLInputElement).checked)"
            />
          </template>

          <template #cell-select="{ row }">
            <input
              type="checkbox"
              class="h-4 w-4 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="selectedJobIds.has(row.id)"
              @change="toggleJobSelection(row.id, ($event.target as HTMLInputElement).checked)"
              @click.stop
            />
          </template>

          <template #cell-id="{ row }">
            <div class="flex w-[220px] items-start gap-1" :class="row.is_child ? 'pl-6' : ''">
              <button
                v-if="row.child_count > 0 && !row.is_child"
                type="button"
                class="mt-1 flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-control text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 focus:outline-none focus-visible:ring-2 focus-visible:ring-black/10 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-white dark:focus-visible:ring-primary-500/30"
                :title="
                  expandedParentIds.has(row.id)
                    ? t('batchImage.list.collapseChildren')
                    : t('batchImage.list.expandChildren', { n: row.child_count }, row.child_count)
                "
                @click.stop="toggleChildRows(row.id)"
              >
                <Icon
                  name="chevronRight"
                  class="transition-transform duration-normal"
                  :class="{ 'rotate-90': expandedParentIds.has(row.id) }"
                  size="xs"
                  :animate-on-hover="false"
                />
              </button>
              <span v-else class="w-6 flex-shrink-0" />
              <button
                type="button"
                class="min-w-0 flex-1 rounded-control py-1 text-left transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-black/10 dark:hover:bg-dark-700 dark:focus-visible:ring-primary-500/30"
                @click="selectJob(row.id)"
              >
                <span
                  class="flex min-w-0 items-center gap-2 text-sm font-medium"
                  :class="
                    row.task_name
                      ? 'text-gray-900 dark:text-white'
                      : 'text-gray-500 dark:text-gray-400'
                  "
                >
                  <span class="min-w-0 truncate">{{
                    row.task_name || defaultTaskName(row.created_at)
                  }}</span>
                  <span
                    v-if="row.child_count > 0 && !row.is_child"
                    class="flex-shrink-0 rounded-full bg-gray-100 px-2 py-0.5 text-xs font-normal text-gray-600 dark:bg-dark-700 dark:text-gray-300"
                  >
                    {{ t('batchImage.list.childCount', { n: row.child_count }, row.child_count) }}
                  </span>
                  <span
                    v-if="row.is_child"
                    class="flex-shrink-0 rounded-full bg-amber-50 px-2 py-0.5 text-xs font-normal text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
                  >
                    {{ t('batchImage.list.childBadge') }}
                  </span>
                </span>
                <span
                  class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400"
                >
                  <span>{{ formatDate(row.created_at) }}</span>
                </span>
              </button>
            </div>
          </template>

          <template #cell-model="{ row }">
            <div class="mx-auto max-w-[180px] text-center">
              <p class="truncate text-sm text-gray-700 dark:text-gray-300" :title="row.model">{{
                row.model
              }}</p>
            </div>
          </template>

          <template #cell-api_key_name="{ value }">
            <span class="block truncate text-center text-sm text-gray-700 dark:text-gray-300">
              {{ value || t('batchImage.list.keyNotRecorded') }}
            </span>
          </template>

          <template #cell-status="{ row }">
            <div class="flex justify-center">
              <span :class="statusBadgeClass(displayJob(row))" class="badge">
                {{ statusLabel(displayJob(row)) }}
              </span>
            </div>
          </template>

          <template #cell-counts="{ row }">
            <div class="flex items-center justify-center gap-2 text-sm tabular-nums">
              <span class="text-emerald-600 dark:text-emerald-300">{{
                displayJob(row).success_count
              }}</span>
              <span class="text-gray-300 dark:text-dark-500">/</span>
              <span
                :class="
                  displayJob(row).fail_count > 0
                    ? 'text-red-600 dark:text-red-300'
                    : 'text-gray-400 dark:text-gray-500'
                "
                >{{ displayJob(row).fail_count }}</span
              >
              <span class="text-xs text-gray-400 dark:text-gray-500">{{
                t('batchImage.list.totalCount', { n: displayJob(row).item_count })
              }}</span>
            </div>
          </template>

          <template #cell-cost="{ row }">
            <span class="block text-center text-sm text-gray-700 dark:text-gray-300">
              {{ costLabel(displayJob(row)) }}
            </span>
          </template>

          <template #cell-downloaded="{ row }">
            <span
              class="block text-center text-sm"
              :class="
                row.downloaded_at
                  ? 'text-emerald-700 dark:text-emerald-300'
                  : 'text-gray-500 dark:text-gray-400'
              "
            >
              {{
                row.downloaded_at
                  ? formatDate(row.downloaded_at)
                  : t('batchImage.list.notDownloaded')
              }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center justify-center gap-1">
              <button
                type="button"
                class="batch-row-action flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-black/10 dark:hover:bg-dark-700 dark:hover:text-primary-400 dark:focus-visible:ring-primary-500/30"
                :title="t('batchImage.actions.viewDetail')"
                @click="selectJob(row.id)"
              >
                <Icon name="eye" size="sm" />
                <span class="text-xs">{{ t('common.view') }}</span>
              </button>
              <button
                type="button"
                class="batch-row-action flex flex-col items-center gap-0.5 rounded-control p-1.5 transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-black/10 dark:focus-visible:ring-primary-500/30"
                :class="
                  canDownload(row)
                    ? 'text-gray-500 hover:bg-green-50 hover:text-green-600 dark:hover:bg-green-900/20 dark:hover:text-green-400'
                    : 'text-gray-300 dark:text-dark-500'
                "
                :disabled="!canDownload(row) || downloading"
                :title="t('batchImage.actions.downloadZip')"
                @click="downloadJob(row)"
              >
                <Icon
                  :name="isDownloadingJob(row.id) ? 'refresh' : 'download'"
                  size="sm"
                  :class="isDownloadingJob(row.id) ? 'animate-spin' : ''"
                />
                <span class="text-xs">{{ t('batchImage.actions.download') }}</span>
              </button>
              <div v-if="canRetry(row) || canDeleteRecord(row)">
                <button
                  type="button"
                  class="batch-row-action flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 focus:outline-none focus-visible:ring-2 focus-visible:ring-black/10 dark:hover:bg-dark-700 dark:hover:text-white dark:focus-visible:ring-primary-500/30"
                  :class="{
                    'bg-gray-100 text-gray-900 dark:bg-dark-700 dark:text-white':
                      openMoreJobId === row.id,
                  }"
                  :title="t('batchImage.actions.moreActions')"
                  @click.stop="toggleMoreMenu(row, $event)"
                >
                  <Icon name="more" size="sm" />
                  <span class="text-xs">{{ t('common.more') }}</span>
                </button>
              </div>
            </div>
          </template>

          <template #empty>
            <div
              class="flex min-h-[260px] flex-col items-center justify-center py-6 md:min-h-[300px]"
            >
              <Icon
                name="sparkles"
                size="xl"
                class="mb-4 h-12 w-12 text-gray-400 dark:text-dark-500"
              />
              <p class="text-lg font-medium text-gray-900 dark:text-gray-100">{{
                t('batchImage.list.empty')
              }}</p>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ t('batchImage.list.emptyHint') }}
              </p>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <div
          v-if="visibleBatchJobs.length > 0 || pagination.page > 1"
          class="batch-pagination-root flex flex-col gap-3 border-t border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800 lg:flex-row lg:items-center lg:justify-between lg:px-6"
        >
          <div class="flex flex-wrap items-center gap-3 text-sm text-gray-700 dark:text-gray-300">
            <i18n-t keypath="batchImage.pagination.pageNumber" tag="span" scope="global">
              <template #page>
                <span class="font-medium">{{ pagination.page }}</span>
              </template>
            </i18n-t>
            <i18n-t keypath="batchImage.pagination.pageItems" tag="span" scope="global">
              <template #count>
                <span class="font-medium">{{ visibleBatchJobs.length }}</span>
              </template>
            </i18n-t>
            <div class="flex items-center gap-2">
              <span>{{ t('pagination.perPage') }}</span>
              <Select
                v-model="pagination.page_size"
                :options="batchPageSizeOptions"
                class="w-24"
                @change="handlePageSizeChange"
              />
            </div>
          </div>
          <div class="flex items-center justify-end gap-2">
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="pagination.page <= 1 || loadingJobs"
              @click="handlePageChange(pagination.page - 1)"
            >
              <Icon name="chevronLeft" size="sm" class="mr-1" :animate-on-hover="false" />
              {{ t('pagination.previous') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="!pagination.has_more || loadingJobs"
              @click="handlePageChange(pagination.page + 1)"
            >
              {{ t('pagination.next') }}
              <Icon name="chevronRight" size="sm" class="ml-1" :animate-on-hover="false" />
            </button>
          </div>
        </div>
      </template>
    </TablePageLayout>

    <Teleport to="body">
      <div
        v-if="openMoreJobId"
        class="dropdown fixed z-teleport-tooltip w-44 overflow-hidden text-sm"
        :style="moreMenuStyle"
        @click.stop
      >
        <template v-for="job in batchJobs" :key="job.id">
          <template v-if="job.id === openMoreJobId">
            <button
              v-if="canRetry(job)"
              type="button"
              class="flex w-full items-center gap-2 px-3 py-2 text-left text-gray-700 transition-colors hover:bg-amber-50 hover:text-amber-700 disabled:opacity-60 dark:text-gray-200 dark:hover:bg-amber-900/20 dark:hover:text-amber-300"
              :disabled="retryingBatchId === job.id"
              @click="retryFailedJob(job)"
            >
              <Icon
                name="refresh"
                size="sm"
                :class="retryingBatchId === job.id ? 'animate-spin' : ''"
              />
              {{ t('batchImage.actions.retryFailedItems') }}
            </button>
            <button
              v-if="canDeleteRecord(job)"
              type="button"
              class="flex w-full items-center gap-2 px-3 py-2 text-left text-red-600 transition-colors hover:bg-red-50 disabled:opacity-60 dark:text-red-400 dark:hover:bg-red-900/20"
              :disabled="deletingBatchId === job.id"
              @click="deleteJob(job)"
            >
              <Icon
                :name="deletingBatchId === job.id ? 'refresh' : 'trash'"
                size="sm"
                :class="deletingBatchId === job.id ? 'animate-spin' : ''"
              />
              {{ t('batchImage.actions.deleteRecords') }}
            </button>
          </template>
        </template>
      </div>
    </Teleport>

    <Teleport to="body">
      <MotionTransition name="fade">
        <div
          v-if="promptPopover.visible"
          :inert="!promptPopover.visible || undefined"
          class="batch-prompt-popover fixed z-teleport-tooltip rounded-surface border border-gray-200 bg-white p-3 text-sm text-gray-800 shadow-xl ring-1 ring-black/5 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-100 dark:ring-white/10"
          :style="promptPopover.style"
          @mouseenter="cancelPromptPopoverClose"
          @mouseleave="schedulePromptPopoverClose"
        >
          <div class="mb-2 flex items-center justify-between gap-3">
            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">{{
              t('batchImage.promptPopover.title')
            }}</span>
            <button
              type="button"
              class="rounded-control px-2 py-1 text-xs font-medium text-primary-600 transition-colors hover:bg-primary-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/30 dark:text-primary-300 dark:hover:bg-primary-900/20"
              @click="copyPromptPopover"
            >
              {{ t('common.copy') }}
            </button>
          </div>
          <p
            class="max-h-48 overflow-y-auto whitespace-pre-wrap break-words leading-6 selection:bg-primary-100 selection:text-primary-900 dark:selection:bg-primary-900/60 dark:selection:text-primary-100"
          >
            {{ promptPopover.text }}
          </p>
        </div>
      </MotionTransition>
    </Teleport>

    <BaseDialog
      :show="!!currentJob"
      :title="t('batchImage.detail.title')"
      width="extra-wide"
      @close="closeDetail"
    >
      <div v-if="currentJob" class="space-y-4">
        <div
          class="rounded-control border border-gray-200 bg-gray-50/70 px-4 py-3 dark:border-dark-700 dark:bg-dark-900/40"
        >
          <div class="grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-4">
            <div class="min-w-0 text-center">
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('common.status') }}</p>
              <div class="mt-1 flex justify-center">
                <span
                  :class="statusBadgeClass(currentDisplayJob || currentJob)"
                  class="badge whitespace-nowrap"
                >
                  {{ statusLabel(currentDisplayJob || currentJob) }}
                </span>
              </div>
            </div>
            <div class="min-w-0 text-center">
              <p class="text-xs text-gray-500 dark:text-gray-400">{{
                hasChildJobs(currentJob.id)
                  ? t('batchImage.detail.aggregatedResult')
                  : t('batchImage.detail.result')
              }}</p>
              <p class="mt-1 flex items-center justify-center gap-2 font-medium tabular-nums">
                <span class="text-emerald-600 dark:text-emerald-300">{{
                  (currentDisplayJob || currentJob).success_count
                }}</span>
                <span class="text-gray-300 dark:text-dark-500">/</span>
                <span
                  :class="
                    (currentDisplayJob || currentJob).fail_count > 0
                      ? 'text-red-600 dark:text-red-300'
                      : 'text-gray-400 dark:text-gray-500'
                  "
                  >{{ (currentDisplayJob || currentJob).fail_count }}</span
                >
              </p>
            </div>
            <div class="min-w-0 text-center">
              <p class="text-xs text-gray-500 dark:text-gray-400">{{
                t('batchImage.detail.cost')
              }}</p>
              <p class="mt-1 truncate font-medium text-gray-900 dark:text-white">{{
                costLabel(currentDisplayJob || currentJob)
              }}</p>
            </div>
            <div class="min-w-0 text-center">
              <p class="text-xs text-gray-500 dark:text-gray-400">{{
                t('batchImage.detail.downloadStatus')
              }}</p>
              <p class="mt-1 truncate font-medium text-gray-900 dark:text-white">
                {{
                  currentJob.downloaded_at
                    ? formatDate(currentJob.downloaded_at)
                    : t('batchImage.list.notDownloaded')
                }}
              </p>
            </div>
          </div>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-3">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{
            t('batchImage.detail.items')
          }}</h3>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="refreshing || loadingItems"
            @click="refreshDetail"
          >
            <Icon
              name="refresh"
              size="sm"
              class="mr-1.5"
              :class="refreshing || loadingItems ? 'animate-spin' : ''"
            />
            {{ t('common.refresh') }}
          </button>
        </div>

        <div
          v-if="items.length || loadingItems"
          class="overflow-x-auto rounded-control border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
        >
          <table
            :aria-busy="loadingItems && !items.length"
            class="w-full min-w-[860px] table-fixed divide-y divide-gray-200 text-sm dark:divide-dark-700"
          >
            <colgroup>
              <col class="w-[18%]" />
              <col class="w-[34%]" />
              <col class="w-[12%]" />
              <col class="w-[10%]" />
              <col class="w-[26%]" />
            </colgroup>
            <thead class="bg-gray-50 dark:bg-dark-800/80">
              <tr>
                <th
                  class="px-3 py-3 text-center text-sm font-medium text-gray-500 dark:text-gray-400"
                  >Custom ID</th
                >
                <th class="px-3 py-3 text-left text-sm font-medium text-gray-500 dark:text-gray-400"
                  >Prompt</th
                >
                <th
                  class="px-3 py-3 text-center text-sm font-medium text-gray-500 dark:text-gray-400"
                  >{{ t('common.status') }}</th
                >
                <th
                  class="px-3 py-3 text-center text-sm font-medium text-gray-500 dark:text-gray-400"
                  >{{ t('batchImage.detail.preview') }}</th
                >
                <th
                  class="px-3 py-3 text-center text-sm font-medium text-gray-500 dark:text-gray-400"
                  >{{ t('batchImage.detail.result') }}</th
                >
              </tr>
            </thead>
            <TableSkeletonBody
              v-if="loadingItems && !items.length"
              :columns="5"
              cell-class="px-3 py-3"
            />
            <tbody v-else class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr
                v-for="item in items"
                :key="itemPreviewKey(item)"
                class="align-middle"
                :class="detailItemRowClass(item)"
              >
                <td class="px-3 py-2.5 text-center">
                  <span
                    class="block min-w-0 truncate font-mono text-sm"
                    :class="
                      isRecoveredOriginalFailure(item)
                        ? 'text-gray-400 dark:text-gray-500'
                        : 'text-gray-900 dark:text-white'
                    "
                    :title="item.custom_id"
                  >
                    {{ item.custom_id }}
                  </span>
                </td>
                <td
                  class="px-3 py-2.5 text-left"
                  :class="
                    isRecoveredOriginalFailure(item)
                      ? 'text-gray-400 dark:text-gray-500'
                      : 'text-gray-700 dark:text-gray-300'
                  "
                >
                  <div
                    class="batch-prompt-trigger cursor-default truncate rounded-compact px-1 text-sm leading-6 focus:outline-none"
                    tabindex="0"
                    @pointerenter="schedulePromptPopoverOpen($event, item.prompt_preview || '-')"
                    @pointerleave="schedulePromptPopoverClose"
                    @mouseenter="schedulePromptPopoverOpen($event, item.prompt_preview || '-')"
                    @mouseleave="schedulePromptPopoverClose"
                    @click="showPromptPopover($event, item.prompt_preview || '-')"
                    @focus="showPromptPopover($event, item.prompt_preview || '-')"
                    @focusin="showPromptPopover($event, item.prompt_preview || '-')"
                    @blur="schedulePromptPopoverClose"
                  >
                    {{ item.prompt_preview || '-' }}
                  </div>
                </td>
                <td class="px-3 py-2.5 text-center">
                  <span
                    :class="itemDisplayStatusBadgeClass(item)"
                    class="badge max-w-full truncate whitespace-nowrap"
                    :title="itemDisplayStatusLabel(item)"
                  >
                    {{ itemDisplayStatusLabel(item) }}
                  </span>
                </td>
                <td class="px-3 py-2.5 text-center">
                  <div
                    class="mx-auto h-12 w-12 overflow-hidden rounded-control border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-800"
                  >
                    <button
                      v-if="
                        itemPreviewUrls[itemPreviewKey(item)] &&
                        !previewErrorIds.has(itemPreviewKey(item))
                      "
                      type="button"
                      class="block h-full w-full overflow-hidden"
                      :title="t('batchImage.detail.previewZoom', { id: item.custom_id })"
                      @click="openImagePreview(item)"
                    >
                      <img
                        :src="itemPreviewUrls[itemPreviewKey(item)]"
                        class="h-full w-full object-cover"
                        alt=""
                        @error="handlePreviewError(itemPreviewKey(item))"
                      />
                    </button>
                    <button
                      v-else-if="canLoadItemPreview(item)"
                      type="button"
                      class="flex h-full w-full items-center justify-center text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 disabled:cursor-wait disabled:opacity-70 dark:text-gray-400 dark:hover:bg-dark-700"
                      :disabled="previewLoadingIds.has(itemPreviewKey(item))"
                      :title="
                        previewErrorIds.has(itemPreviewKey(item))
                          ? t('batchImage.detail.previewReload')
                          : t('batchImage.detail.previewLoad')
                      "
                      @click="loadItemPreview(item)"
                    >
                      <Icon
                        :name="previewLoadingIds.has(itemPreviewKey(item)) ? 'refresh' : 'eye'"
                        size="sm"
                        :class="previewLoadingIds.has(itemPreviewKey(item)) ? 'animate-spin' : ''"
                      />
                    </button>
                    <div
                      v-else
                      class="flex h-full w-full items-center justify-center text-gray-400"
                      :title="
                        item.image_count > 0
                          ? t('batchImage.detail.previewUnavailable')
                          : t('batchImage.detail.noImage')
                      "
                    >
                      <Icon name="document" size="sm" />
                    </div>
                  </div>
                </td>
                <td class="px-3 py-2.5 text-center">
                  <span
                    class="inline-flex max-w-full items-center justify-center truncate rounded-compact px-2.5 py-1 text-xs font-medium leading-5 ring-1 ring-inset"
                    :class="itemResultClass(item)"
                    :title="itemResultLabel(item)"
                  >
                    {{ itemResultLabel(item) }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div
          v-else
          class="rounded-control border border-dashed border-gray-200 py-10 text-center dark:border-dark-700"
        >
          <Icon name="refresh" size="lg" class="mx-auto mb-3 text-gray-400" />
          <p class="text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('batchImage.detail.noItems') }}
          </p>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('batchImage.detail.noItemsHint') }}
          </p>
        </div>
      </div>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="!currentJob || !canCancel(currentJob) || cancelling"
            @click="cancelSelected"
          >
            <Icon
              v-if="cancelling"
              name="refresh"
              size="sm"
              class="mr-2 animate-spin"
              :animate-on-hover="false"
            />
            {{ t('batchImage.actions.cancelJob') }}
          </button>
          <button
            v-if="currentJob && currentDisplayJob && canRetry(currentDisplayJob)"
            type="button"
            class="btn btn-secondary inline-flex min-w-[116px] items-center justify-center"
            :disabled="retryingBatchId === currentJob.id"
            @click="retrySelected"
          >
            <Icon
              name="refresh"
              size="sm"
              class="mr-2"
              :class="currentJob && retryingBatchId === currentJob.id ? 'animate-spin' : ''"
            />
            {{ t('batchImage.actions.retryFailedItems') }}
          </button>
          <button
            type="button"
            class="btn btn-primary inline-flex min-w-[112px] items-center justify-center"
            :disabled="!currentJob || !canDownload(currentJob) || downloading"
            @click="downloadSelected"
          >
            <Icon
              :name="currentJob && isDownloadingJob(currentJob.id) ? 'refresh' : 'download'"
              size="sm"
              class="mr-2"
              :class="currentJob && isDownloadingJob(currentJob.id) ? 'animate-spin' : ''"
            />
            {{ t('batchImage.actions.downloadZip') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="!!previewImageItem"
      :title="previewImageItem?.custom_id || t('batchImage.imagePreview.title')"
      width="extra-wide"
      :z-index="60"
      @close="closeImagePreview"
    >
      <div class="space-y-3">
        <div
          class="rounded-control border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-100"
        >
          {{ t('batchImage.imagePreview.notice') }}
        </div>
        <div
          class="flex min-h-[420px] items-center justify-center rounded-control bg-gray-50 p-4 dark:bg-dark-900"
        >
          <img
            v-if="previewImageUrl"
            :src="previewImageUrl"
            class="max-h-[70vh] max-w-full rounded-control object-contain"
            :alt="previewImageItem?.custom_id || ''"
          />
        </div>
      </div>
    </BaseDialog>

    <BaseDialog
      :show="showCreateModal"
      :title="t('batchImage.create.title')"
      width="wide"
      @close="closeCreateModal"
    >
      <form class="space-y-5" @submit.prevent="submitJob">
        <div class="grid gap-4 md:grid-cols-2">
          <div class="md:col-span-2">
            <label class="input-label">{{ t('batchImage.create.taskName') }}</label>
            <input
              v-model="form.taskName"
              type="text"
              maxlength="255"
              class="input"
              :placeholder="t('batchImage.create.taskNamePlaceholder')"
            />
          </div>

          <div class="md:col-span-2">
            <label class="input-label">API Key</label>
            <Select
              v-model="form.apiKeyId"
              :options="createApiKeyOptions"
              :disabled="loadingKeys"
              class="w-full"
            />
            <p
              v-if="!loadingKeys && geminiApiKeys.length === 0"
              class="input-hint text-amber-600 dark:text-amber-400"
            >
              {{ t('batchImage.create.noKeysHint') }}
            </p>
          </div>

          <div>
            <label class="input-label">{{ t('batchImage.create.model') }}</label>
            <Select
              v-model="form.model"
              :options="createModelOptions"
              :disabled="loadingModels || availableBatchImageModels.length === 0"
              class="w-full"
            />
            <p v-if="modelLoadError" class="input-hint text-amber-600 dark:text-amber-400">
              {{ modelLoadError }}
            </p>
            <p
              v-else-if="selectedApiKey && !loadingModels && availableBatchImageModels.length === 0"
              class="input-hint text-amber-600 dark:text-amber-400"
            >
              {{ batchImageText('noModelsHint') }}
            </p>
          </div>

          <div>
            <label class="input-label">{{ t('batchImage.create.imageSize') }}</label>
            <div
              class="input flex items-center bg-gray-50 text-gray-600 dark:bg-dark-900 dark:text-gray-300"
            >
              1K
            </div>
            <p class="input-hint">{{ t('batchImage.create.imageSizeHint') }}</p>
          </div>

          <div>
            <label class="input-label">{{ t('batchImage.create.outputFormat') }}</label>
            <Select
              v-model="form.responseMimeType"
              :options="responseMimeTypeOptions"
              class="w-full"
            />
          </div>

          <div>
            <label class="input-label">{{ t('batchImage.create.estimatedOutput') }}</label>
            <div
              class="input flex items-center bg-gray-50 text-gray-600 dark:bg-dark-900 dark:text-gray-300"
            >
              {{
                t('batchImage.create.estimatedOutputValue', {
                  images: estimatedOutputCount,
                  prompts: promptRows.length,
                })
              }}
            </div>
          </div>
        </div>

        <div class="space-y-3">
          <div class="flex items-center justify-between gap-3">
            <label class="input-label mb-0">Prompt</label>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{
              t('batchImage.create.promptAdded', { count: promptRows.length })
            }}</span>
          </div>
          <div class="rounded-control border border-gray-200 p-3 dark:border-dark-700">
            <textarea
              v-model="promptDraft"
              rows="3"
              class="input h-[76px] resize-y px-3 py-2 leading-5"
              :placeholder="t('batchImage.create.promptPlaceholder')"
            />
            <div
              class="mt-2 grid gap-2 md:grid-cols-[minmax(0,1fr)_112px_132px_112px] md:items-center"
            >
              <input
                v-model="customIdDraft"
                type="text"
                maxlength="255"
                class="input text-sm"
                :placeholder="t('batchImage.create.customIdPlaceholder')"
              />
              <Select
                v-model="outputCountDraft"
                :options="outputCountSelectOptions"
                class="w-full"
              />
              <label
                class="btn btn-secondary cursor-pointer justify-center text-sm"
                :class="
                  referenceImageDrafts.length >= selectedModelReferenceLimit
                    ? 'pointer-events-none opacity-60'
                    : ''
                "
              >
                <Icon name="upload" size="sm" class="mr-1.5" />
                {{ t('batchImage.create.referenceImage') }}
                <input
                  type="file"
                  accept="image/png,image/jpeg,image/webp"
                  multiple
                  class="hidden"
                  :disabled="referenceImageDrafts.length >= selectedModelReferenceLimit"
                  @change="handleReferenceImageFiles"
                />
              </label>
              <button
                type="button"
                class="btn btn-secondary justify-center whitespace-nowrap px-4 text-sm"
                :disabled="!promptDraft.trim()"
                @click="addPromptRow"
              >
                <Icon name="plus" size="sm" class="mr-1.5" />
                {{ t('common.add') }}
              </button>
            </div>
            <div v-if="referenceImageDrafts.length" class="mt-3 flex flex-wrap gap-2">
              <span
                v-for="(ref, refIndex) in referenceImageDrafts"
                :key="`${ref.name}-${refIndex}`"
                class="inline-flex max-w-full items-center gap-1 rounded-compact border border-gray-200 bg-gray-50 px-2 py-1 text-xs text-gray-700 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-200"
              >
                <span class="max-w-[180px] truncate">{{ ref.name }}</span>
                <button
                  type="button"
                  class="text-gray-400 hover:text-red-600"
                  :title="t('batchImage.create.removeReferenceImage')"
                  @click="removeReferenceImageDraft(refIndex)"
                >
                  <Icon name="x" size="xs" />
                </button>
              </span>
            </div>
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
              {{
                t('batchImage.create.limitsHint', {
                  maxPerItem: BATCH_IMAGE_MAX_OUTPUTS_PER_ITEM,
                  maxPerJob: BATCH_IMAGE_MAX_OUTPUTS_PER_JOB,
                  refLimit: selectedModelReferenceLimit,
                })
              }}
            </p>
          </div>
          <div
            v-if="promptRows.length"
            class="overflow-hidden rounded-control border border-gray-200 dark:border-dark-700"
          >
            <div
              v-for="(row, index) in promptRows"
              :key="row.localId"
              class="flex items-center gap-3 border-b border-gray-100 px-3 py-2 last:border-b-0 dark:border-dark-700"
            >
              <span class="w-20 flex-shrink-0 font-mono text-xs text-gray-500 dark:text-gray-400">{{
                row.custom_id
              }}</span>
              <p class="min-w-0 flex-1 truncate text-sm text-gray-800 dark:text-gray-100">{{
                row.prompt
              }}</p>
              <span
                v-if="row.output_count > 1"
                class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400"
              >
                x{{ row.output_count }}
              </span>
              <span
                v-if="row.reference_images.length"
                class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400"
              >
                {{
                  t(
                    'batchImage.create.referenceCount',
                    { n: row.reference_images.length },
                    row.reference_images.length,
                  )
                }}
              </span>
              <button
                type="button"
                class="btn-ghost btn-icon flex-shrink-0 text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"
                :title="t('common.delete')"
                @click="removePromptRow(index)"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </div>
          <div
            v-else
            class="rounded-control border border-dashed border-gray-200 px-3 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400"
          >
            {{ t('batchImage.create.noPrompts') }}
          </div>
        </div>

        <div
          class="rounded-control border border-amber-200 bg-amber-50 p-3 text-sm leading-6 text-amber-900 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-100"
        >
          {{ t('batchImage.create.cancelNotice') }}
        </div>
        <div
          v-if="submitting"
          class="rounded-control border border-sky-200 bg-sky-50 p-3 text-sm leading-6 text-sky-800 dark:border-sky-800 dark:bg-sky-950/30 dark:text-sky-100"
        >
          {{ t('batchImage.create.submittingNotice') }}
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="submitting"
            @click="closeCreateModal"
            >{{ t('common.cancel') }}</button
          >
          <button
            type="button"
            class="btn btn-primary inline-flex min-w-[120px] justify-center"
            :disabled="
              submitting ||
              loadingModels ||
              (parsedItems.length === 0 && !promptDraft.trim()) ||
              !selectedApiKey ||
              !form.model
            "
            @click="submitJob"
          >
            <Icon
              v-if="submitting"
              name="refresh"
              size="sm"
              class="mr-2 animate-spin"
              :animate-on-hover="false"
            />
            {{ submitting ? t('common.submitting') : t('batchImage.actions.submitJob') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="showGuideModal"
      :title="t('batchImage.guide.title')"
      width="wide"
      @close="showGuideModal = false"
    >
      <div class="space-y-5">
        <section class="space-y-3">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{
            t('batchImage.guide.uiTitle')
          }}</h3>
          <div
            class="rounded-surface border border-gray-200 bg-gray-50 p-3 text-sm leading-6 text-gray-700 dark:border-dark-700 dark:bg-dark-900/50 dark:text-gray-200"
          >
            <p>{{ t('batchImage.guide.step1') }}</p>
            <p>{{ t('batchImage.guide.step2') }}</p>
            <p>{{ t('batchImage.guide.step3') }}</p>
            <p>{{ t('batchImage.guide.step4') }}</p>
          </div>
        </section>
        <section class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{
              t('batchImage.guide.skillTitle')
            }}</h3>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{
              t('batchImage.guide.skillDesc')
            }}</p>
          </div>
          <textarea
            :value="agentInstruction"
            readonly
            class="min-h-[420px] w-full resize-y rounded-control border border-primary-900/10 bg-gray-50 p-4 font-mono text-sm leading-6 text-gray-800 outline-none focus:border-primary-900/10 focus:ring-2 focus:ring-black/10 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-100 dark:focus:border-primary-500 dark:focus:ring-primary-900/40"
          />
        </section>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="showGuideModal = false">{{
            t('common.close')
          }}</button>
          <button type="button" class="btn btn-primary" @click="copyInstruction">
            <Icon name="copy" size="sm" class="mr-2" />
            {{ t('batchImage.actions.copyInstruction') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { useLocaleRefresh } from '@/composables/useLocaleRefresh'
import { getLocale } from '@/i18n'
import TableSkeletonBody from '@/components/common/TableSkeletonBody.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/table/DataTable.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import Icon from '@/components/icons/Icon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { useClipboard } from '@/composables/useClipboard'
import { getPersistedPageSize, setPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useAppStore } from '@/stores/app'
import { keysAPI } from '@/api'
import {
  cancelBatchImageJob,
  deleteBatchImageJobRecord,
  downloadBatchImageZip,
  getBatchImageItemContent,
  getBatchImageJob,
  listBatchImageJobs,
  listBatchImageItems,
  listBatchImageModels,
  saveBlob,
  submitBatchImageJob,
  type BatchImageItem,
  type BatchImageJob,
  type BatchImageJobsListOptions,
  type BatchImageReferenceImage,
  type BatchImageStatus,
  type BatchImageSubmitItem,
} from '@/api/batchImage'
import type { ApiKey } from '@/api/keys.types'
import type { Column } from '@/components/common/table/types'
import {
  createPreviewCache,
  previewCacheKey,
  previewCacheSupported,
} from './batch-image/previewCache'
import { createPreviewUrls, createThumbnailBlob } from './batch-image/previewImages'

// BatchImageGuideView.vue manages batch image creation, job browsing, retry
// details, and on-demand image previews. Eligible API keys determine model
// discovery and authenticate each job operation. The page retains separate
// creation and selected-detail key identities so table actions and open
// details can resolve the credential attached to a listed job.
//
// Root jobs combine their visible retry children for displayed counts and
// costs, while item records retain their source batch identifiers for
// downloads and cache keys. Page-local modules own thumbnail encoding,
// persistent retention, and published browser URLs. The page coordinates
// requests, polling, draft state, and resource cleanup; asynchronous job and
// preview completions retain the completion ordering of their individual
// handlers.

// BatchImageJobRow combines server job fields with table credential and retry
// metadata.
type BatchImageJobRow = Pick<
  BatchImageJob,
  | 'id'
  | 'task_name'
  | 'parent_batch_id'
  | 'status'
  | 'model'
  | 'platform'
  | 'item_count'
  | 'success_count'
  | 'fail_count'
  | 'estimated_cost'
  | 'hold_amount'
  | 'actual_cost'
  | 'created_at'
  | 'downloaded_at'
> & {
  // api_key_id and api_key_name identify the key used to fetch this row.
  // Job actions resolve that identity against currently eligible keys before sending requests.
  api_key_id: number // owning API key identifier
  api_key_name: string // owning API key label
  child_count: number // listed retry child count
  // is_child is set when flattening expanded parents, allowing table rendering
  // to indent children while stored job rows retain their server identities.
  is_child?: boolean
}

// BatchImageDetailItem attaches its source job identity and label to API
// items.
type BatchImageDetailItem = BatchImageItem & {
  // batch_id and source_task_name are attached from the job whose items were
  // loaded, keeping combined parent and retry details attributable to their source.
  batch_id: string // owning job identifier
  source_task_name: string // localized source task label
}

// PromptRow holds one queued prompt and its prepared submission attachments.
type PromptRow = {
  localId: string // local rendering row identity
  // custom_id is sanitized and deduplicated again during payload construction,
  // giving the collected submission rows normalized, unique identifiers within one batch.
  custom_id: string
  prompt: string // complete user prompt text
  // output_count is normalized when the row is added and when submission
  // items are built, limiting each prompt's requested repetitions to one through four.
  output_count: number
  // reference_images retains API attachments copied from the editor when a row
  // is added, independently of the editor drafts cleared immediately after insertion.
  reference_images: BatchImageReferenceImage[]
}

// ReferenceImageDraft adds browser file metadata to a prepared API reference
// image.
type ReferenceImageDraft = BatchImageReferenceImage & {
  name: string // uploaded browser file name
  size: number // bytes
}

// Terminal statuses control polling and available job actions, while output
// and page size limits constrain the creation and list controls.
const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled', 'output_deleted'])
const BATCH_IMAGE_MAX_OUTPUTS_PER_ITEM = 4
const BATCH_IMAGE_MAX_OUTPUTS_PER_JOB = 200
const outputCountOptions = Array.from(
  { length: BATCH_IMAGE_MAX_OUTPUTS_PER_ITEM },
  (_, index) => index + 1,
)
const responseMimeTypeOptions: SelectOption[] = [
  { value: 'image/png', label: 'PNG' },
  { value: 'image/jpeg', label: 'JPEG' },
  { value: 'image/webp', label: 'WebP' },
]
const batchPageSizeOptions: SelectOption[] = [20, 50, 100].map((size) => ({
  value: size,
  label: String(size),
}))

// Shared app, clipboard, balance, and translation services supply page
// notifications, localized display values, and browser copy operations.
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()
const { formatBalanceAmount } = useBalanceDisplay()
const { t, locale } = useI18n()
// Localized output options, table columns, and status filters recompute their
// display labels when the interface language changes.
const outputCountSelectOptions = computed<SelectOption[]>(() =>
  outputCountOptions.map((count) => ({
    value: count,
    label: t('batchImage.create.outputCountOption', { n: count }, count),
  })),
)

const columns = computed<Column[]>(() => [
  { key: 'select', label: '', sortable: false },
  {
    key: 'id',
    label: t('batchImage.columns.taskName'),
    sortable: false,
    class: 'w-[240px] max-w-[240px]',
  },
  {
    key: 'model',
    label: t('batchImage.columns.model'),
    sortable: false,
    class: 'w-[180px] max-w-[180px] text-center',
  },
  {
    key: 'api_key_name',
    label: t('batchImage.columns.apiKey'),
    sortable: false,
    class: 'w-40 max-w-40 text-center',
  },
  { key: 'status', label: t('common.status'), sortable: false, class: 'w-28 text-center' },
  {
    key: 'counts',
    label: t('batchImage.columns.result'),
    sortable: false,
    class: 'w-32 text-center',
  },
  { key: 'cost', label: t('batchImage.columns.cost'), sortable: false, class: 'w-36 text-center' },
  {
    key: 'downloaded',
    label: t('batchImage.columns.downloadStatus'),
    sortable: false,
    class: 'w-40 text-center',
  },
  { key: 'actions', label: t('common.actions'), sortable: false, class: 'w-40 text-center' },
])

const statusFilterOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('batchImage.filters.allStatuses') },
  { value: 'queued', label: t('batchImage.status.queued') },
  { value: 'running', label: t('batchImage.status.running') },
  { value: 'processing_results', label: t('batchImage.status.processingResults') },
  { value: 'settling', label: t('batchImage.status.settling') },
  { value: 'completed', label: t('batchImage.status.completed') },
  { value: 'failed', label: t('batchImage.status.failed') },
  { value: 'cancelled', label: t('batchImage.status.cancelled') },
  { value: 'output_deleted', label: t('batchImage.status.outputDeleted') },
])

const downloadFilterOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('batchImage.filters.allDownloadStates') },
  { value: 'true', label: t('batchImage.filters.downloaded') },
  { value: 'false', label: t('batchImage.filters.notDownloaded') },
])

// Creation fields retain the selected credential and model across dialog
// resets, while task and output format values belong to the submitted draft.
const form = reactive({
  // apiKeyId selects model discovery and submission credentials, while model
  // is cleared before each catalog request and filled from its first valid result.
  apiKeyId: 0, // selected API key identifier
  taskName: '', // optional submitted task label
  model: '', // selected catalog model identifier
  responseMimeType: 'image/png', // requested output MIME type
})

// List filters apply independently of the creation form and become query
// options or eligible key selections when the list reloads.
const filters = reactive({
  taskName: '', // task name search text
  apiKeyId: '', // optional API key filter
  status: '', // optional server status filter
  downloaded: '', // optional download state filter
})

// Pagination combines a local page number and saved page size with the server
// indication that another page exists.
const pagination = reactive({
  page: 1, // one-based visible page number
  // page_size starts from the shared preference capped to this endpoint's
  // limit, and later selection changes persist the normalized preference for other tables.
  page_size: Math.min(getPersistedPageSize(20), 100),
  has_more: false, // another server page exists
})

// Loaded credentials and operation flags belong to this page instance and
// drive loading indicators, row action availability, and request suppression.
const apiKeys = ref<ApiKey[]>([])
const loadingKeys = ref(false)
const loadingJobs = ref(false)
const submitting = ref(false)
const refreshing = ref(false)
const cancelling = ref(false)
const downloading = ref(false)
const downloadingBatchId = ref('')
const retryingBatchId = ref('')
const bulkDownloading = ref(false)
const bulkDeleting = ref(false)
const deletingBatchId = ref('')
const loadingItems = ref(false)
const loadingModels = ref(false)
// Dialog visibility and selected detail state drive page overlays, while
// selectedBatchApiKeyId retains the credential attached to the opened job.
const showCreateModal = ref(false)
const showGuideModal = ref(false)
const currentJob = ref<BatchImageJob | null>(null)
const selectedBatchId = ref('')
const selectedBatchApiKeyId = ref(0)
// Detail items and listed jobs retain server identities; selection and parent
// expansion sets determine bulk actions and visible child rows.
const items = ref<BatchImageDetailItem[]>([])
const batchJobs = ref<BatchImageJobRow[]>([])
const selectedJobIds = ref(new Set<string>())
const expandedParentIds = ref(new Set<string>())
// Prompt rows hold committed creation entries, while the adjacent editor
// fields hold the next prompt and its reference attachments.
const promptRows = ref<PromptRow[]>([])
const promptDraft = ref('')
const customIdDraft = ref('')
const outputCountDraft = ref(1)
const referenceImageDrafts = ref<ReferenceImageDraft[]>([])
// Preview URL resources and cache operations are page-local instances;
// template URLs share the resource collection, and storage persists across
// page visits.
const previewUrls = createPreviewUrls()
const itemPreviewUrls = previewUrls.urls
const { getCachedPreviewBlob, putCachedPreviewBlob, cleanupPreviewCache } = createPreviewCache()
// Preview loading and error sets use encoded batch item identities, while
// previewImageItem controls which available image opens in the modal.
const previewLoadingIds = ref(new Set<string>())
const previewErrorIds = ref(new Set<string>())
const previewImageItem = ref<BatchImageItem | null>(null)
// The current model catalog and load error follow the selected creation key,
// and row menu state controls one floating action menu.
const availableBatchImageModels = ref<
  Array<{
    value: string // requested model identifier
    label: string // displayed model identifier
  }>
>([])
const modelLoadError = ref('')
const openMoreJobId = ref('')
const moreMenuStyle = ref<Record<string, string>>({})
// The prompt popover combines visibility, displayed text, and viewport
// position for the currently active table prompt trigger.
const promptPopover = reactive({
  visible: false, // prompt popover display state
  text: '', // complete displayed prompt text
  style: {} as Record<string, string>, // fixed viewport position styles
})
// Model request generations reject stale catalog responses; interval and
// popover handles retain resources until their owning cleanup operation runs.
let modelRequestSeq = 0
let pollTimer: ReturnType<typeof setInterval> | null = null
let previewCacheCleanupTimer: ReturnType<typeof setInterval> | null = null
let promptPopoverCloseTimer: ReturnType<typeof setTimeout> | null = null
let promptPopoverOpenTimer: ReturnType<typeof setTimeout> | null = null
let activePromptPopoverTarget: HTMLElement | null = null

// Batch-capable keys require active status and the image_batches group
// protocol, and option lists expose those eligible credentials to page
// controls.
const geminiApiKeys = computed(() =>
  apiKeys.value.filter(
    (key) =>
      key.status === 'active' && key.group?.allowed_protocols?.includes('image_batches') === true,
  ),
)

const createApiKeyOptions = computed<SelectOption[]>(() => [
  {
    value: 0,
    label: loadingKeys.value
      ? t('batchImage.create.loadingKeys')
      : t('batchImage.create.selectKeyPlaceholder'),
    disabled: true,
  },
  ...geminiApiKeys.value.map((key) => ({
    value: key.id,
    label: `${key.name} · ${key.group?.display_name || key.group?.name || 'Gemini'}`,
  })),
])

// Model selector options display a disabled loading or empty entry until the
// selected key has returned a usable catalog.
const createModelOptions = computed<SelectOption[]>(() => {
  if (loadingModels.value) {
    return [{ value: '', label: batchImageText('loadingModels'), disabled: true }]
  }
  if (availableBatchImageModels.value.length === 0) {
    return [{ value: '', label: batchImageText('noModels'), disabled: true }]
  }
  return availableBatchImageModels.value
})

// Creation and list key selections resolve independently against eligible
// credentials, keeping list filtering separate from the key used for new
// submissions.
const selectedApiKey = computed(
  () => geminiApiKeys.value.find((key) => key.id === Number(form.apiKeyId)) || null,
)

const filteredApiKeys = computed(() => {
  const selectedFilterID = Number(filters.apiKeyId || 0)
  if (!selectedFilterID) return geminiApiKeys.value
  return geminiApiKeys.value.filter((key) => key.id === selectedFilterID)
})

// The key filter includes an unrestricted option and names each eligible
// credential for selecting one key's job list.
const apiKeyFilterOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('batchImage.filters.allApiKeys') },
  ...geminiApiKeys.value.map((key) => ({
    value: String(key.id),
    label: key.name || `API Key #${key.id}`,
  })),
])

// Selected rows resolve stored identifiers against the loaded jobs, so bulk
// operations use the current server metadata for each selected job.
const selectedRows = computed(() =>
  batchJobs.value.filter((job) => selectedJobIds.value.has(job.id)),
)

// Retry children are grouped by their root parent and sorted oldest first;
// expanded parents flatten these groups into the visible table rows.
const childrenByParent = computed(() => {
  const groups = new Map<string, BatchImageJobRow[]>()
  for (const job of batchJobs.value) {
    if (!job.parent_batch_id) continue
    const rows = groups.get(job.parent_batch_id) || []
    rows.push(job)
    groups.set(job.parent_batch_id, rows)
  }
  for (const rows of groups.values()) {
    rows.sort((a, b) => a.created_at - b.created_at)
  }
  return groups
})

const visibleBatchJobs = computed(() => {
  const rows: BatchImageJobRow[] = []
  for (const job of batchJobs.value.filter((item) => !item.parent_batch_id)) {
    rows.push(job)
    if (expandedParentIds.value.has(job.id)) {
      rows.push(
        ...(childrenByParent.value.get(job.id) || []).map((child) => ({
          ...child,
          is_child: true,
        })),
      )
    }
  }
  return rows
})

// Bulk download candidates and checkbox states derive from the currently
// selected and visible rows, respecting job eligibility and collapsed parent
// groups.
const selectedDownloadableRows = computed(() =>
  selectedRows.value.filter((job) => canDownload(job)),
)

const allVisibleSelected = computed(
  () =>
    visibleBatchJobs.value.length > 0 &&
    visibleBatchJobs.value.every((job) => selectedJobIds.value.has(job.id)),
)

const someVisibleSelected = computed(
  () =>
    visibleBatchJobs.value.some((job) => selectedJobIds.value.has(job.id)) &&
    !allVisibleSelected.value,
)

// The enlarged image modal resolves its current item through the same encoded
// identity used by thumbnail storage and resource ownership.
const previewImageUrl = computed(() => {
  const item = previewImageItem.value
  if (!item) return ''
  return itemPreviewUrls[itemPreviewKey(item)] || ''
})

// Successful child items contribute their source custom identifiers to
// recovery display, allowing failed root entries to be marked as recovered by
// retries.
const recoveredOriginalCustomIds = computed(() => {
  const rootBatchId = detailRootBatchId()
  if (!rootBatchId) return new Set<string>()
  const ids = new Set<string>()
  for (const item of items.value) {
    if (!isChildDetailItem(item) || !isSuccessfulImageItem(item)) continue
    const sourceCustomID = retrySourceCustomID(item.custom_id)
    if (sourceCustomID) ids.add(sourceCustomID)
  }
  return ids
})

// The open detail uses the same root and child count aggregation as table
// rows, keeping displayed costs and retry outcomes consistent.
const currentDisplayJob = computed(() => {
  if (!currentJob.value) return null
  return displayJob(currentJob.value)
})

// Agent instructions prefer the configured API endpoint, then browser origin,
// and finally a localized placeholder when rendered without a browser.
const endpointBase = computed(() => {
  const configured = appStore.apiBaseUrl?.trim()
  if (configured) return configured.replace(/\/+$/, '')
  if (typeof window !== 'undefined') return window.location.origin.replace(/\/+$/, '')
  return isZhLocale() ? '<你的 TokenRouter API 端点>' : '<Your TokenRouter API endpoint>'
})

// Creation validation derives attachment limits from the selected model and
// sums normalized repetition counts across all committed prompt rows.
const selectedModelReferenceLimit = computed(() => referenceImageLimitForModel(form.model))

const estimatedOutputCount = computed(() =>
  promptRows.value.reduce((sum, row) => sum + normalizeOutputCount(row.output_count), 0),
)

// Submission items sanitize and deduplicate custom identifiers in row order,
// omit default repetition counts, and retain each row's prepared reference
// attachments.
const parsedItems = computed<BatchImageSubmitItem[]>(() => {
  const used = new Set<string>()
  return promptRows.value
    .map((row, index) => {
      const customID = uniqueCustomID(
        row.custom_id || `img_${String(index + 1).padStart(3, '0')}`,
        used,
        index,
      )
      const item: BatchImageSubmitItem = { custom_id: customID, prompt: row.prompt.trim() }
      const outputCount = normalizeOutputCount(row.output_count)
      if (outputCount > 1) {
        item.output_count = outputCount
      }
      if (row.reference_images.length) {
        item.reference_images = row.reference_images
      }
      return item
    })
    .filter((item) => item.prompt)
})

// referenceImageLimitForModel returns the supported attachment count for the
// selected model name. Pro image models accept fourteen references, flash
// image models accept three, and unrecognized names disable reference
// attachments in the form.
function referenceImageLimitForModel(model: string) {
  const normalized = String(model || '').toLowerCase()
  // Recognized image families define the editor's permitted attachment counts.
  if (normalized.includes('pro-image')) return 14
  if (normalized.includes('flash-image')) return 3
  return 0
}

// Agent instructions select the active interface language while keeping
// endpoint paths, request fields, and example payloads suitable for copying.
const agentInstruction = computed(() =>
  !isZhLocale()
    ? `---
name: tokenrouter-batch-image
description: Use for Gemini or Vertex batch image generation, result downloads, and retries of failed images.
---

Build the batch from the user's conversation, attached files, or directory. Ask for missing decisions about the task or output directory. Use this endpoint:
${endpointBase.value}

Preparation:
1. Preserve every prompt in full and assign stable custom_id values such as img_001.
2. Infer a task name from the request or use the current timestamp.
3. Use the requested output directory; ask for it when missing.
4. Sum output_count across all items before submitting. Each batch allows up to 200 output images. Split larger requests into separate batches.
5. Associate reference images with the intended items. Gemini 2.5 Flash Image supports up to 3 references per item; Gemini 3 Pro Image supports up to 14. References are input attachments. The backend counts attachments again for every output_count repetition when applying its attachment limit.
6. Prefer gs:// file_uri references or split batches when reference images are large or repeatedly reused. Each repetition consumes upstream input tokens.
7. Fetch available keys and models. Use the requested model when supported by the chosen key, otherwise the key's default or first available model. Select by user-visible key and model information.
8. Submit, poll, and download through the API.

API endpoints:
- Models: GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/models')}
- Submit: POST ${joinEndpointPath(endpointBase.value, '/v1/images/batches')}
- Status: GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}')}
- Items: GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}/items')}
- Download: GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}/download')}
- Cancel: POST ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}/cancel')}

Authenticate with Authorization: Bearer <API key>. Use Accept-Language: en.
Example request:
{
  "model": "<available model>",
  "task_name": "<task name or timestamp>",
  "items": [{
    "custom_id": "img_001",
    "prompt": "<complete prompt>",
    "output_count": 1,
    "reference_images": [{ "id": "face", "type": "subject", "mime_type": "image/png", "data": "<base64 without data URL prefix>" }]
  }]
}

Execution and recovery:
- Keep API keys out of repository files, logs, recovery records, and replies.
- Keep reference image base64 out of logs and replies. Save request JSON containing base64 in the user's output directory and exclude it from commits. Recovery records identify reference files, their purpose and count, and the request file path.
- output_count defaults to 1 and supports up to 4 per item. The system expands each repetition into a separate item. Check the 200-output limit before submitting.
- User billing follows successful output image count. References create upstream input token and temporary storage costs for each repetition. Displayed reserved and settled amounts follow output image count.
- After submission, immediately write batch-image-resume.json in the output directory with endpoint, task_name, batch_id, model, output_dir, request_file, submitted_at, last_status, status_url, items_url, download_url, prompt_count, expected_output_count, and the custom_id/prompt/reference details needed for failed-item retries.
- After every status query, update last_checked_at, last_status, success and failure counts, charged amount, and a failure summary. Resume from this record after interruption.
- Wait about 20 to 30 seconds before the first query. Poll queued batches every 60 to 120 seconds. After three consecutive queued results, stop active polling, tell the user the batch is queued, and keep the recovery record for later resumption.
- Poll running batches about every 60 seconds, or less frequently for large batches or a busy server. Poll processing_results every 20 to 45 seconds.
- On completion, report task name, batch ID, success and failure counts, charged amount, and output path.
- Download successful images. For failures, show custom_id, error code, error source, and a short explanation. Retry failed items only. If their original prompts are missing, ask the user to provide them.
- Before cancellation, explain that indexed successful images remain chargeable and the remaining reserved balance is released.
- Load image previews on demand.
`
    : `---
name: tokenrouter-batch-image
description: 当用户希望用 Gemini/Vertex 批量生成图片、批量跑提示词、下载批量生图结果、重试失败图片时使用。
---

你是 Codex 中的批量生图执行 Agent。用户不需要手动填写页面表单；你应从当前聊天、用户给的文件、目录或上下文中整理任务名称、prompt 列表和输出目录，只有缺少关键决策时才向用户提问。

默认端点：
${endpointBase.value}

你需要自己完成：
1. 从用户聊天或附件中提取 prompt。每条 prompt 保留完整文本，按顺序生成稳定 custom_id，例如 img_001、img_002。
2. 从用户要求或上下文推断任务名称；没有明确名称时用当前时间生成任务名。
3. 从用户要求或上下文推断输出目录；如果用户没有说保存到哪里，才询问用户。
4. 提交前必须先计算 expected_output_count = 所有 item 的 output_count 之和。单个批量任务硬性最多 200 张输出图；超过 200 张必须拆成多组任务，不能提交一个超大任务，也不能把参考图附件上限当成生成张数上限。
5. 如果用户提供参考图，把参考图按用途绑定到具体 item。参考图只是输入附件，不是输出图数量。模型单条限制必须按模型执行：Gemini 2.5 Flash Image 每条最多 3 张参考图；Gemini 3 Pro Image 每条最多 14 张参考图。不要把后端附件风控理解成 Pro 单条能力：按 output_count 展开后，所有 item 的参考图附件总数还有内部保护阈值 1000 个，inline base64 参考图解码后总量最多 128MB。这个 1000 只是服务器拒绝异常请求的保护阈值，不是推荐规模；参考图很多或总请求体较大时应主动拆分任务。
6. 参考图会按 output_count 重复消耗输入 token；大量任务、重复复用同一张参考图或参考图总体积较大时，优先使用 gs:// file_uri 或拆分成多组任务。
7. 选择 API Key 和模型：先获取当前可用的批量生图 Key/模型；如果用户指定模型且该 Key 支持，则使用用户指定模型；否则使用该 Key 可用模型中的默认/第一个。不要展示或询问内部 provider 名称。
8. 调用批量生图 API 提交、轮询、下载，不要求用户去页面里手填。

API 调用规范：
- 模型：GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/models')}
- 提交：POST ${joinEndpointPath(endpointBase.value, '/v1/images/batches')}
- 查询：GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}')}
- 明细：GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}/items')}
- 下载：GET ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}/download')}
- 取消：POST ${joinEndpointPath(endpointBase.value, '/v1/images/batches/{id}/cancel')}

提交请求体：
{
  "model": "<按所选 Key 可用模型填写>",
  "task_name": "<从聊天推断；为空则用当前时间>",
  "image_size": "1K",
  "response_mime_type": "image/png",
  "items": [
    {
      "custom_id": "img_001",
      "prompt": "<第一条完整 prompt>",
      "output_count": 1,
      "reference_images": [
        {
          "id": "face",
          "type": "subject",
          "mime_type": "image/png",
          "data": "<base64，不含 data:image/png;base64, 前缀>"
        }
      ]
    }
  ]
}

必须遵守：
- 不要把 API Key 写入仓库、日志、提交记录或最终回复。
- 不要把参考图 base64 写入最终回复、日志或公开文件。恢复记录中只保存参考图文件名、用途、数量和请求 JSON 文件路径；若请求 JSON 文件包含 base64，应保存在用户指定输出目录且不要提交到仓库。
- output_count 表示同一 prompt 和参考图重复生成几张，默认 1，每条最多 4；这不是依赖 Gemini 单次请求返回多图，而是系统展开成多个真实任务项。提交前必须确认预计输出图总数不超过 200，超过就拆分成多组任务。绝不能因为参考图附件有更高的内部保护阈值，就提交会生成超过 200 张图的任务。
- 当前对用户的批量生图计费仍按成功输出图片数量结算，不单独对参考图加价。可以向用户说明：参考图会产生少量上游输入 token 和临时存储成本，且会随 output_count 重复计算；页面显示的冻结/结算金额按输出图片数量计算。
- 提交成功后，必须立刻在输出目录写入本地恢复记录，例如 batch-image-resume.json。不要在恢复记录里保存 API Key。
- 恢复记录至少包含：endpoint、task_name、batch_id、model、output_dir、request_file、submitted_at、last_status、status_url、items_url、download_url、prompt_count、expected_output_count，以及可用于失败重试的 custom_id 到 prompt 映射或请求 JSON 文件路径。
- 每次查询状态后更新恢复记录，写入 last_checked_at、last_status、成功数、失败数、实际扣费和失败摘要。会话中断或暂停后，下次必须能凭该文件继续查询、下载或重试。
- 不要高频轮询。首次查询等待约 20 到 30 秒；queued 状态每 60 到 120 秒查询一次；如果连续 3 次仍是 queued，就先停止主动查询，告诉用户任务仍在排队，并保留恢复记录，之后可继续其他任务或等待用户稍后让你恢复。
- running 状态每约 60 秒查询一次，服务器压力大或大批量任务时可以更久；processing_results 等接近完成的状态可每 20 到 45 秒查询一次。
- 任务完成后报告任务名、任务 id、成功数、失败数、实际扣费和保存路径。
- 只下载成功图片。部分失败时，先展示失败 custom_id、错误码、错误来源和简要原因。
- 重试只能重试失败项，不能重复提交已成功项。若历史任务没有保存失败项 prompt，必须告诉用户无法自动重试，并询问用户是否提供原 prompt。
- 取消任务前必须提醒：已被系统索引为成功的图片仍会按成功项结算扣费，其余冻结金额会释放。
- 图片预览按需加载；不要为了查看列表自动批量加载图片内容。`,
)

// joinEndpointPath joins an endpoint and route with one separating slash.
function joinEndpointPath(base: string, path: string): string {
  return `${base.replace(/\/+$/, '')}/${path.replace(/^\/+/, '')}`
}

// uniqueCustomID sanitizes an item identifier and reserves a unique value in
// the supplied set. Empty sanitized names use the one-based padded item index,
// and collisions append increasing numeric suffixes beginning at two.
//
// Callers share the set across rows in submission order because different
// user labels can normalize to the same base. Each result is registered before
// returning, including suffixed values that might collide with a later row's
// requested label. The row's rendering identity remains independent.
function uniqueCustomID(raw: string, used: Set<string>, index: number): string {
  // Normalization can collapse distinct user labels to the same value; reserve
  // each chosen suffix in this batch so later rows cannot reuse it.
  const base =
    raw.replace(/[^\w.-]+/g, '_').replace(/^_+|_+$/g, '') ||
    `img_${String(index + 1).padStart(3, '0')}`
  let candidate = base
  let suffix = 2
  while (used.has(candidate)) {
    candidate = `${base}_${suffix}`
    suffix += 1
  }
  used.add(candidate)
  return candidate
}

// normalizeOutputCount converts a draft repetition count into an integer
// between one and four. Empty and nonfinite values select one, and finite
// values are floored before applying the form's permitted range.
function normalizeOutputCount(value: unknown): number {
  // Fractional repetitions become whole image counts before enforcing limits.
  const parsed = Math.floor(Number(value || 1))
  if (!Number.isFinite(parsed)) return 1
  return Math.min(BATCH_IMAGE_MAX_OUTPUTS_PER_ITEM, Math.max(1, parsed))
}

// addPromptRow commits the current prompt editor into the page's submission
// rows. Empty prompts do nothing; accepted rows receive a unique custom
// identifier and normalized repetition count before editor fields are cleared.
//
// Reference attachments are copied without their browser filename and size
// display fields, retaining the API payload attached to this prompt. The row
// keeps a separate local identity for rendering, so removing rows does not
// require using the submitted custom identifier as the component key.
function addPromptRow() {
  const prompt = promptDraft.value.trim()
  if (!prompt) return
  const outputCount = normalizeOutputCount(outputCountDraft.value)
  const used = new Set(promptRows.value.map((row) => row.custom_id))
  const customID = uniqueCustomID(
    customIdDraft.value || `img_${String(promptRows.value.length + 1).padStart(3, '0')}`,
    used,
    promptRows.value.length,
  )
  promptRows.value = [
    ...promptRows.value,
    {
      localId: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      custom_id: customID,
      prompt,
      output_count: outputCount,
      reference_images: referenceImageDrafts.value.map(
        ({ name: _name, size: _size, ...ref }) => ref,
      ),
    },
  ]
  promptDraft.value = ''
  customIdDraft.value = ''
  outputCountDraft.value = 1
  referenceImageDrafts.value = []
}

// removePromptRow removes the prompt row at the supplied editor index.
function removePromptRow(index: number) {
  promptRows.value = promptRows.value.filter((_, currentIndex) => currentIndex !== index)
}

// removeReferenceImageDraft removes the reference attachment at the supplied
// draft index.
function removeReferenceImageDraft(index: number) {
  referenceImageDrafts.value = referenceImageDrafts.value.filter(
    (_, currentIndex) => currentIndex !== index,
  )
}

// handleReferenceImageFiles reads supported uploaded images into the current
// prompt's reference drafts. The selected model limits accepted slots before
// MIME type and ten MiB checks; rejected files produce a localized error and
// remaining accepted files continue.
//
// The file input is cleared immediately so users can select the same file
// again. Reads run sequentially and append their completed drafts together,
// retaining each filename and byte size for display alongside the API
// attachment bytes.
async function handleReferenceImageFiles(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || [])
  input.value = ''
  if (files.length === 0) return
  const limit = selectedModelReferenceLimit.value
  if (limit <= 0) {
    appStore.showError(t('batchImage.create.modelNoReferenceImages'))
    return
  }
  const slots = Math.max(0, limit - referenceImageDrafts.value.length)
  if (slots <= 0) {
    appStore.showError(t('batchImage.create.refLimitReached', { limit }))
    return
  }
  // Reserve the model's remaining attachment slots before reading image bytes,
  // so excess selections never enter this prompt's encoded reference payload.
  const accepted = files.slice(0, slots)
  if (accepted.length < files.length) {
    appStore.showError(t('batchImage.create.refLimitExceededIgnored', { limit }))
  }
  const next: ReferenceImageDraft[] = []
  for (const file of accepted) {
    if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type)) {
      appStore.showError(t('batchImage.create.refFormatUnsupported'))
      continue
    }
    // Reject oversized attachments before allocating their base64 representation.
    if (file.size > 10 * 1024 * 1024) {
      appStore.showError(t('batchImage.create.refFileTooLarge', { name: file.name }))
      continue
    }
    const data = await readFileAsBase64(file)
    next.push({
      id: file.name,
      type: 'reference',
      mime_type: file.type,
      data,
      name: file.name,
      size: file.size,
    })
  }
  referenceImageDrafts.value = [...referenceImageDrafts.value, ...next]
}

// readFileAsBase64 reads a selected file and returns its encoded payload
// without a data URL prefix. FileReader errors reject the promise, and a
// result without a comma is returned directly as text.
function readFileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error || new Error('Failed to read file'))
    reader.onload = () => {
      const result = String(reader.result || '')
      // The API accepts raw base64 bytes, so strip FileReader's data URL
      // header while retaining results that already contain an unprefixed payload.
      resolve(result.includes(',') ? result.slice(result.indexOf(',') + 1) : result)
    }
    reader.readAsDataURL(file)
  })
}

useLocaleRefresh(loadApiKeys)

// loadApiKeys loads the first hundred active keys and reconciles the page's
// selected creation key. It selects the first batch-capable key when the
// existing choice is unavailable, clears invalid key filters, and reports API
// failures through the page notification store.
async function loadApiKeys() {
  loadingKeys.value = true
  try {
    const response = await keysAPI.list(1, 100, {
      status: 'active',
      sort_by: 'created_at',
      sort_order: 'desc',
    })
    apiKeys.value = response.items || []
    // A removed or newly ineligible selection needs a usable creation key;
    // choose the first eligible result in the API's existing response order.
    if (!selectedApiKey.value && geminiApiKeys.value.length > 0) {
      form.apiKeyId = geminiApiKeys.value[0].id
    }
    if (
      filters.apiKeyId &&
      !geminiApiKeys.value.some((key) => String(key.id) === filters.apiKeyId)
    ) {
      filters.apiKeyId = ''
    }
    if (!selectedApiKey.value) {
      availableBatchImageModels.value = []
      form.model = ''
    }
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('loadKeysFailed')))
  } finally {
    loadingKeys.value = false
  }
}

// loadAvailableModels loads batch image models for the currently selected
// creation key. Each invocation clears the prior model selection and advances
// a request sequence before reading the catalog, including invocations with no
// available key.
//
// Only the newest sequence publishes catalog results, an error message, or the
// final loading state. Returned identifiers are trimmed and deduplicated in
// server order, then the first surviving identifier becomes the form
// selection. Sequence checks apply to model loading independently of job and
// thumbnail requests.
async function loadAvailableModels() {
  const key = selectedApiKey.value
  // Advance before the empty-key return so changing to no credential also
  // invalidates a catalog request still completing for the previous selection.
  const requestID = ++modelRequestSeq
  modelLoadError.value = ''
  availableBatchImageModels.value = []
  form.model = ''
  if (!key) return

  loadingModels.value = true
  try {
    const result = await listBatchImageModels(key.key)
    // A newer key selection owns the catalog; discard this completion before
    // it can replace either the model options or the newer request's error.
    if (requestID !== modelRequestSeq) return
    const seen = new Set<string>()
    availableBatchImageModels.value = (result.data || [])
      .map((model) => String(model.id || '').trim())
      .filter((model) => {
        if (!model || seen.has(model)) return false
        seen.add(model)
        return true
      })
      .map((model) => ({ value: model, label: model }))
    form.model = availableBatchImageModels.value[0]?.value || ''
  } catch (error: any) {
    // A newer key selection owns the catalog; discard this completion before
    // it can replace either the model options or the newer request's error.
    if (requestID !== modelRequestSeq) return
    modelLoadError.value = batchImageErrorMessage(error, batchImageText('loadModelsFailed'))
  } finally {
    // Only the current catalog request may clear its loading indicator; an
    // older completion must leave the newer request's pending state visible.
    if (requestID === modelRequestSeq) {
      loadingModels.value = false
    }
  }
}

// refreshPage refreshes usable keys before requesting the filtered job list.
// Key refresh handles its own errors, so job loading still runs using
// whichever eligible keys remain available in page state.
async function refreshPage() {
  await loadApiKeys()
  await loadBatchJobs()
}

// applyFilters restarts the job list at its first page and clears row
// selection. Loading proceeds asynchronously using the current filters, so
// callers can update several filter fields before invoking this operation.
function applyFilters() {
  pagination.page = 1
  selectedJobIds.value = new Set()
  void loadBatchJobs()
}

// resetFilters clears all job list filters and reloads the first page. The
// selected creation key and draft prompts remain independent of these list
// controls and are retained by the page.
function resetFilters() {
  filters.taskName = ''
  filters.apiKeyId = ''
  filters.status = ''
  filters.downloaded = ''
  applyFilters()
}

// listOptions builds list query options from pagination and the current
// filters. Page numbers become offset cursor strings, blank optional filters
// are omitted, and task names are trimmed before transmission.
function listOptions(): BatchImageJobsListOptions {
  const options: BatchImageJobsListOptions = {
    limit: pagination.page_size,
    cursor: String((pagination.page - 1) * pagination.page_size),
  }
  if (filters.taskName.trim()) options.taskName = filters.taskName.trim()
  if (filters.status) options.status = filters.status
  if (filters.downloaded) options.downloaded = filters.downloaded
  return options
}

// toJobRow copies an API job into the table's display and credential metadata.
// Missing task names use the creation timestamp, and the optional key defaults
// to the page's selected creation key.
function toJobRow(job: BatchImageJob, key = selectedApiKey.value): BatchImageJobRow {
  return {
    id: job.id,
    task_name: job.task_name || defaultTaskName(job.created_at),
    parent_batch_id: job.parent_batch_id || null,
    status: job.status,
    model: job.model,
    platform: job.platform,
    item_count: job.item_count,
    success_count: job.success_count,
    fail_count: job.fail_count,
    estimated_cost: job.estimated_cost,
    hold_amount: job.hold_amount,
    actual_cost: job.actual_cost,
    created_at: job.created_at,
    downloaded_at: job.downloaded_at,
    api_key_id: key?.id || 0,
    api_key_name: key?.name || '',
    child_count: 0,
  }
}

// applyChildCounts returns job rows annotated with their visible retry child
// counts.
function applyChildCounts(rows: BatchImageJobRow[]) {
  const counts = new Map<string, number>()
  for (const row of rows) {
    if (!row.parent_batch_id) continue
    counts.set(row.parent_batch_id, (counts.get(row.parent_batch_id) || 0) + 1)
  }
  return rows.map((row) => ({ ...row, child_count: counts.get(row.id) || 0 }))
}

// displayJob combines a root job with its listed retry children for table and
// detail display. Child rows pass through unchanged, and roots without listed
// children retain their server fields.
//
// Successful outputs are capped at the root item count, with remaining items
// displayed as failures. Estimates, holds, and known actual costs include the
// children; unknown child costs keep an unknown root cost unresolved. A
// terminal root whose aggregate failures reach zero displays as completed,
// while retry operations retain each stored job's identity.
function displayJob<
  T extends Pick<
    BatchImageJob,
    | 'id'
    | 'parent_batch_id'
    | 'status'
    | 'item_count'
    | 'success_count'
    | 'fail_count'
    | 'estimated_cost'
    | 'hold_amount'
    | 'actual_cost'
  >,
>(job: T): T {
  if (job.parent_batch_id) return job
  const children = childrenByParent.value.get(job.id) || []
  if (!children.length) return job

  const childSuccess = children.reduce((sum, child) => sum + child.success_count, 0)
  const childEstimated = children.reduce((sum, child) => sum + child.estimated_cost, 0)
  const childHold = children.reduce((sum, child) => sum + child.hold_amount, 0)
  const childActual = children.reduce((sum, child) => sum + (child.actual_cost || 0), 0)
  const childActualReady = children.every((child) => child.actual_cost !== null)
  // Retry successes describe recovery of root items, so cap the combined
  // count at the root total before calculating outstanding failures and displayed status.
  const successCount = Math.min(job.item_count, job.success_count + childSuccess)
  const failCount = Math.max(0, job.item_count - successCount)
  // Unknown root settlement can display child totals only when every child
  // has settled; an existing root amount accumulates the child amounts already known.
  const actualCost =
    job.actual_cost === null
      ? childActualReady
        ? childActual
        : null
      : job.actual_cost + childActual

  return {
    ...job,
    success_count: successCount,
    fail_count: failCount,
    status: failCount === 0 && TERMINAL_STATUSES.has(job.status) ? 'completed' : job.status,
    estimated_cost: job.estimated_cost + childEstimated,
    hold_amount: job.hold_amount + childHold,
    actual_cost: actualCost,
  }
}

// hasChildJobs reports whether the loaded list contains children for this job.
function hasChildJobs(batchId: string) {
  return (childrenByParent.value.get(batchId) || []).length > 0
}

// toggleChildRows toggles the expanded state of one parent job row.
function toggleChildRows(batchId: string) {
  const next = new Set(expandedParentIds.value)
  if (next.has(batchId)) next.delete(batchId)
  else next.add(batchId)
  expandedParentIds.value = next
}

// closeMoreMenu clears the identity of the currently open row menu.
function closeMoreMenu() {
  openMoreJobId.value = ''
}

// toggleMoreMenu opens the selected row's action menu beside its button.
// Selecting the already open row closes it; other rows require a measurable
// trigger and clamp the floating position to the viewport.
function toggleMoreMenu(job: BatchImageJobRow, event: MouseEvent) {
  if (openMoreJobId.value === job.id) {
    closeMoreMenu()
    return
  }
  const trigger = event.currentTarget as HTMLElement | null
  const rect = trigger?.getBoundingClientRect()
  if (!rect) return
  // Reserve room for both menu actions within the viewport.
  const menuWidth = 176
  const margin = 8
  const left = Math.max(
    margin,
    Math.min(rect.right - menuWidth, window.innerWidth - menuWidth - margin),
  )
  const top = Math.min(rect.bottom + margin, window.innerHeight - 96)
  moreMenuStyle.value = {
    left: `${left}px`,
    top: `${Math.max(margin, top)}px`,
  }
  openMoreJobId.value = job.id
}

// cancelPromptPopoverClose cancels the scheduled prompt popover dismissal and
// clears its timer handle. Calling it when no dismissal is pending does
// nothing, allowing pointer entry and immediate opening to share cleanup.
function cancelPromptPopoverClose() {
  if (!promptPopoverCloseTimer) return
  // Clearing the retained handle prevents a previous pointer-leave timer from
  // dismissing content after the user has moved into the current popover.
  clearTimeout(promptPopoverCloseTimer)
  promptPopoverCloseTimer = null
}

// cancelPromptPopoverOpen cancels the scheduled prompt popover opening and
// clears its timer handle. Calling it when no opening is pending does nothing,
// allowing focus changes and detail cleanup to share cancellation.
function cancelPromptPopoverOpen() {
  if (!promptPopoverOpenTimer) return
  // Cancel the earlier hover delay before changing targets or closing details,
  // so that timer cannot display a prompt after ownership has moved.
  clearTimeout(promptPopoverOpenTimer)
  promptPopoverOpenTimer = null
}

// closePromptPopover cancels pending popover timers and clears the displayed
// prompt content. It also removes the active trigger identity so delayed hover
// callbacks cannot reopen a popover for the dismissed target.
function closePromptPopover() {
  cancelPromptPopoverOpen()
  cancelPromptPopoverClose()
  promptPopover.visible = false
  promptPopover.text = ''
  promptPopover.style = {}
  activePromptPopoverTarget = null
}

// schedulePromptPopoverClose schedules popover dismissal after the pointer
// leaves its active area. Existing opening and closing timers are cancelled
// first, letting entry into the floating content cancel this pending
// dismissal.
function schedulePromptPopoverClose() {
  cancelPromptPopoverOpen()
  cancelPromptPopoverClose()
  // Delay dismissal briefly to permit crossing from the row trigger into
  // the floating content, where pointer entry can cancel this pending timer.
  promptPopoverCloseTimer = setTimeout(() => {
    closePromptPopover()
  }, 180)
}

// schedulePromptPopoverOpen schedules a prompt popover after a sustained hover
// over a usable trigger. Blank prompts and placeholder dashes do not open a
// popover; scheduling replaces pending open and close timers.
//
// The callback captures the trigger and text, then verifies that the trigger
// is still the active target and remains attached to the document. These
// checks prevent a delayed hover callback from opening content after row
// replacement or a newer trigger takes ownership.
function schedulePromptPopoverOpen(event: MouseEvent | PointerEvent, text: string) {
  const target = event.currentTarget as HTMLElement | null
  if (!target) return
  const value = String(text || '').trim()
  if (!value || value === '-') return
  activePromptPopoverTarget = target
  cancelPromptPopoverOpen()
  cancelPromptPopoverClose()
  // Delay hover opening to reduce accidental popovers while moving through
  // rows, then verify the captured trigger still owns this request and exists.
  promptPopoverOpenTimer = setTimeout(() => {
    if (activePromptPopoverTarget !== target || !document.body.contains(target)) return
    openPromptPopover(target, value)
  }, 520)
}

// showPromptPopover opens usable prompt text immediately for the event's
// current target. It cancels both delayed hover timers first and ignores blank
// values, placeholder dashes, and events without a usable target.
function showPromptPopover(event: MouseEvent | FocusEvent, text: string) {
  const value = String(text || '').trim()
  if (!value || value === '-') return
  const target = event.currentTarget as HTMLElement | null
  cancelPromptPopoverClose()
  cancelPromptPopoverOpen()
  if (!target) return
  activePromptPopoverTarget = target
  openPromptPopover(target, value)
}

// openPromptPopover positions and displays prompt text near its current
// trigger element. The width and horizontal position are constrained by the
// viewport, and a fixed height estimate chooses placement above when space
// below is insufficient.
function openPromptPopover(target: HTMLElement, value: string) {
  const rect = target.getBoundingClientRect()
  if (!rect) return
  // Position estimates constrain the floating prompt to the current viewport.
  const viewportWidth = window.innerWidth || 1280
  const viewportHeight = window.innerHeight || 720
  const width = Math.min(440, Math.max(320, viewportWidth - 32))
  const left = Math.max(16, Math.min(rect.left, viewportWidth - width - 16))
  const estimatedHeight = 178
  const preferredTop = rect.bottom + 8
  const top =
    preferredTop + estimatedHeight > viewportHeight
      ? Math.max(16, rect.top - estimatedHeight - 8)
      : preferredTop
  promptPopover.text = value
  promptPopover.style = {
    left: `${left}px`,
    top: `${top}px`,
    width: `${width}px`,
  }
  promptPopover.visible = true
}

// copyPromptPopover copies the visible prompt text with a localized success
// message.
function copyPromptPopover() {
  if (!promptPopover.text) return
  void copyToClipboard(promptPopover.text, t('batchImage.promptPopover.copied'))
}

// loadBatchJobs loads jobs for all keys admitted by the current key filter.
// Responses are combined, sorted newest first, and truncated to the selected
// page size before retry child counts and visible selections are reconciled.
//
// Each key receives the same offset cursor and filter options, and any
// response indicating more rows enables the next page. A failed request leaves
// the previously displayed jobs intact and reports an error. Calls share page
// state and publish on completion without a request generation guard.
async function loadBatchJobs() {
  const keys = filteredApiKeys.value
  if (!keys.length) {
    batchJobs.value = []
    pagination.has_more = false
    return
  }
  loadingJobs.value = true
  closeMoreMenu()
  try {
    const options = listOptions()
    const results = await Promise.all(
      keys.map(async (key) => {
        const result = await listBatchImageJobs(key.key, options)
        return {
          hasMore: Boolean(result.has_more),
          rows: (result.data || []).map((job) => toJobRow(job, key)),
        }
      }),
    )
    // Combine independently fetched key pages before sorting and truncating,
    // then derive child counts from the rows that remain visible in this page.
    batchJobs.value = applyChildCounts(
      results
        .flatMap((result) => result.rows)
        .sort((a, b) => b.created_at - a.created_at)
        .slice(0, pagination.page_size),
    )
    pagination.has_more = results.some((result) => result.hasMore)
    // Retain selections only for currently visible rows after refreshing, so
    // bulk actions cannot include jobs omitted by filtering or collapsed retry groups.
    selectedJobIds.value = new Set(
      [...selectedJobIds.value].filter((id) => visibleBatchJobs.value.some((job) => job.id === id)),
    )
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('loadJobsFailed')))
  } finally {
    loadingJobs.value = false
  }
}

// upsertJob updates a listed job or prepends a newly observed job. Existing
// child display markers survive replacement, while inserted rows are capped to
// the page size before child counts are recalculated.
function upsertJob(job: BatchImageJob) {
  const next = toJobRow(job)
  const index = batchJobs.value.findIndex((item) => item.id === job.id)
  if (index >= 0) {
    const rows = [...batchJobs.value]
    rows[index] = { ...next, is_child: rows[index].is_child }
    batchJobs.value = applyChildCounts(rows)
    return
  }
  batchJobs.value = applyChildCounts([next, ...batchJobs.value].slice(0, pagination.page_size))
}

// handlePageChange loads a different positive page and clears selected job
// identities. Repeating the current page or requesting a number below one does
// nothing, keeping list navigation tied to valid page changes.
function handlePageChange(page: number) {
  if (page < 1 || page === pagination.page) return
  pagination.page = page
  selectedJobIds.value = new Set()
  void loadBatchJobs()
}

// handlePageSizeChange stores a normalized page size and reloads from the
// first page. Null and boolean inputs are ignored; numeric sizes are
// constrained to one through one hundred and row selections are cleared.
function handlePageSizeChange(value: string | number | boolean | null) {
  if (value === null || typeof value === 'boolean') return
  // Normalize selection values to this endpoint's permitted page sizes.
  const nextSize = Math.min(Math.max(Number(value) || 20, 1), 100)
  pagination.page_size = nextSize
  pagination.page = 1
  // Persist the normalized preference before reloading rows so this page
  // and subsequent tables share the size the user actually selected.
  setPersistedPageSize(nextSize)
  selectedJobIds.value = new Set()
  void loadBatchJobs()
}

// openCreateModal opens the creation dialog and starts loading keys when none
// are available. Existing key state and draft fields remain available,
// allowing a previously opened form to retain its selected model.
function openCreateModal() {
  showCreateModal.value = true
  if (!apiKeys.value.length) {
    void loadApiKeys()
  }
}

// closeCreateModal closes the creation dialog and clears its draft when
// submission is idle. An in-flight submission keeps the dialog state intact
// until the submission handler publishes its result or error.
function closeCreateModal() {
  if (submitting.value) return
  showCreateModal.value = false
  resetCreateDraft()
}

// resetCreateDraft clears task and prompt editor drafts while retaining key
// and model selections. The response MIME type returns to PNG, and all queued
// prompts and reference image attachments are removed.
function resetCreateDraft() {
  form.taskName = ''
  form.responseMimeType = 'image/png'
  promptRows.value = []
  promptDraft.value = ''
  customIdDraft.value = ''
  outputCountDraft.value = 1
  referenceImageDrafts.value = []
}

// closeDetail clears the selected job, item rows, and current thumbnail
// resources. Popover timers and preview state are released through the page
// cleanup operations; independently pending requests retain their existing
// completion behavior.
function closeDetail() {
  closePromptPopover()
  currentJob.value = null
  selectedBatchId.value = ''
  selectedBatchApiKeyId.value = 0
  items.value = []
  clearItemPreviews()
}

// keyForSelectedBatch resolves the retained detail key before the current
// creation key. If the retained key is no longer eligible, the page uses its
// selected creation key, which may itself be null.
function keyForSelectedBatch(): ApiKey | null {
  if (selectedBatchApiKeyId.value) {
    const key = geminiApiKeys.value.find((item) => item.id === selectedBatchApiKeyId.value)
    if (key) return key
  }
  return selectedApiKey.value
}

// requireApiKey returns the selected creation key or reports that a key is
// required. Callers stop their operation on null so API calls are not made
// with an absent credential.
function requireApiKey(): ApiKey | null {
  if (!selectedApiKey.value) {
    appStore.showError(batchImageText('selectApiKey'))
    return null
  }
  return selectedApiKey.value
}

// validateForm reports the first creation error and returns whether submission
// may proceed. Validation requires a key, model, prompt rows, permitted total
// output count, and reference counts supported by the selected model.
function validateForm(): boolean {
  if (!requireApiKey()) return false
  if (!form.model) {
    appStore.showError(
      availableBatchImageModels.value.length === 0
        ? batchImageText('noModelsForKey')
        : batchImageText('selectModel'),
    )
    return false
  }
  if (parsedItems.value.length === 0) {
    appStore.showError(batchImageText('promptRequired'))
    return false
  }
  if (estimatedOutputCount.value > BATCH_IMAGE_MAX_OUTPUTS_PER_JOB) {
    appStore.showError(batchImageText('tooManyOutputImages'))
    return false
  }
  const refLimit = selectedModelReferenceLimit.value
  if (promptRows.value.some((row) => row.reference_images.length > refLimit)) {
    appStore.showError(batchImageText('tooManyReferenceImages'))
    return false
  }
  return true
}

// submitJob submits validated prompt rows and opens the returned job as the
// selected detail. A nonempty editor prompt is first committed into the row
// list, and the submitting flag prevents overlapping form submissions.
//
// The request uses the selected key, current output parameters, and a
// generated idempotency key. Success records the returned job and credential
// identity before clearing the creation draft, loading its items, and starting
// polling. Failure keeps the draft available and reports the server error.
async function submitJob() {
  if (submitting.value) return
  if (promptDraft.value.trim()) addPromptRow()
  if (!validateForm()) return
  const key = requireApiKey()
  if (!key) return
  submitting.value = true
  try {
    const job = await submitBatchImageJob(
      key.key,
      {
        model: form.model,
        task_name: form.taskName.trim() || defaultTaskName(),
        image_size: '1K',
        response_mime_type: form.responseMimeType,
        items: parsedItems.value,
      },
      `tokenrouter-ui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    )
    // Publish the returned job and its credential before loading details,
    // because those requests resolve their batch and key from selected page state.
    currentJob.value = job
    selectedBatchId.value = job.id
    selectedBatchApiKeyId.value = key.id
    items.value = []
    upsertJob(job)
    showCreateModal.value = false
    resetCreateDraft()
    appStore.showSuccess(batchImageText('submitted'))
    void loadItems()
    startPolling()
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('submitFailed')))
  } finally {
    submitting.value = false
  }
}

// refreshSelected fetches the selected job with its retained detail key and
// updates its listed row. Terminal responses stop polling, and failures report
// an error while retaining the currently displayed job state.
async function refreshSelected() {
  if (!selectedBatchId.value) return
  const key = keyForSelectedBatch() || requireApiKey()
  if (!key) return
  refreshing.value = true
  try {
    const job = await getBatchImageJob(key.key, selectedBatchId.value)
    currentJob.value = job
    upsertJob(job)
    if (TERMINAL_STATUSES.has(job.status)) stopPolling()
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('refreshFailed')))
  } finally {
    refreshing.value = false
  }
}

// refreshDetail refreshes the selected job metadata and its item list
// concurrently. Each operation owns its own loading indicator and error
// reporting, so one failure does not discard the other operation's result.
async function refreshDetail() {
  await Promise.all([refreshSelected(), loadItems()])
}

// selectJob opens a listed batch and starts metadata and item requests. A
// still-eligible row key becomes both the creation selection and retained
// detail credential; otherwise detail credential lookup follows the current
// selection.
function selectJob(batchId: string) {
  const row = batchJobs.value.find((job) => job.id === batchId)
  if (row?.api_key_id && geminiApiKeys.value.some((key) => key.id === row.api_key_id)) {
    form.apiKeyId = row.api_key_id
    selectedBatchApiKeyId.value = row.api_key_id
  } else {
    selectedBatchApiKeyId.value = 0
  }
  selectedBatchId.value = batchId
  currentJob.value = null
  items.value = []
  void refreshSelected()
  void loadItems()
}

// startPolling replaces the current polling interval with an eight second
// metadata refresh. Each tick stops when the selected job is absent or
// terminal, while active jobs reuse refreshSelected for requests and error
// reporting.
function startPolling() {
  stopPolling()
  // Replacing the interval keeps one scheduled polling source for this page;
  // the request handler independently stops it when a terminal response arrives.
  pollTimer = setInterval(() => {
    if (!currentJob.value || TERMINAL_STATUSES.has(currentJob.value.status)) {
      stopPolling()
      return
    }
    void refreshSelected()
  }, 8000)
}

// stopPolling clears the page's active polling interval and releases its timer
// handle. An already running metadata request continues independently;
// repeated calls with no timer have no additional effect.
function stopPolling() {
  if (pollTimer) {
    // Clear future ticks when the job is terminal or the page unmounts;
    // metadata requests already started by earlier ticks continue to settle independently.
    clearInterval(pollTimer)
    pollTimer = null
  }
}

// canCancel reports whether the job has a nonterminal server status.
function canCancel(job: Pick<BatchImageJob, 'status'>) {
  return !TERMINAL_STATUSES.has(job.status)
}

// canDownload reports whether a completed job contains any successful image
// outputs.
function canDownload(job: Pick<BatchImageJob, 'status' | 'success_count'>) {
  return job.status === 'completed' && job.success_count > 0
}

// canRetry reports whether a terminal displayed job still contains failed
// items. Identified jobs first include visible child retry results, allowing
// completed recoveries to remove the retry action from the parent display.
function canRetry(job: Pick<BatchImageJob, 'status' | 'fail_count'>) {
  const display = 'id' in job ? displayJob(job as BatchImageJob) : job
  return TERMINAL_STATUSES.has(display.status) && display.fail_count > 0
}

// isDownloadingJob reports whether the active ZIP download belongs to this
// batch.
function isDownloadingJob(batchId: string) {
  return downloading.value && downloadingBatchId.value === batchId
}

// applyJobApiKey selects a row's key when that key remains batch-capable. Jobs
// without stored key metadata and rows whose key is unavailable leave the
// current creation selection unchanged.
function applyJobApiKey(job: BatchImageJobRow | Pick<BatchImageJob, 'id'>) {
  if (
    'api_key_id' in job &&
    job.api_key_id &&
    geminiApiKeys.value.some((key) => key.id === job.api_key_id)
  ) {
    form.apiKeyId = job.api_key_id
  }
}

// apiKeyForJob resolves a row's stored key or the current creation key. A row
// with a stored but unavailable key returns null, letting the calling action
// decide whether to stop or try another selection.
function apiKeyForJob(job: BatchImageJobRow | Pick<BatchImageJob, 'id'>): ApiKey | null {
  if ('api_key_id' in job && job.api_key_id) {
    return geminiApiKeys.value.find((key) => key.id === job.api_key_id) || null
  }
  return selectedApiKey.value
}

// toggleJobSelection adds or removes one batch identifier from the selected
// set.
function toggleJobSelection(batchId: string, checked: boolean) {
  const next = new Set(selectedJobIds.value)
  if (checked) next.add(batchId)
  else next.delete(batchId)
  selectedJobIds.value = next
}

// toggleAllVisible updates selection membership for every currently visible
// parent and child row.
function toggleAllVisible(checked: boolean) {
  const next = new Set(selectedJobIds.value)
  for (const job of visibleBatchJobs.value) {
    if (checked) next.add(job.id)
    else next.delete(job.id)
  }
  selectedJobIds.value = next
}

// canDeleteRecord reports whether a job has reached a deletable terminal
// status.
function canDeleteRecord(job: Pick<BatchImageJob, 'status'>) {
  return TERMINAL_STATUSES.has(job.status)
}

// cancelSelected requests cancellation of the current job after user
// confirmation. It resolves the detail credential first, updates the returned
// job and table row on success, and reports failures through the page
// notification store.
async function cancelSelected() {
  if (!currentJob.value) return
  const key = keyForSelectedBatch() || requireApiKey()
  if (!key) return
  if (!window.confirm(batchImageText('cancelConfirm'))) return
  cancelling.value = true
  try {
    const job = await cancelBatchImageJob(key.key, currentJob.value.id)
    currentJob.value = job
    upsertJob(job)
    appStore.showSuccess(batchImageText('cancelled'))
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('cancelFailed')))
  } finally {
    cancelling.value = false
  }
}

// downloadSelected downloads the current detail job when one is selected. The
// shared download operation owns credential resolution, concurrent download
// suppression, browser file saving, and the resulting downloaded timestamp
// update.
async function downloadSelected() {
  if (!currentJob.value) return
  await downloadJob(currentJob.value)
}

// retrySelected retries failed items from the current detail job when one
// exists. Retry eligibility, credential resolution, prompt recovery, and
// submission state remain owned by the shared retry operation.
async function retrySelected() {
  if (!currentJob.value) return
  await retryFailedJob(currentJob.value)
}

// retryFailedJob creates a child job from failed items whose prompt previews
// remain available. The active retry identity prevents overlapping retries,
// and eligible source rows resolve their stored key before using the current
// detail or creation key.
//
// Retry custom identifiers retain the source identifier as a prefix, and
// parent_batch_id points to the original root even when retrying a child.
// Success selects the child, expands its listed parent, and starts detail
// loading and polling. Missing usable prompts report an error instead of
// submitting an empty retry.
async function retryFailedJob(job: BatchImageJobRow | BatchImageJob) {
  if (!canRetry(job) || retryingBatchId.value) return
  closeMoreMenu()
  const key = apiKeyForJob(job) || keyForSelectedBatch() || requireApiKey()
  if (!key) return
  retryingBatchId.value = job.id
  try {
    const sourceItems = await ensureItemsForRetry(key.key, job.id)
    // Recovery submits failed items with available prompts, retaining source
    // identifiers in retry suffixes so later detail aggregation can identify recovered roots.
    const failedItems = sourceItems
      .filter((item) => item.status === 'failed')
      .map((item) => ({
        custom_id: retryCustomID(item.custom_id),
        prompt: String(item.prompt_preview || '').trim(),
      }))
      .filter((item) => item.prompt)
    if (failedItems.length === 0) {
      appStore.showError(batchImageText('retryMissingPrompts'))
      return
    }
    const retryJob = await submitBatchImageJob(
      key.key,
      {
        model: job.model,
        task_name: `${job.task_name || defaultTaskName()} ${t('batchImage.messages.retryTaskNameSuffix')}`,
        parent_batch_id: rootBatchIdForRetry(job),
        platform: job.platform,
        image_size: '1K',
        response_mime_type: form.responseMimeType,
        items: failedItems,
      },
      `tokenrouter-ui-retry-${job.id}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
    )
    // Select the child together with its request credential before starting
    // item loading and polling, which read their targets from current detail state.
    currentJob.value = retryJob
    selectedBatchId.value = retryJob.id
    selectedBatchApiKeyId.value = key.id
    items.value = []
    upsertJob(retryJob)
    if (retryJob.parent_batch_id) {
      expandedParentIds.value = new Set([...expandedParentIds.value, retryJob.parent_batch_id])
    }
    appStore.showSuccess(batchImageText('retrySubmitted'))
    void loadItems()
    startPolling()
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('retryFailed')))
  } finally {
    retryingBatchId.value = ''
  }
}

// ensureItemsForRetry returns loaded detail items when they match the
// requested selected batch. Otherwise it fetches that batch's items with the
// supplied key, allowing retries initiated directly from a table row.
async function ensureItemsForRetry(apiKey: string, batchId: string) {
  if (selectedBatchId.value === batchId && items.value.length > 0) {
    return items.value
  }
  const result = await listBatchImageItems(apiKey, batchId)
  return result.data || []
}

// retryCustomID appends a timestamp suffix to a sanitized source item
// identifier. Empty identifiers use item as their base, and repeated retry
// suffixes remain recognizable by the recovery display's source identifier
// parser.
function retryCustomID(customID: string) {
  const base =
    String(customID || 'item')
      .replace(/[^\w.-]+/g, '_')
      .replace(/^_+|_+$/g, '') || 'item'
  return `${base}_retry_${Date.now().toString(36)}`
}

// rootBatchIdForRetry returns the existing root parent or the supplied job
// identifier.
function rootBatchIdForRetry(job: BatchImageJobRow | BatchImageJob) {
  return job.parent_batch_id || job.id
}

// downloadJob downloads one job's ZIP and marks its local row as downloaded. A
// page-wide download flag prevents overlapping single downloads, and the
// chosen row key is applied before requesting its archive.
async function downloadJob(job: BatchImageJobRow | Pick<BatchImageJob, 'id'>) {
  if (downloading.value) return
  closeMoreMenu()
  applyJobApiKey(job)
  const key = apiKeyForJob(job) || requireApiKey()
  if (!key) return
  downloading.value = true
  downloadingBatchId.value = job.id
  try {
    const blob = await downloadBatchImageZip(key.key, job.id)
    saveBlob(blob, `${job.id}.zip`)
    markJobDownloaded(job.id)
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('downloadFailed')))
  } finally {
    downloading.value = false
    downloadingBatchId.value = ''
  }
}

// downloadSelectedJobs downloads selected eligible jobs sequentially with
// their individual stored keys. Rows lacking a usable key are skipped, while a
// request failure stops the remaining sequence and reports an error after
// retaining prior downloaded markers.
async function downloadSelectedJobs() {
  if (bulkDownloading.value || selectedDownloadableRows.value.length === 0) return
  bulkDownloading.value = true
  try {
    for (const row of selectedDownloadableRows.value) {
      const key = apiKeyForJob(row)
      if (!key) continue
      downloading.value = true
      downloadingBatchId.value = row.id
      const blob = await downloadBatchImageZip(key.key, row.id)
      saveBlob(blob, `${row.id}.zip`)
      markJobDownloaded(row.id)
    }
    appStore.showSuccess(batchImageText('batchDownloadStarted'))
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('downloadFailed')))
  } finally {
    bulkDownloading.value = false
    downloading.value = false
    downloadingBatchId.value = ''
  }
}

// deleteJob deletes a terminal job record after user confirmation and removes
// its local row. A page-wide deletion identity prevents simultaneous single
// deletions, and the stored row key is required before sending the request.
async function deleteJob(job: BatchImageJobRow) {
  if (!canDeleteRecord(job) || deletingBatchId.value) return
  closeMoreMenu()
  const key = apiKeyForJob(job)
  if (!key) return
  if (!window.confirm(batchImageText('deleteConfirm'))) return
  deletingBatchId.value = job.id
  try {
    await deleteBatchImageJobRecord(key.key, job.id)
    removeJobFromList(job.id)
    appStore.showSuccess(batchImageText('deleted'))
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('deleteFailed')))
  } finally {
    deletingBatchId.value = ''
  }
}

// deleteSelectedJobs deletes selected terminal job records sequentially after
// one confirmation. Rows without usable keys are skipped; successful deletions
// disappear immediately, and a later failure leaves those prior removals in
// place.
async function deleteSelectedJobs() {
  const rows = selectedRows.value.filter((job) => canDeleteRecord(job))
  if (bulkDeleting.value || rows.length === 0) return
  if (!window.confirm(batchImageText('deleteSelectedConfirm'))) return
  bulkDeleting.value = true
  try {
    for (const row of rows) {
      const key = apiKeyForJob(row)
      if (!key) continue
      deletingBatchId.value = row.id
      await deleteBatchImageJobRecord(key.key, row.id)
      removeJobFromList(row.id)
    }
    appStore.showSuccess(batchImageText('deleted'))
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('deleteFailed')))
  } finally {
    bulkDeleting.value = false
    deletingBatchId.value = ''
  }
}

// markJobDownloaded records the first observed download time in epoch seconds
// for matching rows. Existing downloaded timestamps remain intact, and the
// selected detail receives the same timestamp when it belongs to this batch.
function markJobDownloaded(batchId: string) {
  // Server job timestamps use whole seconds rather than milliseconds.
  const downloadedAt = Math.floor(Date.now() / 1000)
  batchJobs.value = batchJobs.value.map((job) =>
    job.id === batchId ? { ...job, downloaded_at: job.downloaded_at || downloadedAt } : job,
  )
  if (currentJob.value?.id === batchId && !currentJob.value.downloaded_at) {
    currentJob.value = { ...currentJob.value, downloaded_at: downloadedAt }
  }
}

// removeJobFromList removes a job from the table and its selection set. If the
// current detail belongs to that job, it also closes the detail and releases
// its published preview resources.
function removeJobFromList(batchId: string) {
  batchJobs.value = batchJobs.value.filter((job) => job.id !== batchId)
  toggleJobSelection(batchId, false)
  if (currentJob.value?.id === batchId) closeDetail()
}

// canLoadItemPreview reports whether an item has successful status and image
// outputs.
function canLoadItemPreview(item: BatchImageItem) {
  // Item responses accept both successful status spellings, but preview and
  // recovery eligibility also require an actual image count before exposing image actions.
  return (item.status === 'succeeded' || item.status === 'success') && item.image_count > 0
}

// isSuccessfulImageItem reports whether an item contains a successfully
// produced image output.
function isSuccessfulImageItem(item: Pick<BatchImageItem, 'status' | 'image_count'>) {
  // Item responses accept both successful status spellings, but preview and
  // recovery eligibility also require an actual image count before exposing image actions.
  return (item.status === 'succeeded' || item.status === 'success') && item.image_count > 0
}

// detailRootBatchId returns the current job's parent before the selected batch
// or current job identifier. Empty detail state returns an empty string so
// retry recovery classification can treat the root as unavailable.
function detailRootBatchId() {
  return currentJob.value?.parent_batch_id || selectedBatchId.value || currentJob.value?.id || ''
}

// isChildDetailItem reports whether an item belongs to a different batch than
// the detail root. Missing root or item batch identifiers yield false, keeping
// incomplete item metadata out of retry child classification.
function isChildDetailItem(item: Pick<BatchImageDetailItem, 'batch_id'>) {
  const rootBatchId = detailRootBatchId()
  return Boolean(rootBatchId && item.batch_id && item.batch_id !== rootBatchId)
}

// retrySourceCustomID removes trailing retry timestamps to recover the
// original custom identifier. Consecutive retry suffixes are removed together,
// allowing a successful retry of a child to match its root item's identifier.
function retrySourceCustomID(customID: string) {
  // Repeated child retries append more timestamp suffixes, so strip the
  // entire trailing suffix sequence before comparing a success with the root identifier.
  return String(customID || '').replace(/(?:_retry_[a-z0-9]+)+$/i, '')
}

// isRecoveredOriginalFailure reports whether a failed root item has a
// successful retry child. The current detail must resolve a root identity,
// and the item must belong to that root rather than one of its child jobs.
//
// Recovery membership comes from successful child items after their retry
// suffixes are removed. Matching the original custom identifier lets the detail
// mute a failed root entry while retaining that entry's server error and status
// for other readers of the item record.
function isRecoveredOriginalFailure(item: BatchImageDetailItem) {
  const rootBatchId = detailRootBatchId()
  return Boolean(
    rootBatchId &&
    item.batch_id === rootBatchId &&
    item.status === 'failed' &&
    recoveredOriginalCustomIds.value.has(item.custom_id),
  )
}

// detailItemRowClass returns muted row styling for failures recovered by a
// retry.
function detailItemRowClass(item: BatchImageDetailItem) {
  if (isRecoveredOriginalFailure(item)) {
    return 'bg-gray-50/80 text-gray-400 hover:bg-gray-100/80 dark:bg-dark-900/60 dark:text-gray-500 dark:hover:bg-dark-800/70'
  }
  return 'hover:bg-gray-50/70 dark:hover:bg-dark-800/60'
}

// itemPreviewKey resolves an item's batch identity and encodes its first image
// cache key. Item metadata takes precedence over the selected batch and
// current job, keeping child job previews distinct in combined details.
function itemPreviewKey(item: Pick<BatchImageItem, 'batch_id' | 'custom_id'>) {
  return previewCacheKey(
    item.batch_id || selectedBatchId.value || currentJob.value?.id || '',
    item.custom_id,
    0,
  )
}

// hydrateCachedItemPreviews publishes cached thumbnails for successful items
// in a newly loaded detail. Unsupported browser storage, existing URLs, and
// recorded preview failures skip work; individual cache errors behave as
// misses.
//
// Each lookup captures its batch and preview identity before awaiting storage.
// Publication rechecks whether another path has already filled that identity,
// so a completed cache hydration does not overwrite that URL. Detail resets do
// not cancel pending lookups, and their later completion can still publish
// into the page collection.
async function hydrateCachedItemPreviews(detailItems: BatchImageDetailItem[]) {
  const previewableItems = detailItems.filter((item) => canLoadItemPreview(item))
  if (!previewableItems.length || !previewCacheSupported()) return

  await Promise.all(
    previewableItems.map(async (item) => {
      const batchId = item.batch_id || selectedBatchId.value || currentJob.value?.id || ''
      const previewKey = itemPreviewKey(item)
      if (!batchId || itemPreviewUrls[previewKey] || previewErrorIds.value.has(previewKey)) return
      // Browser storage failure behaves like a missing thumbnail during
      // passive hydration; authenticated image downloads remain available through the preview button.
      const cached = await getCachedPreviewBlob(previewCacheKey(batchId, item.custom_id, 0)).catch(
        () => null,
      )
      // Another preview path may have published while storage was pending;
      // keep that existing URL instead of allocating a duplicate for this identity.
      if (!cached || itemPreviewUrls[previewKey]) return
      previewUrls.publish(previewKey, cached)
    }),
  )
}

// loadItems loads the selected batch's items together with its listed retry
// children. It clears existing previews before requesting item lists and
// attaches each source job identity and display name before publishing the
// combined detail rows.
//
// Cached previews hydrate asynchronously after the item rows become available,
// leaving image content downloads on demand. Failures retain any previously
// stored item rows and report an error. Overlapping detail loads share state
// and publish their results on completion without request generation
// cancellation.
async function loadItems() {
  const batchId = selectedBatchId.value || currentJob.value?.id || ''
  if (!batchId) return
  const key = keyForSelectedBatch() || requireApiKey()
  if (!key) return
  loadingItems.value = true
  try {
    // Release displayed resources before reading the next detail set; source
    // job identifiers are attached below before cached thumbnails are hydrated again.
    clearItemPreviews()
    const jobs = detailJobsForBatch(batchId)
    const results = await Promise.all(
      jobs.map(async (job) => {
        const result = await listBatchImageItems(key.key, job.id)
        return (result.data || []).map((item) => ({
          ...item,
          batch_id: job.id,
          source_task_name: detailSourceName(job, batchId),
        }))
      }),
    )
    const detailItems = results.flat()
    items.value = detailItems
    void hydrateCachedItemPreviews(detailItems)
  } catch (error: any) {
    appStore.showError(batchImageErrorMessage(error, batchImageText('loadItemsFailed')))
  } finally {
    loadingItems.value = false
  }
}

// detailJobsForBatch selects the jobs contributing items to the requested
// detail. Listed rows take precedence over the current job; root details
// include loaded children, child details include themselves, and unresolved
// identities return no jobs.
function detailJobsForBatch(batchId: string): BatchImageJobRow[] {
  const row = batchJobs.value.find((job) => job.id === batchId)
  const base =
    row ||
    (currentJob.value && currentJob.value.id === batchId
      ? toJobRow(currentJob.value, keyForSelectedBatch() || selectedApiKey.value)
      : null)
  if (!base) return []
  if (base.parent_batch_id) return [base]
  return [base, ...(childrenByParent.value.get(base.id) || [])]
}

// detailSourceName labels an item source as the main task or a retry child.
function detailSourceName(
  job: Pick<BatchImageJobRow, 'id' | 'task_name' | 'parent_batch_id'>,
  rootBatchId: string,
) {
  const name = job.task_name || job.id
  if (job.id === rootBatchId) return t('batchImage.detail.mainTask', { name })
  return t('batchImage.detail.childTask', { name })
}

// loadItemPreview loads the first successful image for a detail item and
// publishes its preview URL. The encoded batch and custom identifier separate
// same-named items across jobs, while existing healthy URLs skip duplicate
// work.
//
// A failed displayed URL is removed before cache lookup. Cache hits display
// directly; misses download authenticated content, attempt thumbnail encoding,
// and display original bytes when encoding fails. Encoded thumbnails and
// originals up to one MiB are persisted asynchronously. Loading and error sets
// track that identity, while pending requests continue through detail resets
// and unmount.
async function loadItemPreview(item: BatchImageItem) {
  const batchId = item.batch_id || selectedBatchId.value || currentJob.value?.id || ''
  const previewKey = itemPreviewKey(item)
  if (
    !batchId ||
    !canLoadItemPreview(item) ||
    (itemPreviewUrls[previewKey] && !previewErrorIds.value.has(previewKey))
  )
    return
  const key = keyForSelectedBatch() || requireApiKey()
  if (!key) return
  const cacheKey = previewCacheKey(batchId, item.custom_id, 0)
  previewLoadingIds.value = new Set([...previewLoadingIds.value, previewKey])
  try {
    previewErrorIds.value = new Set([...previewErrorIds.value].filter((id) => id !== previewKey))
    // A displayed image error permits retrying this identity; revoke its
    // previous URL before attempting cache lookup or fetching replacement image bytes.
    previewUrls.remove(previewKey)
    const cached = await getCachedPreviewBlob(cacheKey)
    if (cached) {
      previewUrls.publish(previewKey, cached)
      return
    }
    const blob = await getBatchImageItemContent(key.key, batchId, item.custom_id, 0)
    // Decoding and canvas support vary by browser, so failed thumbnail
    // creation still displays the downloaded original instead of failing the preview.
    const thumbnail = await createThumbnailBlob(blob).catch(() => blob)
    previewUrls.publish(previewKey, thumbnail)
    // Encoded thumbnails enter persistent storage, while original image bytes
    // qualify only below the small-image limit to constrain failed-encoding cache pressure.
    if (thumbnail !== blob || thumbnail.size <= 1024 * 1024) {
      void putCachedPreviewBlob(cacheKey, thumbnail)
    }
  } catch (error: any) {
    previewErrorIds.value = new Set([...previewErrorIds.value, previewKey])
    appStore.showError(batchImageErrorMessage(error, batchImageText('loadPreviewFailed')))
  } finally {
    const next = new Set(previewLoadingIds.value)
    next.delete(previewKey)
    previewLoadingIds.value = next
  }
}

// openImagePreview opens an item whose preview URL exists and has no display
// error. Missing URLs and failed image displays leave the currently opened
// preview unchanged so the modal receives usable image content.
function openImagePreview(item: BatchImageItem) {
  const previewKey = itemPreviewKey(item)
  if (!itemPreviewUrls[previewKey] || previewErrorIds.value.has(previewKey)) return
  previewImageItem.value = item
}

// closeImagePreview clears the item currently shown by the enlarged preview
// modal.
function closeImagePreview() {
  previewImageItem.value = null
}

// handlePreviewError removes the failed preview URL and marks its encoded
// identity as errored. The next manual preview load can retry that image,
// while passive cache hydration skips identities with recorded display errors.
function handlePreviewError(customID: string) {
  previewUrls.remove(customID)
  previewErrorIds.value = new Set([...previewErrorIds.value, customID])
}

// clearItemPreviews releases all currently published preview URLs and clears
// detail preview indicators. It also closes the prompt popover and enlarged
// image modal; pending cache or download operations are owned by their
// original callers.
function clearItemPreviews() {
  closePromptPopover()
  // Revoke the URLs already published before clearing display indicators,
  // so closing details or unmounting releases the browser resources currently shown.
  previewUrls.clear()
  previewLoadingIds.value = new Set()
  previewErrorIds.value = new Set()
  previewImageItem.value = null
}

// copyInstruction copies the current localized agent instructions with a
// success notification.
function copyInstruction() {
  void copyToClipboard(agentInstruction.value, batchImageText('copiedInstruction'))
}

// statusLabel returns the localized job status, distinguishing partial success
// and all failures for completed job records. Raw status strings use their
// direct label, and unrecognized values are returned unchanged for display.
function statusLabel(
  jobOrStatus: BatchImageStatus | Pick<BatchImageJob, 'status' | 'success_count' | 'fail_count'>,
) {
  const status = typeof jobOrStatus === 'string' ? jobOrStatus : jobOrStatus.status
  if (typeof jobOrStatus !== 'string' && status === 'completed' && jobOrStatus.fail_count > 0) {
    if (jobOrStatus.success_count > 0) return t('batchImage.status.partialSuccess')
    return t('batchImage.status.allFailed')
  }
  const statusKeys: Record<string, string> = {
    queued: 'queued',
    running: 'running',
    // Legacy indexing and current processing_results both describe result assembly,
    // so their status labels share the same localized processing-results translation key.
    indexing: 'processingResults',
    processing_results: 'processingResults',
    settling: 'settling',
    completed: 'completed',
    failed: 'failed',
    cancelled: 'cancelled',
    output_deleted: 'outputDeleted',
  }
  const key = statusKeys[status]
  return key ? t(`batchImage.status.${key}`) : status
}

// statusBadgeClass selects job badge styling from status and available
// completion counts. Completed records with failures distinguish partial
// success from all failures, while status-only callers receive the base status
// color.
function statusBadgeClass(
  jobOrStatus: BatchImageStatus | Pick<BatchImageJob, 'status' | 'success_count' | 'fail_count'>,
) {
  const status = typeof jobOrStatus === 'string' ? jobOrStatus : jobOrStatus.status
  if (typeof jobOrStatus !== 'string' && status === 'completed' && jobOrStatus.fail_count > 0) {
    if (jobOrStatus.success_count > 0) return 'badge-warning'
    return 'badge-danger'
  }
  if (status === 'completed') return 'badge-success'
  if (status === 'failed' || status === 'cancelled') return 'badge-danger'
  if (status === 'output_deleted') return 'badge-gray'
  return 'badge-primary'
}

// itemStatusLabel returns the localized label for known image item statuses.
// Both success spellings share a label, and unknown server values pass through
// unchanged so the detail can still display them.
function itemStatusLabel(status: string) {
  const statusKeys: Record<string, string> = {
    pending: 'pending',
    succeeded: 'succeeded',
    success: 'succeeded',
    failed: 'failed',
    cancelled: 'cancelled',
  }
  const key = statusKeys[status]
  return key ? t(`batchImage.itemStatus.${key}`) : status
}

// itemDisplayStatusLabel labels recovered root failures before using the
// item's server status.
function itemDisplayStatusLabel(item: BatchImageDetailItem) {
  if (isRecoveredOriginalFailure(item)) return t('batchImage.itemStatus.recovered')
  return itemStatusLabel(item.status)
}

// itemStatusBadgeClass returns the badge class for an image item's status.
function itemStatusBadgeClass(status: string) {
  if (status === 'succeeded' || status === 'success') return 'badge-success'
  if (status === 'failed' || status === 'cancelled') return 'badge-danger'
  return 'badge-primary'
}

// itemDisplayStatusBadgeClass mutes recovered failures and otherwise uses the
// item status badge.
function itemDisplayStatusBadgeClass(item: BatchImageDetailItem) {
  if (isRecoveredOriginalFailure(item)) return 'badge-gray'
  return itemStatusBadgeClass(item.status)
}

// itemResultLabel describes the item result using recovery, error, and preview
// availability. Recovered failures take precedence over their original error,
// while successful items distinguish a loaded preview from an available
// download.
function itemResultLabel(item: BatchImageDetailItem) {
  if (isRecoveredOriginalFailure(item)) return t('batchImage.itemResult.recoveredByRetry')
  if (item.error) return friendlyItemError(item.error)
  if (item.status === 'succeeded' || item.status === 'success') {
    return itemPreviewUrls[itemPreviewKey(item)]
      ? t('batchImage.itemResult.readyPreview')
      : t('batchImage.itemResult.readyDownload')
  }
  if (item.status === 'failed') return t('batchImage.itemResult.noUsableImage')
  if (item.status === 'cancelled') return t('batchImage.itemResult.cancelled')
  return t('batchImage.itemResult.waiting')
}

// itemResultClass returns result cell styling for recovered, failed,
// successful, or pending items.
function itemResultClass(item: BatchImageDetailItem) {
  if (isRecoveredOriginalFailure(item))
    return 'bg-gray-100 text-gray-500 ring-gray-200 dark:bg-dark-800 dark:text-gray-400 dark:ring-dark-700'
  if (item.error || item.status === 'failed' || item.status === 'cancelled')
    return 'bg-red-50 text-red-700 ring-red-100 dark:bg-red-950/30 dark:text-red-300 dark:ring-red-900/50'
  if (item.status === 'succeeded' || item.status === 'success')
    return 'bg-emerald-50 text-emerald-700 ring-emerald-100 dark:bg-emerald-950/30 dark:text-emerald-300 dark:ring-emerald-900/50'
  return 'bg-gray-50 text-gray-500 ring-gray-200 dark:bg-dark-800 dark:text-gray-400 dark:ring-dark-700'
}

// friendlyItemError returns a localized explanation for known empty-output and
// provider item failures. Other errors use their message or code, and absent
// error information displays the page's dash placeholder.
function friendlyItemError(error: BatchImageItem['error']) {
  if (!error) return '-'
  if (error.code === 'EMPTY_IMAGE_OUTPUT') return t('batchImage.itemResult.emptyImageOutput')
  if (error.code === 'PROVIDER_ITEM_FAILED') return t('batchImage.itemResult.providerItemFailed')
  return error.message || error.code || '-'
}

// formatMoney formats job costs with the site balance unit and two decimals.
function formatMoney(value: number | null | undefined) {
  return formatBalanceAmount(value, { fractionDigits: 2 })
}

// terminalZeroCost reports whether a failed or cancelled job has no settled
// cost.
function terminalZeroCost(job: Pick<BatchImageJob, 'status' | 'actual_cost'>) {
  return job.actual_cost === null && (job.status === 'failed' || job.status === 'cancelled')
}

// costLabel formats settled cost, terminal zero cost, or the current held
// amount. A known actual cost takes precedence, and an unresolved nonterminal
// cost is presented as a hold using the site balance format.
function costLabel(job: Pick<BatchImageJob, 'status' | 'hold_amount' | 'actual_cost'>) {
  if (job.actual_cost !== null) return formatMoney(job.actual_cost)
  if (terminalZeroCost(job)) return formatMoney(0)
  return t('batchImage.detail.holdCost', { amount: formatMoney(job.hold_amount) })
}

// BatchImageTextKey enumerates localized operation messages available to batch
// image handlers.
type BatchImageTextKey =
  | 'loadKeysFailed'
  | 'loadModelsFailed'
  | 'loadJobsFailed'
  | 'selectApiKey'
  | 'noModelsForKey'
  | 'selectModel'
  | 'promptRequired'
  | 'submitted'
  | 'submitFailed'
  | 'refreshFailed'
  | 'cancelConfirm'
  | 'cancelled'
  | 'cancelFailed'
  | 'batchDownloadStarted'
  | 'downloadFailed'
  | 'retrySubmitted'
  | 'retryFailed'
  | 'retryMissingPrompts'
  | 'deleteConfirm'
  | 'deleteSelectedConfirm'
  | 'deleted'
  | 'deleteFailed'
  | 'loadItemsFailed'
  | 'loadPreviewFailed'
  | 'copiedInstruction'
  | 'loadingModels'
  | 'noModels'
  | 'noModelsHint'
  | 'noCompatibleProvider'
  | 'unsupportedProvider'
  | 'providerSubmitFailed'
  | 'vertexGcsBucketMissing'
  | 'queueFailed'
  | 'billingHoldFailed'
  | 'groupDisabled'
  | 'pricingMissing'
  | 'insufficientBalance'
  | 'invalidModel'
  | 'invalidItems'
  | 'duplicateCustomId'
  | 'promptTooLong'
  | 'invalidReferenceImage'
  | 'tooManyReferenceImages'
  | 'referenceImagesTooLarge'
  | 'tooManyOutputImages'
  | 'idempotencyConflict'
  | 'notReady'
  | 'outputDeleted'
  | 'resultMissing'
  | 'itemFailed'
  | 'itemImageIndexOutOfRange'
  | 'downloadLimited'
  | 'downloadTooLarge'
  | 'deleteNotReady'
  | 'disabled'
  | 'authRequired'
  | 'adminReference'
  | 'errorReference'

// isZhLocale reports whether the active interface locale uses a Chinese
// prefix.
function isZhLocale() {
  return String(locale.value || '')
    .toLowerCase()
    .startsWith('zh')
}

// batchImageText looks up a supported batch image operation message key.
function batchImageText(key: BatchImageTextKey) {
  return t(`batchImage.messages.${key}`)
}

// batchImageErrorReference formats available error code, request identifier,
// and HTTP status references. A code suppresses the redundant status field,
// and localized separators follow the active interface language when multiple
// references are present.
function batchImageErrorReference(error: any) {
  const parts: string[] = []
  const code = String(error?.code || '').trim()
  const requestId = String(error?.requestId || '').trim()
  const status = String(error?.status || '').trim()
  if (code) parts.push(t('batchImage.messages.errorCodeRef', { code }))
  if (requestId) parts.push(t('batchImage.messages.requestIdRef', { id: requestId }))
  if (!code && status) parts.push(t('batchImage.messages.httpStatusRef', { status }))
  return parts.length ? `（${parts.join(isZhLocale() ? '，' : ', ')}）` : ''
}

// batchImageAdminError combines an operation explanation with diagnostics and
// the administrator contact hint.
function batchImageAdminError(base: string, error: any) {
  const reference = batchImageErrorReference(error)
  return `${base}${reference ? ` ${reference}` : ''} ${batchImageText('adminReference')}`
}

// batchImagePlainError returns the supplied user-facing explanation as its
// display text.
function batchImagePlainError(base: string) {
  return base
}

// batchImageErrorMessage selects a localized explanation for batch image API
// failures. Known authentication, validation, resource, and operation codes
// receive dedicated messages; provider and infrastructure failures include
// diagnostics and an administrator hint.
//
// Compatibility message patterns recognize provider failures even when a
// specific code is absent. Unrecognized Chinese-language errors append the
// server message or administrator hint to the operation context, while other
// locales prefer the server message and use the supplied context when it is
// empty. The formatter retains request references for troubleshooting.
function batchImageErrorMessage(error: any, fallback: string) {
  const code = String(error?.code || '').trim()
  const message = String(error?.message || '').trim()
  // HTTP-style code strings and named batch errors share user actions,
  // allowing normalized API failures to choose the same localized authentication explanation.
  if (code === 'API_KEY_REQUIRED' || code === '401') {
    return batchImagePlainError(batchImageText('authRequired'))
  }
  if (
    code === 'BATCH_IMAGE_NO_PROVIDER_AVAILABLE' ||
    /no compatible batch image provider/i.test(message)
  ) {
    return batchImageAdminError(batchImageText('noCompatibleProvider'), error)
  }
  if (
    code === 'BATCH_IMAGE_UNSUPPORTED_PROVIDER' ||
    /unsupported batch image provider/i.test(message)
  ) {
    return batchImageAdminError(batchImageText('unsupportedProvider'), error)
  }
  if (
    code === 'BATCH_IMAGE_VERTEX_GCS_BUCKET_MISSING' ||
    code === 'VERTEX_MANAGED_GCS_BUCKET_MISSING'
  ) {
    return batchImageAdminError(batchImageText('vertexGcsBucketMissing'), error)
  }
  if (
    code === 'BATCH_IMAGE_PROVIDER_SUBMIT_FAILED' ||
    code === 'BATCH_IMAGE_PROVIDER_MISSING_API_KEY' ||
    code === 'BATCH_IMAGE_PROVIDER_MISSING_SERVICE_ACCOUNT' ||
    code === 'BATCH_IMAGE_PROVIDER_UNSUPPORTED_PROVIDER'
  ) {
    return batchImageAdminError(batchImageText('providerSubmitFailed'), error)
  }
  if (code === 'BATCH_IMAGE_QUEUE_FAILED' || code === 'BATCH_IMAGE_QUEUE_NOT_CONFIGURED') {
    return batchImageAdminError(batchImageText('queueFailed'), error)
  }
  if (code === 'BATCH_IMAGE_BILLING_HOLD_FAILED') {
    return batchImageAdminError(batchImageText('billingHoldFailed'), error)
  }
  if (code === 'BATCH_IMAGE_GROUP_DISABLED') {
    return batchImagePlainError(batchImageText('groupDisabled'))
  }
  if (code === 'BATCH_IMAGE_SETTLEMENT_PRICING_MISSING') {
    return batchImageAdminError(batchImageText('pricingMissing'), error)
  }
  if (code === 'BATCH_IMAGE_INSUFFICIENT_BALANCE') {
    return batchImagePlainError(batchImageText('insufficientBalance'))
  }
  if (code === 'BATCH_IMAGE_INVALID_MODEL') {
    return batchImageText('invalidModel')
  }
  if (code === 'BATCH_IMAGE_INVALID_ITEMS') {
    return batchImageText('invalidItems')
  }
  if (code === 'BATCH_IMAGE_DUPLICATE_CUSTOM_ID') {
    return batchImageText('duplicateCustomId')
  }
  if (code === 'BATCH_IMAGE_PROMPT_TOO_LONG') {
    return batchImageText('promptTooLong')
  }
  if (code === 'BATCH_IMAGE_INVALID_REFERENCE_IMAGE') {
    return batchImageText('invalidReferenceImage')
  }
  if (code === 'BATCH_IMAGE_TOO_MANY_REFERENCE_IMAGES') {
    return batchImageText('tooManyReferenceImages')
  }
  if (code === 'BATCH_IMAGE_REFERENCE_IMAGES_TOO_LARGE') {
    return batchImageText('referenceImagesTooLarge')
  }
  if (code === 'BATCH_IMAGE_TOO_MANY_OUTPUT_IMAGES') {
    return batchImageText('tooManyOutputImages')
  }
  if (code === 'BATCH_IMAGE_IDEMPOTENCY_CONFLICT') {
    return batchImagePlainError(batchImageText('idempotencyConflict'))
  }
  if (code === 'BATCH_IMAGE_NOT_READY') {
    return batchImageText('notReady')
  }
  if (code === 'BATCH_IMAGE_OUTPUT_DELETED') {
    return batchImageText('outputDeleted')
  }
  if (code === 'BATCH_IMAGE_RESULT_MISSING') {
    return batchImageAdminError(batchImageText('resultMissing'), error)
  }
  if (code === 'BATCH_IMAGE_ITEM_FAILED') {
    return batchImagePlainError(batchImageText('itemFailed'))
  }
  if (code === 'BATCH_IMAGE_ITEM_IMAGE_INDEX_OUT_OF_RANGE') {
    return batchImagePlainError(batchImageText('itemImageIndexOutOfRange'))
  }
  if (code === 'BATCH_IMAGE_DOWNLOAD_LIMITED') {
    return batchImageText('downloadLimited')
  }
  if (code === 'BATCH_IMAGE_DOWNLOAD_TOO_LARGE') {
    return batchImageText('downloadTooLarge')
  }
  if (code === 'BATCH_IMAGE_RECORD_DELETE_NOT_READY') {
    return batchImagePlainError(batchImageText('deleteNotReady'))
  }
  if (code === 'BATCH_IMAGE_DISABLED') {
    return batchImageAdminError(batchImageText('disabled'), error)
  }
  if (code === 'INTERNAL_ERROR' || code === '500') {
    return batchImageAdminError(fallback, error)
  }
  if (isZhLocale()) {
    const detail = message
      ? `${batchImageText('errorReference')}：${message}`
      : batchImageText('adminReference')
    return `${fallback}。${detail} ${batchImageErrorReference(error)}`
  }
  // Unmapped non-Chinese failures prefer the server detail so actionable
  // messages remain visible; absent details use the operation-specific localized failure text.
  return message || fallback
}

// formatDate formats a server epoch-second timestamp in the active locale. A
// zero timestamp returns an empty string, allowing table cells to distinguish
// missing server dates from the Unix epoch.
function formatDate(timestamp: number) {
  if (!timestamp) return ''
  // API timestamps are seconds; browser dates require milliseconds.
  return new Date(timestamp * 1000).toLocaleString(getLocale())
}

// defaultTaskName creates a localized task name from a server timestamp or the
// current time. A provided truthy timestamp is measured in seconds, while
// omitted or zero values use a newly sampled browser date.
function defaultTaskName(timestamp?: number) {
  // Convert server seconds before creating locale-aware task labels.
  const date = timestamp ? new Date(timestamp * 1000) : new Date()
  return date.toLocaleString(getLocale())
}

// Mount starts page data loading and periodic cache maintenance, then installs
// overlay dismissal listeners that the unmount hook releases with the page resources.
onMounted(() => {
  void appStore.fetchPublicSettings()
  void refreshPage()
  void cleanupPreviewCache()
  // Hourly maintenance expires thumbnails even when the user leaves this
  // page open without downloading additional images that would trigger a cache write.
  previewCacheCleanupTimer = setInterval(
    () => {
      void cleanupPreviewCache()
    },
    60 * 60 * 1000,
  )
  document.addEventListener('click', closeMoreMenu)
  window.addEventListener('resize', closeMoreMenu)
  window.addEventListener('scroll', closeMoreMenu, true)
  window.addEventListener('resize', closePromptPopover)
  window.addEventListener('scroll', closePromptPopover, true)
})

watch(
  () => form.apiKeyId,
  () => {
    void loadAvailableModels()
  },
)

watch(
  () => form.model,
  () => {
    const limit = selectedModelReferenceLimit.value
    if (limit <= 0) {
      // A model without reference support discards the current editor attachments,
      // keeping the next added prompt consistent with the new selection's capabilities.
      referenceImageDrafts.value = []
      return
    }
    if (referenceImageDrafts.value.length > limit) {
      // Changing to a smaller model attachment allowance keeps the earliest
      // editor references and removes excess drafts before another prompt is committed.
      referenceImageDrafts.value = referenceImageDrafts.value.slice(0, limit)
    }
  },
)

// Unmount stops future polling and maintenance ticks before releasing currently
// published previews and listeners; already pending API and cache requests continue independently.
onBeforeUnmount(() => {
  stopPolling()
  if (previewCacheCleanupTimer) {
    clearInterval(previewCacheCleanupTimer)
    previewCacheCleanupTimer = null
  }
  clearItemPreviews()
  document.removeEventListener('click', closeMoreMenu)
  window.removeEventListener('resize', closeMoreMenu)
  window.removeEventListener('scroll', closeMoreMenu, true)
  window.removeEventListener('resize', closePromptPopover)
  window.removeEventListener('scroll', closePromptPopover, true)
})
</script>

<style scoped>
/* Row action buttons stack their icon and label within the table action cell. */
.batch-row-action {
  display: flex !important;
  flex-direction: column !important;
  align-items: center !important;
  justify-content: center !important;
  min-width: 42px;
  line-height: 1;
  outline: none;
}

/* Action focus keeps the shared control presentation with its local outline disabled. */
.batch-row-action:focus {
  outline: none;
}

/* Stacked icons discard the horizontal margin used by inline icon buttons. */
.batch-row-action :deep(svg) {
  margin-right: 0 !important;
}

/* Prompt triggers keep their table text presentation while the popover is focused. */
.batch-prompt-trigger:focus {
  outline: none;
  box-shadow: none;
}

/* Prompt text remains selectable for copying from the floating detail surface. */
.batch-prompt-popover {
  user-select: text;
}

/* Long prompts use a narrow scrollbar within the bounded popover content. */
.batch-prompt-popover p {
  scrollbar-width: thin;
}
</style>
