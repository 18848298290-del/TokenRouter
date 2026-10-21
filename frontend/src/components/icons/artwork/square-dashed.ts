// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/square-dashed.svg
import { createFallbackIcon } from '../createFallbackIcon'

// square-dashed.ts supplies a dashed selection square to createFallbackIcon for
// the shared, single scale pulse on interaction. Individual paths form the
// rounded corners and short edge marks, keeping the visible gaps fixed within
// the group's geometry.

export default createFallbackIcon('square-dashed', [
  [
    'path',
    {
      d: 'M5 3a2 2 0 0 0-2 2',
    },
  ],
  [
    'path',
    {
      d: 'M19 3a2 2 0 0 1 2 2',
    },
  ],
  [
    'path',
    {
      d: 'M21 19a2 2 0 0 1-2 2',
    },
  ],
  [
    'path',
    {
      d: 'M5 21a2 2 0 0 1-2-2',
    },
  ],
  [
    'path',
    {
      d: 'M9 3h1',
    },
  ],
  [
    'path',
    {
      d: 'M9 21h1',
    },
  ],
  [
    'path',
    {
      d: 'M14 3h1',
    },
  ],
  [
    'path',
    {
      d: 'M14 21h1',
    },
  ],
  [
    'path',
    {
      d: 'M3 9v1',
    },
  ],
  [
    'path',
    {
      d: 'M21 9v1',
    },
  ],
  [
    'path',
    {
      d: 'M3 14v1',
    },
  ],
  [
    'path',
    {
      d: 'M21 14v1',
    },
  ],
])
