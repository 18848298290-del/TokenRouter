<script setup lang="ts">
import { useFloatingMotion } from '@/composables/useFloatingMotion'
import MotionTransition from '@/components/common/MotionTransition.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  useId,
  useTemplateRef,
  watch,
} from 'vue'

// HelpTooltip.vue presents anchored explanations and interactive detail panels with
// shared hover, click, and controlled visibility. A supplied open prop takes
// precedence over local requests, while a blocked ancestor suppresses the actual panel
// until the anchor is available. Click pinning keeps details open while the pointer
// crosses owned regions.
//
// Keyboard entry moves into the teleported controls after rendering, and dismissal
// restores trigger focus when focus was inside an interactive panel. Positioning
// combines trigger coordinates, measured panel dimensions, and viewport space;
// horizontal clamping also adjusts the caret so it continues pointing toward the
// trigger.

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    content?: string // Plain tooltip body text.

    // Enables dialog accessibility and keyboard movement into panel controls; the
    // default false renders explanatory tooltip content.
    interactive?: boolean

    accessibleLabel?: string // Interactive panel accessible name.
    trigger?: 'hover' | 'click' | 'both' | 'manual' // Tooltip activation mode selector.

    // Overrides internal visibility when supplied; callers consume update:open to
    // accept dismissal and permit a later reopening.
    open?: boolean
    tooltipId?: string // Caller-supplied panel identifier.
    placement?: 'top' | 'bottom' // Preferred vertical panel side.
    widthClass?: string // Panel width class list.

    // Shows the panel close button; other enabled dismissal paths remain available
    // when the button is hidden.
    closable?: boolean
  }>(),
  {
    trigger: 'hover',
    interactive: false,
    placement: 'top',
    widthClass: 'w-64',
    closable: true,
    open: undefined,
  },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  'update:open': [value: boolean] // Requested panel visibility payload.
}>()
// generatedId supplies an instance identifier and panelId uses the caller identifier
// first, generating a default for interactive panels.
const generatedId = useId()
const panelId = computed(() => props.tooltipId ?? (props.interactive ? generatedId : undefined))

// panelObserver follows interactive panel dimensions after mounting and is
// disconnected before the component releases its DOM references.
let panelObserver: ResizeObserver | undefined
// internalShow stores local visibility requests, requestedShow prefers the open prop,
// and clickPinned keeps pointer exits from dismissing a clicked panel.
const internalShow = ref(false)
const requestedShow = computed(() => props.open ?? internalShow.value)
const clickPinned = ref(false)
// resolvedPlacement, tooltipStyle, and caretLeft hold measured panel geometry;
// triggerRef and tooltipRef identify the two owned interaction and measurement
// regions.
const resolvedPlacement = ref<'top' | 'bottom'>(props.placement)
const triggerRef = useTemplateRef<HTMLElement>('trigger')
const tooltipRef = useTemplateRef<HTMLElement>('tooltip')
const tooltipStyle = ref({ top: '0px', left: '0px' })
const caretLeft = ref('50%')
// ancestorBlocked delegates anchor visibility tracking to useFloatingMotion; show
// suppresses requested visibility while that ancestor state prevents interaction.
const ancestorBlocked = useFloatingMotion(
  triggerRef,
  () => requestedShow.value,
  closeTooltip,
  updatePosition,
)
const show = computed(() => requestedShow.value && !ancestorBlocked.value)

// Measure after a visible render so teleported panel dimensions reflect its current
// content before viewport clamping determines the anchor position.
watch(show, (visible) => {
  if (visible) nextTick(updatePosition)
})

// hoverEnabled reports whether the configured trigger mode accepts pointer hover.
function hoverEnabled() {
  return props.trigger === 'hover' || props.trigger === 'both'
}

// clickEnabled reports whether the configured trigger mode accepts click pinning.
function clickEnabled() {
  return props.trigger === 'click' || props.trigger === 'both'
}

// openTooltip requests visibility and measures placement after Vue updates the panel.
// It updates internal state and emits update:open, while a supplied open prop
// continues to control requested visibility.
function openTooltip() {
  internalShow.value = true
  emit('update:open', true)
  nextTick(updatePosition)
}

