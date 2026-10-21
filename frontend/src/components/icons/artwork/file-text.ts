// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/file-text.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// file-text.ts renders a document outline with three animated text strokes. The
// enclosing group grows slightly, and its child paths inherit the animation
// target with staggered delays so the lines redraw in order inside the
// document.

// icon binds the file-text SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'file-text',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        initial: 'normal',
        variants: {
          normal: { scale: 1 },
          animate: {
            scale: 1.05,
            transition: {
              duration: 0.3,
              ease: 'easeOut',
            },
          },
        },
      },
      () => [
        h(
          'path',
          {
            d: 'M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z',
          },
          [],
        ),
        h(
          'path',
          {
            d: 'M14 2v4a2 2 0 0 0 2 2h4',
          },
          [],
        ),
        h(
          motion.path,
          {
            d: 'M10 9H8',
            stroke: 'currentColor',
            variants: {
              normal: {
                pathLength: 1,
                x1: 8,
                x2: 10,
              },
              animate: {
                pathLength: [1, 0, 1],
                x1: [8, 10, 8],
                x2: [10, 10, 10],
                transition: {
                  duration: 0.7,
                  delay: 0.3,
                },
              },
            },
            initial: 'normal',
          },
          () => [],
        ),
        h(
          motion.path,
          {
            d: 'M16 13H8',
            stroke: 'currentColor',
            variants: {
              normal: {
                pathLength: 1,
                x1: 8,
                x2: 16,
              },
              animate: {
                pathLength: [1, 0, 1],
                x1: [8, 16, 8],
                x2: [16, 16, 16],
                transition: {
                  duration: 0.7,
                  delay: 0.5,
                },
              },
            },
            initial: 'normal',
          },
          () => [],
        ),
        h(
          motion.path,
          {
            d: 'M16 17H8',
            stroke: 'currentColor',
            variants: {
              normal: {
                pathLength: 1,
                x1: 8,
                x2: 16,
              },
              animate: {
                pathLength: [1, 0, 1],
                x1: [8, 16, 8],
                x2: [16, 16, 16],
                transition: {
                  duration: 0.7,
                  delay: 0.7,
                },
              },
            },
            initial: 'normal',
          },
          () => [],
        ),
      ],
    ),
}

export default icon
