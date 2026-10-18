<script setup lang="ts">
import { useFloatingMotion } from '@/composables/useFloatingMotion'
import MotionTransition from '@/components/common/MotionTransition.vue'
import Icon from '@/components/icons/Icon.vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, useTemplateRef, watch } from 'vue'

// HelpTooltip.vue presents anchored explanations and optional interactive details.
// Hover and click triggers share a teleported panel and outside-dismiss behavior.
// Interactive panels expose dialog semantics and keyboard access while ordinary
// descriptions keep their tooltip role and existing trigger behavior.
//
// A pinned click keeps the panel available while controls receive focus. Escape
// returns focus to the trigger, and focus leaving both regions dismisses an
// interactive panel. A resize observer updates placement when switching content
// changes the panel dimensions, so protocol selectors remain inside the viewport.

const props = withDefaults(defineProps<{
  content?: string
  // Interactive enables a nonmodal dialog with keyboard access to its controls.
  interactive?: boolean
  // AccessibleLabel names the interactive panel for assistive technology users.
  accessibleLabel?: string
  trigger?: 'hover' | 'click' | 'both' | 'manual'
  // 手动提示跟随调用方状态，外部关闭后同步状态以供下次提交打开。
  open?: boolean
  tooltipId?: string
  placement?: 'top' | 'bottom'
  widthClass?: string
  // 是否显示右上角关闭按钮；纯说明性的短提示可以关闭，靠点击外部/Esc 关闭。
  closable?: boolean
}>(), {
  trigger: 'hover',
  interactive: false,
  placement: 'top',
  widthClass: 'w-64',
  closable: true,
  open: undefined,
})

// Panel identity connects optional dialog semantics to its trigger while the
// visibility and placement refs retain the existing controlled or local lifecycle.
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const generatedId = useId()
const panelId = computed(() => props.tooltipId ?? (props.interactive ? generatedId : undefined))
// panelObserver follows interactive content dimensions and is disconnected on
// unmount so removed panels cannot retain controls or schedule later positioning.
let panelObserver: ResizeObserver | undefined
const internalShow = ref(false)
const requestedShow = computed(() => props.open ?? internalShow.value)
const clickPinned = ref(false)
const resolvedPlacement = ref<'top' | 'bottom'>(props.placement)
const triggerRef = useTemplateRef<HTMLElement>('trigger')
const tooltipRef = useTemplateRef<HTMLElement>('tooltip')
const tooltipStyle = ref({ top: '0px', left: '0px' })
const caretLeft = ref('50%')
const ancestorBlocked = useFloatingMotion(triggerRef, () => requestedShow.value, closeTooltip, updatePosition)
const show = computed(() => requestedShow.value && !ancestorBlocked.value)

watch(show, (visible) => {
  if (visible) nextTick(updatePosition)
})

function hoverEnabled() {
  return props.trigger === 'hover' || props.trigger === 'both'
}

function clickEnabled() {
  return props.trigger === 'click' || props.trigger === 'both'
}

function openTooltip() {
  internalShow.value = true
  emit('update:open', true)
  nextTick(updatePosition)
}

// closeTooltip dismisses the panel and restores an interactive control's focus.
// Pointer dismissals outside the panel follow the browser's ordinary focus change;
// closing a focused panel returns keyboard users to its trigger button.
function closeTooltip() {
  const restoreFocus = props.interactive && tooltipRef.value?.contains(document.activeElement)
  internalShow.value = false
  emit('update:open', false)
  clickPinned.value = false
  if (restoreFocus) triggerRef.value?.querySelector<HTMLElement>('button, [tabindex]')?.focus()
}

function onEnter() {
  if (!hoverEnabled() || clickPinned.value) return
  openTooltip()
}

function isInside(container: HTMLElement | null, target: EventTarget | null): boolean {
  return target instanceof Node && !!container?.contains(target)
}

function onLeave(event?: MouseEvent) {
  if (!hoverEnabled() || clickPinned.value) return
  if (event && isInside(tooltipRef.value, event.relatedTarget)) return
  closeTooltip()
}

function onTooltipLeave(event: MouseEvent) {
  if (props.trigger !== 'hover') return
  if (isInside(triggerRef.value, event.relatedTarget)) return
  closeTooltip()
}

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

function onDocumentInteraction(event: Event) {
  if ((!clickEnabled() && props.trigger !== 'manual') || !show.value) return
  const target = event.target as Node | null
  if (!target) return
  if (triggerRef.value?.contains(target) || tooltipRef.value?.contains(target)) return
  closeTooltip()
}

// onDocumentKeydown handles Escape using the trigger's established dismissal rules.
// Interactive controls receive focus restoration through closeTooltip, whereas
// ordinary hover-only explanations retain their existing Escape behavior.
function onDocumentKeydown(event: KeyboardEvent) {
  if (!clickEnabled() && props.trigger !== 'manual') return
  if (event.key === 'Escape') {
    closeTooltip()
  }
}