// closeTooltip requests dismissal and clears click pinning for the panel. When an
// interactive panel currently contains focus, it returns focus to the trigger; outside
// pointer dismissals retain their browser focus destination.
function closeTooltip() {
  const restoreFocus = props.interactive && tooltipRef.value?.contains(document.activeElement)
  internalShow.value = false
  emit('update:open', false)
  clickPinned.value = false
  if (restoreFocus) triggerRef.value?.querySelector<HTMLElement>('button, [tabindex]')?.focus()
}

// onEnter opens a hover-enabled panel unless a click already pinned it. Pinned state
// keeps pointer movement from changing the visibility chosen by the most recent click
// interaction.
function onEnter() {
  if (!hoverEnabled() || clickPinned.value) return
  openTooltip()
}

// isInside reports whether a DOM target belongs to the supplied container.
function isInside(container: HTMLElement | null, target: EventTarget | null): boolean {
  return target instanceof Node && !!container?.contains(target)
}

// onLeave closes an unpinned hover panel when the pointer leaves its owned regions.
// Movement into the tooltip element remains open so users can reach and copy its
// contents.
function onLeave(event?: MouseEvent) {
  if (!hoverEnabled() || clickPinned.value) return
  if (event && isInside(tooltipRef.value, event.relatedTarget)) return
  closeTooltip()
}

// onTooltipLeave closes a hover-only panel after leaving its owned trigger and panel.
// Movement back to the trigger is retained, while combined and click modes follow
// their separate dismissal paths.
function onTooltipLeave(event: MouseEvent) {
  if (props.trigger !== 'hover') return
  if (isInside(triggerRef.value, event.relatedTarget)) return
  closeTooltip()
}

// onClick toggles the click-pinned panel when the trigger mode permits clicks. It
// stops propagation before opening or closing, preventing the same click from becoming
// an outside dismissal.
function onClick(event: MouseEvent) {
  if (!clickEnabled()) return
  event.stopPropagation()
  if (clickPinned.value) {
    closeTooltip()
    return
  }
  clickPinned.value = true
  openTooltip()
}

// onDocumentInteraction dismisses a visible click-capable or manually controlled panel
// after an outside interaction. Targets inside either the trigger or teleported panel
// are retained as part of the component.
function onDocumentInteraction(event: Event) {
  if ((!clickEnabled() && props.trigger !== 'manual') || !show.value) return
  const target = event.target as Node | null
  if (!target) return
  if (triggerRef.value?.contains(target) || tooltipRef.value?.contains(target)) return
  closeTooltip()
}

// onDocumentKeydown dismisses click-capable and manual panels when Escape is pressed.
// Hover-only mode ignores this path, and closeTooltip determines whether an
// interactive panel needs focus returned to its trigger.
function onDocumentKeydown(event: KeyboardEvent) {
  if (!clickEnabled() && props.trigger !== 'manual') return
  if (event.key === 'Escape') {
    closeTooltip()
  }
}

// onTriggerKeydown moves forward keyboard navigation into interactive panel controls.
// ArrowDown opens the panel, while Tab enters an already visible panel; focus waits
// for the next rendered tick.
function onTriggerKeydown(event: KeyboardEvent) {
  if (!props.interactive || event.shiftKey) return
  if (event.key !== 'ArrowDown' && !(event.key === 'Tab' && show.value)) return
  event.preventDefault()
  clickPinned.value = true
  openTooltip()
  // Teleport places panel controls outside trigger DOM order, so focus waits for the
  // rendered panel before entering its first available control.
  nextTick(() =>
    tooltipRef.value
      ?.querySelector<HTMLElement>('button:not(:disabled), [href], [tabindex="0"]')
      ?.focus(),
  )
}

