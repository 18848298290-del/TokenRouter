<template>
  <Transition
    name="collapse"
    :appear="appear"
    :css="animate && !skipAnimation && !reducedMotion"
    @before-enter="beforeEnter"
    @before-leave="beforeLeave"
    @after-enter="finishEnter"
    @after-leave="finishLeave"
    @enter-cancelled="moving = false"
    @leave-cancelled="moving = false"
  >
    <div
      v-show="open"
      class="motion-collapse"
      :class="{ 'motion-collapse-moving': moving }"
      :inert="!open || undefined"
      :aria-hidden="!open || undefined"
      @form-field-reveal.capture="revealImmediately"
      @onboarding-reveal.capture="revealImmediately"
    >
      <div class="motion-collapse-inner">
        <RetainedContent v-if="renderContent" :freeze="unmountOnHide && !open">
          <slot />
        </RetainedContent>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { usePreferredReducedMotion } from '@vueuse/core'
import RetainedContent from './RetainedContent'
import { isolateLeavingElement, restoreEnteringElement } from '@/utils/leavingElement'

// @project-doc docs/architecture/frontend_ui_conventions.md#ui_motion
// Collapse.vue coordinates expandable content with retained component state and
// transition completion. Closed regions become inert immediately, while optional
// unmounting waits until the leave transition finishes. Content is restored
// synchronously before entering so height measurement can include the next slot tree.
// The operating system motion preference and reveal events can bypass animation.
//
// An interrupted leave can be followed by an enter, so completion checks the current
// open prop before releasing retained content or publishing completion.
// RetainedContent freezes slot output while an on-demand region leaves, keeping
// cleared business data from collapsing the outgoing frame prematurely.

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    open: boolean // Requested region visibility state.
    // Unmounts child content after leave completion; false keeps child component state
    // while the region is hidden.
    unmountOnHide?: boolean
    animate?: boolean // Enables CSS collapse animation.
    appear?: boolean // Animates the initial entry.
  }>(),
  { unmountOnHide: false, animate: true, appear: false },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  'after-enter': [] // Completed visible transition notification.
  'after-leave': [] // Completed hidden transition notification.
}>()
// preference and reducedMotion select animation policy, moving controls clipping,
// skipAnimation covers one reveal update, and renderContent retains children until a
// completed leave permits unmounting.
const preference = usePreferredReducedMotion()
const reducedMotion = computed(() => preference.value === 'reduce')
const moving = ref(false)
const skipAnimation = ref(false)
const renderContent = ref(!props.unmountOnHide || props.open)

watch(
  () => [props.open, props.unmountOnHide],
  () => {
    // Restore slot presence synchronously before Vue measures entering height;
    // deferred restoration would leave the transition measuring an empty region.
    if (props.open || !props.unmountOnHide) renderContent.value = true
  },
  { flush: 'sync' },
)

// beforeEnter restores the region before its entering transition begins. Restoring
// isolated controls precedes marking the region as moving, allowing an interrupted
// leave to return to editable content.
function beforeEnter(element: Element) {
  restoreEnteringElement(element)
  moving.value = true
}

// beforeLeave marks the region as moving and isolates on-demand content before exit.
// Retained regions keep their controls available to field-reveal logic, while content
// scheduled for unmount leaves native validation.
function beforeLeave(element: Element) {
  // On-demand children are leaving the form, so suspend their validation before exit;
  // retained children remain available for field-reveal navigation.
  if (props.unmountOnHide) isolateLeavingElement(element)
  moving.value = true
}

// finishEnter ends the moving phase and reports entry when the region is still open.
// Vue invokes this hook after entry completes, while the parent can change open
// independently of that completion. Read the current prop before notifying listeners
// so a close requested in the meantime suppresses the outdated entry notification.
// Clearing moving releases transition clipping even when the notification is skipped;
// content mounting remains controlled by the synchronous visibility watcher and the
// leave completion hook.
function finishEnter() {
  moving.value = false
  if (props.open) emit('after-enter')
}

// finishLeave ends the moving phase and completes a close that is still requested.
// Vue invokes this hook after leaving, while the parent may have reopened the region
// before the callback runs. Current visibility therefore decides whether retained
// content can be released and whether listeners receive after-leave.
//
// A reopened region keeps its mounted children for the entering transition. A region
// that remains closed releases children only when unmountOnHide is enabled, then
// notifies listeners after that mounting decision.
function finishLeave() {
  moving.value = false
  // An enter can interrupt the pending leave, so check current visibility before
  // removing slot content or publishing the closed-state notification.
  if (props.open) return
  if (props.unmountOnHide) renderContent.value = false
  emit('after-leave')
}

// revealImmediately bypasses animation for the current field-reveal update. It clears
// the bypass on the next Vue tick, allowing validation and onboarding to locate the
// revealed control immediately.
function revealImmediately() {
  skipAnimation.value = true
  // Keep the animation bypass through the reveal update so validation can locate the
  // field, then restore ordinary motion for later interactions.
  void nextTick(() => {
    skipAnimation.value = false
  })
}
</script>
