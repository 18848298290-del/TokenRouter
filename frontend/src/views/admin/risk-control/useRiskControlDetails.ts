import { ref } from 'vue'
import type { Ref } from 'vue'
import type {
  ContentModerationCyberWarning,
  ContentModerationLog,
  ContentModerationMedia,
} from '@/api/admin/riskControl'

/**
 * useRiskControlDetails.ts loads moderation and cyber details and owns their
 * authenticated media previews. Both dialogs share a generation counter because
 * their image elements read the same URL map. Opening either detail invalidates
 * pending work from the other detail, and closing either dialog invalidates the
 * current generation before releasing its published URLs.
 *
 * Detail requests retain the selected list row until the full response arrives.
 * Media downloads run together after that response and publish together after
 * all downloads finish. Failed downloads leave individual previews unavailable.
 * A superseded batch releases its newly created URLs when it finishes; the page
 * calls dispose during unmount to apply the same ownership rule to pending work.
 */

/** DetailServices supplies authenticated requests and the page's localized error presentation. */
interface DetailServices {
  /** getLog retrieves the complete moderation record for its stored identifier. */
  getLog: (id: number) => Promise<ContentModerationLog> // Moderation record detail request.
  /** getCyberWarning retrieves the complete cyber warning for its stored identifier. */
  getCyberWarning: (id: number) => Promise<ContentModerationCyberWarning> // Cyber warning detail request.
  /** getMediaContent downloads authenticated snapshot bytes for one stored media identifier. */
  getMediaContent: (id: number) => Promise<Blob> // Authenticated stored media download.
  /** onError displays the supplied failure using the owning page notification service. */
  onError: (error: unknown) => void // Current detail failure notification.
}

/**
 * useRiskControlDetails creates the detail dialog state and its media resource
 * owner. Call it once during page setup and call dispose when the page unmounts.
 * Open actions expose the selected row immediately and replace it after its
 * request succeeds, keeping the page responsive while full audit content loads.
 *
 * A single generation decides which asynchronous detail or media result may
 * publish. Closing either dialog and opening another invalidate pending work.
 * Stale failures remain silent, while stale media batches revoke their preview
 * URLs before returning. Each dialog keeps its own row and loading flag.
 */
