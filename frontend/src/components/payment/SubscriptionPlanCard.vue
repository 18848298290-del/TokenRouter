<template>
  <article
    :class="[
      'card flex flex-col transition-colors',
      isRenewal
        ? 'border-primary-500/60 dark:border-primary-500/50'
        : 'hover:border-black/20 dark:hover:border-dark-500',
    ]"
  >
    <div class="flex flex-1 flex-col p-6">
      <!-- Parallel plan cards reserve two title lines so prices align across a row; a single-column layout follows the title content height. -->
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0 flex-1">
          <h3
            :title="plan.name"
            class="min-w-0 break-words sm:h-12 [overflow-wrap:anywhere] text-base font-semibold leading-6 text-gray-900 dark:text-white line-clamp-2"
          >
            {{ plan.name }}
          </h3>
        </div>
        <span
          v-if="isRenewal"
          class="inline-flex shrink-0 items-center gap-1 rounded-full bg-primary-500/10 px-2 py-0.5 text-xs font-medium text-primary-600 dark:text-primary-400"
        >
          <Icon name="badge" size="xs" :animate-on-hover="false" />
          {{ t('payment.planCard.current') }}
        </span>
        <span
          v-else-if="discountText"
          class="shrink-0 rounded-full bg-red-500/10 px-2 py-0.5 text-xs font-semibold text-red-600 dark:text-red-400"
        >
          {{ discountText }}
        </span>
      </div>
      <p
        v-if="plan.description"
        class="mt-1 text-sm leading-relaxed text-gray-500 dark:text-dark-400 line-clamp-2"
      >
        {{ plan.description }}
      </p>

      <!-- Current and original prices share one row, keeping quota sections aligned even when a plan omits its original price. -->
      <div class="mt-5 flex flex-wrap items-baseline gap-x-1">
        <span class="text-xl font-semibold text-gray-900 dark:text-white">{{
          planCurrencySymbol
        }}</span>
        <span
          class="text-4xl font-semibold tracking-tight tabular-nums text-gray-900 dark:text-white"
          >{{ plan.price }}</span
        >
        <span v-if="plan.currency" class="text-xs font-medium text-gray-500 dark:text-dark-400">{{
          plan.currency
        }}</span>
        <span class="ml-1 text-sm text-gray-500 dark:text-dark-400">/ {{ validitySuffix }}</span>
        <span
          v-if="plan.original_price"
          class="ml-2 text-sm text-gray-400 line-through dark:text-dark-500"
        >
          {{ planCurrencySymbol }}{{ plan.original_price
          }}<template v-if="plan.currency"> {{ plan.currency }}</template>
        </span>
      </div>

      <!-- Quota blocks show configured periods; plans with no positive limits display the unlimited state. -->
      <dl v-if="quotaItems.length > 0" :class="['mt-5 grid gap-2', quotaGridClass]">
        <div
          v-for="item in quotaItems"
          :key="item.key"
          class="min-w-0 rounded-control bg-gray-50 px-3 py-2.5 dark:bg-dark-950"
        >
          <dt class="truncate text-xs text-gray-500 dark:text-dark-400">{{ item.label }}</dt>
          <dd
            class="mt-1 truncate text-sm font-semibold tabular-nums text-gray-900 dark:text-dark-50"
          >
            {{ item.value }}
          </dd>
        </div>
      </dl>
      <div
        v-else
        class="mt-5 flex items-center gap-3 rounded-control bg-gray-50 px-3 py-2.5 dark:bg-dark-950"
      >
        <span class="text-xl leading-none text-primary-500" aria-hidden="true">∞</span>
        <div class="min-w-0">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('payment.planCard.quota') }}</p>
          <p class="text-sm font-semibold text-gray-900 dark:text-dark-50">
            {{ t('payment.planCard.unlimited') }}
          </p>
        </div>
      </div>

      <!-- Allowed model families. -->
      <div v-if="modelScopeLabels.length > 0" class="mt-4 flex flex-wrap items-center gap-1.5">
        <span class="mr-1 text-xs text-gray-500 dark:text-dark-400">{{
          t('payment.planCard.models')
        }}</span>
        <span
          v-for="scope in modelScopeLabels"
          :key="scope"
          class="rounded-compact border border-gray-200 px-1.5 py-0.5 text-xs font-medium text-gray-600 dark:border-dark-600 dark:text-dark-200"
        >
          {{ scope }}
        </span>
      </div>

      <!-- Plan feature list. -->
      <ul
        v-if="plan.features.length > 0"
        class="mt-5 space-y-2.5 border-t border-gray-100 pt-5 dark:border-dark-700"
      >
        <li v-for="feature in plan.features" :key="feature" class="flex items-start gap-2.5">
          <span
            class="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-primary-500/10 text-primary-600 dark:text-primary-400"
          >
            <Icon name="check" size="xs" :stroke-width="3" :animate-on-hover="false" />
          </span>
          <span class="text-sm text-gray-600 dark:text-dark-200">{{ feature }}</span>
        </li>
      </ul>

      <div class="flex-1" />

      <button
        type="button"
        :class="['btn mt-6 w-full', isRenewal ? 'btn-secondary' : 'btn-primary']"
        @click="emit('select', plan)"
      >
        {{ isRenewal ? t('payment.renewNow') : t('payment.subscribeNow') }}
      </button>
    </div>
  </article>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SubscriptionPlan } from '@/api/payment.types'
