import { computed, ref, watch, type ComputedRef } from 'vue'
import type { Column } from '@/components/common/table/types'

// useTableColumnOrder.ts reconciles browser column preferences with the columns
// currently supplied to DataTable. Selection and action columns retain their
// declared positions while other columns follow their stored key order. A
// temporarily hidden column keeps its preference entry, so showing it again
// restores its place among previously known columns. New keys append after the
// known data columns.
//
// The storage key enables ordering and separates independent tables. Changing it
// reloads that table's preference before merging current keys. Reads discard
// malformed entries and structural keys. Writes update the current page first,
// allowing dragging and keyboard movement to remain available when browser
// storage rejects persistence.

// isStructuralColumn identifies selection and action columns whose declared positions remain fixed.
const isStructuralColumn = (key: string) => key === 'select' || key === 'actions'

// useTableColumnOrder exposes reactive columns and controlled movement of their
// stored key order. Call it during component setup with a reactive storage key;
// an absent key disables reordering and returns the supplied order. Hidden keys
// remain in the preference while new data keys append, so temporary column
// visibility changes do not discard user choices. Movement updates reactive
// state before attempting browser persistence, and failed storage writes leave
// the current page usable.
export function useTableColumnOrder(
  columns: () => Column[],
  storageKey: ComputedRef<string | undefined>,
) {
  const columnOrder = ref<string[]>([])

  // readOrder loads unique movable keys from the requested browser preference.
  // Missing keys, invalid JSON, non-array values, and storage errors produce an
  // empty order for the current column merge.
  const readOrder = (key: string | undefined): string[] => {
    if (!key) return []
    try {
      const stored: unknown = JSON.parse(localStorage.getItem(key) || '[]')
      if (!Array.isArray(stored)) return []
      return [
        ...new Set(
          stored.filter(
            (value): value is string => typeof value === 'string' && !isStructuralColumn(value),
          ),
        ),
      ]
    } catch {
      // Treat inaccessible or malformed preferences as an empty order so the
      // current columns remain available for rendering.
      return []
    }
  }

  watch(
    [storageKey, () => columns().map((column) => column.key)],
    ([key, keys], previous) => {
      if (key !== previous?.[0]) columnOrder.value = readOrder(key)
      // Retain absent column keys so later visibility changes restore their
      // positions, then append newly encountered movable keys.
      const known = new Set(columnOrder.value)
      columnOrder.value = [
        ...columnOrder.value,
        ...keys.filter((key) => !isStructuralColumn(key) && !known.has(key)),
      ]
    },
    { immediate: true },
  )

  const orderedColumns = computed(() => {
    if (!storageKey.value) return columns()
    const positions = new Map(columnOrder.value.map((key, index) => [key, index]))
    const movable = columns()
      .filter((column) => !isStructuralColumn(column.key))
      .sort((a, b) => (positions.get(a.key) ?? Infinity) - (positions.get(b.key) ?? Infinity))
    let index = 0
    return columns().map((column) => (isStructuralColumn(column.key) ? column : movable[index++]))
  })

  const movableColumns = computed(() =>
    orderedColumns.value.filter((column) => !isStructuralColumn(column.key)),
  )
  // canReorder reports whether an enabled preference contains multiple movable
  // columns including the requested key. Structural columns and missing keys
  // cannot participate in either side of a move.
  const canReorder = (key: string) =>
    Boolean(storageKey.value) &&
    movableColumns.value.length > 1 &&
    movableColumns.value.some((column) => column.key === key)

  // moveColumn places a movable source immediately before or after the target and
  // reports success. Invalid pairs return false; successful moves retain hidden
  // keys and update page state before writing storage.
  const moveColumn = (source: string, target: string, side: 'before' | 'after') => {
    if (source === target || !canReorder(source) || !canReorder(target)) return false
    const next = columnOrder.value.filter((key) => key !== source)
    const targetIndex = next.indexOf(target)
    next.splice(targetIndex + (side === 'after' ? 1 : 0), 0, source)
    columnOrder.value = next
    try {
      localStorage.setItem(storageKey.value!, JSON.stringify(next))
    } catch {
      // Keep the reactive order already applied above when browser storage
      // rejects the write, so this page remains reorderable.
    }
    return true
  }

  return { orderedColumns, movableColumns, canReorder, moveColumn }
}
