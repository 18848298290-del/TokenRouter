// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/images.svg
import { createFallbackIcon } from '../createFallbackIcon'

// images.ts supplies overlapping image frames to createFallbackIcon for the
// shared, single scale pulse on interaction. The foreground frame includes a
// mountain path and filled sun dot, while a separate rear outline gives the
// stack its depth.

export default createFallbackIcon('images', [
  [
    'path',
    {
      d: 'm22 11-1.296-1.296a2.4 2.4 0 0 0-3.408 0L11 16',
    },
  ],
  [
    'path',
    {
      d: 'M4 8a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2',
    },
  ],
  [
    'circle',
    {
      cx: '13',
      cy: '7',
      r: '1',
      fill: 'currentColor',
    },
  ],
  [
    'rect',
    {
      x: '8',
      y: '2',
      width: '14',
      height: '14',
      rx: '2',
    },
  ],
])
