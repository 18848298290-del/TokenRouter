import { computed, ref, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSubscriptionStore } from '@/stores/subscriptions'
import type { CheckoutInfoResponse, SubscriptionPlan } from '@/api/payment.types'
import type { UserSubscription } from '@/api/subscriptions.types'

/**
 * usePaymentSelection.ts coordinates subscription selection and renewal prompts
 * on the purchase page. Selecting an already active plan prompts for an
 * acknowledgement before the page can create another order for that plan.
 *
 * Subscription refresh can fail or return a value other than an array, so the
 * current store list supplies the duplicate check in those cases. The stored
 * acknowledgement belongs to one plan identifier and is reset when another plan
 * is applied. Renewal selection closes its dialog after confirmation, while
 * ordinary plan selection leaves renewal state untouched until that action runs.
 */

/** SelectionInputs names the purchase page state updated by subscription choices. */
interface SelectionInputs {
  checkout: Ref<CheckoutInfoResponse> // loaded plans and methods
  selectedPlan: Ref<SubscriptionPlan | null> // selected subscription plan
  errorMessage: Ref<string> // checkout error display
}

/**
 * usePaymentSelection returns subscription choices, duplicate prompts, and
 * renewal dialog state for one purchase page. Invoke it during component setup
 * so translated prompts and store subscription updates remain reactive.
 *
 * Duplicate detection refreshes active subscriptions before each decision and
 * uses the cached list if refresh fails. A confirmation callback records the
 * selected plan identifier before allowing submission. The checkout page can
 * repeat hasActivePlan immediately before creating an order, covering a plan
 * that became active after the user first selected it.
 */