export function useRiskControlDetails(services: DetailServices) {
  // Both dialog states belong to this page instance; switching the shared
  // generation invalidates requests while each close action clears its own row.
  const inputDetailRow = ref<ContentModerationLog | null>(null)
  const cyberDetailRow = ref<ContentModerationCyberWarning | null>(null)
  const inputDetailLoading = ref(false)
  const cyberDetailLoading = ref(false)
  // mediaObjectURLs owns published previews until the next open, close, or
  // disposal. detailLoadVersion also invalidates media that has not published yet.
  const mediaObjectURLs = ref<Record<number, string>>({})
  let detailLoadVersion = 0

  /**
   * revokeDetailMediaURLs releases the currently published previews and clears
   * their lookup map. Pending batches retain their own URLs until their
   * generation check decides whether they can publish.
   */
  function revokeDetailMediaURLs() {
    for (const value of Object.values(mediaObjectURLs.value)) {
      URL.revokeObjectURL(value)
    }
    mediaObjectURLs.value = {}
  }

  /**
   * dispose invalidates pending detail work and releases published preview URLs.
   * The page calls it when unmounting, and close actions call it before clearing
   * their corresponding dialog state.
   */
  function dispose() {
    // Invalidate first so any continuation awaiting detail or media cannot
    // republish content after its owning dialog has been closed or unmounted.
    detailLoadVersion++
    revokeDetailMediaURLs()
  }

  /**
   * loadDetailMedia downloads ready snapshots and publishes one preview map for
   * the supplied generation. The caller obtains that generation before its
   * detail request and passes it through unchanged, so media belongs to the same
   * selected record even when downloads complete in a different order.
   *
   * Failed downloads contribute no preview and do not reject the whole batch.
   * Every successful download creates an owned Blob URL. After all downloads
   * settle, an outdated generation releases those URLs instead of publishing
   * them into the active detail's shared preview map.
   */
  async function loadDetailMedia(mediaItems: ContentModerationMedia[], version: number) {
    // Snapshot identifiers come from distinct stored media rows and identify the
    // preview map entries used by both dialogs to render their authenticated images.
    const readyItems = mediaItems.filter((item) => item.snapshot_status === 'ready')
    // Fetch protected bytes through the authenticated client before creating URLs;
    // image elements cannot supply that client's authentication when reading media.
    const entries = await Promise.all(
      readyItems.map(async (item) => {
        try {
          const blob = await services.getMediaContent(item.id)
          return [item.id, URL.createObjectURL(blob)] as const
        } catch {
          // Keep successful sibling previews available when one snapshot cannot
          // be downloaded; the detail still contains its snapshot status and error.
          return null
        }
      }),
    )
    const urls: Record<number, string> = {}
    for (const entry of entries) {
      if (entry) urls[entry[0]] = entry[1]
    }
    // These URLs were created after the last synchronous cleanup, so a stale
    // batch must release its own allocations before discarding its result.
    if (version !== detailLoadVersion) {
      for (const value of Object.values(urls)) URL.revokeObjectURL(value)
      return
    }
    mediaObjectURLs.value = urls
  }

  /**
   * openDetail presents a selected row and loads its complete record and media.
   * The destination refs identify which dialog receives the row and loading
   * state; fetchDetail supplies the endpoint for that record kind. Each call
   * supersedes pending requests from either dialog through the shared generation.
   *
   * The full row publishes before media downloads begin. Request errors reach
   * the page only while this generation remains current, and the loading flag
   * clears after media settles. Superseded requests leave the newer dialog's
   * loading state and displayed preview map untouched.
   */
  async function openDetail<T extends ContentModerationLog | ContentModerationCyberWarning>(
    row: T,
    target: Ref<T | null>,
    loading: Ref<boolean>,
    fetchDetail: (id: number) => Promise<T>,
  ) {
    // Claim the shared generation before clearing previews so every pending
    // continuation can recognize that a different record now owns the dialog.
    const version = ++detailLoadVersion
    revokeDetailMediaURLs()
    target.value = row
    loading.value = true
    try {
      const detail = await fetchDetail(row.id)
      // A later open or close owns the UI now; ignore this response before
      // starting media requests that would belong to an obsolete selection.
      if (version !== detailLoadVersion) return
      target.value = detail
      await loadDetailMedia(detail.media || [], version)
    } catch (error: unknown) {
      // Superseded failures belong to an abandoned selection and must not show
      // an error toast over whichever record the administrator is reviewing now.
      if (version === detailLoadVersion) services.onError(error)
    } finally {
      if (version === detailLoadVersion) loading.value = false
    }
  }

  /**
   * openInputDetail loads the selected moderation record into its detail dialog.
   * It returns after the record and ready media finish loading, including handled
   * authenticated request failures.
   */
  function openInputDetail(row: ContentModerationLog) {
    return openDetail(row, inputDetailRow, inputDetailLoading, services.getLog)
  }

  /**
   * openCyberDetail loads the selected cyber warning into its detail dialog.
   * It returns after the record and ready media finish loading, including handled
   * authenticated request failures.
   */
  function openCyberDetail(row: ContentModerationCyberWarning) {
    return openDetail(row, cyberDetailRow, cyberDetailLoading, services.getCyberWarning)
  }

  /**
   * closeInputDetail clears the moderation dialog and invalidates outstanding
   * requests. Shared media URLs are released before the selected row disappears,
   * including previews published by either detail endpoint.
   */
  function closeInputDetail() {
    dispose()
    inputDetailLoading.value = false
    inputDetailRow.value = null
  }

  /**
   * closeCyberDetail clears the cyber dialog and invalidates outstanding
   * requests. Shared media URLs are released before the selected row disappears,
   * including previews published by either detail endpoint.
   */
  function closeCyberDetail() {
    dispose()
    cyberDetailLoading.value = false
    cyberDetailRow.value = null
  }

  return {
    inputDetailRow,
    cyberDetailRow,
    inputDetailLoading,
    cyberDetailLoading,
    mediaObjectURLs,
    openInputDetail,
    closeInputDetail,
    openCyberDetail,
    closeCyberDetail,
    dispose,
  }
}
