// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/book-open.svg
import { createFallbackIcon } from '../createFallbackIcon'

// book-open.ts supplies an open book to createFallbackIcon for the shared,
// single scale pulse on interaction. A center spine divides the page outline,
// and both paths inherit their color and stroke width from the enclosing SVG.

export default createFallbackIcon('book-open', [
  [
    'path',
    {
      d: 'M12 5v16',
    },
  ],
  [
    'path',
    {
      d: 'M20.001 19A2 2 0 0022 17V5a2 2 0 00-1.999-2L16 3.002A5 5 0 0012 5a5 5 0 00-4-2H4a2 2 0 00-2 2v12a2 2 0 001.999 2H8a5 5 0 014 2 5 5 0 014-2z',
    },
  ],
])
