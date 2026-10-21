<template>
  <!--
    The card stays in its caller DOM location, with zIndex choosing its layer when
    nested inside BaseDialog.
  -->
  <MotionTransition name="modal" appear @after-leave="handleAfterLeave">
    <div
      v-if="show"
      :inert="!show || undefined"
      role="dialog"
      aria-modal="true"
      class="fixed inset-0 z-modal overflow-y-auto"
      :style="zIndexStyle"
      @click.self="handleOverlay"
    >
      <div class="flex min-h-full items-center justify-center p-4">
        <div class="fixed inset-0 bg-[var(--overlay-bg)]" @click="handleOverlay"></div>

        <div
          ref="dialogRef"
          tabindex="-1"
          class="auth-dialog-content relative w-full max-w-md transform rounded-surface bg-white p-6 shadow-xl dark:bg-dark-900 sm:rounded-dialog"
        >
          <slot />
        </div>
      </div>
    </div>
  </MotionTransition>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { computed, ref } from 'vue'
import { useDialogLifecycle } from '@/composables/useDialogLifecycle'
import { Z_INDEX } from '@/constants/overlay'

// AuthCardDialog.vue renders the centered card shell for authentication and elevation
// flows. The caller supplies the card contents and controls visibility, while the
// shared dialog lifecycle manages focus and scrolling through the leave transition.

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    // Controls shell visibility when supplied; the default true also supports parents
    // that mount the shell conditionally.
    show?: boolean

    // Overrides the default modal layer when this shell needs to appear above another
    // visible dialog.
    zIndex?: number

    closeOnOverlay?: boolean // Allows backdrop dismissal requests.
  }>(),
  {
    show: true,
    zIndex: Z_INDEX.MODAL,
    closeOnOverlay: true,
  },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  // Requests parent dismissal.
  (e: 'close'): void
  // Reports completed shell exit.
  (e: 'after-leave'): void
}>()
// dialogRef identifies the focusable card for the lifecycle composable, whose
// afterLeave callback releases resources after the visual exit.
const dialogRef = ref<HTMLElement | null>(null)
const { afterLeave } = useDialogLifecycle(() => props.show, dialogRef)
// handleAfterLeave releases dialog lifecycle resources after the visual exit. It emits
// completion only while show remains false, allowing callers to retain a shell that
// reopened during its transition.
function handleAfterLeave() {
  // Release the shared dialog lifecycle before notifying the parent, since that
  // notification may unmount the authentication shell immediately.
  afterLeave()
  if (!props.show) emit('after-leave')
}

// zIndexStyle emits an inline layer override when the caller chooses a modal layer
// other than the shared default.
const zIndexStyle = computed(() =>
  props.zIndex !== Z_INDEX.MODAL ? { zIndex: props.zIndex } : undefined,
)
// handleOverlay requests dismissal when overlay closing is enabled by the caller.
// Interrupt-sensitive authentication flows can disable this prop so an incidental
// backdrop click leaves the flow visible.
const handleOverlay = () => {
  if (props.closeOnOverlay) emit('close')
}
</script>
