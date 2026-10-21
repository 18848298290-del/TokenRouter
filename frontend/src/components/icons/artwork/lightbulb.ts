// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/lightbulb.svg
import { createFallbackIcon } from '../createFallbackIcon'

// lightbulb.ts supplies a lightbulb outline to createFallbackIcon for the
// shared, single scale pulse on interaction. The bulb contour sits above two
// separate base strokes, and all parts inherit the common SVG line color and
// thickness.

export default createFallbackIcon('lightbulb', [
  [
    'path',
    {
      d: 'M15 14c.2-1 .7-1.7 1.5-2.5 1-.9 1.5-2.2 1.5-3.5A6 6 0 0 0 6 8c0 1 .2 2.2 1.5 3.5.7.7 1.3 1.5 1.5 2.5',
    },
  ],
  [
    'path',
    {
      d: 'M9 18h6',
    },
  ],
  [
    'path',
    {
      d: 'M10 22h4',
    },
  ],
])
