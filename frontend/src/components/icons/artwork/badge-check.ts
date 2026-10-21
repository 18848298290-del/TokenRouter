// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/badge-check.svg
import { createFallbackIcon } from '../createFallbackIcon'

// badge-check.ts supplies a scalloped badge and check mark to
// createFallbackIcon for the shared, single scale pulse on interaction. The
// badge outline and interior check are separate paths, allowing the inherited
// stroke to describe both shapes consistently.

export default createFallbackIcon('badge-check', [
  [
    'path',
    {
      d: 'M3.85 8.62a4 4 0 0 1 4.78-4.77 4 4 0 0 1 6.74 0 4 4 0 0 1 4.78 4.78 4 4 0 0 1 0 6.74 4 4 0 0 1-4.77 4.78 4 4 0 0 1-6.75 0 4 4 0 0 1-4.78-4.77 4 4 0 0 1 0-6.76Z',
    },
  ],
  [
    'path',
    {
      d: 'm16 9-5.5 5.5L8 12',
    },
  ],
])