// onPanelKeydown returns reverse Tab navigation from the first panel control to its
// trigger. Later controls use ordinary browser order, and noninteractive panels leave
// keyboard handling to their surrounding page.
function onPanelKeydown(event: KeyboardEvent) {
  if (!props.interactive || event.key !== 'Tab' || !event.shiftKey) return
  const first = tooltipRef.value?.querySelector('button:not(:disabled), [href], [tabindex="0"]')
  if (document.activeElement === first) {
    event.preventDefault()
    triggerRef.value?.querySelector<HTMLElement>('button, [tabindex]')?.focus()
  }
}

// onInteractiveFocusout dismisses interactive details when focus leaves both owned
// regions. A related target inside the trigger or panel keeps the details open during
// internal keyboard focus movement.
function onInteractiveFocusout(event: FocusEvent) {
  if (
    !props.interactive ||
    isInside(triggerRef.value, event.relatedTarget) ||
    isInside(tooltipRef.value, event.relatedTarget)
  )
    return
  closeTooltip()
}

// onViewportChange refreshes placement for a currently visible panel after layout
// movement. Hidden panels skip this measurement and receive a new calculation when
// their visibility watcher next opens them.
function onViewportChange() {
  if (!show.value) return
  updatePosition()
}

// updatePosition combines trigger geometry, panel dimensions, and viewport space into
// anchored coordinates. It prefers the requested vertical side, flips when the
// opposite side fits, and clamps a panel that fits neither side against viewport
// padding.
//
// Horizontal positioning starts at the trigger center and uses half the measured panel
// width to keep its edges inside the viewport. The caret offset then follows the
// actual trigger center after clamping, while the top-side coordinate accounts for the
// template translation.
function updatePosition() {
  const el = triggerRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const tooltipRect = tooltipRef.value?.getBoundingClientRect()
  const tooltipWidth = tooltipRect?.width ?? 0
  const tooltipHeight = tooltipRect?.height ?? 0
  const centeredLeft = rect.left + rect.width / 2
  // Reserve a viewport inset around the measured tooltip panel.
  const viewportPadding = 12
  const halfTooltipWidth = tooltipWidth / 2
  const minLeft = halfTooltipWidth + viewportPadding
  const maxLeft = Math.max(minLeft, window.innerWidth - halfTooltipWidth - viewportPadding)
  const left = tooltipWidth > 0 ? Math.min(maxLeft, Math.max(minLeft, centeredLeft)) : centeredLeft
  let placement = props.placement
  let tooltipTop = rect.bottom + 8
  // Measured height allows side selection to consider both available spaces; before
  // measurement, the requested side supplies an initial anchor coordinate.
  if (tooltipHeight > 0) {
    const topPosition = rect.top - 8 - tooltipHeight
    const bottomPosition = rect.bottom + 8
    const fitsAbove = topPosition >= viewportPadding
    const fitsBelow = bottomPosition + tooltipHeight <= window.innerHeight - viewportPadding

    if (placement === 'bottom' && !fitsBelow && fitsAbove) {
      placement = 'top'
      tooltipTop = topPosition
    } else if (placement === 'top' && !fitsAbove && fitsBelow) {
      placement = 'bottom'
      tooltipTop = bottomPosition
    } else if (!fitsAbove && !fitsBelow) {
      tooltipTop = Math.max(viewportPadding, window.innerHeight - tooltipHeight - viewportPadding)
    } else if (placement === 'top') {
      tooltipTop = topPosition
    }
  } else if (placement === 'top') {
    tooltipTop = rect.top - 8
  }
  resolvedPlacement.value = placement

  // After horizontal clamping shifts the panel, offset the caret back toward the
  // trigger center and keep it inside the panel edges.
  caretLeft.value = `${Math.max(8, Math.min(tooltipWidth - 8, centeredLeft - left + halfTooltipWidth))}px`
  tooltipStyle.value = {
    top: `${placement === 'top' ? tooltipTop + tooltipHeight + 8 : tooltipTop}px`,
    left: `${left}px`,
  }
}

