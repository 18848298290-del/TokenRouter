import type { ModelStat } from '@/api/usage.types'
import type { UsageMetric } from './usageChartState'

// topModels.ts ranks model usage for the dashboard and summarizes models outside the visible
// list. Request counts, charged costs, and token volumes each supply an additive ranking
// amount, while the cache-hit metric uses token volume to support meaningful totals.
//
// Positive amounts determine both ranking eligibility and the denominator for shares. Taking
// the visible slice happens after totaling all eligible models, so a leader retains its
// share of overall activity when the display limit changes. The remainder keeps its model
// count, combined amount, and percentage together for the card footer; no remaining models
// produces null.

// RankedModel pairs one usage record with its share of the selected total.
export interface RankedModel {
  model: ModelStat // Shared API usage record.
  share: number // Percentage of eligible usage.
}

// ModelRanking separates visible leaders from the combined remainder of eligible models.
export interface ModelRanking {
  top: RankedModel[] // Descending visible model entries.
  // others combines positive models beyond the visible slice; null tells the dashboard that
  // no remainder row is needed.
  others: {
    count: number // Remaining eligible model count.
    amount: number // Combined selected-metric usage amount.
    share: number // Percentage of eligible total.
  } | null
}

// rankingAmountOf selects the additive quantity used to rank a model. Cache-hit rate uses
// token volume because percentages cannot be summed into model shares or remainder totals.
const rankingAmountOf = (model: ModelStat, metric: UsageMetric): number => {
  if (metric === 'requests') return model.requests
  if (metric === 'cost') return model.actual_cost
  return model.total_tokens
}

// rankModels ranks positive model usage and returns the requested leading slice with
// percentage shares. The selected metric determines the additive total, with cache-hit rate
// ranking by tokens. Shares use all eligible models as their denominator, including models
// beyond the visible limit. Those remaining models are combined into one amount and share,
// while an empty remainder returns null so the card omits its summary row.
export const rankModels = (
  models: ModelStat[],
  metric: UsageMetric,
  limit: number,
): ModelRanking => {
  const sorted = models
    .filter((model) => rankingAmountOf(model, metric) > 0)
    .sort((a, b) => rankingAmountOf(b, metric) - rankingAmountOf(a, metric))
  const total = sorted.reduce((sum, model) => sum + rankingAmountOf(model, metric), 0)
  // shareOf converts an eligible amount into its percentage of all ranked usage.
  const shareOf = (amount: number) => (total > 0 ? (amount / total) * 100 : 0)
  const top = sorted
    .slice(0, limit)
    .map((model) => ({ model, share: shareOf(rankingAmountOf(model, metric)) }))
  const rest = sorted.slice(limit)
  const restAmount = rest.reduce((sum, model) => sum + rankingAmountOf(model, metric), 0)
  return {
    top,
    others:
      rest.length > 0
        ? { count: rest.length, amount: restAmount, share: shareOf(restAmount) }
        : null,
  }
}
