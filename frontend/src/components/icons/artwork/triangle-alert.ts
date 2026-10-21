// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/triangle-alert.svg
import { createFallbackIcon } from '../createFallbackIcon'

// triangle-alert.ts supplies a triangular warning mark to createFallbackIcon
// for the shared, single scale pulse on interaction. The triangle encloses a
// vertical exclamation stroke and dot, with the entire symbol inheriting the
// outer SVG's line color and thickness.

export default createFallbackIcon('triangle-alert', [
  [
    'path',
    {
      d: 'm21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3',
    },
  ],
  [
    'path',
    {
      d: 'M12 9v4',
    },
  ],
  [
    'path',
    {
      d: 'M12 17h.01',
    },
  ],
])
