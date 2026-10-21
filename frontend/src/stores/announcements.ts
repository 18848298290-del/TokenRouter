import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { announcementsAPI } from '@/api'
import type { UserAnnouncement } from '@/api/announcements.types'

// announcements.ts keeps the visible announcement list and sequences unread popup
// announcements for the application shell. Fetch admission records its timestamp before
// requesting data, so ordinary concurrent calls are throttled. A forced fetch bypasses
// that timestamp, and a failed fetch resets it to allow a retry.
//
// Popup candidates come from the first twenty server-ordered records. The queue avoids
// duplicate identifiers, while a session-local set records announcements when displayed.
// Dismissal clears the active popup before marking it read; the component's close
// callback normally continues playback after its exit lifecycle. Fetch admission can
// also start playback whenever currentPopup is empty, including during exit. Reset
// clears local state but does not cancel an already running request.

// Announcement admission uses a twenty-minute ordinary-fetch window and considers at
// most twenty server-ordered records when building popup candidates.
const THROTTLE_MS = 20 * 60 * 1000 // 20 minutes
const MAX_POPUP_CANDIDATES = 20

// useAnnouncementStore exposes the visible announcement list and popup playback
// controls. The application shell owns refresh triggers and session reset, while the
// popup component reports completed closure for normal dismissal continuation. Fetch
// admission can also start playback while the current popup reference is empty.
export const useAnnouncementStore = defineStore('announcements', () => {
  // Announcement refs retain the visible list, request state, admission timestamp,
  // queued popups, and active popup for shared application consumers.
  const announcements = ref<UserAnnouncement[]>([])
  const loading = ref(false)
  const lastFetchTime = ref(0)
  const popupQueue = ref<UserAnnouncement[]>([])
  const currentPopup = ref<UserAnnouncement | null>(null)

  // shownPopupIds belongs to this store session and records identifiers when displayed;
  // reset replaces the set when authentication ends.
  let shownPopupIds = new Set<number>()

  // unreadCount derives the unread badge from every visible announcement, independently
  // from the smaller set considered for popup display.
  const unreadCount = computed(() => announcements.value.filter((a) => !a.read_at).length)

  // fetchAnnouncements refreshes the visible list and popup records unless the twenty-minute admission window is active. Force bypasses throttling and is forwarded to the
  // API cache controls. Admission records its timestamp before awaiting the request so
  // ordinary concurrent calls return early. Success updates existing queued records by
  // identifier before admitting candidates. Failure resets the timestamp for retry and
  // is logged; overlapping forced calls are not ordered by generation.
  async function fetchAnnouncements(force = false) {
    const now = Date.now()
    if (!force && lastFetchTime.value > 0 && now - lastFetchTime.value < THROTTLE_MS) {
      return
    }
    // Record admission before awaiting the API so another ordinary call during the
    // request observes the throttle window and returns early.
    lastFetchTime.value = now

    try {
      loading.value = true
      const all = await announcementsAPI.list(false, force)
      announcements.value = all
      if (currentPopup.value)
        currentPopup.value = all.find((item) => item.id === currentPopup.value?.id) || null
      popupQueue.value = popupQueue.value.flatMap((item) => {
        const updated = all.find((candidate) => candidate.id === item.id)
        return updated ? [updated] : []
      })
      enqueueNewPopups()
    } catch (err: any) {
      // Reset failed admission so a subsequent ordinary call can retry immediately
      // instead of waiting for the twenty-minute throttle window.
      lastFetchTime.value = 0
      console.error('Failed to fetch announcements:', err)
    } finally {
      loading.value = false
    }
  }

  // enqueueNewPopups admits unread popup records from the first twenty server-ordered
  // announcements. The candidate limit applies before filtering, so lower-ranked popup
  // records cannot enter merely because earlier records are silent or already read. The
  // session set excludes previously displayed identifiers, and the existing queue
  // excludes duplicate pending entries. If no popup is active after admission, playback
  // starts immediately with the first queued record.
  function enqueueNewPopups() {
    // Limit the server-ordered candidate window before unread and display-mode
    // filtering, keeping popup eligibility tied to announcement priority rather than
    // filtered-list size.
    const newPopups = announcements.value
      .slice(0, MAX_POPUP_CANDIDATES)
      .filter((a) => a.notify_mode === 'popup' && !a.read_at && !shownPopupIds.has(a.id))
    if (newPopups.length === 0) return

    for (const p of newPopups) {
      if (!popupQueue.value.some((q) => q.id === p.id)) {
        popupQueue.value.push(p)
      }
    }

    if (!currentPopup.value) {
      showNextPopup()
    }
  }

  // showNextPopup selects the next queued record and marks its identifier shown for this
  // store session. An empty queue clears the active popup. Fetch admission invokes it
  // when no popup is active, while the component close callback uses it to continue
  // playback after dismissal.
  function showNextPopup() {
    if (popupQueue.value.length === 0) {
      currentPopup.value = null
      return
    }
    currentPopup.value = popupQueue.value.shift()!
    shownPopupIds.add(currentPopup.value.id)
  }

  // dismissPopup clears the active popup and starts an asynchronous mark-read request.
  // The component normally continues playback through onPopupClosed after its exit
  // lifecycle. A concurrent fetch can admit candidates and start playback while the
  // active reference is empty, before that close callback runs.
  async function dismissPopup() {
    if (!currentPopup.value) return
    const id = currentPopup.value.id
    // Clear the active reference to trigger component exit before read persistence
    // settles. Fetch admission can use this empty reference to start another popup.
    currentPopup.value = null
    markAsRead(id)
  }

  // onPopupClosed advances the queue after the popup component finishes closing. The
  // component also calls it when unmounted during exit, and an empty queue requires no
  // further action.
  function onPopupClosed() {
    if (popupQueue.value.length > 0) {
      showNextPopup()
    }
  }

  // markAsRead persists one announcement's read status and updates a matching visible
  // record after success. Failures are logged and suppressed, leaving the existing read
  // timestamp available to consumers.
  async function markAsRead(id: number) {
    try {
      await announcementsAPI.markRead(id)
      const ann = announcements.value.find((a) => a.id === id)
      if (ann) {
        ann.read_at = new Date().toISOString()
      }
    } catch (err: any) {
      // Keep the existing local read timestamp when persistence fails; popup dismissal
      // can still complete and the next fetch can reconcile server state.
      console.error('Failed to mark announcement as read:', err)
    }
  }

  // markAllAsRead submits read requests for the currently unread visible records
  // together. Local timestamps change after every request succeeds, so an error from one
  // request leaves local read state unchanged even if other requests already succeeded
  // remotely. The failure is logged and propagated for caller handling. Loading is
  // cleared when the batch settles; records are read again from current reactive state
  // when applying successful timestamps.
  async function markAllAsRead() {
    const unread = announcements.value.filter((a) => !a.read_at)
    if (unread.length === 0) return

    try {
      loading.value = true
      // Wait for every server update before applying local timestamps; partial server
      // success can therefore remain unreflected when another request rejects.
      await Promise.all(unread.map((a) => announcementsAPI.markRead(a.id)))
      announcements.value.forEach((a) => {
        if (!a.read_at) {
          a.read_at = new Date().toISOString()
        }
      })
    } catch (err: any) {
      console.error('Failed to mark all as read:', err)
      throw err
    } finally {
      loading.value = false
    }
  }

  // reset clears visible announcements, popup playback, throttling, and session
  // deduplication state. It does not cancel in-flight fetches or mark-read requests,
  // whose completions can still update the store.
  function reset() {
    announcements.value = []
    lastFetchTime.value = 0
    shownPopupIds = new Set()
    popupQueue.value = []
    currentPopup.value = null
    loading.value = false
  }

  return {
    announcements,
    loading,
    currentPopup,
    unreadCount,
    fetchAnnouncements,
    dismissPopup,
    onPopupClosed,
    markAsRead,
    markAllAsRead,
    reset,
  }
})
