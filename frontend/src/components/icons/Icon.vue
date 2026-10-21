<script lang="ts">
import { defineComponent, h, normalizeClass, onMounted, onUpdated, ref, type PropType } from 'vue'
import { icons, type IconName } from './registry'
import type { IconControls, IconDefinition } from './types'
import { useIconAnimation } from './useIconAnimation'

// Icon.vue renders registered artwork inside a stable SVG root with shared size
// and accessibility defaults. The keyed Artwork child remounts when the
// semantic name changes, while root attributes, caller events, and outer CSS
// transforms remain on the wrapper.

// IconSize selects square dimensions: xs is 12px, sm 16px, md 18px, lg
// 24px, and xl 32px before caller dimensions override either axis.
type IconSize = 'xs' | 'sm' | 'md' | 'lg' | 'xl'
// SIZE_CLASSES maps the five size presets to height and width utilities applied
// when callers omit those dimensions.
const SIZE_CLASSES: Record<IconSize, [string, string]> = {
  xs: ['h-3', 'w-3'],
  sm: ['h-4', 'w-4'],
  md: ['h-[18px]', 'w-[18px]'],
  lg: ['h-6', 'w-6'],
  xl: ['h-8', 'w-8'],
}

// Artwork renders the selected definition with its shared controller and
// requested stroke width. Icon.vue keys this child by semantic name so changing
// artwork remounts its internal motion nodes.
const Artwork = (props: {
  definition: IconDefinition // selected artwork renderer
  controls: IconControls // instance animation controls
  strokeWidth: number // inherited stroke width
}) => props.definition.render(props.controls, props.strokeWidth)

// Icon declares the SVG wrapper and reactive inputs consumed by its setup and
// render functions.
export default defineComponent({
  name: 'Icon',
  inheritAttrs: false,
  props: {
    // name selects a registry entry and also keys the inner artwork component
    // for replacement.
    name: { type: String as PropType<IconName>, required: true },
    // size supplies default dimensions; caller height, width, or matching
    // utility classes take precedence independently.
    size: { type: String as PropType<IconSize>, default: 'md' },
    strokeWidth: { type: Number, default: 1.75 }, // SVG stroke width
    // animateOnHover enables pointer and focus feedback; animationActive can
    // still request playback when this is false.
    animateOnHover: { type: Boolean, default: true },
    // animationActive requests playback independently of hover, while disabled
    // and reduced-motion conditions still suppress animation.
    animationActive: { type: Boolean, default: false },
  },
  // setup connects the root SVG to its animation controller and returns the
  // root renderer. Mounted and updated hooks remove internal artwork nodes from
  // sequential focus after their motion handlers attach.
  setup(props, { attrs, slots }) {
    // svgRef and controls belong to this mounted wrapper; definition reads the
    // current registry entry for rendering and target resets.
    const svgRef = ref<SVGSVGElement | null>(null)
    // definition returns the registry artwork selected by the current name
    // prop.
    const definition = () => icons[props.name]
    const controls = useIconAnimation(
      svgRef,
      definition,
      () => props.animateOnHover,
      () => props.animationActive,
    )

    // excludeArtworkFromFocus removes rendered shape nodes from sequential
    // keyboard navigation. It runs after mount and updates because motion-v
    // attaches focus handlers to artwork nodes, including nodes replaced by a
    // name change.
    const excludeArtworkFromFocus = () => {
      const artwork = svgRef.value?.querySelectorAll(
        'g, path, circle, rect, line, polyline, polygon, ellipse',
      )
      artwork?.forEach((node) => {
        node.setAttribute('tabindex', '-1')
        node.setAttribute('focusable', 'false')
      })
    }
    onMounted(excludeArtworkFromFocus)
    onUpdated(excludeArtworkFromFocus)

    return () => {
      const className = normalizeClass(attrs.class)
      const [height, width] = SIZE_CLASSES[props.size]
      const labelled = Boolean(attrs['aria-label'] || attrs['aria-labelledby'])

      return h(
        'svg',
        {
          xmlns: 'http://www.w3.org/2000/svg',
          fill: 'none',
          viewBox: '0 0 24 24',
          stroke: 'currentColor',
          'stroke-width': props.strokeWidth,
          'stroke-linecap': 'round',
          'stroke-linejoin': 'round',
          'aria-hidden': labelled ? undefined : true,
          role: labelled ? 'img' : undefined,
          focusable: 'false',
          tabindex: -1,
          ...attrs,
          ref: svgRef,
          'data-animated-icon': definition().name,
          // Caller dimensions take precedence per axis so preset utilities do
          // not compete with explicitly sized icons.
          class: [
            'shrink-0 select-none',
            !attrs.height && !/(?:^|\s)(?:\S+:)?(?:h-|size-)/.test(className) && height,
            !attrs.width && !/(?:^|\s)(?:\S+:)?(?:w-|size-)/.test(className) && width,
            className,
          ],
        },
        [
          h(Artwork, {
            key: props.name,
            definition: definition(),
            controls,
            strokeWidth: props.strokeWidth,
          }),
          slots.default?.(),
        ],
      )
    }
  },
})
</script>