export function usePaymentSelection({ checkout, selectedPlan, errorMessage }: SelectionInputs) {
  const { t } = useI18n()
  const subscriptionStore = useSubscriptionStore()
  const activeSubscriptions = computed(() => subscriptionStore.activeSubscriptions)
  // The displayed duplicate plan and its deferred action share one acknowledgement decision;
  // changing or closing the dialog clears that pending choice.
  const duplicatePlanDialogPlan = ref<SubscriptionPlan | null>(null)
  const duplicatePlanDialogConfirm = ref<(() => void) | null>(null)
  const duplicatePlanAcknowledgedId = ref<number | null>(null)
  // Renewal and duplicate prompts are owned by this page instance and share the selected plan
  // acknowledgement below.
  const showRenewalModal = ref(false)
  const renewGroupId = ref<number | null>(null)

  /**
   * isPlanAvailableForGroup reports whether a plan accepts a positive group identifier. An
   * empty modern group list accepts every group, while an absent list falls back to the
   * older single-group field when populated.
   */
  function isPlanAvailableForGroup(plan: SubscriptionPlan, groupId: number): boolean {
    if (!Number.isSafeInteger(groupId) || groupId <= 0) return false
    if (Array.isArray(plan.group_ids)) {
      return plan.group_ids.length === 0 || plan.group_ids.includes(groupId)
    }
    if (typeof plan.group_id === 'number' && plan.group_id > 0) {
      return plan.group_id === groupId
    }
    return true
  }

  // Renewal candidates use the requested group and current checkout plans, while the duplicate
  // message names the plan awaiting confirmation.
  const renewalPlans = computed(() => {
    const groupId = renewGroupId.value
    if (groupId == null) return []
    return checkout.value.plans.filter((plan) => isPlanAvailableForGroup(plan, groupId))
  })

  const duplicatePlanDialogMessage = computed(() =>
    t('payment.duplicatePlan.message', {
      plan: duplicatePlanDialogPlan.value?.name || t('payment.confirmSubscription'),
    }),
  )

  /**
   * loadActiveSubscriptionsForSelection refreshes active subscriptions for duplicate
   * detection. A rejected refresh or a non-array result uses the currently cached store
   * list, allowing selection to continue with the available subscription information.
   */
  async function loadActiveSubscriptionsForSelection(): Promise<UserSubscription[]> {
    try {
      const subscriptions = await subscriptionStore.fetchActiveSubscriptions(true)
      return Array.isArray(subscriptions) ? subscriptions : activeSubscriptions.value
    } catch {
      // Selection remains available after a refresh failure, using the subscriptions already
      // loaded in this user's store.
      return activeSubscriptions.value
    }
  }

  /**
   * hasActivePlan checks refreshed subscriptions for an active instance of the plan. Refresh
   * failure uses the current store list, and pending or expired subscriptions do not trigger
   * the duplicate acknowledgement.
   */
  async function hasActivePlan(planId: number): Promise<boolean> {
    const subscriptions = await loadActiveSubscriptionsForSelection()
    return subscriptions.some((sub) => sub.plan_id === planId && sub.status === 'active')
  }

  /**
   * applySelectedPlan selects a plan, records its acknowledgement, and clears checkout
   * errors. The acknowledgement stores this plan identifier only when confirmed, so
   * selecting another plan requires its own duplicate decision.
   */
  function applySelectedPlan(plan: SubscriptionPlan, acknowledgedDuplicate = false) {
    selectedPlan.value = plan
    duplicatePlanAcknowledgedId.value = acknowledgedDuplicate ? plan.id : null
    errorMessage.value = ''
  }

  /**
   * openDuplicatePlanDialog publishes a plan and its confirmation callback together. The
   * callback belongs to that dialog selection and runs only when
   * confirmDuplicatePlanPurchase consumes the currently stored callback.
   */
  function openDuplicatePlanDialog(plan: SubscriptionPlan, onConfirm: () => void) {
    duplicatePlanDialogPlan.value = plan
    duplicatePlanDialogConfirm.value = onConfirm
  }

  /**
   * closeDuplicatePlanDialog clears both the displayed plan and its deferred action.
   * Dismissing the dialog therefore leaves checkout selection unchanged and prevents a later
   * confirmation from invoking a stale callback.
   */
  function closeDuplicatePlanDialog() {
    duplicatePlanDialogPlan.value = null
    duplicatePlanDialogConfirm.value = null
  }

  /**
   * confirmDuplicatePlanPurchase consumes the current confirmation action after closing the
   * dialog. Clearing the stored action before invoking it keeps subsequent dialog state
   * owned by whatever the callback does next.
   */
  function confirmDuplicatePlanPurchase() {
    const onConfirm = duplicatePlanDialogConfirm.value
    closeDuplicatePlanDialog()
    onConfirm?.()
  }

  /**
   * selectPlan refreshes duplicate detection before applying an ordinary plan selection.
   * Active plans open the acknowledgement dialog; a confirmed duplicate is marked against
   * that plan so submission can recognize the acknowledgement.
   */
  async function selectPlan(plan: SubscriptionPlan) {
    // An active copy extends the same plan duration, so show its acknowledgement before
    // applying the checkout selection.
    if (await hasActivePlan(plan.id)) {
      openDuplicatePlanDialog(plan, () => applySelectedPlan(plan, true))
      return
    }
    applySelectedPlan(plan)
  }

  /**
   * selectPlanFromModal applies a renewal choice after checking active subscriptions. For a
   * duplicate plan the renewal modal stays open until confirmation, then closes before
   * applying the acknowledged selection to checkout.
   */
  async function selectPlanFromModal(plan: SubscriptionPlan) {
    // Renewal selection uses the same duplicate acknowledgement, retaining the modal until the
    // user accepts that plan purchase.
    if (await hasActivePlan(plan.id)) {
      openDuplicatePlanDialog(plan, () => {
        closeRenewalModal()
        applySelectedPlan(plan, true)
      })
      return
    }
    closeRenewalModal()
    applySelectedPlan(plan)
  }

  /**
   * closeRenewalModal hides renewal choices and clears their selected group identifier.
   */
  function closeRenewalModal() {
    showRenewalModal.value = false
    renewGroupId.value = null
  }

  return {
    duplicatePlanDialogPlan,
    duplicatePlanAcknowledgedId,
    duplicatePlanDialogMessage,
    showRenewalModal,
    renewGroupId,
    renewalPlans,
    isPlanAvailableForGroup,
    hasActivePlan,
    openDuplicatePlanDialog,
    closeDuplicatePlanDialog,
    confirmDuplicatePlanPurchase,
    selectPlan,
    selectPlanFromModal,
    closeRenewalModal,
  }
}
