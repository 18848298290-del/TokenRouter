// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/inbox.svg
import { createFallbackIcon } from '../createFallbackIcon'

// inbox.ts supplies an inbox tray to createFallbackIcon for the shared, single
// scale pulse on interaction. A notched polyline defines the upper tray edge,
// and the surrounding path completes its sloping sides and rounded lower
// corners.

export default createFallbackIcon('inbox', [
  [
    'polyline',
    {
      points: '22 12 16 12 14 15 10 15 8 12 2 12',
    },
  ],
  [
    'path',
    {
      d: 'M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z',
    },
  ],
])
