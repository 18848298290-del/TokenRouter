<template>
  <div :class="props.embedded ? '' : 'card'">
    <div v-if="!props.embedded" class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.avatar.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.avatar.description') }}
      </p>
    </div>

    <div
      :class="
        props.embedded
          ? 'flex items-center gap-4'
          : 'flex flex-col gap-5 px-6 py-6 sm:flex-row sm:items-start'
      "
    >
      <UserAvatar
        :avatar-url="avatarDisplayUrl"
        :user-id="user?.id"
        :alt="displayName"
        :size-class="props.embedded ? 'h-16 w-16 shrink-0' : 'h-24 w-24 shrink-0'"
        :data-testid="avatarDisplayUrl ? 'profile-avatar-preview' : undefined"
      />

      <div :class="props.embedded ? 'min-w-0 space-y-2' : 'min-w-0 flex-1 space-y-4'">
        <div class="space-y-1">
          <p v-if="props.embedded" class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('profile.avatar.title') }}
          </p>
          <p v-else class="text-sm font-medium text-gray-900 dark:text-white">
            {{ displayName }}
          </p>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <label
            class="btn btn-secondary cursor-pointer"
            :class="{ 'pointer-events-none opacity-50': avatarSaving }"
          >
            <input
              data-testid="profile-avatar-file-input"
              type="file"
              accept="image/*"
              class="hidden"
              :disabled="avatarSaving"
              @change="handleAvatarFileChange"
            />
            {{ t('profile.avatar.uploadAction') }}
          </label>

          <button
            data-testid="profile-avatar-delete"
            type="button"
            class="btn btn-secondary"
            :disabled="avatarSaving"
            @click="handleAvatarDelete"
          >
            {{ t('common.delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { userAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/api/identity.types'
import UserAvatar from '@/components/common/UserAvatar.vue'
import { extractApiErrorMessage } from '@/utils/apiError'

// ProfileAvatarCard.vue edits the current account avatar through the profile API and
// publishes successful responses to the auth store. Upload preparation checks image type and
// size before creating the base64 payload expected by that endpoint. Small images pass
// through, and oversized GIFs report an error because re-encoding would change their
// animation representation.
//
// Oversized static images are decoded once, then tested across descending dimensions and
// WebP qualities until a candidate fits the byte budget. The shared save flag prevents
// upload and deletion from overlapping. Failed preparation or persistence leaves the
// currently stored profile in place, while the file input resets for a later retry.

// Profile ownership and presentation props select the current avatar source and whether the
// surrounding profile card supplies its own heading.
const props = withDefaults(
  defineProps<{
    user: User | null // Current account profile.
    embedded?: boolean // Parent supplies card framing.
  }>(),
  {
    embedded: false,
  },
)

// The auth store publishes saved profiles, while app notifications and translated labels
// report upload and deletion outcomes.
const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()

// targetAvatarUploadBytes limits prepared images to twenty kibibytes; scale and quality
// steps search progressively smaller WebP encodings for oversized static images.
const targetAvatarUploadBytes = 20 * 1024
const avatarScaleSteps = [1, 0.92, 0.84, 0.76, 0.68, 0.6, 0.52, 0.44, 0.36]
const avatarQualitySteps = [0.92, 0.84, 0.76, 0.68, 0.6, 0.52, 0.44, 0.36]
// avatarSaving guards overlapping avatar operations and disables the upload and delete
// controls until their current request settles.
const avatarSaving = ref(false)

// displayName selects a trimmed username, email, or localized account label;
// avatarDisplayUrl supplies the trimmed image source for the preview.
const displayName = computed(
  () => props.user?.username?.trim() || props.user?.email?.trim() || t('profile.user'),
)
const avatarDisplayUrl = computed(() => props.user?.avatar_url?.trim() || '')

// normalizeUploadedAvatar accepts a nonempty base64 image data URL after trimming. Empty
// input returns null silently, while another representation reports the upload-required
// error and stops profile submission.
function normalizeUploadedAvatar(value: string): string | null {
  const normalized = value.trim()
  if (!normalized) {
    return null
  }

  if (!/^data:image\/[a-zA-Z0-9.+-]+;base64,/i.test(normalized)) {
    appStore.showError(t('profile.avatar.uploadRequired'))
    return null
  }

  return normalized
}

// readFileAsDataURL resolves the browser FileReader result as a string for avatar
// preparation. Read failures reject with the reader error or a local error when the browser
// omits its cause.
function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '')
    reader.onerror = () => reject(reader.error ?? new Error('avatar_read_failed'))
    reader.readAsDataURL(file)
  })
}

