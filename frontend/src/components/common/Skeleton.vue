<template>
  <div
    aria-hidden="true"
    :class="['skeleton', variant === 'circle' ? 'rounded-full' : 'rounded-compact', customClass]"
    :style="style"
  ></div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

/**
 * Skeleton.vue renders a decorative loading shape with caller supplied dimensions and
 * classes. Numeric dimensions become pixel lengths, string dimensions pass through as
 * CSS, and the text variant supplies em based height and margins when no height is
 * provided.
 */

/**
 * Props describes the caller supplied values consumed by Skeleton.
 */
interface Props {
  variant?: 'rect' | 'circle' | 'text' /* visual presentation variant */
  /**
   * width accepts a CSS length or pixel count; zero skips an inline width and lets
   * layout determine the result.
   */
  width?: string | number
  /**
   * height accepts a CSS length or pixel count; absent or zero height lets the text
   * variant use its em dimensions.
   */
  height?: string | number
  class?: string /* caller supplied classes */
}

/**
 * props defines the decorative shape, dimensions, and extra classes consumed by the
 * loading placeholder element and computed inline style.
 */
const props = withDefaults(defineProps<Props>(), {
  variant: 'rect',
  width: '100%',
})

/**
 * customClass forwards the caller supplied class string alongside the skeleton shape
 * classes, using an empty string when omitted.
 */
const customClass = computed(() => props.class || '')

/**
 * style converts numeric dimensions to CSS pixels and passes through string lengths;
 * text shapes receive em based dimensions when height is absent.
 */
const style = computed(() => {
  const s: Record<string, string> = {}

  if (props.width) {
    /**
     * Use pixels for numeric widths because CSS length values require a unit.
     */
    s.width = typeof props.width === 'number' ? `${props.width}px` : props.width
  }

  if (props.height) {
    /**
     * Use pixels for numeric heights because CSS length values require a unit.
     */
    s.height = typeof props.height === 'number' ? `${props.height}px` : props.height
  } else if (props.variant === 'text') {
    s.height = '1em'
    s.marginTop = '0.25em'
    s.marginBottom = '0.25em'
  }

  return s
})
</script>
