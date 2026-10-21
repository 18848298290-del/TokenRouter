// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/funnel.svg
import { createFallbackIcon } from '../createFallbackIcon'

// funnel.ts supplies a filter funnel to createFallbackIcon for the shared,
// single scale pulse on interaction. The path narrows from its broad opening
// into the stem, keeping the filtering symbol contained in a single unfilled
// outline.

export default createFallbackIcon('funnel', [
  [
    'path',
    {
      d: 'M10 20a1 1 0 0 0 .553.895l2 1A1 1 0 0 0 14 21v-7a2 2 0 0 1 .517-1.341L21.74 4.67A1 1 0 0 0 21 3H3a1 1 0 0 0-.742 1.67l7.225 7.989A2 2 0 0 1 10 14z',
    },
  ],
])
