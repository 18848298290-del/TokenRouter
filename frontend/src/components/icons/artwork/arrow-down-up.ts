// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/arrow-down-up.svg
import { createFallbackIcon } from '../createFallbackIcon'

// arrow-down-up.ts supplies opposing vertical arrows to createFallbackIcon for
// the shared, single scale pulse on interaction. Four paths define two shafts
// and arrowheads, with their opposite directions retained inside one scaling
// group.

export default createFallbackIcon('arrow-down-up', [
  [
    'path',
    {
      d: 'm3 16 4 4 4-4',
    },
  ],
  [
    'path',
    {
      d: 'M7 20V4',
    },
  ],
  [
    'path',
    {
      d: 'm21 8-4-4-4 4',
    },
  ],
  [
    'path',
    {
      d: 'M17 4v16',
    },
  ],
])
