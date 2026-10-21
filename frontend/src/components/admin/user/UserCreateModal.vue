<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.createUser')"
    width="normal"
    @close="$emit('close')"
  >
    <form id="create-user-form" @submit.prevent="submit" class="space-y-5">
      <div>
        <label class="input-label">{{ t('admin.users.email') }}</label>
        <input
          v-model="form.email"
          type="email"
          required
          class="input"
          :placeholder="t('admin.users.enterEmail')"
        />
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.password') }}</label>
        <div class="flex gap-2">
          <div class="relative flex-1">
            <input
              v-model="form.password"
              type="text"
              required
              class="input pr-10"
              :placeholder="t('admin.users.enterPassword')"
            />
          </div>
          <button type="button" @click="generateRandomPassword" class="btn btn-secondary px-3">
            <Icon name="refresh" size="sm" />
          </button>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.username') }}</label>
        <input
          v-model="form.username"
          type="text"
          class="input"
          :placeholder="t('admin.users.enterUsername')"
        />
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.form.roleLabel') }}</label>
        <Select v-model="form.role" :options="roleOptions" />
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div>
          <label class="input-label">{{ t('admin.users.columns.balance') }}</label>
          <input v-model="form.balance" type="number" step="any" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.columns.concurrency') }}</label>
          <input v-model.number="form.concurrency" type="number" class="input" />
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.form.rpmLimit') }}</label>
        <input
          v-model.number="form.rpm_limit"
          type="number"
          min="0"
          :max="MAX_USER_API_KEY_LIMIT"
          step="1"
          class="input"
          :placeholder="t('admin.users.form.rpmLimitPlaceholder')"
        />
        <p class="input-hint">{{ t('admin.users.form.rpmLimitHint') }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.form.apiKeyLimit') }}</label>
        <input
          v-model="form.api_key_limit"
          type="number"
          min="0"
          step="1"
          class="input"
          data-test="api-key-limit-input"
          :placeholder="t('admin.users.form.apiKeyLimitPlaceholder')"
        />
        <p class="input-hint">{{ t('admin.users.form.apiKeyLimitCreateHint') }}</p>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" type="button" class="btn btn-secondary">
          {{ t('common.cancel') }}
        </button>
        <button type="submit" form="create-user-form" :disabled="loading" class="btn btn-primary">
          {{ loading ? t('admin.users.creating') : t('common.create') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <!-- The step-up dialog supplies TOTP verification when administrator creation requires it before retrying the request. -->
  <TotpStepUpDialog :controller="stepUp" />
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  useStepUp,
  isStepUpBlocked,
  isStepUpCancelled,
  stepUpBlockReason,
} from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { MAX_USER_API_KEY_LIMIT } from '@/constants/user'

// UserCreateModal.vue creates user accounts and delegates sensitive creation to step-up
// verification. The form represents optional starting balance and API-key limits with
// blank values so the administrator can request server defaults. Explicit zero values
// are included as numbers, making their meaning different from leaving an input blank.
//
// Submission validates the key limit before capturing the creation payload. That
// snapshot is reused by the verification callback while the administrator completes
// TOTP interaction. Successful persistence emits success before close so the parent can
// refresh its table; cancellation leaves the draft open, and blocked verification or
// request failures produce the corresponding notification.

// props and emit connect parent-controlled visibility with successful user creation and
// the request to close the completed form.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
}>()
const emit = defineEmits(['close', 'success'])
// The translation and notification services supply role labels and creation feedback,
// including blocked or cancelled step-up verification outcomes.
const { t } = useI18n()
const appStore = useAppStore()

// roleOptions supplies the two account roles shown in the administrator's user creation
// form and translated for the current locale.
const roleOptions = computed(() => [
  {
    value: 'user', // Stored option value.
    label: t('admin.users.roles.user'), // Displayed option label.
  },
  {
    value: 'admin', // Stored option value.
    label: t('admin.users.roles.admin'), // Displayed option label.
  },
])

// form stores the new user's editable identity and limits; blank balance and API-key
// limit values request server defaults when submit builds the payload.
const form = reactive({
  email: '', // User email address.
  password: '', // Draft account password.
  username: '', // User display name.
  notes: '', // Administrator account notes.
  role: 'user' as 'user' | 'admin', // Draft account role.
  // balance stays blank to request the server default; explicit zero is converted into
  // an included numeric amount.
  balance: '',
  concurrency: 1, // Concurrent request limit.
  rpm_limit: 0, // Requests per minute.
  // api_key_limit stays blank to request the server default; explicit zero remains
  // included after integer range validation.
  api_key_limit: '' as number | '',
})

// stepUp coordinates verification retries for sensitive creation requests, while
// loading blocks duplicate submissions until the operation completes or is cancelled.
const stepUp = useStepUp()
const loading = ref(false)

// submit validates an optional API-key limit and creates a user through step-up
// verification. Blank balance and key-limit drafts are omitted, whereas explicit zero
// values become numeric request fields. The payload snapshots the draft before
// verification so retried creation uses those captured values. Cancellation retains
// the open form quietly; other failures show their appropriate message. Successful
// creation emits success before close, and loading clears after every outcome.
const submit = async () => {
  if (loading.value) return
  const rawAPIKeyLimit = String(form.api_key_limit).trim()
  if (rawAPIKeyLimit !== '') {
    const apiKeyLimit = Number(rawAPIKeyLimit)
    if (
      !Number.isSafeInteger(apiKeyLimit) ||
      apiKeyLimit < 0 ||
      apiKeyLimit > MAX_USER_API_KEY_LIMIT
    ) {
      appStore.showError(t('admin.users.form.apiKeyLimitInvalid'))
      return
    }
  }
  loading.value = true
  try {
    const { balance: rawBalance, api_key_limit: rawLimit, ...rest } = { ...form }
    const balance = String(rawBalance).trim()
    const payload: typeof rest & {
      balance?: number // Draft starting balance.
      api_key_limit?: number // API-key creation limit.
    } = { ...rest }
    if (balance !== '') {
      payload.balance = Number(balance)
    }
    if (String(rawLimit).trim() !== '') {
      payload.api_key_limit = Number(rawLimit)
    }

    // The verification composable retries when the server requests step-up
    // authentication, keeping the captured creation payload together through the
    // administrator's TOTP interaction.
    await stepUp.run(() => adminAPI.users.create(payload))
    appStore.showSuccess(t('admin.users.userCreated'))
    emit('success')
    emit('close')
  } catch (e: any) {
    if (isStepUpCancelled(e)) {
      // Cancelled verification leaves the creation draft open for further editing, so
      // dismissal does not generate the ordinary creation-failure notification.
    } else if (isStepUpBlocked(e)) {
      appStore.showError(
        stepUpBlockReason(e) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
          ? t('stepUp.adminApiKeyForbidden')
          : t('stepUp.notEnabled'),
      )
    } else {
      appStore.showError(e?.message || t('admin.users.failedToCreate'))
    }
  } finally {
    loading.value = false
  }
}

// Opening resets the account draft, including blank optional numeric fields, so server
// defaults remain available for each newly created user.
watch(
  () => props.show,
  (v) => {
    if (v)
      Object.assign(form, {
        email: '', // User email address.
        password: '', // Draft account password.
        username: '', // User display name.
        notes: '', // Administrator account notes.
        role: 'user', // Draft account role.
        // balance stays blank to request the server default; explicit zero is converted
        // into an included numeric amount.
        balance: '',
        concurrency: 1, // Concurrent request limit.
        rpm_limit: 0, // Requests per minute.
        // api_key_limit stays blank to request the server default; explicit zero
        // remains included after integer range validation.
        api_key_limit: '',
      })
  },
)

// generateRandomPassword fills the draft with sixteen randomly selected allowed
// password characters.
const generateRandomPassword = () => {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789!@#$%^&*'
  let p = ''
  // Generate sixteen characters from the configured password alphabet.
  for (let i = 0; i < 16; i++) p += chars.charAt(Math.floor(Math.random() * chars.length))
  form.password = p
}
</script>
