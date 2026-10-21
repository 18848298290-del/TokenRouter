import { describe, it, expect } from 'vitest'

import { findRowIndexByDomPosition } from '../useSwipeSelect'

// useSwipeSelect.spec.ts checks viewport-to-row lookup for fully rendered tables using
// synthetic row rectangles. Unequal heights, gaps, outside coordinates, and remapped data
// indices demonstrate that selection uses rendered geometry and returns sorted-data
// identities when virtualization is absent.

// makeScrollEl supplies synthetic row rectangles and data-index attributes for coordinate
// lookup. Input indices represent positions in sorted data and may differ from array
// positions; callers supply rows in vertical order.
function makeScrollEl(
  rows: Array<{
    index: number // Sorted data row index.
    top: number // Viewport top coordinate in pixels.
    bottom: number // Viewport bottom coordinate in pixels.
  }>,
): Element {
  const trs = rows.map((r) => ({
    getAttribute: (name: string) => (name === 'data-index' ? String(r.index) : null),
    getBoundingClientRect: () => ({
      top: r.top,
      bottom: r.bottom,
      left: 0,
      right: 0,
      width: 0,
      height: r.bottom - r.top,
      x: 0,
      y: r.top,
      toJSON: () => ({}),
    }),
  })) as unknown as HTMLElement[]

  return {
    querySelectorAll: (sel: string) =>
      (sel === 'tbody tr[data-index]' ? trs : []) as unknown as NodeListOf<Element>,
  } as unknown as Element
}

describe('findRowIndexByDomPosition (swipe-select full-render fallback)', () => {
  // The third fixture row is taller so coordinate assertions detect implementations that
  // substitute one uniform estimated height for rendered geometry.
  const rows = [
    { index: 0, top: 100, bottom: 200 },
    { index: 1, top: 200, bottom: 300 },
    { index: 2, top: 300, bottom: 450 },
  ]
  const el = makeScrollEl(rows)

  // The scrolling element exposes no rendered rows for the requested viewport coordinate.
  // Lookup must return minus one so callers can skip selection when no row exists.
  it('returns -1 when no rows are rendered', () => {
    expect(findRowIndexByDomPosition(makeScrollEl([]), 250)).toBe(-1)
  })

  // Rendered rows have differing heights, including a taller third row. Coordinates inside
  // each rectangle must resolve to that row's data index using its actual bounds.
  it('locates the row whose rect contains the Y coordinate', () => {
    expect(findRowIndexByDomPosition(el, 150)).toBe(0)
    expect(findRowIndexByDomPosition(el, 250)).toBe(1)
    expect(findRowIndexByDomPosition(el, 400)).toBe(2)
  })

  // Coordinates fall above the first row and below the last row. Lookup must clamp to the
  // corresponding endpoint data index instead of returning an absent row.
  it('clamps to the first/last row when Y is outside the rendered range', () => {
    expect(findRowIndexByDomPosition(el, 50)).toBe(0)
    expect(findRowIndexByDomPosition(el, 999)).toBe(2)
  })

  // Two row rectangles have a visible vertical gap between them. Coordinates nearer each
  // side of that gap must resolve to the corresponding neighboring row.
  it('picks the closer row when Y falls in a gap between rows', () => {
    const gapped = makeScrollEl([
      { index: 0, top: 100, bottom: 180 },
      { index: 1, top: 220, bottom: 300 },
    ])
    expect(findRowIndexByDomPosition(gapped, 190)).toBe(0)
    expect(findRowIndexByDomPosition(gapped, 215)).toBe(1)
  })

  // Rendered rows carry data indices different from their positions in the supplied array.
  // Lookup must return those attributes so full-render selection addresses sorted data
  // correctly.
  it('returns the data-index attribute value, not the array position', () => {
    const remapped = makeScrollEl([
      { index: 5, top: 100, bottom: 200 },
      { index: 9, top: 200, bottom: 300 },
    ])
    expect(findRowIndexByDomPosition(remapped, 150)).toBe(5)
    expect(findRowIndexByDomPosition(remapped, 250)).toBe(9)
  })
})
