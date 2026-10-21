<template>
  <BaseDialog :show="show" :title="t('creative.cropper.title')" width="wide" @close="handleCancel">
    <div class="space-y-4">
      <div
        ref="containerRef"
        class="creative-cropper-container overflow-hidden rounded-control bg-black/5 dark:bg-dark-950"
      ></div>
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('creative.cropper.hint') }}</p>
    </div>

    <template #footer>
      <div class="flex justify-end space-x-3">
        <button type="button" class="btn btn-secondary" @click="handleSkip">
          {{ t('creative.cropper.skip') }}
        </button>
        <button type="button" :disabled="processing" class="btn btn-primary" @click="handleConfirm">
          {{ processing ? t('common.processing') : t('creative.cropper.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Cropper from 'cropperjs'
import BaseDialog from '@/components/common/BaseDialog.vue'

/**
 * CropperModal.vue presents one queued source image through Cropper.js and
 * emits cropped or original image bytes. The caller controls dialog visibility
 * and advances the upload queue after confirm or skip. Cropper.js owns its
 * injected custom elements, while this component owns their container and the
 * source Blob URL.
 *
 * Changes to visibility or image input destroy the current cropper before
 * initialization is scheduled for a later tick. The initialization callback
 * uses the container available after that wait. Confirmation exports the
 * selection as PNG and tracks processing through finally; closure and unmount
 * release the current cropper and URL, while pending initialization and export
 * callbacks continue independently of later visibility changes.
 */

/**
 * Props controls crop dialog visibility and the current queued image bytes.
 */
interface Props {
  show: boolean // dialog visibility flag
  /**
   * The current queued image is null when no crop source is available;
   * initialization requires both visibility and image bytes.
   */
  blob: Blob | null
}

/**
 * Emits returns cropped bytes, original bytes, or cancellation to the upload
 * queue.
 */
interface Emits {
  /**
   * The confirm event returns the exported PNG Blob to the upload queue. The
   * parent removes the current crop entry and starts placing these bytes on the
   * canvas.
   */
  (e: 'confirm', blob: Blob): void
  /**
   * The skip event returns the unchanged source Blob to the upload queue. The
   * parent advances the queue and places the original image through the normal
   * upload path.
   */
  (e: 'skip', blob: Blob): void
  /**
   * The cancel event asks the parent to discard the remaining crop queue. Closing
   * the dialog updates its visibility and lets the watcher release current
   * cropper resources.
   */
  (e: 'cancel'): void
}

/**
 * props, emit, and t connect the parent upload queue to the crop dialog
 * controls and localized labels.
 */
const props = defineProps<Props>()
const emit = defineEmits<Emits>()
const { t } = useI18n()

/**
 * containerRef owns the DOM insertion point that Cropper.js populates for the
 * current source image.
 */
const containerRef = ref<HTMLDivElement | null>(null)
/**
 * processing disables repeated confirmation while selection export and PNG
 * conversion are awaiting their asynchronous results.
 */
const processing = ref(false)
/**
 * cropper and objectUrl retain the current widget and source resource until
 * visibility changes or component teardown.
 */
let cropper: Cropper | null = null
let objectUrl = ''

/**
 * Visibility and input changes destroy the current cropper before starting the
 * next initialization, releasing its widget nodes and source URL together.
 */
watch(
  () => [props.show, props.blob] as const,
  ([show, blob]) => {
    if (show && blob) {
      void initCropper(blob)
    } else {
      teardownCropper()
    }
  },
  { immediate: true },
)

/**
 * initCropper destroys the current cropper and creates one for the supplied
 * Blob after a scheduled tick. It requires the container to exist after that
 * wait and owns the created source URL until teardown.
 */
async function initCropper(blob: Blob): Promise<void> {
  teardownCropper()
  /**
   * Defer container access until the dialog has a chance to render its content;
   * the existing initializer resumes with whichever container is then mounted.
   */
  await new Promise((resolve) => setTimeout(resolve, 0))
  const container = containerRef.value
  if (!container) return

  objectUrl = URL.createObjectURL(blob)
  const image = document.createElement('img')
  image.src = objectUrl
  image.alt = 'cropper-source'
  container.appendChild(image)
  cropper = new Cropper(image, { container })
}

/**
 * teardownCropper destroys the current Cropper instance, clears its container,
 * and revokes its source URL. It accepts repeated cleanup calls and resets
 * stored resource references for subsequent initialization.
 */
function teardownCropper(): void {
  /**
   * Destroy the widget before emptying its container and revoking the source URL
   * so cleanup follows the ownership established during initialization.
   */
  if (cropper) {
    cropper.destroy()
    cropper = null
  }
  if (containerRef.value) {
    containerRef.value.innerHTML = ''
  }
  if (objectUrl) {
    URL.revokeObjectURL(objectUrl)
    objectUrl = ''
  }
}

/**
 * handleConfirm exports the crop selection as a PNG and emits usable bytes.
 * Missing selection delegates to skipping, null Blob output emits nothing, and
 * processing resets in finally when export succeeds or rejects.
 */
async function handleConfirm(): Promise<void> {
  const selection = cropper?.getCropperSelection()
  if (!selection) {
    /**
     * A cropper without a current selection delegates to the original image path,
     * allowing the parent upload queue to continue without cropped bytes.
     */
    handleSkip()
    return
  }
  processing.value = true
  try {
    const canvas = await selection.$toCanvas()
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/png'))
    if (blob) {
      emit('confirm', blob)
    }
  } finally {
    processing.value = false
  }
}

/**
 * handleSkip emits the original source Blob when one remains available. The
 * parent advances its queue on that event, while a missing Blob leaves the
 * current dialog state unchanged.
 */
function handleSkip(): void {
  if (props.blob) {
    emit('skip', props.blob)
  }
}

/**
 * handleCancel emits cancellation so the parent can discard its upload queue.
 */
function handleCancel(): void {
  emit('cancel')
}

onBeforeUnmount(() => {
  teardownCropper()
})
</script>

<style scoped>
/* Cropper fills the available container width while a viewport-relative height cap leaves room for dialog instructions and confirmation controls. */
.creative-cropper-container :deep(cropper-canvas) {
  display: block;
  width: 100%;
  height: min(60vh, 480px);
}
</style>
