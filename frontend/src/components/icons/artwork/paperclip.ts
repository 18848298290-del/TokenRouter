// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/paperclip.svg
import { createFallbackIcon } from '../createFallbackIcon'

// paperclip.ts supplies a paperclip outline to createFallbackIcon for the
// shared, single scale pulse on interaction. Its continuous path describes the
// nested bends of the clip, with an unfilled interior and line styling
// inherited from the outer SVG.

export default createFallbackIcon('paperclip', [
  [
    'path',
    {
      d: 'm16 6-8.414 8.586a2 2 0 0 0 2.829 2.829l8.414-8.586a4 4 0 1 0-5.657-5.657l-8.379 8.551a6 6 0 1 0 8.485 8.485l8.379-8.551',
    },
  ],
])
