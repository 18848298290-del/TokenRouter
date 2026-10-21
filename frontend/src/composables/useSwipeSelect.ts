import { ref, onMounted, onUnmounted, type Ref } from 'vue'
import type { Virtualizer } from '@tanstack/vue-virtual'
import { Z_INDEX } from '@/constants/overlay'

// useSwipeSelect.ts coordinates mouse-drag table selection with scrolling and a viewport
// marquee. A pointer press inside the table layout first enters a movement-threshold
// phase, allowing ordinary clicks to finish before a drag session begins. Interactive
// controls and text targets retain their usual browser behavior.
//
// A drag snapshots initial membership and chooses select or deselect from its starting
// row. Expanding the range applies that mode; shrinking restores each excluded row's
// original membership. DOM and virtualized tables use different index lookup paths but
// share this rule. Mouse movement, wheel frames, and edge scrolling update the range until
// release, window blur, or component unmount cleans up gesture resources.

// SwipeSelectAdapter reads and changes numeric row selection for drag gestures.
export interface SwipeSelectAdapter {
  isSelected: (id: number) => boolean // current row membership lookup
  select: (id: number) => void // individual row selection callback
  deselect: (id: number) => void // individual row deselection callback
  // batchUpdate must apply the supplied draft updater synchronously so the gesture can
  // record its resulting endpoint after selection changes complete.
  batchUpdate?: (updater: (draft: Set<number>) => void) => void // optional synchronous selection draft updater
}

// SwipeSelectVirtualContext connects viewport lookup to a virtual table's sorted data.
export interface SwipeSelectVirtualContext {
  // getVirtualizer may return null while the table is unavailable; coordinate lookup then
  // declines to select a row.
  getVirtualizer: () => Virtualizer<HTMLElement, Element> | null // current virtual table instance

  // getSortedData returns the complete current sorted list, including unrendered rows;
  // stable ordering during a gesture keeps range indices meaningful.
  getSortedData: () => any[] // complete sorted row collection

  // getRowId supplies stable selection identities for sorted data and must agree with the
  // adapter's IDs throughout the gesture.
  getRowId: (row: any, index: number) => number // stable numeric row identity
}

// findRowIndexByDomPosition returns the sorted-data index nearest a viewport Y coordinate.
// It searches rendered rows in vertical order and returns each row's data-index attribute,
// which may differ from its array position.
//
// Coordinates outside the rendered range select the nearest endpoint. Coordinates in a gap
// select the closer rectangle, with equal distances choosing the lower row. An empty
// rendered list returns minus one. Callers provide vertically ordered rows with numeric
// data-index values and usable bounding rectangles.
export function findRowIndexByDomPosition(scrollEl: Element, clientY: number): number {
  const domRows = Array.from(scrollEl.querySelectorAll('tbody tr[data-index]')) as HTMLElement[]
  const len = domRows.length
  if (len === 0) return -1
  // idxOf reads a rendered row's numeric sorted-data index attribute.
  const idxOf = (el: HTMLElement) => Number(el.getAttribute('data-index'))

  if (clientY < domRows[0].getBoundingClientRect().top) return idxOf(domRows[0])
  if (clientY > domRows[len - 1].getBoundingClientRect().bottom) return idxOf(domRows[len - 1])

  let lo = 0,
    hi = len - 1
  while (lo <= hi) {
    const mid = (lo + hi) >>> 1
    const rect = domRows[mid].getBoundingClientRect()
    if (clientY < rect.top) hi = mid - 1
    else if (clientY > rect.bottom) lo = mid + 1
    else return idxOf(domRows[mid])
  }

  if (hi < 0) return idxOf(domRows[0])
  if (lo >= len) return idxOf(domRows[len - 1])
  const rHi = domRows[hi].getBoundingClientRect()
  const rLo = domRows[lo].getBoundingClientRect()
  return clientY - rHi.bottom < rLo.top - clientY ? idxOf(domRows[hi]) : idxOf(domRows[lo])
}

