// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/sun.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// sun.ts renders a fixed sun disk with rays that appear in sequence. The ray
// array supplies one-based custom indices to the opacity variant, making the
// first ray wait one interval before the remaining rays follow around the disk.

// PATH_VARIANTS delays each ray's opacity transition by its custom order so the
// radial sequence progresses while the center circle remains visible.
const PATH_VARIANTS: Variants = {
  normal: { opacity: 1 },
  // One-based ray indices space entrances by one tenth of a second.
  animate: (i: number) => ({
    opacity: [0, 1],
    transition: { delay: i * 0.1, duration: 0.3 },
  }),
}

// icon binds the sun SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'sun',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'circle',
        {
          cx: '12',
          cy: '12',
          r: '4',
        },
        [],
      ),
      [
        'M12 2v2',
        'm19.07 4.93-1.41 1.41',
        'M20 12h2',
        'm17.66 17.66 1.41 1.41',
        'M12 20v2',
        'm6.34 17.66-1.41 1.41',
        'M2 12h2',
        'm4.93 4.93 1.41 1.41',
      ].map((d, index) =>
        h(
          motion.path,
          {
            animate: controls,
            custom: index + 1,
            d: d,
            key: d,
            variants: PATH_VARIANTS,
            initial: 'normal',
          },
          () => [],
        ),
      ),
    ]),
}

export default icon
