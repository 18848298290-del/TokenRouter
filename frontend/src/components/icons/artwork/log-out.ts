// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/log-out.svg
import { createFallbackIcon } from '../createFallbackIcon'

// log-out.ts supplies an arrow leaving a doorway to createFallbackIcon for the
// shared, single scale pulse on interaction. The door outline sits to the left
// of the outward arrow, whose shaft and head remain separate paths inside the
// common group.

export default createFallbackIcon('log-out', [
  [
    'path',
    {
      d: 'm16 17 5-5-5-5',
    },
  ],
  [
    'path',
    {
      d: 'M21 12H9',
    },
  ],
  [
    'path',
    {
      d: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4',
    },
  ],
])
