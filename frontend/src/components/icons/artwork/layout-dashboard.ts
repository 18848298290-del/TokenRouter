// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/layout-dashboard.svg
import { createFallbackIcon } from '../createFallbackIcon'

// layout-dashboard.ts supplies a dashboard tile grid to createFallbackIcon for
// the shared, single scale pulse on interaction. Four rounded rectangles form
// two columns with alternating heights, preserving the uneven tile arrangement
// when the group scales during interaction.

export default createFallbackIcon('layout-dashboard', [
  [
    'rect',
    {
      width: '7',
      height: '9',
      x: '3',
      y: '3',
      rx: '1',
    },
  ],
  [
    'rect',
    {
      width: '7',
      height: '5',
      x: '14',
      y: '3',
      rx: '1',
    },
  ],
  [
    'rect',
    {
      width: '7',
      height: '9',
      x: '14',
      y: '12',
      rx: '1',
    },
  ],
  [
    'rect',
    {
      width: '7',
      height: '5',
      x: '3',
      y: '16',
      rx: '1',
    },
  ],
])
