// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/circle-x.svg
import { createFallbackIcon } from '../createFallbackIcon'

// circle-x.ts supplies a circled cross to createFallbackIcon for the shared,
// single scale pulse on interaction. Two diagonal paths form the interior
// cross, while a separate circle supplies the enclosing outline for the
// combined status symbol.

export default createFallbackIcon('circle-x', [
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
      d: 'm15 9-6 6',
    },
  ],
  [
    'path',
    {
      d: 'm9 9 6 6',
    },
  ],
])
