// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/clipboard.svg
import { createFallbackIcon } from '../createFallbackIcon'

// clipboard.ts supplies a clipboard outline to createFallbackIcon for the
// shared, single scale pulse on interaction. A small rounded rectangle forms
// the clip above the page-shaped path, keeping the two parts visible under the
// same inherited stroke.

export default createFallbackIcon('clipboard', [
  [
    'rect',
    {
      width: '8',
      height: '4',
      x: '8',
      y: '2',
      rx: '1',
      ry: '1',
    },
  ],
  [
    'path',
    {
      d: 'M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2',
    },
  ],
])
