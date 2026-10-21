// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/tag.svg
import { createFallbackIcon } from '../createFallbackIcon'

// tag.ts supplies a price tag to createFallbackIcon for the shared, single
// scale pulse on interaction. The main outline surrounds a small filled hole,
// whose currentColor fill follows the same caller color as the tag's stroke.

export default createFallbackIcon('tag', [
  [
    'path',
    {
      d: 'M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z',
    },
  ],
  [
    'circle',
    {
      cx: '7.5',
      cy: '7.5',
      r: '.5',
      fill: 'currentColor',
    },
  ],
])
