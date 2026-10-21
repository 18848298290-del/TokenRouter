// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/columns-3.svg
import { createFallbackIcon } from '../createFallbackIcon'

// columns-3.ts supplies a three-column panel to createFallbackIcon for the
// shared, single scale pulse on interaction. Two vertical dividers split the
// rounded outer rectangle into columns, and all three elements use the
// enclosing SVG's stroke settings.

export default createFallbackIcon('columns-3', [
  [
    'rect',
    {
      width: '18',
      height: '18',
      x: '3',
      y: '3',
      rx: '2',
    },
  ],
  [
    'path',
    {
      d: 'M9 3v18',
    },
  ],
  [
    'path',
    {
      d: 'M15 3v18',
    },
  ],
])
