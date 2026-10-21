// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/mail.svg
import { createFallbackIcon } from '../createFallbackIcon'

// mail.ts supplies an envelope outline to createFallbackIcon for the shared,
// single scale pulse on interaction. A rounded rectangle forms the envelope
// body, and a folded flap path meets near its center under the same inherited
// stroke.

export default createFallbackIcon('mail', [
  [
    'path',
    {
      d: 'm22 7-8.991 5.727a2 2 0 0 1-2.009 0L2 7',
    },
  ],
  [
    'rect',
    {
      x: '2',
      y: '4',
      width: '20',
      height: '16',
      rx: '2',
    },
  ],
])
