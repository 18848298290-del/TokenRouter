// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/server.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// server.ts renders two server units that exchange vertical positions and then
// return. Each unit groups its chassis and indicator line, with opposite
// offsets and matching keyframe times keeping the exchange synchronized through
// the shared animation target.

// TOP_RECT_VARIANTS and BOTTOM_RECT_VARIANTS apply opposite vertical offsets
// with matching times so each server unit exchanges positions and returns as
// one group.
const TOP_RECT_VARIANTS: Variants = {
  normal: { y: 0 },
  animate: {
    y: [0, 12, 12, 0],
    transition: {
      duration: 0.9,
      ease: 'easeInOut',
      repeat: 0,
      times: [0, 0.35, 0.65, 1],
    },
  },
}

const BOTTOM_RECT_VARIANTS: Variants = {
  normal: { y: 0 },
  animate: {
    y: [0, -12, -12, 0],
    transition: {
      duration: 0.9,
      ease: 'easeInOut',
      repeat: 0,
      times: [0, 0.35, 0.65, 1],
    },
  },
}

// icon binds the server SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'server',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          variants: TOP_RECT_VARIANTS,
        },
        () => [
          h(
            'rect',
            {
              height: '8',
              rx: '2',
              ry: '2',
              width: '20',
              x: '2',
              y: '2',
            },
            [],
          ),
          h(
            'line',
            {
              x1: '6',
              x2: '10',
              y1: '6',
              y2: '6',
            },
            [],
          ),
        ],
      ),
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          variants: BOTTOM_RECT_VARIANTS,
        },
        () => [
          h(
            'rect',
            {
              height: '8',
              rx: '2',
              ry: '2',
              width: '20',
              x: '2',
              y: '14',
            },
            [],
          ),
          h(
            'line',
            {
              x1: '6',
              x2: '10',
              y1: '18',
              y2: '18',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
