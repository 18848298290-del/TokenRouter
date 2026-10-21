// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/square.svg
import { createFallbackIcon } from '../createFallbackIcon'

// square.ts supplies a rounded square outline to createFallbackIcon for the
// shared, single scale pulse on interaction. A single rectangle occupies the
// central 18-unit region of the canvas, with the outer SVG supplying its stroke
// and unfilled interior.

export default createFallbackIcon('square', [
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
])
