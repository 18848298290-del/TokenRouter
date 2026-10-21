<template>
  <div :class="props.embedded ? '' : 'card'">
    <div v-if="!props.embedded" class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.editProfile') }}
      </h2>
    </div>
    <div :class="props.embedded ? '' : 'px-6 py-6'">
      <form @submit.prevent="handleUpdateProfile" class="space-y-4">
        <div>
          <label for="username" class="input-label">
            {{ t('profile.username') }}
          </label>
          <div class="flex flex-col gap-3 sm:flex-row">
            <input
              id="username"
              v-model="username"
              type="text"
              class="input min-w-0 flex-1"
              :placeholder="t('profile.enterUsername')"
            />
            <button type="submit" :disabled="loading" class="btn btn-primary shrink-0">
              {{ loading ? t('profile.updating') : t('profile.updateProfile') }}
            </button>
          </div>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { userAPI } from '@/api'

// ProfileEditForm.vue edits the profile username and publishes the returned account record
// to the auth store. Parent profile changes resynchronize the local draft, while submission
// trims whitespace, rejects empty names, and keeps the form available after a request
// failure.

// The parent supplies the initial username and optionally owns card framing; subsequent
// profile updates replace this form's local username draft.
const props = withDefaults(
  defineProps<{
    initialUsername: string // Profile username to edit.
    embedded?: boolean // Parent supplies card framing.
  }>(),
  {
    embedded: false,
  },
)

// Auth and app stores publish the saved profile and notify the user with the form's
// localized result messages.
const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()

// username holds the editable display name, and loading disables the submit control until
// its update request finishes.
const username = ref(props.initialUsername)
const loading = ref(false)

watch(
  () => props.initialUsername,
  (val) => {
    username.value = val
  },
)

// handleUpdateProfile trims and validates the username before requesting a profile update.
// Empty names display a validation message; successful responses replace the auth profile,
// and failures keep the draft available for correction.
const handleUpdateProfile = async () => {
  const trimmedUsername = username.value.trim()
  if (!trimmedUsername) {
    appStore.showError(t('profile.usernameRequired'))
    return
  }

  loading.value = true
  try {
    const updatedUser = await userAPI.updateProfile({
      username: trimmedUsername,
    })
    authStore.user = updatedUser
    appStore.showSuccess(t('profile.updateSuccess'))
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('profile.updateFailed'))
  } finally {
    loading.value = false
  }
}
</script>
