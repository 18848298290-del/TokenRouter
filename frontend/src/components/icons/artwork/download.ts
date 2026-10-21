// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/download.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// download.ts renders a download arrow moving toward a stationary tray. The
// shaft and arrowhead share one animated group and spring target, keeping them
// connected while the normal variant restores their vertical position after the
// interaction.

// ARROW_VARIANTS moves the complete arrow downward through a spring, then
// supplies zero vertical displacement when the controller requests the resting
// target.
const ARROW_VARIANTS: Variants = {
  normal: { y: 0 },
  animate: {
    y: 2,
    transition: {
      type: 'spring',
      stiffness: 200,
      damping: 10,
      mass: 1,
    },
  },
}

// icon binds the download SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'download',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4',
        },
        [],
      ),
      h(
        motion.g,
        {
          animate: controls,
          variants: ARROW_VARIANTS,
          initial: 'normal',
        },
        () => [
          h(
            'polyline',
            {
              points: '7 10 12 15 17 10',
            },
            [],
          ),
          h(
            'line',
            {
              x1: '12',
              x2: '12',
              y1: '15',
              y2: '3',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
