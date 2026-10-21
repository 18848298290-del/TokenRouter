import { describe, expect, it } from 'vitest'

import type { ModelStat } from '@/api/usage.types'
import { rankModels } from '../topModels'

// topModels.spec.ts checks model ranking and remainder arithmetic using fixed request,
// token, and cost totals. Each metric selects its eligible positive models, while shares
// retain the full eligible total even when the visible list is limited.

// model creates a usage record with independent request, token, and charged-cost totals.
// Input-side categories stay zero because these cases inspect ranking amounts rather than
// model-specific cache percentages.
const model = (name: string, requests: number, tokens: number, cost: number): ModelStat => ({
  model: name,
  requests,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: tokens,
  cost,
  actual_cost: cost,
})

describe('rankModels', () => {
  const models = [
    model('a', 1, 600, 3),
    model('b', 3, 300, 1),
    model('c', 0, 100, 0),
    model('d', 2, 0, 0),
  ]

  // The visible token ranking is limited below the eligible model count. The test protects
  // descending leaders, shares of the full total, and the combined remainder amount.
  it('按指标降序排列并计算占全部模型的比例', () => {
    const ranking = rankModels(models, 'tokens', 2)
    expect(ranking.top.map((item) => item.model.model)).toEqual(['a', 'b'])
    expect(ranking.top.map((item) => item.share)).toEqual([60, 30])
    expect(ranking.others).toEqual({ count: 1, amount: 100, share: 10 })
  })

  // Different models have zero usage under different ranking metrics. The test protects
  // positive-only eligibility and token-volume ordering when the selected metric is cache-
  // hit rate.
  it('跳过该指标为 0 的模型，命中率按 Token 排序', () => {
    expect(rankModels(models, 'requests', 5).top.map((item) => item.model.model)).toEqual([
      'b',
      'd',
      'a',
    ])
    expect(rankModels(models, 'cacheHitRate', 5).top.map((item) => item.model.model)).toEqual([
      'a',
      'b',
      'c',
    ])
  })

  // Every positive-cost model fits the visible ranking and the input can also be empty. The
  // test protects the absence of an unnecessary remainder row.
  it('全部模型都在排行内时没有其他行', () => {
    expect(rankModels(models, 'cost', 5).others).toBeNull()
    expect(rankModels([], 'cost', 5)).toEqual({ top: [], others: null })
  })
})
