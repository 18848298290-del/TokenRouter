<template>
  <div class="flex items-start gap-4">
    <!-- Preview Box -->
    <div class="flex-shrink-0">
      <div
        class="flex items-center justify-center overflow-hidden rounded-surface border-2 border-dashed border-gray-300 bg-gray-50 dark:border-dark-600 dark:bg-dark-800"
        :class="[previewSizeClass, { 'border-solid': !!modelValue }]"
      >
        <!-- SVG mode: render inline -->
        <span
          v-if="mode === 'svg' && modelValue"
          class="text-gray-600 dark:text-gray-300 [&>svg]:h-full [&>svg]:w-full"
          :class="innerSizeClass"
          v-html="sanitizedValue"
        ></span>
        <!-- Image mode: show as img -->
        <img
          v-else-if="mode === 'image' && modelValue"
          :src="modelValue"
          alt=""
          class="h-full w-full object-contain"
        />
        <!-- Empty placeholder -->
        <Icon
          name="modalityImage"
          size="md"
          v-else
          class="text-gray-400 dark:text-dark-500"
          :class="placeholderSizeClass"
        />
      </div>
    </div>

    <!-- Controls -->
    <div class="flex-1 space-y-2">
      <div class="flex items-center gap-2">
        <label class="btn btn-secondary btn-sm cursor-pointer">
          <input type="file" :accept="acceptTypes" class="hidden" @change="handleUpload" />
          <Icon name="upload" size="sm" class="mr-1.5" :stroke-width="2" />
          {{ resolvedUploadLabel }}
        </label>
        <button
          v-if="modelValue"
          type="button"
          class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400"
          @click="$emit('update:modelValue', '')"
        >
          <Icon name="trash" size="sm" class="mr-1.5" :stroke-width="2" />
          {{ resolvedRemoveLabel }}
        </button>
      </div>
      <p v-if="hint" class="text-xs text-gray-500 dark:text-gray-400">{{ hint }}</p>
      <p v-if="error" class="text-xs text-red-500">{{ error }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { sanitizeSvg } from '@/utils/sanitize'

// ImageUpload.vue reads a chosen image or SVG into a parent-owned string value. SVG
// previews pass through sanitization, image previews use data URLs, and file-size or
// read failures remain local feedback beside the upload control.

// t resolves the component action and status labels using the active application
// locale.
const { t } = useI18n()

// props holds parent-supplied component options consumed by the rendering and
// interaction handlers below.
const props = withDefaults(
  defineProps<{
    modelValue: string // Parent-owned current field value.
    mode?: 'image' | 'svg' // Uploaded content representation mode.
    size?: 'sm' | 'md' // Control size recipe selector.
    uploadLabel?: string // Upload action label override.
    removeLabel?: string // Remove action label override.
    hint?: string // Optional explanatory hint text.
    // Maximum file size in bytes; the default is 300 KiB, and zero disables the size
    // rejection before browser reading begins.
    maxSize?: number
  }>(),
  {
    mode: 'image',
    size: 'md',
    uploadLabel: '',
    removeLabel: '',
    hint: '',
    maxSize: 300 * 1024,
  },
)

// emit publishes the declared component notifications so the parent can update its
// owned state.
const emit = defineEmits<{
  'update:modelValue': [value: string] // Replacement parent model payload.
}>()

// error holds the current local validation or read failure message, cleared when
// another file selection begins.
const error = ref('')

// resolvedUploadLabel and resolvedRemoveLabel supply translated action defaults;
// acceptTypes describes the file picker filter, and sanitizedValue cleans SVG markup
// before inline rendering.
const resolvedUploadLabel = computed(() => props.uploadLabel || t('common.upload'))
const resolvedRemoveLabel = computed(() => props.removeLabel || t('common.remove'))

const acceptTypes = computed(() => (props.mode === 'svg' ? '.svg' : 'image/*'))

const sanitizedValue = computed(() =>
  props.mode === 'svg' ? sanitizeSvg(props.modelValue ?? '') : '',
)

// previewSizeClass, innerSizeClass, and placeholderSizeClass select outer, SVG, and
// empty-state dimensions from the same caller size option.
const previewSizeClass = computed(() => (props.size === 'sm' ? 'h-14 w-14' : 'h-20 w-20'))
const innerSizeClass = computed(() => (props.size === 'sm' ? 'h-7 w-7' : 'h-12 w-12'))
const placeholderSizeClass = computed(() => (props.size === 'sm' ? 'h-5 w-5' : 'h-8 w-8'))

// handleUpload reads the first chosen file and publishes its encoded contents. A
// truthy maxSize rejects larger files, image mode checks the MIME prefix, and SVG mode
// reads trimmed text. The input is cleared so the same file can be selected again.
// FileReader failures set local feedback; readers are not cancelled when another
// selection starts.
function handleUpload(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  error.value = ''

  if (!file) return

  if (props.maxSize && file.size > props.maxSize) {
    error.value = t('common.fileTooLargeKb', {
      // Report sizes in kibibytes with one fractional digit for the selected file.
      size: (file.size / 1024).toFixed(1),
      max: (props.maxSize / 1024).toFixed(0),
    })
    // Reset the native selection so choosing the same file again still dispatches a
    // change event after this attempt.
    input.value = ''
    return
  }

  const reader = new FileReader()
  if (props.mode === 'svg') {
    reader.onload = (e) => {
      const text = e.target?.result as string
      if (text) emit('update:modelValue', text.trim())
    }
    reader.readAsText(file)
  } else {
    if (!file.type.startsWith('image/')) {
      error.value = t('common.selectImageFile')
      // Reset the native selection so choosing the same file again still dispatches a
      // change event after this attempt.
      input.value = ''
      return
    }
    reader.onload = (e) => {
      emit('update:modelValue', e.target?.result as string)
    }
    reader.readAsDataURL(file)
  }

  // A failed browser read leaves the parent model untouched; local feedback identifies
  // the failure beside the file controls for another attempt.
  reader.onerror = () => {
    error.value = t('common.fileReadFailed')
  }
  // Reset the native selection so choosing the same file again still dispatches a
  // change event after this attempt.
  input.value = ''
}
</script>
