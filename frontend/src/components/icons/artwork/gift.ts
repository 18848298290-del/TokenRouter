// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/gift.svg
import { createFallbackIcon } from '../createFallbackIcon'

// gift.ts supplies a wrapped gift to createFallbackIcon for the shared, single
// scale pulse on interaction. Separate paths describe the ribbon, bow, and box
// body, while a rounded rectangle supplies the lid beneath the bow.

export default createFallbackIcon('gift', [
  [
    'path',
    {
      d: 'M12 7v14',
    },
  ],
  [
    'path',
    {
      d: 'M20 11v8a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-8',
    },
  ],
  [
    'path',
    {
      d: 'M7.5 7a1 1 0 0 1 0-5A4.8 8 0 0 1 12 7a4.8 8 0 0 1 4.5-5 1 1 0 0 1 0 5',
    },
  ],
  [
    'rect',
    {
      x: '3',
      y: '7',
      width: '18',
      height: '4',
      rx: '1',
    },
  ],
])