import type { UserSubscription } from '@/api/subscriptions.types'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { currencySymbol } from '@/utils/payment/currency'
import { planValiditySuffix } from '@/utils/payment/validity'

/**
 * SubscriptionPlanCard.vue presents a selectable subscription with price, duration, quota
 * periods, and supported model families. Existing active subscriptions mark the card as a
 * renewal, while plans without positive quota limits display the unlimited state in the
 * shared card layout.
 */

const props = defineProps<{
  plan: SubscriptionPlan // displayed subscription plan
  activeSubscriptions?: UserSubscription[] // current user subscription records
}>()
const emit = defineEmits<{
  select: [plan: SubscriptionPlan] // requested subscription plan selection
}>()
const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()
const planCurrencySymbol = computed(() => currencySymbol(props.plan.currency || 'USD'))

// Card presentation derives renewal, discount, quota, and duration from props; the parent owns
// any selected plan and purchase side effects.
const isRenewal = computed(
  () =>
    props.activeSubscriptions?.some((s) => s.plan_id === props.plan.id && s.status === 'active') ??
    false,
)

const discountText = computed(() => {
  if (!props.plan.original_price || props.plan.original_price <= 0) return ''
  const pct = Math.round((1 - props.plan.price / props.plan.original_price) * 100)
  return pct > 0 ? `-${pct}%` : ''
})

/**
 * formatPlanQuota displays quota in the account balance unit with integer or two-decimal
 * precision.
 */
function formatPlanQuota(value: number | null | undefined): string {
  const amount = Number(value)
  return formatBalanceAmount(value, { fractionDigits: Number.isInteger(amount) ? 0 : 2 })
}

/**
 * hasPlanQuota reports whether a configured subscription quota is positive.
 */
function hasPlanQuota(value: number | null | undefined): boolean {
  return value != null && value > 0
}

// Only periods with positive quota limits enter this list; an empty list selects the unlimited
// presentation in the template.
const quotaItems = computed(() => {
  const windows = [
    { key: 'daily', label: t('payment.planCard.dailyLimit'), limit: props.plan.daily_limit_usd },
    { key: 'weekly', label: t('payment.planCard.weeklyLimit'), limit: props.plan.weekly_limit_usd },
    {
      key: 'monthly',
      label: t('payment.planCard.monthlyLimit'),
      limit: props.plan.monthly_limit_usd,
    },
  ]
  return windows
    .filter((item) => hasPlanQuota(item.limit))
    .map((item) => ({ key: item.key, label: item.label, value: formatPlanQuota(item.limit) }))
})

// Quota blocks divide the row according to the number of configured periods, keeping each
// available quota equally readable.
const quotaGridClass = computed(() => {
  if (quotaItems.value.length === 1) return 'grid-cols-1'
  if (quotaItems.value.length === 2) return 'grid-cols-2'
  return 'grid-cols-3'
})

// Model scope labels turn configured family identifiers into readable names while unknown
// identifiers remain visible on the plan card.
const MODEL_SCOPE_LABELS: Record<string, string> = {
  claude: 'Claude',
  gemini_text: 'Gemini',
  gemini_image: 'Imagen',
}

const modelScopeLabels = computed(() => {
  // Group policies supply these model-family restrictions, so the card keeps unknown scope
  // codes visible when no label is configured.
  const scopes = props.plan.supported_model_scopes
  if (!scopes || scopes.length === 0) return []
  return scopes.map((s) => MODEL_SCOPE_LABELS[s] || s)
})

const validitySuffix = computed(() => {
  return planValiditySuffix(props.plan, t)
})
</script>
