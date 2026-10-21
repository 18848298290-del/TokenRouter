import { defineStore } from 'pinia'
import { ref } from 'vue'
import { paymentAPI } from '@/api/payment'
import type {
  PaymentConfig,
  PaymentOrder,
  SubscriptionPlan,
  CreateOrderRequest,
} from '@/api/payment.types'

// payment.ts stores payment configuration, plan choices, and the order selected by
// checkout consumers. Configuration reads reuse loaded values and return the current
// value while a request is busy. Plan reads normalize newline-delimited features for
// display and retain their previous shared list when a request fails.
//
// Order creation returns data for the caller to select. Polling captures an order
// identifier for the request, then compares that identifier with the current selection
// when the response arrives. A response for another selected order still reaches its
// caller but does not overwrite shared currentOrder. Clearing the selection is a local
// action and does not cancel pending requests.

// usePaymentStore exposes payment configuration, normalized plans, and the selected
// checkout order. Callers own currentOrder selection; creating an order returns its data
// and does not assign that shared reference.
export const usePaymentStore = defineStore('payment', () => {
  // Payment state holds the cached configuration, caller-selected active order, and
  // normalized plan list used by checkout screens across route transitions.
  const config = ref<PaymentConfig | null>(null)
  const currentOrder = ref<PaymentOrder | null>(null)
  const plans = ref<SubscriptionPlan[]>([])

  // configLoading prevents duplicate requests while configLoaded permits ordinary calls
  // to reuse successfully loaded configuration until a forced refresh.
  const configLoading = ref(false)
  const configLoaded = ref(false)

  // fetchConfig returns loaded payment configuration for an ordinary read before
  // checking whether a request is active. Force bypasses this loaded check, but the
  // busy check still returns the current value immediately, which can be null before
  // the first success. Concurrent forced callers therefore receive the current value
  // instead of waiting for the active response.
  //
  // An admitted request publishes the response configuration and marks it loaded on
  // success. Failure logs the error and returns null while retaining prior configuration
  // and loaded state. Settlement clears the busy flag so later callers can request
  // configuration again.
  async function fetchConfig(force = false): Promise<PaymentConfig | null> {
    if (configLoaded.value && !force) return config.value
    if (configLoading.value) return config.value

    configLoading.value = true
    try {
      const response = await paymentAPI.getConfig()
      config.value = response.data
      configLoaded.value = true
      return config.value
    } catch (error: unknown) {
      // Configuration failure returns null while retaining the previous cached value;
      // checkout callers can decide whether that existing configuration remains usable.
      console.error('[payment] Failed to fetch config:', error)
      return null
    } finally {
      configLoading.value = false
    }
  }

  // fetchPlans loads available subscription plans and normalizes newline-separated
  // features to trimmed nonempty entries. Array features pass through, missing features
  // become empty arrays, and request failure returns an empty result while retaining
  // stored plans.
  async function fetchPlans(): Promise<SubscriptionPlan[]> {
    try {
      const response = await paymentAPI.getPlans()
      // The API may supply newline-delimited plan features; trim and discard blank lines
      // so checkout receives display-ready entries alongside existing array responses.
      plans.value = (response.data || []).map(
        (p: Omit<SubscriptionPlan, 'features'> & { features: string | string[] }) => ({
          ...p,
          features:
            typeof p.features === 'string'
              ? p.features
                  .split('\n')
                  .map((f: string) => f.trim())
                  .filter(Boolean)
              : p.features || [],
        }),
      )
      return plans.value
    } catch (error: unknown) {
      // Return an empty result to the requesting caller after failure while retaining
      // the previously displayed plans in shared state.
      console.error('[payment] Failed to fetch plans:', error)
      return []
    }
  }

  // createOrder submits the supplied checkout parameters and returns the created order
  // data.
  async function createOrder(params: CreateOrderRequest) {
    const response = await paymentAPI.createOrder(params)
    return response.data
  }

  // pollOrderStatus reads the stored server order by identifier and returns its
  // response. The request can complete after checkout changes its selected order, so
  // shared currentOrder is replaced only when the selection still has the requested
  // identifier. The caller receives the response even when it no longer matches that
  // selection. A failed read logs its error and returns null while retaining the
  // currently selected order.
  async function pollOrderStatus(orderId: number): Promise<PaymentOrder | null> {
    try {
      const response = await paymentAPI.getOrder(orderId)
      const order = response.data
      // The selection can change while awaiting the request, so compare identifiers at
      // completion before replacing shared checkout state with this response.
      if (currentOrder.value?.id === orderId) {
        currentOrder.value = order
      }
      return order
    } catch (error: unknown) {
      // A failed status read leaves the selected order unchanged and returns null so
      // checkout can continue its own polling or recovery flow.
      console.error('[payment] Failed to poll order status:', error)
      return null
    }
  }

  // clearCurrentOrder removes the selected order from shared checkout state.
  function clearCurrentOrder() {
    currentOrder.value = null
  }

  return {
    config,
    currentOrder,
    plans,
    configLoading,
    configLoaded,
    fetchConfig,
    fetchPlans,
    createOrder,
    pollOrderStatus,
    clearCurrentOrder,
  }
})
