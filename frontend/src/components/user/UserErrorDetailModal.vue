<template>
  <BaseDialog
    :show="show"
    :title="t('usage.errors.detail.title')"
    width="wide"
    @close="emit('update:show', false)"
  >
    <!-- Loading details -->
    <ContentSkeleton v-if="loading" variant="detail" :rows="8" class="p-6" />

    <!-- Retrieval failure -->
    <div v-else-if="loadError" class="py-8 text-center text-sm text-red-500">
      {{ t('usage.errors.detail.loadFailed') }}
    </div>

    <!-- Selected error details -->
    <div v-else-if="detail" class="space-y-4 text-sm">
      <div class="grid grid-cols-2 gap-x-6 gap-y-3">
        <!-- Request identifier -->
        <div v-if="detail.request_id" class="col-span-2">
          <span class="font-medium text-gray-500 dark:text-dark-400">{{ t('requests.id') }}</span>
          <p class="mt-0.5"><RequestIdLink :value="detail.request_id" full /></p>
        </div>
        <!-- Recorded time -->
        <div>
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.time')
          }}</span>
          <p class="mt-0.5 text-gray-900 dark:text-dark-100">
            {{ formatDateTime(detail.created_at) }}
          </p>
        </div>
        <!-- Model -->
        <div>
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.model')
          }}</span>
          <p class="mt-0.5 text-gray-900 dark:text-dark-100">{{ detail.model || '-' }}</p>
        </div>
        <!-- Endpoint -->
        <div>
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.endpoint')
          }}</span>
          <p class="mt-0.5 text-gray-900 dark:text-dark-100">
            {{ detail.inbound_endpoint || '-' }}
          </p>
        </div>
        <!-- HTTP status -->
        <div>
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.status')
          }}</span>
          <p class="mt-0.5">
            <span class="badge" :class="statusClass(detail.status_code)">{{
              detail.status_code || '-'
            }}</span>
          </p>
        </div>
        <!-- Error category -->
        <div>
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.category')
          }}</span>
          <p class="mt-0.5 text-gray-900 dark:text-dark-100">
            {{ t('usage.errors.categories.' + detail.category) }}
          </p>
        </div>
        <!-- Platform -->
        <div>
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.platform')
          }}</span>
          <p class="mt-0.5 text-gray-900 dark:text-dark-100">{{ detail.platform || '-' }}</p>
        </div>
        <!-- Upstream HTTP status -->
        <div v-if="detail.upstream_status_code != null">
          <span class="font-medium text-gray-500 dark:text-dark-400">{{
            t('usage.errors.detail.upstreamStatus')
          }}</span>
          <p class="mt-0.5 text-gray-900 dark:text-dark-100">{{ detail.upstream_status_code }}</p>
        </div>
      </div>

      <!-- Error message -->
      <div v-if="detail.message">
        <span class="font-medium text-gray-500 dark:text-dark-400">{{
          t('usage.errors.message')
        }}</span>
        <p class="mt-0.5 text-gray-900 dark:text-dark-100 break-all">{{ detail.message }}</p>
      </div>

      <!-- Stored error response body -->
      <div v-if="detail.error_body">
        <span class="font-medium text-gray-500 dark:text-dark-400">{{
          t('usage.errors.detail.responseBody')
        }}</span>
        <pre
          class="mt-1 overflow-auto max-h-[40vh] whitespace-pre-wrap break-all rounded-surface bg-gray-50 dark:bg-dark-900 border border-gray-200 dark:border-dark-700 p-3 text-xs text-gray-800 dark:text-dark-200"
          >{{ detail.error_body }}</pre>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import RequestIdLink from '@/components/common/RequestIdLink.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getMyErrorDetail } from '@/api/usage'
import { formatDateTime } from '@/utils/format'
import type { UserErrorRequestDetail } from '@/api/usage.types'

// UserErrorDetailModal.vue loads one user-visible error record when its dialog opens or
// selection changes. The detail view distinguishes loading, retrieval failure, and
// populated content; closing clears the displayed record while the parent retains ownership
// of visibility and selection.

// props supplies parent-owned visibility and the selected error identifier used by the
// detail watcher.
const props = defineProps<{
  show: boolean // parent dialog visibility
  // errorId selects the record to fetch while visible; null leaves the watcher without a
  // request target.
  errorId: number | null
}>()

// emit publishes visibility changes so the table owner can dismiss the selected error
// detail dialog.
const emit = defineEmits<{
  // update:show requests a visibility change while the parent retains the selected error
  // identity.
  (e: 'update:show', v: boolean): void
}>()

// The translation binding supplies labels for detail fields, loading failure, and the
// dialog controls.
const { t } = useI18n()

// Detail state separates request progress, retrieval failure, and the last successfully
// assigned error record.
const loading = ref(false)
const loadError = ref(false)
const detail = ref<UserErrorRequestDetail | null>(null)

watch(
  () => [props.show, props.errorId] as const,
  ([show, id]) => {
    if (show && id != null) {
      fetchDetail(id)
    } else if (!show) {
      detail.value = null
      loadError.value = false
    }
  },
)

// fetchDetail clears the previous record and retrieves the selected user error. Failures
// log diagnostic context and show the error state; overlapping calls can still finish in
// any order.
async function fetchDetail(id: number) {
  loading.value = true
  loadError.value = false
  detail.value = null
  try {
    detail.value = await getMyErrorDetail(id)
  } catch (e) {
    // The previous detail was cleared before retrieval, so failure displays the error
    // panel rather than showing another selected record's content.
    console.error('[UserErrorDetailModal] Failed to load error detail:', e)
    loadError.value = true
  } finally {
    loading.value = false
  }
}

// statusClass maps server failures and throttling to their error badge palettes. Status 429
// uses a warning badge, and every remaining code uses the neutral gray badge.
function statusClass(code: number) {
  if (code >= 500) return 'badge-danger'
  if (code === 429) return 'badge-warning'
  return 'badge-gray'
}
</script>
