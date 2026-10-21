// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/type.svg
import { createFallbackIcon } from '../createFallbackIcon'

// type.ts supplies a capital T text symbol to createFallbackIcon for the
// shared, single scale pulse on interaction. The upright, capped top, and lower
// foot are separate paths, retaining their letterform while the enclosing group
// supplies the interaction animation.

export default createFallbackIcon('type', [
  [
    'path',
    {
      d: 'M12 4v16',
    },
  ],
  [
    'path',
    {
      d: 'M4 7V5a1 1 0 0 1 1-1h14a1 1 0 0 1 1 1v2',
    },
  ],
  [
    'path',
    {
      d: 'M9 20h6',
    },
  ],
])
