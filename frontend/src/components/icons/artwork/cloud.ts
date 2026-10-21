// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/cloud.svg
import { createFallbackIcon } from '../createFallbackIcon'

// cloud.ts supplies a cloud outline to createFallbackIcon for the shared,
// single scale pulse on interaction. The single continuous path supplies the
// complete cloud contour, with the wrapper providing its unfilled appearance
// and current text color.

export default createFallbackIcon('cloud', [
  [
    'path',
    {
      d: 'M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z',
    },
  ],
])
