// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/globe.svg
import { createFallbackIcon } from '../createFallbackIcon'

// globe.ts supplies a globe outline to createFallbackIcon for the shared,
// single scale pulse on interaction. The outer circle, curved meridian, and
// horizontal equator share the inherited SVG stroke, retaining their
// intersections throughout the scale pulse.

export default createFallbackIcon('globe', [
  [
    'circle',
    {
      cx: '12',
      cy: '12',
      r: '10',
    },
  ],
  [
    'path',
    {
      d: 'M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20',
    },
  ],
  [
    'path',
    {
      d: 'M2 12h20',
    },
  ],
])