// useSwipeSelect installs mouse-drag selection for the supplied table container and
// adapter. Invoke it during component setup and provide stable numeric row identities. DOM
// rows use data-row-id; a virtual context supplies sorted data and coordinate lookup when
// virtualization is available.
//
// The gesture snapshots selection once at drag start, applies one selection mode across
// its range, and restores snapshot membership as the range shrinks. Its frame loops,
// document listeners, and marquee belong to that gesture and are released on mouseup,
// blur, or unmount.
export function useSwipeSelect(
  containerRef: Ref<HTMLElement | null>,
  adapter: SwipeSelectAdapter,
  virtualContext?: SwipeSelectVirtualContext,
) {
  const isDragging = ref(false)

  // dragMode, startRowIndex, lastEndIndex, and initialSelectedSnapshot describe one
  // gesture; viewport coordinates and cached DOM resources are discarded when it ends.
  let dragMode: 'select' | 'deselect' = 'select'
  let startRowIndex = -1
  let lastEndIndex = -1
  let startY = 0
  let lastMouseY = 0
  let pendingStartY = 0
  let initialSelectedSnapshot = new Map<number, boolean>()
  let cachedRows: HTMLElement[] = []
  let marqueeEl: HTMLDivElement | null = null
  let cachedScrollParent: HTMLElement | null = null

  // Vertical movement activates selection; viewport edge distance and per-frame pixel
  // steps govern the separate automatic scrolling loop.
  const DRAG_THRESHOLD = 5
  const SCROLL_ZONE = 60
  const SCROLL_SPEED = 8

  // getActivationRoot returns the nearest table-page layout or the table container itself.
  // A missing container returns null, so pointer presses cannot start selection before the
  // table is available.
  function getActivationRoot(): HTMLElement | null {
    const container = containerRef.value
    if (!container) return null
    return (container.closest('.table-page-layout') as HTMLElement | null) || container
  }

  // getDataRows returns rendered rows carrying selection identifiers inside the container.
  // Missing containers yield an empty array, allowing drag activation to stop while the
  // table is absent.
  function getDataRows(): HTMLElement[] {
    const container = containerRef.value
    if (!container) return []
    return Array.from(container.querySelectorAll('tbody tr[data-row-id]'))
  }

  // getRowId parses a row's data-row-id as a finite number. Missing attributes and
  // nonfinite conversions return null, so invalid DOM rows do not participate in selection
  // updates.
  function getRowId(el: HTMLElement): number | null {
    const raw = el.getAttribute('data-row-id')
    if (raw === null) return null
    const id = Number(raw)
    return Number.isFinite(id) ? id : null
  }

  // findRowIndexAtY finds the cached DOM row nearest a viewport coordinate. Vertically
  // ordered rectangles permit binary search, while coordinates above or below the list
  // clamp to its first or last row.
  //
  // A coordinate between rectangles chooses whichever edge is closer, with a tie going to
  // the lower row. This helper returns the cache position used by applyRange, unlike
  // findRowIndexByDomPosition, which returns the persisted data-index attribute. An empty
  // cache returns minus one.
  function findRowIndexAtY(clientY: number): number {
    const len = cachedRows.length
    if (len === 0) return -1

    const firstRect = cachedRows[0].getBoundingClientRect()
    if (clientY < firstRect.top) return 0
    const lastRect = cachedRows[len - 1].getBoundingClientRect()
    if (clientY > lastRect.bottom) return len - 1

    let lo = 0,
      hi = len - 1
    while (lo <= hi) {
      const mid = (lo + hi) >>> 1
      const rect = cachedRows[mid].getBoundingClientRect()
      if (clientY < rect.top) hi = mid - 1
      else if (clientY > rect.bottom) lo = mid + 1
      else return mid
    }

    if (hi < 0) return 0
    if (lo >= len) return len - 1
    const rHi = cachedRows[hi].getBoundingClientRect()
    const rLo = cachedRows[lo].getBoundingClientRect()
    return clientY - rHi.bottom < rLo.top - clientY ? hi : lo
  }

  // findRowIndexAtYVirtual converts a viewport coordinate into an index in sorted virtual
  // data. It subtracts the scroll container and header offsets, then adds scrolling before
  // checking rendered virtual item bounds. Missing virtualizers or scroll elements return
  // minus one.
  //
  // When the virtual window is empty, rendered DOM rows supply their absolute indices.
  // Other out-of-window coordinates use estimated row height and clamp to available data.
  // Empty data returns minus one; callers supply a usable positive virtualizer size
  // estimate.
  function findRowIndexAtYVirtual(clientY: number): number {
    const virt = virtualContext!.getVirtualizer()
    if (!virt) return -1
    const scrollEl = virt.scrollElement
    if (!scrollEl) return -1

    const scrollRect = scrollEl.getBoundingClientRect()
    const thead = scrollEl.querySelector('thead')
    const theadHeight = thead ? thead.getBoundingClientRect().height : 0
    // Convert pointer coordinates into virtual content space before matching item offsets,
    // accounting for both the header and current scrolling.
    const contentY = clientY - scrollRect.top - theadHeight + scrollEl.scrollTop

    const items = virt.getVirtualItems()
    for (const item of items) {
      if (contentY >= item.start && contentY < item.end) return item.index
    }

    // An empty virtual window can mean full DOM rendering, so use actual row rectangles
    // before estimating positions from configured heights.
    if (items.length === 0) {
      const domIdx = findRowIndexByDomPosition(scrollEl, clientY)
      if (domIdx >= 0) return domIdx
    }

    const totalCount = virtualContext!.getSortedData().length
    if (totalCount === 0) return -1
    // Outside rendered bounds, estimate and clamp to data length.
    const est = virt.options.estimateSize(0)
    const guess = Math.floor(contentY / est)
    return Math.max(0, Math.min(totalCount - 1, guess))
  }

  // onSelectStart prevents browser text selection while drag activation is pending.
  function onSelectStart(e: Event) {
    e.preventDefault()
  }

  // createMarquee replaces any previous drag overlay and appends a styled viewport
  // rectangle to the document body. It reads the current theme once per gesture and leaves
  // pointer events available to underlying content.
  function createMarquee() {
    // Replace any retained overlay before allocating the new one so an interrupted earlier
    // gesture cannot leave multiple rectangles attached.
    removeMarquee()
    marqueeEl = document.createElement('div')
    const isDark = document.documentElement.classList.contains('dark') // check-ui-allow: drag overlay reads theme once per gesture
    Object.assign(marqueeEl.style, {
      position: 'fixed',
      background: isDark ? 'rgba(96, 165, 250, 0.15)' : 'rgba(59, 130, 246, 0.12)',
      border: isDark
        ? '1.5px solid rgba(96, 165, 250, 0.5)'
        : '1.5px solid rgba(59, 130, 246, 0.4)',
      borderRadius: '4px',
      pointerEvents: 'none',
      zIndex: String(Z_INDEX.TOAST),
      transition: 'none',
    })
    document.body.appendChild(marqueeEl)
  }

  // updateMarquee aligns the viewport overlay with the table width and current drag
  // extent. Missing overlays or containers are ignored; its vertical bounds span the
  // starting and current pointer coordinates.
  function updateMarquee(currentY: number) {
    if (!marqueeEl || !containerRef.value) return
    const containerRect = containerRef.value.getBoundingClientRect()
    const top = Math.min(startY, currentY)
    const bottom = Math.max(startY, currentY)
    marqueeEl.style.left = containerRect.left + 'px'
    marqueeEl.style.width = containerRect.width + 'px'
    marqueeEl.style.top = top + 'px'
    marqueeEl.style.height = bottom - top + 'px'
  }

  // removeMarquee removes the current overlay from the document and clears its handle.
  // Repeated calls are safe, supporting cleanup both before a new gesture and after
  // interrupted gestures.
  function removeMarquee() {
    if (marqueeEl) {
      marqueeEl.remove()
      marqueeEl = null
    }
  }

  // applyRange applies the current drag mode to cached DOM rows in the new inclusive
  // range. It examines the union of previous and current ranges so rows leaving a
  // shrinking range recover their initial membership. Invalid numeric row IDs are skipped.
  //
  // An available batchUpdate callback publishes those changes together; otherwise select
  // and deselect apply them individually. Rows outside both ranges remain untouched. The
  // final endpoint is recorded only after updates, providing the previous range for the
  // next pointer movement.
  function applyRange(endIndex: number) {
    if (startRowIndex < 0 || endIndex < 0) return
    const rangeMin = Math.min(startRowIndex, endIndex)
    const rangeMax = Math.max(startRowIndex, endIndex)
    const prevMin = lastEndIndex >= 0 ? Math.min(startRowIndex, lastEndIndex) : rangeMin
    const prevMax = lastEndIndex >= 0 ? Math.max(startRowIndex, lastEndIndex) : rangeMax
    // Cover both extents so shrinking a drag restores rows that were affected previously
    // but now lie outside the active range.
    const lo = Math.min(rangeMin, prevMin)
    const hi = Math.max(rangeMax, prevMax)

    if (adapter.batchUpdate) {
      adapter.batchUpdate((draft) => {
        for (let i = lo; i <= hi && i < cachedRows.length; i++) {
          const id = getRowId(cachedRows[i])
          if (id === null) continue
          const shouldBeSelected =
            i >= rangeMin && i <= rangeMax
              ? dragMode === 'select'
              : (initialSelectedSnapshot.get(id) ?? false)
          if (shouldBeSelected) draft.add(id)
          else draft.delete(id)
        }
      })
    } else {
      for (let i = lo; i <= hi && i < cachedRows.length; i++) {
        const id = getRowId(cachedRows[i])
        if (id === null) continue
        if (i >= rangeMin && i <= rangeMax) {
          if (dragMode === 'select') adapter.select(id)
          else adapter.deselect(id)
        } else {
          const wasSelected = initialSelectedSnapshot.get(id) ?? false
          if (wasSelected) adapter.select(id)
          else adapter.deselect(id)
        }
      }
    }
    lastEndIndex = endIndex
  }

  // applyRangeVirtual applies the drag mode to sorted data rows in the new inclusive
  // range. It examines both previous and current extents, restoring snapshot membership
  // for rows that leave the active selection area. Stable row IDs connect the starting
  // snapshot to subsequent sorted-data reads.
  //
  // The optional batch updater receives one draft mutation covering the affected range;
  // other adapters receive individual selection calls. Rows beyond the current data length
  // are skipped, and the resulting endpoint becomes the comparison range for the next
  // update.
  function applyRangeVirtual(endIndex: number) {
    if (startRowIndex < 0 || endIndex < 0) return
    const rangeMin = Math.min(startRowIndex, endIndex)
    const rangeMax = Math.max(startRowIndex, endIndex)
    const prevMin = lastEndIndex >= 0 ? Math.min(startRowIndex, lastEndIndex) : rangeMin
    const prevMax = lastEndIndex >= 0 ? Math.max(startRowIndex, lastEndIndex) : rangeMax
    const lo = Math.min(rangeMin, prevMin)
    const hi = Math.max(rangeMax, prevMax)
    // Read the current sorted dataset for virtual range updates; snapshot membership is
    // keyed by stable IDs so rendered-window changes do not lose selection identity.
    const data = virtualContext!.getSortedData()

    if (adapter.batchUpdate) {
      adapter.batchUpdate((draft) => {
        for (let i = lo; i <= hi && i < data.length; i++) {
          const id = virtualContext!.getRowId(data[i], i)
          const shouldBeSelected =
            i >= rangeMin && i <= rangeMax
              ? dragMode === 'select'
              : (initialSelectedSnapshot.get(id) ?? false)
          if (shouldBeSelected) draft.add(id)
          else draft.delete(id)
        }
      })
    } else {
      for (let i = lo; i <= hi && i < data.length; i++) {
        const id = virtualContext!.getRowId(data[i], i)
        if (i >= rangeMin && i <= rangeMax) {
          if (dragMode === 'select') adapter.select(id)
          else adapter.deselect(id)
        } else {
          const wasSelected = initialSelectedSnapshot.get(id) ?? false
          if (wasSelected) adapter.select(id)
          else adapter.deselect(id)
        }
      }
    }
    lastEndIndex = endIndex
  }

  // getScrollParent finds the nearest ancestor whose overflow permits scrolling. If no
  // such ancestor appears before the root element, it returns the document root for
  // viewport edge scrolling.
  function getScrollParent(el: HTMLElement): HTMLElement {
    let parent = el.parentElement
    while (parent && parent !== document.documentElement) {
      const { overflow, overflowY } = getComputedStyle(parent)
      if (/(auto|scroll)/.test(overflow + overflowY)) return parent
      parent = parent.parentElement
    }
    return document.documentElement
  }

  // isOnScrollbar checks whether a pointer lies on a target or ancestor scrollbar,
  // including document scrollbars. It compares pointer coordinates against client
  // dimensions so drag selection can leave scrollbar interactions to the browser.
  function isOnScrollbar(e: MouseEvent): boolean {
    let el = e.target as HTMLElement | null
    while (el && el !== document.documentElement) {
      const hasVScroll = el.scrollHeight > el.clientHeight
      const hasHScroll = el.scrollWidth > el.clientWidth
      if (hasVScroll || hasHScroll) {
        const rect = el.getBoundingClientRect()

        if (hasVScroll && e.clientX > rect.left + el.clientWidth) return true
        if (hasHScroll && e.clientY > rect.top + el.clientHeight) return true
      }
      el = el.parentElement
    }

    const docEl = document.documentElement
    if (e.clientX >= docEl.clientWidth || e.clientY >= docEl.clientHeight) return true
    return false
  }

  // shouldPreferNativeTextSelection keeps inner table-cell content available for ordinary
  // browser selection. Cell padding can start a drag, while descendants require an
  // explicit data-swipe-select-handle to opt into row selection.
  function shouldPreferNativeTextSelection(target: HTMLElement): boolean {
    const row = target.closest('tbody tr[data-row-id]')
    if (!row) return false

    const cell = target.closest('td, th')
    if (!cell) return false

    return target !== cell && !target.closest('[data-swipe-select-handle]')
  }

  // hasDirectTextContent detects nonblank text nodes directly inside an element.
  function hasDirectTextContent(target: HTMLElement): boolean {
    return Array.from(target.childNodes).some(
      (node) => node.nodeType === Node.TEXT_NODE && (node.textContent?.trim().length ?? 0) > 0,
    )
  }

  // shouldPreferNativeSelectionOutsideRows detects direct text content inside the
  // activation layout but outside selectable rows. It protects layout text from starting a
  // selection gesture and ignores targets outside the layout.
  function shouldPreferNativeSelectionOutsideRows(target: HTMLElement): boolean {
    const activationRoot = getActivationRoot()
    if (!activationRoot) return false
    if (!activationRoot.contains(target)) return false
    if (target.closest('tbody tr[data-row-id]')) return false

    return hasDirectTextContent(target)
  }

  // onMouseDown prepares selection for an eligible left-button press inside the table
  // layout. Scrollbars, interactive controls, and ordinary text targets keep browser
  // handling. Virtual tables require sorted data, while DOM tables cache selectable rows
  // before activation.
  //
  // An eligible press records its Y coordinate and installs threshold listeners plus a
  // text-selection blocker. Selection membership remains unchanged until sufficient
  // vertical movement begins a drag. Mouse release or blur during this pending phase
  // removes those temporary listeners.
  function onMouseDown(e: MouseEvent) {
    if (e.button !== 0) return
    if (!containerRef.value) return

    const target = e.target as HTMLElement
    const activationRoot = getActivationRoot()
    if (!activationRoot || !activationRoot.contains(target)) return

    if (isOnScrollbar(e)) return

    if (
      target.closest(
        'button, a, input, select, textarea, [role="button"], [role="menuitem"], [role="combobox"], [role="dialog"]',
      )
    )
      return
    if (shouldPreferNativeTextSelection(target)) return
    if (shouldPreferNativeSelectionOutsideRows(target)) return

    if (virtualContext) {
      const data = virtualContext.getSortedData()
      if (data.length === 0) return
    } else {
      cachedRows = getDataRows()
      if (cachedRows.length === 0) return
    }

    pendingStartY = e.clientY

    // Block text selection during the threshold interval because the browser can otherwise
    // select text before the row gesture becomes active.
    document.addEventListener('selectstart', onSelectStart)
    document.addEventListener('mousemove', onThresholdMove)
    document.addEventListener('mouseup', onThresholdUp)
  }

  // onThresholdMove promotes a pending pointer press into an active drag after sufficient
  // vertical movement. It first removes threshold listeners, then starts either DOM or
  // virtual selection from the original press coordinate.
  //
  // The same event that crosses the threshold updates the marquee and endpoint, so its
  // displacement is not lost. Edge scrolling starts with that event, and active mouse and
  // wheel listeners replace threshold listeners for the remainder of the gesture.
  function onThresholdMove(e: MouseEvent) {
    if (Math.abs(e.clientY - pendingStartY) < DRAG_THRESHOLD) return

    // Remove threshold listeners before activation so later movement events use the active
    // gesture handlers instead of starting another drag.
    document.removeEventListener('mousemove', onThresholdMove)
    document.removeEventListener('mouseup', onThresholdUp)

    if (virtualContext) {
      beginDragVirtual(pendingStartY)
    } else {
      beginDrag(pendingStartY)
    }

    lastMouseY = e.clientY
    updateMarquee(e.clientY)
    const findIdx = virtualContext ? findRowIndexAtYVirtual : findRowIndexAtY
    const apply = virtualContext ? applyRangeVirtual : applyRange
    const rowIdx = findIdx(e.clientY)
    if (rowIdx >= 0) apply(rowIdx)
    autoScroll(e)

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
    document.addEventListener('wheel', onWheel, { passive: true })
  }

  // onThresholdUp releases threshold-phase listeners after a click that did not become a
  // drag. It also removes the text-selection blocker and clears cached DOM rows before
  // another press.
  function onThresholdUp() {
    document.removeEventListener('mousemove', onThresholdMove)
    document.removeEventListener('mouseup', onThresholdUp)

    document.removeEventListener('selectstart', onSelectStart)
    cachedRows = []
  }

  // beginDrag starts a DOM-row gesture at the supplied viewport coordinate. The starting
  // row's membership chooses deselect for an already selected row and select otherwise. It
  // snapshots all cached row memberships before applying any range changes.
  //
  // The gesture records its viewport origin, resets its prior endpoint, and finds the
  // scrolling ancestor. It creates the marquee and applies the initial row before clearing
  // browser text selection, while the threshold-phase blocker remains installed for the
  // active drag.
  function beginDrag(clientY: number) {
    startRowIndex = findRowIndexAtY(clientY)
    const startRowId = startRowIndex >= 0 ? getRowId(cachedRows[startRowIndex]) : null
    dragMode = startRowId !== null && adapter.isSelected(startRowId) ? 'deselect' : 'select'

    initialSelectedSnapshot = new Map()
    for (const row of cachedRows) {
      const id = getRowId(row)
      if (id !== null) initialSelectedSnapshot.set(id, adapter.isSelected(id))
    }

    isDragging.value = true
    startY = clientY
    lastMouseY = clientY
    lastEndIndex = -1
    cachedScrollParent =
      cachedRows.length > 0
        ? getScrollParent(cachedRows[0])
        : containerRef.value
          ? getScrollParent(containerRef.value)
          : null

    createMarquee()
    updateMarquee(clientY)
    applyRange(startRowIndex)

    // Discard text selection created before the blocker took effect so the active marquee
    // gesture has a single visible selection mode.
    window.getSelection()?.removeAllRanges()
  }

  // beginDragVirtual starts a gesture using the virtual table's sorted data and row
  // identities. The starting row determines whether the gesture selects or deselects, and
  // a full data snapshot records membership before the initial range is applied.
  //
  // The virtualizer's scroll element takes priority over a container ancestor. The marquee
  // still uses viewport coordinates, while range updates address sorted-data indices.
  // Clearing browser text selection completes activation after the threshold listener has
  // already blocked new native selection.
  function beginDragVirtual(clientY: number) {
    startRowIndex = findRowIndexAtYVirtual(clientY)
    const data = virtualContext!.getSortedData()
    const startRowId =
      startRowIndex >= 0 && startRowIndex < data.length
        ? virtualContext!.getRowId(data[startRowIndex], startRowIndex)
        : null
    dragMode = startRowId !== null && adapter.isSelected(startRowId) ? 'deselect' : 'select'

    initialSelectedSnapshot = new Map()
    for (let i = 0; i < data.length; i++) {
      const id = virtualContext!.getRowId(data[i], i)
      initialSelectedSnapshot.set(id, adapter.isSelected(id))
    }

    isDragging.value = true
    startY = clientY
    lastMouseY = clientY
    lastEndIndex = -1

    const virt = virtualContext!.getVirtualizer()
    cachedScrollParent =
      virt?.scrollElement ?? (containerRef.value ? getScrollParent(containerRef.value) : null)

    createMarquee()
    updateMarquee(clientY)
    applyRangeVirtual(startRowIndex)
    window.getSelection()?.removeAllRanges()
  }

  // moveRAF owns the coalesced pointer-position update; replacing it keeps rapid mouse
  // events from queuing redundant range calculations.
  let moveRAF = 0

  // onMouseMove schedules one range update for the latest pointer coordinate during an
  // active drag. It cancels the previous movement frame, then updates marquee and row
  // endpoint on the replacement frame.
  //
  // DOM and virtual coordinate paths are selected once per event, and unchanged endpoints
  // skip membership work. Edge scrolling is updated immediately from the event rather than
  // waiting for the movement frame, allowing a stationary edge pointer to continue
  // selecting newly exposed rows.
  function onMouseMove(e: MouseEvent) {
    if (!isDragging.value) return
    lastMouseY = e.clientY
    const findIdx = virtualContext ? findRowIndexAtYVirtual : findRowIndexAtY
    const apply = virtualContext ? applyRangeVirtual : applyRange
    // Replace the pending movement callback so rapid pointer events measure and apply the
    // most recent coordinate on the next frame.
    cancelAnimationFrame(moveRAF)
    moveRAF = requestAnimationFrame(() => {
      updateMarquee(lastMouseY)
      const rowIdx = findIdx(lastMouseY)
      if (rowIdx >= 0 && rowIdx !== lastEndIndex) apply(rowIdx)
    })
    autoScroll(e)
  }

  // onWheel schedules a row lookup after the browser applies wheel scrolling during an
  // active drag. Rows can move under a stationary pointer, so the callback uses the
  // latest pointer coordinate rather than a coordinate captured from the wheel event.
  //
  // The queued frame checks isDragging again before changing membership because mouseup
  // or blur may have finished the gesture in the meantime. DOM and virtual lookup paths
  // both apply the resulting endpoint through their corresponding range updater.
  function onWheel() {
    if (!isDragging.value) return
    const findIdx = virtualContext ? findRowIndexAtYVirtual : findRowIndexAtY
    const apply = virtualContext ? applyRangeVirtual : applyRange

    // Wheel scrolling changes row rectangles after the event, so defer index lookup until
    // the browser has applied the movement.
    requestAnimationFrame(() => {
      if (!isDragging.value) return
      const rowIdx = findIdx(lastMouseY)
      if (rowIdx >= 0) apply(rowIdx)
    })
  }

  // cleanupDrag releases the active gesture's listeners, overlay, cached rows, and frame
  // loops. It marks dragging false before cancelling work so already queued wheel
  // callbacks can recognize a finished gesture.
  //
  // Range endpoints and snapshot membership are cleared, then movement and automatic
  // scrolling are cancelled before the marquee disappears. This helper owns active-drag
  // resources; callers cleaning up a pending threshold phase also remove its separate
  // mousemove and mouseup listeners. Repeated cleanup leaves no active gesture state.
  function cleanupDrag() {
    isDragging.value = false
    startRowIndex = -1
    lastEndIndex = -1
    cachedRows = []
    initialSelectedSnapshot.clear()
    cachedScrollParent = null
    cancelAnimationFrame(moveRAF)
    stopAutoScroll()
    removeMarquee()
    document.removeEventListener('selectstart', onSelectStart)
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
    document.removeEventListener('wheel', onWheel)
  }

  // onMouseUp finishes the active drag through its shared cleanup operation.
  function onMouseUp() {
    cleanupDrag()
  }

  // onWindowBlur cleans up an active drag and any pending threshold listeners. Losing
  // focus can prevent the expected mouseup event, so this path also releases the text-selection blocker.
  function onWindowBlur() {
    if (isDragging.value) cleanupDrag()

    document.removeEventListener('mousemove', onThresholdMove)
    document.removeEventListener('mouseup', onThresholdUp)
    document.removeEventListener('selectstart', onSelectStart)
  }

  // scrollRAF owns the repeating edge-scroll callback, which pointer movement replaces and
  // gesture cleanup cancels independently of movement rendering.
  let scrollRAF = 0

  // autoScroll replaces the edge-scrolling frame loop according to the pointer's distance
  // from the scroll viewport. It cancels the old loop before checking the current scroll
  // owner, and uses document height or container bounds to choose direction.
  //
  // Each frame moves by the fixed scroll step and recomputes the selected endpoint only
  // when scrolling actually changed. The loop continues until a pointer event replaces it
  // or gesture cleanup cancels it, including while the scroll position has reached an
  // edge.
  function autoScroll(e: MouseEvent) {
    // Cancel the preceding edge-scroll loop before choosing a direction so each gesture
    // owns only one active scrolling frame sequence.
    cancelAnimationFrame(scrollRAF)
    const scrollEl = cachedScrollParent
    if (!scrollEl) return

    let dy = 0
    if (scrollEl === document.documentElement) {
      if (e.clientY < SCROLL_ZONE) dy = -SCROLL_SPEED
      else if (e.clientY > window.innerHeight - SCROLL_ZONE) dy = SCROLL_SPEED
    } else {
      const rect = scrollEl.getBoundingClientRect()
      if (e.clientY < rect.top + SCROLL_ZONE) dy = -SCROLL_SPEED
      else if (e.clientY > rect.bottom - SCROLL_ZONE) dy = SCROLL_SPEED
    }

    if (dy !== 0) {
      const findIdx = virtualContext ? findRowIndexAtYVirtual : findRowIndexAtY
      const apply = virtualContext ? applyRangeVirtual : applyRange
      // step advances the captured scroll owner and updates the range after actual
      // movement. It uses the latest pointer coordinate to identify newly exposed rows and
      // avoids reapplying unchanged endpoints.
      //
      // The callback schedules another frame regardless of whether scrolling was clamped,
      // keeping the edge gesture ready for movement. Its handle belongs to the current
      // autoScroll sequence and is cancelled when direction is recomputed or the drag
      // ends; it does not independently inspect isDragging.
      const step = () => {
        const prevScrollTop = scrollEl.scrollTop
        scrollEl.scrollTop += dy

        if (scrollEl.scrollTop !== prevScrollTop) {
          const rowIdx = findIdx(lastMouseY)
          if (rowIdx >= 0 && rowIdx !== lastEndIndex) apply(rowIdx)
        }
        scrollRAF = requestAnimationFrame(step)
      }
      scrollRAF = requestAnimationFrame(step)
    }
  }

  // stopAutoScroll cancels the pending automatic-scroll frame for the active gesture. Its
  // stored numeric handle may refer to completed work, which the browser cancellation API
  // tolerates during repeated cleanup.
  function stopAutoScroll() {
    cancelAnimationFrame(scrollRAF)
  }

  // Register activation and blur listeners once for this component; gesture-specific
  // listeners are added only after an eligible pointer press.
  onMounted(() => {
    document.addEventListener('mousedown', onMouseDown)
    window.addEventListener('blur', onWindowBlur)
  })

  // Remove permanent activation listeners and both gesture phases during teardown,
  // including a press that never crossed the drag threshold.
  onUnmounted(() => {
    document.removeEventListener('mousedown', onMouseDown)
    window.removeEventListener('blur', onWindowBlur)

    document.removeEventListener('mousemove', onThresholdMove)
    document.removeEventListener('mouseup', onThresholdUp)
    document.removeEventListener('selectstart', onSelectStart)
    cleanupDrag()
  })

  return { isDragging }
}