// onTriggerKeydown moves keyboard focus into interactive panel controls.
// ArrowDown opens the panel directly; Tab enters an already open panel because
// teleport placement otherwise separates its controls from the trigger's tab order.
function onTriggerKeydown(event: KeyboardEvent) {
  if (!props.interactive || event.shiftKey) return
  if (event.key !== 'ArrowDown' && !(event.key === 'Tab' && show.value)) return
  event.preventDefault()
  clickPinned.value = true
  openTooltip()
  nextTick(() => tooltipRef.value?.querySelector<HTMLElement>('button:not(:disabled), [href], [tabindex="0"]')?.focus())
}

// onPanelKeydown returns reverse tab navigation to the panel's trigger button.
function onPanelKeydown(event: KeyboardEvent) {
  if (!props.interactive || event.key !== 'Tab' || !event.shiftKey) return
  const first = tooltipRef.value?.querySelector('button:not(:disabled), [href], [tabindex="0"]')
  if (document.activeElement === first) {
    event.preventDefault()
    triggerRef.value?.querySelector<HTMLElement>('button, [tabindex]')?.focus()
  }
}

// onInteractiveFocusout dismisses a panel after focus leaves both owned regions.
function onInteractiveFocusout(event: FocusEvent) {
  if (!props.interactive || isInside(triggerRef.value, event.relatedTarget) || isInside(tooltipRef.value, event.relatedTarget)) return
  closeTooltip()
}

function onViewportChange() {
  if (!show.value) return
  updatePosition()
}

function updatePosition() {
  const el = triggerRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const tooltipRect = tooltipRef.value?.getBoundingClientRect()
  const tooltipWidth = tooltipRect?.width ?? 0
  const tooltipHeight = tooltipRect?.height ?? 0
  const centeredLeft = rect.left + rect.width / 2
  const viewportPadding = 12
  const halfTooltipWidth = tooltipWidth / 2
  const minLeft = halfTooltipWidth + viewportPadding
  const maxLeft = Math.max(
    minLeft,
    window.innerWidth - halfTooltipWidth - viewportPadding,
  )
  const left = tooltipWidth > 0
    ? Math.min(maxLeft, Math.max(minLeft, centeredLeft))
    : centeredLeft
  let placement = props.placement
  let tooltipTop = rect.bottom + 8
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
      tooltipTop = Math.max(
        viewportPadding,
        window.innerHeight - tooltipHeight - viewportPadding,
      )
    } else if (placement === 'top') {
      tooltipTop = topPosition
    }
  } else if (placement === 'top') {
    tooltipTop = rect.top - 8
  }
  resolvedPlacement.value = placement
  // 提示框为了避开屏幕边缘发生横移时，箭头仍对准真实触发点。
  caretLeft.value = `${Math.max(8, Math.min(tooltipWidth - 8, centeredLeft - left + halfTooltipWidth))}px`
  tooltipStyle.value = {
    top: `${placement === 'top' ? tooltipTop + tooltipHeight + 8 : tooltipTop}px`,
    left: `${left}px`,
  }
}

onMounted(() => {
  // Content switches can change panel height independently of viewport events.
  // Observe those dimensions so the anchor position is recalculated after layout settles.
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

onBeforeUnmount(() => {
  // Release the element observer with the document listeners so detached panels
  // cannot retain their controls or schedule placement after the owner is removed.
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
  <div data-icon-trigger
    ref="trigger"
    class="group relative inline-flex items-center align-middle"
    :class="{ 'ml-1': trigger !== 'manual' }"
    @mouseenter="onEnter"
    @mouseleave="onLeave"
    @click="onClick"
    @keydown="onTriggerKeydown"
    @focusout="onInteractiveFocusout"
  >
    <!-- 触发图标 -->
    <slot name="trigger" :open="show" :panel-id="panelId">
      <Icon
        name="infoCircle"
        size="sm"
        class="h-4 w-4 cursor-help text-gray-400 transition-colors hover:text-primary-600 dark:text-gray-500 dark:hover:text-primary-400"
      />
    </slot>

    <!-- 提示挂载到 body，脱离弹窗的 overflow 裁剪区域。 -->
    <Teleport to="body">
      <!-- before: 伪元素向下延伸一段透明区域，盖住提示框与触发图标之间的空隙，让指针能连续移入提示框。 -->
      <MotionTransition name="fade" persisted>
        <div
          ref="tooltip"
          :id="panelId"
          v-show="show" :inert="!(show) || undefined"
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
            top: resolvedPlacement === 'top'
              ? `calc(${tooltipStyle.top} - 8px)`
              : tooltipStyle.top,
            left: tooltipStyle.left,
          }"
          @mouseleave="onTooltipLeave"
        >
          <!-- 内容层独立滚动，小箭头位于滚动区域外。 -->
          <div class="relative max-h-[calc(100vh-1.5rem)] overflow-y-auto p-3 text-xs leading-relaxed">
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
          <!-- 深色模式下箭头只描朝外的两条边，与面板边框连成一体。 -->
          <div
            class="tooltip-caret -translate-x-1/2"
            :style="{ left: caretLeft }"
            :class="resolvedPlacement === 'top' ? '-bottom-1 border-b border-r' : '-top-1 border-l border-t'"
          ></div>
        </div>
      </MotionTransition>
    </Teleport>
  </div>
</template>
