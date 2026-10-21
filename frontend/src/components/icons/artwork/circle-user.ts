// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/circle-user.svg
import { createFallbackIcon } from '../createFallbackIcon'

// circle-user.ts supplies a circular profile badge to createFallbackIcon for
// the shared, single scale pulse on interaction. The head circle and shoulder
// path sit inside the enclosing ring, sharing the wrapper's current color and
// stroke width.

export default createFallbackIcon('circle-user', [
  [
    'circle',
    {
      cx: '12',
      cy: '12',
      r: '10',
    },
  ],
  [
    'circle',
    {
      cx: '12',
      cy: '10',
      r: '3',
    },
  ],
  [
    'path',
    {
      d: 'M7 20.662V19a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v1.662',
    },
  ],
])
