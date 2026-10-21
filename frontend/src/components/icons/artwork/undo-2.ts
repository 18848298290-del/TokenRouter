// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/undo-2.svg
import { createFallbackIcon } from '../createFallbackIcon'

// undo-2.ts supplies a curved undo arrow to createFallbackIcon for the shared,
// single scale pulse on interaction. A left-facing arrowhead sits at the end of
// a returning curved shaft, and both paths share the inherited stroke settings.

export default createFallbackIcon('undo-2', [
  [
    'path',
    {
      d: 'M9 14 4 9l5-5',
    },
  ],
  [
    'path',
    {
      d: 'M4 9h10.5a5.5 5.5 0 0 1 5.5 5.5a5.5 5.5 0 0 1-5.5 5.5H11',
    },
  ],
])
