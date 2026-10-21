// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/database.svg
import { createFallbackIcon } from '../createFallbackIcon'

// database.ts supplies a database cylinder to createFallbackIcon for the
// shared, single scale pulse on interaction. An ellipse forms the top surface,
// while two paths describe the sides and middle rim within the same unfilled
// outline.

export default createFallbackIcon('database', [
  [
    'ellipse',
    {
      cx: '12',
      cy: '5',
      rx: '9',
      ry: '3',
    },
  ],
  [
    'path',
    {
      d: 'M3 5V19A9 3 0 0 0 21 19V5',
    },
  ],
  [
    'path',
    {
      d: 'M3 12A9 3 0 0 0 21 12',
    },
  ],
])
