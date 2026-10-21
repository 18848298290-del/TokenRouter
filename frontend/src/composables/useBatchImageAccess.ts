import { computed, ref } from 'vue'
import { keysAPI } from '@/api/keys'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/api/keys.types'

// useBatchImageAccess.ts checks whether an active API key belongs to a group allowing
// image batches. The cached result and pending promise are shared across all composable
// consumers. Ordinary refresh calls reuse cached results or an unfinished scan, while
// forced calls can start overlapping scans against the same state. Authentication is
// checked before either reuse path.
//
// Each scan requests pages in creation order and stops at the first eligible key or the
// end of the list. Failures become a cached denial. Completion updates shared state
// directly, so forced scans and identity changes have no generation guard against older
// completions.

// loaded, loading, hasAllowedBatchImageKey, and pendingLoad share one module-wide access
// scan across consumers; pageSize bounds each request.
const loaded = ref(false)
const loading = ref(false)
const hasAllowedBatchImageKey = ref(false)
// pendingLoad is reused by ordinary calls until completion, while a forced call replaces
// the shared promise reference.
let pendingLoad: Promise<boolean> | null = null
const pageSize = 100

// keyAllowsBatchImage checks active status and the group image-batch protocol flag.
function keyAllowsBatchImage(key: ApiKey): boolean {
  return key.status === 'active' && key.group?.allowed_protocols?.includes('image_batches') === true
}

// loadBatchImageAccess scans active keys and returns whether one permits image batches. A
// normal call reuses a loaded result before checking pending work; force bypasses both
// reuse paths. Unauthenticated calls publish denial immediately.
//
// The scan updates module state as each terminal result arrives, and request errors become
// cached false results. Callers that force concurrent scans receive separate promises
// whose completions can overwrite shared access and loading flags.
async function loadBatchImageAccess(force = false): Promise<boolean> {
  const authStore = useAuthStore()
  if (!authStore.isAuthenticated) {
    loaded.value = true
    hasAllowedBatchImageKey.value = false
    return false
  }

  if (loaded.value && !force) {
    return hasAllowedBatchImageKey.value
  }

  if (pendingLoad && !force) {
    return pendingLoad
  }

  loading.value = true
  pendingLoad = (async () => {
    let page = 1
    while (true) {
      const response = await keysAPI.list(page, pageSize, {
        status: 'active',
        sort_by: 'created_at',
        sort_order: 'desc',
      })

      if ((response.items || []).some(keyAllowsBatchImage)) {
        hasAllowedBatchImageKey.value = true
        loaded.value = true
        return true
      }

      if (page >= response.pages || (response.items || []).length === 0) {
        hasAllowedBatchImageKey.value = false
        loaded.value = true
        return false
      }

      page += 1
    }
  })()
    // A failed key listing denies access and marks the result loaded, so subsequent
    // ordinary calls reuse denial until forced refresh.
    .catch(() => {
      hasAllowedBatchImageKey.value = false
      loaded.value = true
      return false
    })
    .finally(() => {
      loading.value = false
      pendingLoad = null
    })

  return pendingLoad
}

// useBatchImageAccess exposes shared access flags and the refresh operation. Creating a
// consumer does not start a scan; callers request loading and decide when a forced refresh
// is needed.
export function useBatchImageAccess() {
  const canUseBatchImage = computed(() => hasAllowedBatchImageKey.value)

  return {
    canUseBatchImage,
    batchImageAccessLoaded: computed(() => loaded.value),
    batchImageAccessLoading: computed(() => loading.value),
    refreshBatchImageAccess: loadBatchImageAccess,
  }
}
