// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/log-in.svg
import { createFallbackIcon } from '../createFallbackIcon'

// log-in.ts supplies an arrow entering a doorway to createFallbackIcon for the
// shared, single scale pulse on interaction. The arrowhead and shaft point
// toward the open side of the door outline, keeping the entering direction
// clear within the combined shape.

export default createFallbackIcon('log-in', [
  [
    'path',
    {
      d: 'm10 17 5-5-5-5',
    },
  ],
  [
    'path',
    {
      d: 'M15 12H3',
    },
  ],
  [
    'path',
    {
      d: 'M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4',
    },
  ],
])
