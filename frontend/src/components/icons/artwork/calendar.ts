// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/calendar.svg
import { createFallbackIcon } from '../createFallbackIcon'

// calendar.ts supplies a calendar page to createFallbackIcon for the shared,
// single scale pulse on interaction. A rounded rectangle encloses the page,
// while two binding strokes and a header divider distinguish it from a plain
// square.

export default createFallbackIcon('calendar', [
  [
    'path',
    {
      d: 'M8 2v3',
    },
  ],
  [
    'path',
    {
      d: 'M16 2v3',
    },
  ],
  [
    'rect',
    {
      x: '3',
      y: '3',
      width: '18',
      height: '18',
      rx: '2',
    },
  ],
  [
    'path',
    {
      d: 'M3 9h18',
    },
  ],
])
