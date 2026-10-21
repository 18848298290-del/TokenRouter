// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/arrow-left-right.svg
import { createFallbackIcon } from '../createFallbackIcon'

// arrow-left-right.ts supplies opposing horizontal arrows to createFallbackIcon
// for the shared, single scale pulse on interaction. The upper arrow points
// left and the lower arrow points right, keeping the two directions legible as
// a single symbol.

export default createFallbackIcon('arrow-left-right', [
  [
    'path',
    {
      d: 'M8 3 4 7l4 4',
    },
  ],
  [
    'path',
    {
      d: 'M4 7h16',
    },
  ],
  [
    'path',
    {
      d: 'm16 21 4-4-4-4',
    },
  ],
  [
    'path',
    {
      d: 'M20 17H4',
    },
  ],
])