// loadImage decodes a prepared data URL into a browser image for canvas resizing. Load
// errors reject with the translated read failure, which the upload handler reports through
// its normal error notification.
function loadImage(dataURL: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image()
    image.onload = () => resolve(image)
    image.onerror = () => reject(new Error(t('profile.avatar.readFailed')))
    image.src = dataURL
  })
}

// canvasToBlob encodes the canvas using the requested MIME type and quality. A null browser
// encoder result rejects with a compression failure so preparation cannot submit a missing
// image.
function canvasToBlob(canvas: HTMLCanvasElement, type: string, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => {
        if (!blob) {
          reject(new Error(t('profile.avatar.compressFailed')))
          return
        }
        resolve(blob)
      },
      type,
      quality,
    )
  })
}

// compressAvatarFile searches dimension and quality combinations for an avatar within the
// upload budget. Each scale redraws the decoded source before progressively reducing WebP
// quality, and the first fitting result becomes a new WebP File. Both dimensions remain at
// least one pixel to keep the canvas drawable. Missing canvas support, decoding failures, or
// exhaustion of every candidate reject the operation so the existing account avatar remains
// until a valid payload is submitted.
async function compressAvatarFile(file: File): Promise<File> {
  const sourceDataURL = await readFileAsDataURL(file)
  const image = await loadImage(sourceDataURL)
  const canvas = document.createElement('canvas')
  const ctx = canvas.getContext('2d')
  if (!ctx) {
    throw new Error(t('profile.avatar.compressFailed'))
  }

  // Try each scale at descending quality before shrinking again, keeping the first image
  // whose encoded bytes meet the upload budget.
  for (const scale of avatarScaleSteps) {
    const width = Math.max(1, Math.round(image.naturalWidth * scale))
    const height = Math.max(1, Math.round(image.naturalHeight * scale))
    canvas.width = width
    canvas.height = height
    ctx.clearRect(0, 0, width, height)
    ctx.drawImage(image, 0, 0, width, height)

    for (const quality of avatarQualitySteps) {
      const blob = await canvasToBlob(canvas, 'image/webp', quality)
      if (blob.size <= targetAvatarUploadBytes) {
        const fileName = file.name.replace(/\.[^.]+$/, '') || 'avatar'
        return new File([blob], `${fileName}.webp`, { type: 'image/webp' })
      }
    }
  }

  throw new Error(t('profile.avatar.compressTooLarge'))
}

// prepareAvatarUpload validates image MIME types and applies the byte limit before
// submission. Small images pass through, GIFs above the limit reject to retain their
// animation format, and oversized other images enter WebP compression.
async function prepareAvatarUpload(file: File): Promise<File> {
  if (!file.type.startsWith('image/')) {
    throw new Error(t('profile.avatar.invalidType'))
  }
  if (file.type === 'image/gif') {
    if (file.size > targetAvatarUploadBytes) {
      throw new Error(t('profile.avatar.gifTooLarge'))
    }
    return file
  }
  if (file.size <= targetAvatarUploadBytes) {
    return file
  }
  return compressAvatarFile(file)
}

// handleAvatarFileChange prepares the selected file and submits its base64 image data as the
// profile avatar. It clears the browser input immediately so the same file can be selected
// again, and ignores missing files or overlapping saves. The auth store receives the
// returned profile only after a successful update. Preparation and request failures display
// an error, and the save guard resets after every completion path.
async function handleAvatarFileChange(event: Event) {
  const input = event.target as HTMLInputElement | null
  const file = input?.files?.[0]
  if (input) {
    input.value = ''
  }
  if (!file || avatarSaving.value) {
    return
  }

  avatarSaving.value = true
  try {
    const preparedFile = await prepareAvatarUpload(file)
    const dataURL = await readFileAsDataURL(preparedFile)
    const normalized = normalizeUploadedAvatar(dataURL)
    if (!normalized) {
      return
    }
    const updated = await userAPI.updateProfile({ avatar_url: normalized })
    authStore.user = updated
    appStore.showSuccess(t('profile.avatar.saveSuccess'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    avatarSaving.value = false
  }
}

// handleAvatarDelete clears a present avatar by sending an empty avatar_url. Busy operations
// and already-empty profiles prevent submission; a successful response replaces the shared
// auth profile and reports completion.
async function handleAvatarDelete() {
  if (avatarSaving.value) {
    return
  }
  if (!props.user?.avatar_url?.trim()) {
    appStore.showError(t('profile.avatar.emptyDeleteHint'))
    return
  }

  avatarSaving.value = true
  try {
    const updated = await userAPI.updateProfile({ avatar_url: '' })
    authStore.user = updated
    appStore.showSuccess(t('profile.avatar.deleteSuccess'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    avatarSaving.value = false
  }
}
</script>
