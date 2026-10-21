// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/move-right.svg
import { createFallbackIcon } from '../createFallbackIcon'

// move-right.ts supplies a rightward movement arrow to createFallbackIcon for
// the shared, single scale pulse on interaction. A long shaft reaches the
// right-facing arrowhead, and both paths share the wrapper's stroke width while
// the group scales around the canvas center.

export default createFallbackIcon('move-right', [
  [
    'path',
    {
      d: 'M18 8L22 12L18 16',
    },
  ],
  [
    'path',
    {
      d: 'M2 12H22',
    },
  ],
])
