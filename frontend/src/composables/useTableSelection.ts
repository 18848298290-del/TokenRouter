import { computed, ref, type Ref } from 'vue'

// useTableSelection.ts maintains table row selection as a reactive set of numeric
// identifiers. Visible-row actions update only identifiers represented by the supplied row
// list, allowing selections on other pages to survive changes to the visible page.

// UseTableSelectionOptions supplies visible rows and their numeric selection identities.
interface UseTableSelectionOptions<T> {
  // rows identifies the visible page for bulk selection; replacing its contents does not
  // remove IDs already selected elsewhere.
  rows: Ref<T[]> // currently visible table rows
  // getId supplies stable numeric identities so row replacement and pagination can retain
  // selection across different row objects.
  getId: (row: T) => number // numeric row identity extractor
}

// useTableSelection creates shared selection state for a reactive visible row list. Row
// changes do not prune selected identifiers; callers clear or remove obsolete IDs when
// their owning dataset changes.
export function useTableSelection<T>({ rows, getId }: UseTableSelectionOptions<T>) {
  // selectedSet owns membership across pages, while selectedIds, selectedCount, and
  // allVisibleSelected expose views derived from that set and current visible rows.
  const selectedSet = ref<Set<number>>(new Set())

  const selectedIds = computed(() => Array.from(selectedSet.value))
  const selectedCount = computed(() => selectedSet.value.size)

  // isSelected reports whether an identifier belongs to the current selection.
  const isSelected = (id: number) => selectedSet.value.has(id)

  // replaceSelectedSet publishes the supplied set as the current reactive selection. It
  // retains that set rather than cloning it, so internal callers finish mutation before
  // publishing the replacement.
  const replaceSelectedSet = (next: Set<number>) => {
    selectedSet.value = next
  }

  // setSelectedIds replaces selection with unique IDs copied from the supplied array.
  const setSelectedIds = (ids: number[]) => {
    selectedSet.value = new Set(ids)
  }

  // select adds an identifier by publishing a copied selection set.
  const select = (id: number) => {
    if (selectedSet.value.has(id)) return
    const next = new Set(selectedSet.value)
    next.add(id)
    replaceSelectedSet(next)
  }

  // deselect removes an identifier by publishing a copied selection set.
  const deselect = (id: number) => {
    if (!selectedSet.value.has(id)) return
    const next = new Set(selectedSet.value)
    next.delete(id)
    replaceSelectedSet(next)
  }

  // toggle switches one identifier between selected and unselected table states.
  const toggle = (id: number) => {
    if (selectedSet.value.has(id)) {
      deselect(id)
      return
    }
    select(id)
  }

  // clear empties the selection when at least one identifier is selected.
  const clear = () => {
    if (selectedSet.value.size === 0) return
    replaceSelectedSet(new Set())
  }

  // removeMany removes supplied identifiers and publishes a replacement only when
  // membership changes. Empty input or selection is ignored, and IDs outside the supplied
  // list remain selected.
  const removeMany = (ids: number[]) => {
    if (ids.length === 0 || selectedSet.value.size === 0) return
    const next = new Set(selectedSet.value)
    let changed = false
    ids.forEach((id) => {
      if (next.delete(id)) changed = true
    })
    if (changed) replaceSelectedSet(next)
  }

  const allVisibleSelected = computed(() => {
    if (rows.value.length === 0) return false
    return rows.value.every((row) => selectedSet.value.has(getId(row)))
  })

  // toggleVisible adds or removes identifiers for the current visible rows. It leaves IDs
  // belonging to other pages unchanged and publishes one replacement set after processing
  // the list.
  const toggleVisible = (checked: boolean) => {
    const next = new Set(selectedSet.value)
    rows.value.forEach((row) => {
      const id = getId(row)
      if (checked) {
        next.add(id)
      } else {
        next.delete(id)
      }
    })
    replaceSelectedSet(next)
  }

  // batchUpdate gives the callback a copied draft set and publishes it when the callback
  // returns. A thrown callback leaves the prior selection installed; callers must finish
  // updates synchronously.
  const batchUpdate = (updater: (draft: Set<number>) => void) => {
    const draft = new Set(selectedSet.value)
    updater(draft)
    replaceSelectedSet(draft)
  }

  // selectVisible adds every currently visible row to the existing selection.
  const selectVisible = () => {
    toggleVisible(true)
  }

  return {
    selectedSet,
    selectedIds,
    selectedCount,
    allVisibleSelected,
    isSelected,
    setSelectedIds,
    select,
    deselect,
    toggle,
    clear,
    removeMany,
    toggleVisible,
    selectVisible,
    batchUpdate,
  }
}
