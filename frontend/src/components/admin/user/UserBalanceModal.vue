<template>
  <BaseDialog
    :show="show"
    :title="operation === 'add' ? t('admin.users.deposit') : t('admin.users.withdraw')"
    width="narrow"
    @close="$emit('close')"
  >
    <form v-if="user" id="balance-form" @submit.prevent="handleBalanceSubmit" class="space-y-5">
      <div class="flex items-center gap-3 rounded-surface bg-gray-50 p-4 dark:bg-dark-700">
        <UserAvatar
          :avatar-url="user.avatar_url || ''"
          :user-id="user.id"
          :alt="user.email"
          size-class="h-10 w-10"
        />
        <div class="flex-1">
          <p class="font-medium text-gray-900 dark:text-white">{{ user.email }}</p>
          <p class="text-sm text-gray-500 dark:text-dark-400">
            {{ t('admin.users.currentBalance') }}: {{ formatBalance(user.balance) }}
          </p>
        </div>
      </div>
      <div>
        <label class="input-label">{{
          operation === 'add' ? t('admin.users.depositAmount') : t('admin.users.withdrawAmount')
        }}</label>
        <div class="relative flex gap-2">
          <div class="relative flex-1">
            <div class="input-icon font-medium text-gray-500">{{ balanceUnitSymbol }}</div>
            <input
              v-model.number="form.amount"
              type="number"
              step="any"
              min="0"
              required
              class="input input-has-icon input-icon-text"
            />
          </div>
          <button
            v-if="operation === 'subtract'"
            type="button"
            @click="fillAllBalance"
            class="btn btn-secondary whitespace-nowrap"
          >
            {{ t('admin.users.withdrawAll') }}
          </button>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.notes') }}</label
        ><textarea v-model="form.notes" rows="3" class="input"></textarea>
      </div>
      <div
        v-if="form.amount > 0"
        class="rounded-control border border-blue-200 bg-blue-50 p-4 dark:border-blue-800 dark:bg-blue-950"
      >
        <div class="flex items-center justify-between text-sm">
          <span class="text-gray-700 dark:text-gray-300">{{ t('admin.users.newBalance') }}:</span
          ><span class="font-bold text-gray-900 dark:text-gray-100">{{
            formatBalance(calculateNewBalance())
          }}</span>
        </div>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button
          type="submit"
          form="balance-form"
          :disabled="submitting || !form.amount"
          class="btn"
          :class="operation === 'add' ? 'bg-emerald-600 text-white' : 'btn-danger'"
        >
          {{ submitting ? t('common.saving') : t('common.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AdminUser } from '@/api/admin/users.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import UserAvatar from '@/components/common/UserAvatar.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'

// UserBalanceModal.vue adds or subtracts a selected user's balance with an
// administrator note. The preview combines the selected operation, account balance,
// and draft amount, then normalizes tiny arithmetic residuals to zero before display.
// Monetary formatting inspects up to eight decimal places so small visible amounts
// retain their significant digits while ordinary values show at least two decimals.
//
// Opening the dialog resets the amount and note for the next adjustment. Submission
// requires a positive amount and rejects a withdrawal greater than the displayed
// balance. The endpoint completes before success and close events ask the parent to
// refresh its account data; failed requests keep the draft available.

// props and emit connect the selected user and balance operation to parent refresh and
// close events after a successful adjustment.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
  // user supplies the selected account; null leaves identity-dependent operations
  // waiting until the parent chooses an account.
  user: AdminUser | null
  operation: 'add' | 'subtract' // Balance adjustment direction.
}>()
const emit = defineEmits(['close', 'success'])
// The translation, notification, and balance-display services provide amount labels,
// validation feedback, and monetary previews in the configured balance unit.
const { t } = useI18n()
const appStore = useAppStore()
const { balanceUnitSymbol, formatBalanceAmount } = useBalanceDisplay()

// submitting tracks the balance update while form holds the local amount and
// administrator note reset on each modal opening.
const submitting = ref(false)
const form = reactive({
  amount: 0, // Balance adjustment magnitude.
  notes: '', // Administrator adjustment note.
})
// Opening resets amount and notes so each adjustment starts with an empty draft rather
// than reusing the preceding balance operation.
watch(
  () => props.show,
  (v) => {
    if (v) {
      form.amount = 0
      form.notes = ''
    }
  },
)

// formatBalance displays at least two decimals and up to the significant digits found
// within eight decimal places. Zero uses the balance formatter directly, and the
// configured balance unit remains responsible for the final presentation.
const formatBalance = (value: number) => {
  const normalized = Number(value)
  if (normalized === 0) return formatBalanceAmount(0)
  // Inspect up to eight fractional digits for display precision.
  const fixed = normalized.toFixed(8).replace(/\.?0+$/, '')
  const parts = fixed.split('.')
  const decimal = parts.length === 1 ? 2 : Math.max(2, parts[1].length)
  return formatBalanceAmount(normalized, {
    fractionDigits: decimal, // Displayed fractional digit count.
  })
}

// fillAllBalance copies the selected user's displayed balance into the adjustment
// draft.
const fillAllBalance = () => {
  if (props.user) {
    form.amount = props.user.balance
  }
}

// calculateNewBalance previews adding or subtracting the draft amount from the selected
// user's balance. Missing users return zero, and tiny residual magnitudes are
// normalized to zero for display. The operation determines the sign applied to the
// draft amount, while the tolerance is applied after the arithmetic so a near-zero
// subtraction result cannot render as a negative-zero balance preview. The preview
// is shown when the draft amount is positive.
const calculateNewBalance = () => {
  if (!props.user) return 0
  const result =
    props.operation === 'add' ? props.user.balance + form.amount : props.user.balance - form.amount

  // Normalize tiny residuals to avoid a negative-zero preview.
  return Math.abs(result) < 1e-10 ? 0 : result
}
// handleBalanceSubmit validates a positive adjustment and prevents withdrawals above
// the selected user's displayed balance. It awaits the balance endpoint before emitting
// success and close; server details or a common error are shown on failure.
const handleBalanceSubmit = async () => {
  if (!props.user) return
  if (!form.amount || form.amount <= 0) {
    appStore.showError(t('admin.users.amountRequired'))
    return
  }

  if (props.operation === 'subtract' && form.amount > props.user.balance) {
    appStore.showError(t('admin.users.insufficientBalance'))
    return
  }
  submitting.value = true
  try {
    // Wait for the balance adjustment before success and close events so the parent can
    // reload the balance after persistence has completed.
    await adminAPI.users.updateBalance(props.user.id, form.amount, props.operation, form.notes)
    appStore.showSuccess(t('common.success'))
    emit('success')
    emit('close')
  } catch (e: any) {
    console.error('Failed to update balance:', e)
    appStore.showError(e.response?.data?.detail || t('common.error'))
  } finally {
    submitting.value = false
  }
}
</script>
