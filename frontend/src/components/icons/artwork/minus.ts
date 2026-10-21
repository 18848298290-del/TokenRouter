// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/minus.svg
import { createFallbackIcon } from '../createFallbackIcon'

// minus.ts supplies a horizontal minus mark to createFallbackIcon for the
// shared, single scale pulse on interaction. A single centered path spans the
// icon canvas, with its line caps and stroke thickness supplied by the
// enclosing Icon.vue SVG.

export default createFallbackIcon('minus', [
  [
    'path',
    {
      d: 'M5 12h14',
    },
  ],
])
