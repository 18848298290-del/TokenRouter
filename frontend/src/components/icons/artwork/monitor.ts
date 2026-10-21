// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/monitor.svg
import { createFallbackIcon } from '../createFallbackIcon'

// monitor.ts supplies a desktop monitor to createFallbackIcon for the shared,
// single scale pulse on interaction. The rounded display rectangle sits above a
// vertical support and horizontal foot, with all three pieces retaining their
// relative positions.

export default createFallbackIcon('monitor', [
  [
    'rect',
    {
      width: '20',
      height: '14',
      x: '2',
      y: '3',
      rx: '2',
    },
  ],
  [
    'line',
    {
      x1: '8',
      x2: '16',
      y1: '21',
      y2: '21',
    },
  ],
  [
    'line',
    {
      x1: '12',
      x2: '12',
      y1: '17',
      y2: '21',
    },
  ],
])
