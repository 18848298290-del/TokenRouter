// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/image.svg
import { createFallbackIcon } from '../createFallbackIcon'

// image.ts supplies a framed landscape to createFallbackIcon for the shared,
// single scale pulse on interaction. A rounded frame surrounds a small circular
// sun and mountain path, with all three elements inheriting the enclosing SVG's
// stroke.

export default createFallbackIcon('image', [
  [
    'rect',
    {
      width: '18',
      height: '18',
      x: '3',
      y: '3',
      rx: '2',
      ry: '2',
    },
  ],
  [
    'circle',
    {
      cx: '9',
      cy: '9',
      r: '2',
    },
  ],
  [
    'path',
    {
      d: 'm21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21',
    },
  ],
])
