// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/eraser.svg
import { createFallbackIcon } from '../createFallbackIcon'

// eraser.ts supplies an eraser outline to createFallbackIcon for the shared,
// single scale pulse on interaction. A diagonal seam divides the eraser body,
// with both paths inheriting their stroke settings from the enclosing SVG
// element.

export default createFallbackIcon('eraser', [
  [
    'path',
    {
      d: 'M21 21H8a2 2 0 0 1-1.42-.587l-3.994-3.999a2 2 0 0 1 0-2.828l10-10a2 2 0 0 1 2.829 0l5.999 6a2 2 0 0 1 0 2.828L12.834 21',
    },
  ],
  ['path', { d: 'm5.082 11.09 8.828 8.828' }],
])
