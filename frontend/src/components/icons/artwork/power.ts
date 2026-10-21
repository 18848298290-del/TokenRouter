// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/power.svg
import { createFallbackIcon } from '../createFallbackIcon'

// power.ts supplies a power symbol to createFallbackIcon for the shared, single
// scale pulse on interaction. The vertical stem enters an open circular arc,
// keeping the gap at the top visible while both paths participate in the shared
// pulse.

export default createFallbackIcon('power', [
  [
    'path',
    {
      d: 'M12 2v10',
    },
  ],
  [
    'path',
    {
      d: 'M18.4 6.6a9 9 0 1 1-12.77.04',
    },
  ],
])
