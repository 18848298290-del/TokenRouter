// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/terminal.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// terminal.ts renders a command prompt with a blinking cursor stroke. The
// prompt chevron stays visible as a plain polyline, while the cursor's opacity
// keyframes hide and restore it once during the shared controller's animated
// target.

// LINE_VARIANTS hides and restores the terminal cursor once with linear opacity
// keyframes, keeping the prompt chevron outside the animated target.
const LINE_VARIANTS: Variants = {
  normal: { opacity: 1 },
  animate: {
    opacity: [1, 0, 1],
    transition: {
      duration: 0.8,
      repeat: 0,
      ease: 'linear',
    },
  },
}

// icon binds the terminal SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'terminal',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'polyline',
        {
          points: '4 17 10 11 4 5',
        },
        [],
      ),
      h(
        motion.line,
        {
          animate: controls,
          initial: 'normal',
          variants: LINE_VARIANTS,
          x1: '12',
          x2: '20',
          y1: '19',
          y2: '19',
        },
        () => [],
      ),
    ]),
}

export default icon
