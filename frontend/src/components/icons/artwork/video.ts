// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/video.svg
import { createFallbackIcon } from '../createFallbackIcon'

// video.ts supplies a video camera to createFallbackIcon for the shared, single
// scale pulse on interaction. A rounded rectangle forms the body beside a
// projecting lens path, keeping the camera's two parts together inside the
// scaling group.

export default createFallbackIcon('video', [
  [
    'path',
    {
      d: 'm16 13 5.223 3.482a.5.5 0 0 0 .777-.416V7.87a.5.5 0 0 0-.752-.432L16 10.5',
    },
  ],
  [
    'rect',
    {
      x: '2',
      y: '6',
      width: '14',
      height: '12',
      rx: '2',
    },
  ],
])