// Interactive content can resize independently of scrolling; observe its dimensions
// when supported, then pair document dismissal and viewport listeners with component
// cleanup.
onMounted(() => {
  if (props.interactive && typeof ResizeObserver !== 'undefined' && tooltipRef.value) {
    panelObserver = new ResizeObserver(onViewportChange)
    panelObserver.observe(tooltipRef.value)
  }
  document.addEventListener('click', onDocumentInteraction, true)
  document.addEventListener('pointerdown', onDocumentInteraction, true)
  document.addEventListener('touchstart', onDocumentInteraction, { capture: true, passive: true })
  document.addEventListener('keydown', onDocumentKeydown)
  window.addEventListener('resize', onViewportChange)
  window.addEventListener('scroll', onViewportChange, true)
  if (show.value) nextTick(updatePosition)
})

// Disconnect the dimension observer before releasing document and viewport listeners
// so detached panels stop scheduling placement through either subscription path.
onBeforeUnmount(() => {
  panelObserver?.disconnect()
  document.removeEventListener('click', onDocumentInteraction, true)
  document.removeEventListener('pointerdown', onDocumentInteraction, true)
  document.removeEventListener('touchstart', onDocumentInteraction, true)
  document.removeEventListener('keydown', onDocumentKeydown)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('scroll', onViewportChange, true)
})
</script>

<template>
  <div
    data-icon-trigger
    ref="trigger"
    class="group relative inline-flex items-center align-middle"
    :class="{ 'ml-1': trigger !== 'manual' }"
    @mouseenter="onEnter"
    @mouseleave="onLeave"
    @click="onClick"
    @keydown="onTriggerKeydown"
    @focusout="onInteractiveFocusout"
  >
    <!-- Tooltip trigger -->
    <slot name="trigger" :open="show" :panel-id="panelId">
      <Icon
        name="infoCircle"
        size="sm"
        class="h-4 w-4 cursor-help text-gray-400 transition-colors hover:text-primary-600 dark:text-gray-500 dark:hover:text-primary-400"
      />
    </slot>

    <!-- The tooltip mounts under body so modal content overflow does not clip its panel. -->
    <Teleport to="body">
      <!--
        The transparent before pseudo-element bridges the gap between tooltip and
        trigger so pointer movement can stay inside their interaction area.
      -->
      <MotionTransition name="fade" persisted>
        <div
          ref="tooltip"
          :id="panelId"
          v-show="show"
          :inert="!show || undefined"
          :role="interactive ? 'dialog' : 'tooltip'"
          :aria-label="interactive ? accessibleLabel : undefined"
          @keydown="onPanelKeydown"
          @focusout="onInteractiveFocusout"
          :class="[
            'fixed z-help-tooltip max-w-[calc(100vw-1.5rem)] -translate-x-1/2 tooltip-panel rounded-control shadow-xl',
            resolvedPlacement === 'top' ? '-translate-y-full' : 'translate-y-0',
            props.widthClass,
          ]"
          :style="{
            top: resolvedPlacement === 'top' ? `calc(${tooltipStyle.top} - 8px)` : tooltipStyle.top,
            left: tooltipStyle.left,
          }"
          @mouseleave="onTooltipLeave"
        >
          <!--
            Content scrolls inside its own region while the caret remains outside
            the scrolling surface.
          -->
          <div
            class="relative max-h-[calc(100vh-1.5rem)] overflow-y-auto p-3 text-xs leading-relaxed"
          >
            <button
              v-if="clickEnabled() && closable"
              type="button"
              class="absolute right-1.5 top-1.5 rounded-compact p-1 text-gray-300 transition-colors hover:bg-white/10 hover:text-white"
              aria-label="Close"
              @click.stop="closeTooltip"
            >
              <Icon name="x" size="xs" class="h-3.5 w-3.5" />
            </button>
            <slot>{{ content }}</slot>
          </div>
          <!-- In dark mode the caret outlines its two outward edges to join the panel border. -->
          <div
            class="tooltip-caret -translate-x-1/2"
            :style="{ left: caretLeft }"
            :class="
              resolvedPlacement === 'top'
                ? '-bottom-1 border-b border-r'
                : '-top-1 border-l border-t'
            "
          ></div>
        </div>
      </MotionTransition>
    </Teleport>
  </div>
</template>
