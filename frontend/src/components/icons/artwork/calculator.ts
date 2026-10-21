// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/calculator.svg
import { createFallbackIcon } from '../createFallbackIcon'

// calculator.ts supplies a calculator face to createFallbackIcon for the
// shared, single scale pulse on interaction. The outer rectangle contains a
// display line, keypad marks, and an elongated key, all drawn with the
// inherited SVG stroke.

export default createFallbackIcon('calculator', [
  [
    'rect',
    {
      width: '16',
      height: '20',
      x: '4',
      y: '2',
      rx: '2',
    },
  ],
  [
    'line',
    {
      x1: '8',
      x2: '16',
      y1: '6',
      y2: '6',
    },
  ],
  [
    'line',
    {
      x1: '16',
      x2: '16',
      y1: '14',
      y2: '18',
    },
  ],
  [
    'path',
    {
      d: 'M16 10h.01',
    },
  ],
  [
    'path',
    {
      d: 'M12 10h.01',
    },
  ],
  [
    'path',
    {
      d: 'M8 10h.01',
    },
  ],
  [
    'path',
    {
      d: 'M12 14h.01',
    },
  ],
  [
    'path',
    {
      d: 'M8 14h.01',
    },
  ],
  [
    'path',
    {
      d: 'M12 18h.01',
    },
  ],
  [
    'path',
    {
      d: 'M8 18h.01',
    },
  ],
])
